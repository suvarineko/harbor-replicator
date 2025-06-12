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

// SyncEngineImpl implements the SyncEngine interface
type SyncEngineImpl struct {
	config           *SyncEngineConfig
	replicatorConfig *config.ReplicatorConfig
	workerPool       WorkerPool
	scheduler        Scheduler
	orchestrator     SyncOrchestrator
	progressTracker  ProgressTracker
	metricsCollector MetricsCollector
	healthChecker    HealthChecker
	stateManager     *state.StateManager
	logger           interface{} // Should be *zap.Logger
	
	// Runtime state
	state            EngineState
	stateMu          sync.RWMutex
	startTime        time.Time
	lastSyncTime     time.Time
	nextSyncTime     time.Time
	version          string
	
	// Statistics
	totalSyncsCompleted int64
	totalSyncsFailed    int64
	
	// Lifecycle management
	ctx              context.Context
	cancel           context.CancelFunc
	wg               sync.WaitGroup
	started          int32
	stopped          int32
}

// NewSyncEngineImpl creates a new sync engine implementation
func NewSyncEngineImpl(options *SyncEngineOptions) (*SyncEngineImpl, error) {
	if options == nil {
		return nil, fmt.Errorf("options cannot be nil")
	}
	
	if options.Config == nil {
		options.Config = DefaultSyncEngineConfig()
	}
	
	if options.Config.Config == nil {
		return nil, fmt.Errorf("replicator config is required")
	}

	// Create worker pool
	workerPool := NewWorkerPool(options.Config.WorkerPool)
	
	// Create scheduler
	scheduler := NewScheduler(options.Config.Scheduler, workerPool)
	
	// Create orchestrator
	orchestrator := NewSyncOrchestrator(
		options.Config.Config,
		workerPool,
		scheduler,
		nil, // progressTracker will be set later
		options.MetricsCollector,
		options.StateManager,
	)

	engine := &SyncEngineImpl{
		config:           options.Config,
		replicatorConfig: options.Config.Config,
		workerPool:       workerPool,
		scheduler:        scheduler,
		orchestrator:     orchestrator,
		metricsCollector: options.MetricsCollector,
		stateManager:     options.StateManager,
		logger:           options.Logger,
		state:            EngineStateInitializing,
		version:          "1.0.0", // This should come from build info
	}

	return engine, nil
}

// Start starts the sync engine
func (e *SyncEngineImpl) Start(ctx context.Context) error {
	if !atomic.CompareAndSwapInt32(&e.started, 0, 1) {
		return fmt.Errorf("sync engine already started")
	}

	e.setState(EngineStateStarting)
	e.ctx, e.cancel = context.WithCancel(ctx)
	e.startTime = time.Now()

	// Start orchestrator
	if err := e.orchestrator.Start(e.ctx); err != nil {
		e.setState(EngineStateError)
		return fmt.Errorf("failed to start orchestrator: %w", err)
	}

	// Start health checking if configured
	if e.config.HealthCheckInterval > 0 {
		e.wg.Add(1)
		go e.healthCheckLoop()
	}

	// Start metrics collection if configured
	if e.config.Metrics.Enabled && e.config.Metrics.CollectionInterval > 0 {
		e.wg.Add(1)
		go e.metricsCollectionLoop()
	}

	e.setState(EngineStateRunning)
	return nil
}

// Stop stops the sync engine
func (e *SyncEngineImpl) Stop(ctx context.Context) error {
	if !atomic.CompareAndSwapInt32(&e.stopped, 0, 1) {
		return fmt.Errorf("sync engine already stopped")
	}

	e.setState(EngineStateStopping)

	// Stop orchestrator
	if err := e.orchestrator.Stop(ctx); err != nil {
		return fmt.Errorf("failed to stop orchestrator: %w", err)
	}

	// Cancel context and wait for goroutines
	e.cancel()
	
	done := make(chan struct{})
	go func() {
		e.wg.Wait()
		close(done)
	}()

	shutdownTimeout := e.config.ShutdownTimeout
	if shutdownTimeout <= 0 {
		shutdownTimeout = 30 * time.Second
	}

	select {
	case <-done:
		e.setState(EngineStateStopped)
		return nil
	case <-time.After(shutdownTimeout):
		e.setState(EngineStateError)
		return fmt.Errorf("sync engine shutdown timeout")
	}
}

