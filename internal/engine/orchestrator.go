package engine

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"harbor-replicator/pkg/config"
	"harbor-replicator/pkg/state"
)

// SyncOrchestratorImpl implements the SyncOrchestrator interface
type SyncOrchestratorImpl struct {
	config           *config.ReplicatorConfig
	workerPool       WorkerPool
	scheduler        Scheduler
	progressTracker  ProgressTracker
	metricsCollector MetricsCollector
	circuitBreakers  map[string]CircuitBreaker
	synchronizers    map[string]ResourceSynchronizer
	stateManager     *state.StateManager
	
	// Runtime state
	activeSyncs      map[string]*activeSyncInfo
	activeSyncsMu    sync.RWMutex
	ctx              context.Context
	cancel           context.CancelFunc
	wg               sync.WaitGroup
	started          int32
	stopped          int32
	
	// Statistics
	totalCycles      int64
	successfulCycles int64
	failedCycles     int64
	lastCycleTime    int64 // Unix timestamp in nanoseconds
	
	// Configuration
	maxConcurrentSyncs int
	syncTimeout        time.Duration
	preHooks           []SyncHook
	postHooks          []SyncHook
}

// activeSyncInfo tracks information about an active sync operation
type activeSyncInfo struct {
	syncID       string
	resourceType string
	startTime    time.Time
	context      *SyncContext
	cancelFunc   context.CancelFunc
}

// SyncHook defines a function that can be called before or after sync operations
type SyncHook func(ctx context.Context, syncCtx *SyncContext) error

// NewSyncOrchestrator creates a new sync orchestrator instance
func NewSyncOrchestrator(
	config *config.ReplicatorConfig,
	workerPool WorkerPool,
	scheduler Scheduler,
	progressTracker ProgressTracker,
	metricsCollector MetricsCollector,
	stateManager *state.StateManager,
) *SyncOrchestratorImpl {
	return &SyncOrchestratorImpl{
		config:           config,
		workerPool:       workerPool,
		scheduler:        scheduler,
		progressTracker:  progressTracker,
		metricsCollector: metricsCollector,
		stateManager:     stateManager,
		activeSyncs:      make(map[string]*activeSyncInfo),
		synchronizers:    make(map[string]ResourceSynchronizer),
		circuitBreakers:  make(map[string]CircuitBreaker),
		maxConcurrentSyncs: 5,
		syncTimeout:      30 * time.Minute,
	}
}

// Start starts the orchestrator
func (o *SyncOrchestratorImpl) Start(ctx context.Context) error {
	if !atomic.CompareAndSwapInt32(&o.started, 0, 1) {
		return fmt.Errorf("orchestrator already started")
	}

	o.ctx, o.cancel = context.WithCancel(ctx)
	
	// Validate dependencies
	if err := o.validateDependencies(); err != nil {
		return fmt.Errorf("dependency validation failed: %w", err)
	}

	// Start components
	if err := o.workerPool.Start(o.ctx); err != nil {
		return fmt.Errorf("failed to start worker pool: %w", err)
	}

	if err := o.scheduler.Start(o.ctx); err != nil {
		return fmt.Errorf("failed to start scheduler: %w", err)
	}

	// Start monitoring goroutine
	o.wg.Add(1)
	go o.monitorActiveSyncs()

	return nil
}

// Stop stops the orchestrator
func (o *SyncOrchestratorImpl) Stop(ctx context.Context) error {
	if !atomic.CompareAndSwapInt32(&o.stopped, 0, 1) {
		return fmt.Errorf("orchestrator already stopped")
	}

	// Cancel all active syncs
	o.cancelAllActiveSyncs()

	// Stop components
	if err := o.scheduler.Stop(ctx); err != nil {
		return fmt.Errorf("failed to stop scheduler: %w", err)
	}

	if err := o.workerPool.Stop(ctx); err != nil {
		return fmt.Errorf("failed to stop worker pool: %w", err)
	}

	// Cancel context and wait for goroutines
	o.cancel()
	
	done := make(chan struct{})
	go func() {
		o.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("orchestrator shutdown timeout")
	}
}