// Restart restarts the sync engine
func (e *SyncEngineImpl) Restart(ctx context.Context) error {
	if err := e.Stop(ctx); err != nil {
		return fmt.Errorf("failed to stop engine during restart: %w", err)
	}
	
	// Reset started/stopped flags
	atomic.StoreInt32(&e.started, 0)
	atomic.StoreInt32(&e.stopped, 0)
	
	return e.Start(ctx)
}

// RunSyncCycle executes a complete sync cycle
func (e *SyncEngineImpl) RunSyncCycle(ctx context.Context) (*SyncCycleResult, error) {
	if e.getState() != EngineStateRunning {
		return nil, fmt.Errorf("sync engine is not running")
	}

	result, err := e.orchestrator.RunSyncCycle(ctx)
	
	e.lastSyncTime = time.Now()
	if e.config.Scheduler.Interval > 0 {
		e.nextSyncTime = e.lastSyncTime.Add(e.config.Scheduler.Interval)
	}

	if err != nil {
		atomic.AddInt64(&e.totalSyncsFailed, 1)
	} else {
		atomic.AddInt64(&e.totalSyncsCompleted, 1)
	}

	return result, err
}

// TriggerSync triggers a sync for a specific resource type
func (e *SyncEngineImpl) TriggerSync(resourceType string) error {
	if e.getState() != EngineStateRunning {
		return fmt.Errorf("sync engine is not running")
	}
	
	return e.scheduler.TriggerImmediate(resourceType)
}

// CancelSync cancels a running sync operation
func (e *SyncEngineImpl) CancelSync(syncID string) error {
	return e.orchestrator.CancelSync(syncID)
}

// UpdateConfig updates the engine configuration
func (e *SyncEngineImpl) UpdateConfig(config *config.ReplicatorConfig) error {
	e.replicatorConfig = config
	e.config.Config = config
	
	// Update scheduler interval if changed
	if config.Sync.Interval != e.config.Scheduler.Interval {
		e.config.Scheduler.Interval = config.Sync.Interval
		return e.scheduler.UpdateInterval(config.Sync.Interval)
	}
	
	return nil
}

// GetConfig returns the current configuration
func (e *SyncEngineImpl) GetConfig() *config.ReplicatorConfig {
	return e.replicatorConfig
}

// ValidateConfig validates a configuration
func (e *SyncEngineImpl) ValidateConfig(config *config.ReplicatorConfig) error {
	if config == nil {
		return fmt.Errorf("config cannot be nil")
	}
	
	if config.Sync.Interval <= 0 {
		return fmt.Errorf("sync interval must be positive")
	}
	
	if config.Sync.Concurrency <= 0 {
		return fmt.Errorf("sync concurrency must be positive")
	}
	
	return nil
}

// GetStatus returns the current engine status
func (e *SyncEngineImpl) GetStatus() *EngineStatus {
	status := &EngineStatus{
		State:                   e.getState(),
		StartTime:               e.startTime,
		LastSyncTime:            e.lastSyncTime,
		NextSyncTime:            e.nextSyncTime,
		ActiveSyncs:             len(e.orchestrator.ListActiveSyncs()),
		TotalSyncsCompleted:     atomic.LoadInt64(&e.totalSyncsCompleted),
		TotalSyncsFailed:        atomic.LoadInt64(&e.totalSyncsFailed),
		RegisteredSynchronizers: []string{}, // Will be populated from orchestrator
		Version:                 e.version,
		ResourceStatuses:        make(map[string]SyncStatus),
	}
	
	if !e.startTime.IsZero() {
		status.Uptime = time.Since(e.startTime)
	}
	
	// Get worker pool stats
	if e.workerPool != nil {
		workerStats := e.workerPool.GetStats()
		status.WorkerPoolStats = &workerStats
	}
	
	// Get scheduler stats
	if e.scheduler != nil {
		schedulerStats := e.scheduler.(*SchedulerImpl).GetStats()
		status.SchedulerStats = schedulerStats
	}
	
	return status
}

// GetProgress returns the progress of a specific sync operation
func (e *SyncEngineImpl) GetProgress(syncID string) (*SyncProgress, error) {
	return e.orchestrator.GetProgress(syncID)
}

// ListActiveSyncs returns all currently active sync operations
func (e *SyncEngineImpl) ListActiveSyncs() []string {
	return e.orchestrator.ListActiveSyncs()
}

// GetMetrics returns all collected metrics
func (e *SyncEngineImpl) GetMetrics() map[string]interface{} {
	if e.metricsCollector == nil {
		return map[string]interface{}{}
	}
	
	return e.metricsCollector.GetMetrics()
}