// RunSyncCycle executes a complete sync cycle
func (o *SyncOrchestratorImpl) RunSyncCycle(ctx context.Context) (*SyncCycleResult, error) {
	if atomic.LoadInt32(&o.stopped) == 1 {
		return nil, fmt.Errorf("orchestrator is stopped")
	}

	cycleID := o.generateCycleID()
	startTime := time.Now()
	
	atomic.AddInt64(&o.totalCycles, 1)
	atomic.StoreInt64(&o.lastCycleTime, startTime.UnixNano())

	result := &SyncCycleResult{
		CycleID:   cycleID,
		StartTime: startTime,
		Results:   make(map[string]*SyncResult),
		Errors:    []SyncError{},
		Metrics:   SyncMetrics{},
	}

	// Check if we're already at the concurrent sync limit
	if o.getActiveSyncCount() >= o.maxConcurrentSyncs {
		return result, fmt.Errorf("maximum concurrent syncs reached (%d)", o.maxConcurrentSyncs)
	}

	// Execute pre-sync hooks
	if err := o.executePreHooks(ctx, cycleID); err != nil {
		atomic.AddInt64(&o.failedCycles, 1)
		return result, fmt.Errorf("pre-sync hooks failed: %w", err)
	}

	// Execute syncs for all enabled resources in parallel
	var wg sync.WaitGroup
	syncResults := make(chan *SyncResult, len(o.synchronizers))
	syncErrors := make(chan SyncError, len(o.synchronizers)*10) // Buffer for multiple errors per sync

	// Execute sync for each resource type
	for resourceType, synchronizer := range o.synchronizers {
		if !o.isResourceEnabled(resourceType) {
			continue
		}

		// Check circuit breaker
		if cb, exists := o.circuitBreakers[resourceType]; exists {
			if cb.State() == CircuitBreakerOpen {
				syncErrors <- SyncError{
					ResourceType: resourceType,
					ErrorType:    "circuit_breaker_open",
					ErrorMessage: "circuit breaker is open",
					Timestamp:    time.Now(),
					Retryable:    false,
				}
				continue
			}
		}

		wg.Add(1)
		go func(resourceType string, synchronizer ResourceSynchronizer) {
			defer wg.Done()
			o.executeSyncForResource(ctx, cycleID, resourceType, synchronizer, syncResults, syncErrors)
		}(resourceType, synchronizer)
	}

	// Wait for all syncs to complete
	go func() {
		wg.Wait()
		close(syncResults)
		close(syncErrors)
	}()

	// Collect results
	for syncResult := range syncResults {
		result.Results[syncResult.ResourceType] = syncResult
		result.TotalSyncs++
		
		if syncResult.Status == SyncStatusCompleted {
			result.SuccessfulSyncs++
		} else {
			result.FailedSyncs++
		}
	}

	// Collect errors
	for syncError := range syncErrors {
		result.Errors = append(result.Errors, syncError)
	}

	// Execute post-sync hooks
	if err := o.executePostHooks(ctx, cycleID, result); err != nil {
		// Post-hook failures don't fail the entire cycle, but we log them
		result.Errors = append(result.Errors, SyncError{
			ResourceType: "orchestrator",
			ErrorType:    "post_hook_failure",
			ErrorMessage: err.Error(),
			Timestamp:    time.Now(),
			Retryable:    false,
		})
	}

	// Finalize result
	result.EndTime = time.Now()
	result.Duration = result.EndTime.Sub(result.StartTime)

	// Update statistics
	if result.FailedSyncs == 0 {
		atomic.AddInt64(&o.successfulCycles, 1)
	} else {
		atomic.AddInt64(&o.failedCycles, 1)
	}

	// Record metrics
	if o.metricsCollector != nil {
		o.recordCycleMetrics(result)
	}

	return result, nil
}

// executeSyncForResource executes sync for a specific resource type
func (o *SyncOrchestratorImpl) executeSyncForResource(
	ctx context.Context,
	cycleID string,
	resourceType string,
	synchronizer ResourceSynchronizer,
	results chan<- *SyncResult,
	errors chan<- SyncError,
) {
	syncID := o.generateSyncID(cycleID, resourceType)
	
	// Create sync context with timeout
	syncCtx, cancel := context.WithTimeout(ctx, o.syncTimeout)
	defer cancel()

	syncContext := &SyncContext{
		SyncID:       syncID,
		ResourceType: resourceType,
		DryRun:       o.config.Sync.DryRun,
		StartTime:    time.Now(),
		Timeout:      o.syncTimeout,
		MaxRetries:   3,
		BatchSize:    100,
		Concurrency:  o.config.Sync.Concurrency,
		Tags:         map[string]string{"cycle_id": cycleID},
	}

	// Register active sync
	o.registerActiveSync(syncID, resourceType, syncContext, cancel)
	defer o.unregisterActiveSync(syncID)

	// Start progress tracking
	if o.progressTracker != nil {
		if err := o.progressTracker.StartTracking(syncID, 100); err != nil {
			errors <- SyncError{
				ResourceType: resourceType,
				ResourceID:   syncID,
				ErrorType:    "progress_tracking_error",
				ErrorMessage: err.Error(),
				Timestamp:    time.Now(),
				Retryable:    false,
			}
		}
		defer o.progressTracker.FinishTracking(syncID)
	}

	// Execute sync with circuit breaker protection
	var result *SyncResult
	var err error

	if cb, exists := o.circuitBreakers[resourceType]; exists {
		err = cb.Execute(func() error {
			result, err = synchronizer.Sync(syncCtx, syncContext)
			return err
		})
	} else {
		result, err = synchronizer.Sync(syncCtx, syncContext)
	}

	if err != nil {
		errors <- SyncError{
			ResourceType: resourceType,
			ResourceID:   syncID,
			Operation:    "sync",
			ErrorType:    "sync_failure",
			ErrorMessage: err.Error(),
			Timestamp:    time.Now(),
			Retryable:    true,
		}
		
		// Create a failed result
		result = &SyncResult{
			SyncID:         syncID,
			ResourceType:   resourceType,
			StartTime:      syncContext.StartTime,
			EndTime:        time.Now(),
			Status:         SyncStatusFailed,
			ErrorCount:     1,
			Errors:         []SyncError{{
				ResourceType: resourceType,
				ErrorType:    "sync_failure",
				ErrorMessage: err.Error(),
				Timestamp:    time.Now(),
			}},
		}
		result.Duration = result.EndTime.Sub(result.StartTime)
	}

	if result != nil {
		results <- result
	}
}

// GetProgress returns the current sync progress
func (o *SyncOrchestratorImpl) GetProgress(syncID string) (*SyncProgress, error) {
	if o.progressTracker == nil {
		return nil, fmt.Errorf("progress tracking is not enabled")
	}
	
	return o.progressTracker.GetProgress(syncID)
}

// ListActiveSyncs returns all currently active sync operations
func (o *SyncOrchestratorImpl) ListActiveSyncs() []string {
	o.activeSyncsMu.RLock()
	defer o.activeSyncsMu.RUnlock()
	
	syncs := make([]string, 0, len(o.activeSyncs))
	for syncID := range o.activeSyncs {
		syncs = append(syncs, syncID)
	}
	
	return syncs
}

// CancelSync cancels a running sync operation
func (o *SyncOrchestratorImpl) CancelSync(syncID string) error {
	o.activeSyncsMu.RLock()
	syncInfo, exists := o.activeSyncs[syncID]
	o.activeSyncsMu.RUnlock()
	
	if !exists {
		return fmt.Errorf("sync %s not found or not active", syncID)
	}
	
	syncInfo.cancelFunc()
	return nil
}

// RegisterSynchronizer registers a resource synchronizer
func (o *SyncOrchestratorImpl) RegisterSynchronizer(synchronizer ResourceSynchronizer) error {
	resourceType := synchronizer.GetResourceType()
	if resourceType == "" {
		return fmt.Errorf("synchronizer must have a valid resource type")
	}
	
	o.synchronizers[resourceType] = synchronizer
	return nil
}

// Helper methods

func (o *SyncOrchestratorImpl) validateDependencies() error {
	if o.workerPool == nil {
		return fmt.Errorf("worker pool is required")
	}
	if o.scheduler == nil {
		return fmt.Errorf("scheduler is required")
	}
	if o.config == nil {
		return fmt.Errorf("config is required")
	}
	return nil
}

func (o *SyncOrchestratorImpl) generateCycleID() string {
	return fmt.Sprintf("cycle-%d", time.Now().UnixNano())
}

func (o *SyncOrchestratorImpl) generateSyncID(cycleID, resourceType string) string {
	return fmt.Sprintf("%s-%s-%d", cycleID, resourceType, time.Now().UnixNano())
}