// HealthCheck performs a health check of the engine
func (e *SyncEngineImpl) HealthCheck(ctx context.Context) (*HealthStatus, error) {
	if e.healthChecker != nil {
		return e.healthChecker.Check(ctx)
	}
	
	// Basic health check
	status := &HealthStatus{
		Status:    "healthy",
		Timestamp: time.Now(),
	}
	
	if e.getState() != EngineStateRunning {
		status.Status = "unhealthy"
		status.Message = fmt.Sprintf("engine state is %s", e.getState())
	}
	
	return status, nil
}

// RegisterSynchronizer registers a resource synchronizer
func (e *SyncEngineImpl) RegisterSynchronizer(synchronizer ResourceSynchronizer) error {
	return e.orchestrator.(*SyncOrchestratorImpl).RegisterSynchronizer(synchronizer)
}

// UnregisterSynchronizer unregisters a resource synchronizer
func (e *SyncEngineImpl) UnregisterSynchronizer(resourceType string) error {
	// Implementation would remove the synchronizer from the orchestrator
	return fmt.Errorf("not implemented")
}

// GetSynchronizer returns a resource synchronizer by type
func (e *SyncEngineImpl) GetSynchronizer(resourceType string) (ResourceSynchronizer, error) {
	// Implementation would retrieve the synchronizer from the orchestrator
	return nil, fmt.Errorf("not implemented")
}

// ListSynchronizers returns all registered synchronizer types
func (e *SyncEngineImpl) ListSynchronizers() []string {
	// Implementation would return all registered synchronizer types
	return []string{}
}

// Helper methods

func (e *SyncEngineImpl) setState(state EngineState) {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	e.state = state
}

func (e *SyncEngineImpl) getState() EngineState {
	e.stateMu.RLock()
	defer e.stateMu.RUnlock()
	return e.state
}

func (e *SyncEngineImpl) healthCheckLoop() {
	defer e.wg.Done()
	
	ticker := time.NewTicker(e.config.HealthCheckInterval)
	defer ticker.Stop()
	
	for {
		select {
		case <-ticker.C:
			if _, err := e.HealthCheck(e.ctx); err != nil {
				// Log health check failure
				// In a real implementation, this would use the logger
			}
		case <-e.ctx.Done():
			return
		}
	}
}

func (e *SyncEngineImpl) metricsCollectionLoop() {
	defer e.wg.Done()
	
	ticker := time.NewTicker(e.config.Metrics.CollectionInterval)
	defer ticker.Stop()
	
	for {
		select {
		case <-ticker.C:
			e.collectMetrics()
		case <-e.ctx.Done():
			return
		}
	}
}

func (e *SyncEngineImpl) collectMetrics() {
	if e.metricsCollector == nil {
		return
	}
	
	// Collect engine-level metrics
	labels := map[string]string{
		"engine_version": e.version,
		"engine_state":   string(e.getState()),
	}
	
	e.metricsCollector.SetGauge("sync_engine_uptime_seconds", time.Since(e.startTime).Seconds(), labels)
	e.metricsCollector.SetGauge("sync_engine_active_syncs", float64(len(e.orchestrator.ListActiveSyncs())), labels)
	e.metricsCollector.SetGauge("sync_engine_total_syncs_completed", float64(atomic.LoadInt64(&e.totalSyncsCompleted)), labels)
	e.metricsCollector.SetGauge("sync_engine_total_syncs_failed", float64(atomic.LoadInt64(&e.totalSyncsFailed)), labels)
	
	// Collect worker pool metrics
	if e.workerPool != nil {
		workerStats := e.workerPool.GetStats()
		workerLabels := map[string]string{"component": "worker_pool"}
		
		e.metricsCollector.SetGauge("worker_pool_size", float64(workerStats.Size), workerLabels)
		e.metricsCollector.SetGauge("worker_pool_active_workers", float64(workerStats.ActiveWorkers), workerLabels)
		e.metricsCollector.SetGauge("worker_pool_queued_jobs", float64(workerStats.QueuedJobs), workerLabels)
		e.metricsCollector.SetGauge("worker_pool_completed_jobs", float64(workerStats.CompletedJobs), workerLabels)
		e.metricsCollector.SetGauge("worker_pool_failed_jobs", float64(workerStats.FailedJobs), workerLabels)
	}
}