func (o *SyncOrchestratorImpl) getActiveSyncCount() int {
	o.activeSyncsMu.RLock()
	defer o.activeSyncsMu.RUnlock()
	return len(o.activeSyncs)
}

func (o *SyncOrchestratorImpl) registerActiveSync(syncID, resourceType string, syncCtx *SyncContext, cancel context.CancelFunc) {
	o.activeSyncsMu.Lock()
	defer o.activeSyncsMu.Unlock()
	
	o.activeSyncs[syncID] = &activeSyncInfo{
		syncID:       syncID,
		resourceType: resourceType,
		startTime:    time.Now(),
		context:      syncCtx,
		cancelFunc:   cancel,
	}
}

func (o *SyncOrchestratorImpl) unregisterActiveSync(syncID string) {
	o.activeSyncsMu.Lock()
	defer o.activeSyncsMu.Unlock()
	delete(o.activeSyncs, syncID)
}

func (o *SyncOrchestratorImpl) cancelAllActiveSyncs() {
	o.activeSyncsMu.RLock()
	activeSyncs := make([]*activeSyncInfo, 0, len(o.activeSyncs))
	for _, syncInfo := range o.activeSyncs {
		activeSyncs = append(activeSyncs, syncInfo)
	}
	o.activeSyncsMu.RUnlock()
	
	for _, syncInfo := range activeSyncs {
		syncInfo.cancelFunc()
	}
}

func (o *SyncOrchestratorImpl) isResourceEnabled(resourceType string) bool {
	switch resourceType {
	case "robot_accounts":
		return o.config.Sync.Resources.RobotAccounts.Enabled
	case "oidc_groups":
		return o.config.Sync.Resources.OIDCGroups.Enabled
	default:
		return false
	}
}

func (o *SyncOrchestratorImpl) executePreHooks(ctx context.Context, cycleID string) error {
	for _, hook := range o.preHooks {
		syncCtx := &SyncContext{
			SyncID:    cycleID,
			StartTime: time.Now(),
			Tags:      map[string]string{"hook_type": "pre"},
		}
		if err := hook(ctx, syncCtx); err != nil {
			return err
		}
	}
	return nil
}

func (o *SyncOrchestratorImpl) executePostHooks(ctx context.Context, cycleID string, result *SyncCycleResult) error {
	for _, hook := range o.postHooks {
		syncCtx := &SyncContext{
			SyncID:    cycleID,
			StartTime: time.Now(),
			Tags:      map[string]string{"hook_type": "post"},
			Metadata:  map[string]interface{}{"cycle_result": result},
		}
		if err := hook(ctx, syncCtx); err != nil {
			return err
		}
	}
	return nil
}

func (o *SyncOrchestratorImpl) recordCycleMetrics(result *SyncCycleResult) {
	labels := map[string]string{
		"cycle_id": result.CycleID,
	}
	
	o.metricsCollector.RecordHistogram("sync_cycle_duration_seconds", result.Duration.Seconds(), labels)
	o.metricsCollector.SetGauge("sync_cycle_total_syncs", float64(result.TotalSyncs), labels)
	o.metricsCollector.SetGauge("sync_cycle_successful_syncs", float64(result.SuccessfulSyncs), labels)
	o.metricsCollector.SetGauge("sync_cycle_failed_syncs", float64(result.FailedSyncs), labels)
}

func (o *SyncOrchestratorImpl) monitorActiveSyncs() {
	defer o.wg.Done()
	
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	
	for {
		select {
		case <-ticker.C:
			o.checkSyncTimeouts()
		case <-o.ctx.Done():
			return
		}
	}
}

func (o *SyncOrchestratorImpl) checkSyncTimeouts() {
	now := time.Now()
	timeoutThreshold := o.syncTimeout + (5 * time.Minute) // Add buffer
	
	o.activeSyncsMu.RLock()
	timedOutSyncs := make([]*activeSyncInfo, 0)
	for _, syncInfo := range o.activeSyncs {
		if now.Sub(syncInfo.startTime) > timeoutThreshold {
			timedOutSyncs = append(timedOutSyncs, syncInfo)
		}
	}
	o.activeSyncsMu.RUnlock()
	
	// Cancel timed out syncs
	for _, syncInfo := range timedOutSyncs {
		syncInfo.cancelFunc()
	}
}