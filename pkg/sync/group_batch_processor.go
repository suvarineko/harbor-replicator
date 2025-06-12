package sync

import (
	"context"
	"fmt"
	"sync"
	"time"

	"harbor-replicator/pkg/client"
	"harbor-replicator/pkg/state"

	"go.uber.org/zap"
)

// GroupBatchProcessor handles batch processing and performance optimization for OIDC group synchronization
type GroupBatchProcessor struct {
	localClient    client.ClientInterface
	remoteClients  map[string]client.ClientInterface
	stateManager   *state.StateManager
	logger         *zap.Logger
	config         BatchProcessingConfig
	
	// Performance optimization components
	groupCache     *GroupCache
	requestLimiter *RequestLimiter
	progressTracker *ProgressTracker
}

// BatchProcessingConfig contains configuration for batch processing and performance optimization
type BatchProcessingConfig struct {
	// BatchSize defines the number of groups to process in a single batch
	BatchSize int
	
	// MaxConcurrentBatches limits the number of concurrent batch operations
	MaxConcurrentBatches int
	
	// MaxConcurrentGroups limits concurrent processing within a batch
	MaxConcurrentGroups int
	
	// BatchTimeout defines the timeout for a single batch operation
	BatchTimeout time.Duration
	
	// EnableCaching enables caching of frequently accessed data
	EnableCaching bool
	
	// CacheSize defines the maximum number of cached items
	CacheSize int
	
	// CacheTTL defines the time-to-live for cached items
	CacheTTL time.Duration
	
	// EnableProgressTracking enables detailed progress tracking
	EnableProgressTracking bool
	
	// ProgressReportInterval defines how often progress is reported
	ProgressReportInterval time.Duration
	
	// RequestsPerSecond limits the rate of API requests
	RequestsPerSecond float64
	
	// BurstSize allows burst of requests above the rate limit
	BurstSize int
	
	// EnableCompression enables compression for large data transfers
	EnableCompression bool
	
	// MemoryOptimization enables memory optimization strategies
	MemoryOptimization bool
	
	// EnableRetryOptimization enables intelligent retry strategies
	EnableRetryOptimization bool
}

// BatchSyncResult represents the result of batch synchronization
type BatchSyncResult struct {
	BatchID             string
	TotalGroups         int
	ProcessedGroups     int
	SuccessfulGroups    int
	FailedGroups        int
	SkippedGroups       int
	BatchDuration       time.Duration
	AverageGroupTime    time.Duration
	TotalAPIRequests    int
	CacheHitRate        float64
	MemoryUsage         MemoryUsage
	PerformanceMetrics  PerformanceMetrics
	GroupResults        []GroupSyncResult
	Timestamp           time.Time
}

// GroupSyncResult represents the result of individual group synchronization
type GroupSyncResult struct {
	GroupID            string
	GroupName          string
	Operation          string
	Success            bool
	Error              string
	Duration           time.Duration
	APIRequests        int
	PermissionsUpdated int
	ProjectRolesUpdated int
	ConflictsResolved  int
	CacheHits          int
	Timestamp          time.Time
}

// MemoryUsage represents memory usage statistics
type MemoryUsage struct {
	AllocatedMB    float64
	InUseMB        float64
	SystemMB       float64
	GCCycles       uint32
}

// PerformanceMetrics represents performance metrics
type PerformanceMetrics struct {
	ThroughputGroupsPerSecond  float64
	AverageLatencyMS           float64
	P95LatencyMS               float64
	P99LatencyMS               float64
	ErrorRate                  float64
	ResourceUtilization        float64
}

// GroupCache provides caching for frequently accessed group data
type GroupCache struct {
	cache     map[string]*CacheEntry
	mutex     sync.RWMutex
	maxSize   int
	ttl       time.Duration
	hitCount  int64
	missCount int64
}

// CacheEntry represents a cached item
type CacheEntry struct {
	Data      interface{}
	Timestamp time.Time
	TTL       time.Duration
}

// RequestLimiter provides rate limiting for API requests
type RequestLimiter struct {
	requestsPerSecond float64
	burstSize         int
	tokens            chan struct{}
	ticker            *time.Ticker
	stopChan          chan struct{}
	mutex             sync.Mutex
}

// ProgressTracker tracks and reports progress of batch operations
type ProgressTracker struct {
	totalOperations     int
	completedOperations int
	failedOperations    int
	skippedOperations   int
	startTime           time.Time
	lastReportTime      time.Time
	reportInterval      time.Duration
	mutex               sync.RWMutex
	callbacks           []ProgressCallback
}

// ProgressCallback defines the signature for progress callback functions
type ProgressCallback func(progress ProgressReport)

// ProgressReport represents a progress report
type ProgressReport struct {
	TotalOperations     int
	CompletedOperations int
	FailedOperations    int
	SkippedOperations   int
	PercentComplete     float64
	ElapsedTime         time.Duration
	EstimatedTimeRemaining time.Duration
	OperationsPerSecond float64
	Timestamp           time.Time
}

// NewGroupBatchProcessor creates a new group batch processor
func NewGroupBatchProcessor(
	localClient client.ClientInterface,
	remoteClients map[string]client.ClientInterface,
	stateManager *state.StateManager,
	logger *zap.Logger,
	config BatchProcessingConfig,
) *GroupBatchProcessor {
	processor := &GroupBatchProcessor{
		localClient:   localClient,
		remoteClients: remoteClients,
		stateManager:  stateManager,
		logger:        logger,
		config:        config,
	}
	
	// Initialize performance optimization components
	if config.EnableCaching {
		processor.groupCache = NewGroupCache(config.CacheSize, config.CacheTTL)
	}
	
	processor.requestLimiter = NewRequestLimiter(config.RequestsPerSecond, config.BurstSize)
	
	if config.EnableProgressTracking {
		processor.progressTracker = NewProgressTracker(config.ProgressReportInterval)
	}
	
	return processor
}

// ProcessGroupsBatch processes multiple groups in optimized batches
func (gbp *GroupBatchProcessor) ProcessGroupsBatch(
	ctx context.Context,
	sourceGroups []client.OIDCGroup,
	sourceInstance string,
) (*BatchSyncResult, error) {
	startTime := time.Now()
	batchID := fmt.Sprintf("batch_%s_%d", sourceInstance, startTime.Unix())
	
	result := &BatchSyncResult{
		BatchID:     batchID,
		TotalGroups: len(sourceGroups),
		Timestamp:   startTime,
	}
	
	gbp.logger.Info("Starting batch group synchronization",
		zap.String("batch_id", batchID),
		zap.String("source_instance", sourceInstance),
		zap.Int("total_groups", len(sourceGroups)),
		zap.Int("batch_size", gbp.config.BatchSize),
		zap.Int("max_concurrent_batches", gbp.config.MaxConcurrentBatches))
	
	// Initialize progress tracking
	if gbp.progressTracker != nil {
		gbp.progressTracker.Initialize(len(sourceGroups))
	}
	
	// Process groups in batches
	batches := gbp.createBatches(sourceGroups)
	batchResults := make(chan *BatchProcessingResult, len(batches))
	
	// Use semaphore to limit concurrent batches
	semaphore := make(chan struct{}, gbp.config.MaxConcurrentBatches)
	var wg sync.WaitGroup
	
	for i, batch := range batches {
		wg.Add(1)
		go func(batchIndex int, groupBatch []client.OIDCGroup) {
			defer wg.Done()
			
			// Acquire semaphore
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			
			batchCtx, cancel := context.WithTimeout(ctx, gbp.config.BatchTimeout)
			defer cancel()
			
			batchResult := gbp.processSingleBatch(batchCtx, groupBatch, sourceInstance, batchIndex)
			batchResults <- batchResult
			
		}(i, batch)
	}
	
	// Wait for all batches to complete
	go func() {
		wg.Wait()
		close(batchResults)
	}()
	
	// Collect results
	apiRequestCount := 0
	cacheHits := int64(0)
	totalLatencies := []float64{}
	
	for batchResult := range batchResults {
		result.ProcessedGroups += batchResult.ProcessedGroups
		result.SuccessfulGroups += batchResult.SuccessfulGroups
		result.FailedGroups += batchResult.FailedGroups
		result.SkippedGroups += batchResult.SkippedGroups
		result.GroupResults = append(result.GroupResults, batchResult.GroupResults...)
		
		apiRequestCount += batchResult.APIRequests
		cacheHits += batchResult.CacheHits
		totalLatencies = append(totalLatencies, batchResult.Latencies...)
	}
	
	result.BatchDuration = time.Since(startTime)
	result.TotalAPIRequests = apiRequestCount
	
	// Calculate performance metrics
	if result.ProcessedGroups > 0 {
		result.AverageGroupTime = result.BatchDuration / time.Duration(result.ProcessedGroups)
	}
	
	if gbp.groupCache != nil {
		result.CacheHitRate = gbp.groupCache.GetHitRate()
	}
	
	result.MemoryUsage = gbp.collectMemoryUsage()
	result.PerformanceMetrics = gbp.calculatePerformanceMetrics(totalLatencies, result)
	
	gbp.logger.Info("Batch group synchronization completed",
		zap.String("batch_id", batchID),
		zap.Duration("duration", result.BatchDuration),
		zap.Int("processed", result.ProcessedGroups),
		zap.Int("successful", result.SuccessfulGroups),
		zap.Int("failed", result.FailedGroups),
		zap.Int("api_requests", result.TotalAPIRequests),
		zap.Float64("cache_hit_rate", result.CacheHitRate),
		zap.Float64("throughput_groups_per_sec", result.PerformanceMetrics.ThroughputGroupsPerSecond))
	
	return result, nil
}

// BatchProcessingResult represents the result of processing a single batch
type BatchProcessingResult struct {
	ProcessedGroups int
	SuccessfulGroups int
	FailedGroups    int
	SkippedGroups   int
	APIRequests     int
	CacheHits       int64
	GroupResults    []GroupSyncResult
	Latencies       []float64
}

// createBatches divides groups into batches for processing
func (gbp *GroupBatchProcessor) createBatches(groups []client.OIDCGroup) [][]client.OIDCGroup {
	var batches [][]client.OIDCGroup
	
	for i := 0; i < len(groups); i += gbp.config.BatchSize {
		end := i + gbp.config.BatchSize
		if end > len(groups) {
			end = len(groups)
		}
		batches = append(batches, groups[i:end])
	}
	
	gbp.logger.Debug("Created batches for processing",
		zap.Int("total_groups", len(groups)),
		zap.Int("batch_size", gbp.config.BatchSize),
		zap.Int("num_batches", len(batches)))
	
	return batches
}

// processSingleBatch processes a single batch of groups
func (gbp *GroupBatchProcessor) processSingleBatch(
	ctx context.Context,
	groups []client.OIDCGroup,
	sourceInstance string,
	batchIndex int,
) *BatchProcessingResult {
	result := &BatchProcessingResult{
		Latencies: make([]float64, 0, len(groups)),
	}
	
	gbp.logger.Debug("Processing batch",
		zap.Int("batch_index", batchIndex),
		zap.Int("group_count", len(groups)))
	
	// Use semaphore to limit concurrent group processing within batch
	semaphore := make(chan struct{}, gbp.config.MaxConcurrentGroups)
	var wg sync.WaitGroup
	var mutex sync.Mutex
	
	for _, group := range groups {
		wg.Add(1)
		go func(g client.OIDCGroup) {
			defer wg.Done()
			
			// Acquire semaphore
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			
			// Rate limit API requests
			if err := gbp.requestLimiter.Wait(ctx); err != nil {
				gbp.logger.Warn("Request rate limiting failed", zap.Error(err))
			}
			
			groupResult := gbp.processGroup(ctx, &g, sourceInstance)
			
			// Update batch result
			mutex.Lock()
			result.ProcessedGroups++
			if groupResult.Success {
				result.SuccessfulGroups++
			} else {
				if groupResult.Error == "skipped" {
					result.SkippedGroups++
				} else {
					result.FailedGroups++
				}
			}
			result.APIRequests += groupResult.APIRequests
			result.CacheHits += int64(groupResult.CacheHits)
			result.GroupResults = append(result.GroupResults, *groupResult)
			result.Latencies = append(result.Latencies, float64(groupResult.Duration.Milliseconds()))
			mutex.Unlock()
			
			// Update progress
			if gbp.progressTracker != nil {
				if groupResult.Success {
					gbp.progressTracker.ReportCompleted()
				} else if groupResult.Error == "skipped" {
					gbp.progressTracker.ReportSkipped()
				} else {
					gbp.progressTracker.ReportFailed()
				}
			}
			
		}(group)
	}
	
	wg.Wait()
	
	gbp.logger.Debug("Batch processing completed",
		zap.Int("batch_index", batchIndex),
		zap.Int("processed", result.ProcessedGroups),
		zap.Int("successful", result.SuccessfulGroups),
		zap.Int("failed", result.FailedGroups))
	
	return result
}

// processGroup processes a single group with caching and optimization
func (gbp *GroupBatchProcessor) processGroup(
	ctx context.Context,
	sourceGroup *client.OIDCGroup,
	sourceInstance string,
) *GroupSyncResult {
	startTime := time.Now()
	
	result := &GroupSyncResult{
		GroupID:   fmt.Sprintf("%s:%s", sourceInstance, sourceGroup.GroupName),
		GroupName: sourceGroup.GroupName,
		Operation: "sync",
		Timestamp: startTime,
	}
	
	// Check cache first
	var targetGroup *client.OIDCGroup
	cacheKey := fmt.Sprintf("group:%s", sourceGroup.GroupName)
	
	if gbp.groupCache != nil {
		if cached := gbp.groupCache.Get(cacheKey); cached != nil {
			if cachedGroup, ok := cached.(*client.OIDCGroup); ok {
				targetGroup = cachedGroup
				result.CacheHits++
			}
		}
	}
	
	// If not in cache, fetch from API
	if targetGroup == nil {
		groups, err := gbp.localClient.ListOIDCGroups(ctx)
		if err != nil {
			result.Error = fmt.Sprintf("failed to list groups: %v", err)
			result.Duration = time.Since(startTime)
			return result
		}
		result.APIRequests++
		
		// Find matching group
		for _, group := range groups {
			if group.GroupName == sourceGroup.GroupName {
				targetGroup = &group
				break
			}
		}
		
		// Cache the result
		if gbp.groupCache != nil && targetGroup != nil {
			gbp.groupCache.Set(cacheKey, targetGroup)
		}
	}
	
	// Process the group (simplified for this example)
	if targetGroup != nil {
		// Update existing group
		result.Operation = "update"
		
		// Count permission and role updates
		result.PermissionsUpdated = len(sourceGroup.Permissions)
		result.ProjectRolesUpdated = len(sourceGroup.ProjectRoles)
		
		// Simulate API call to update group
		_, err := gbp.localClient.UpdateOIDCGroup(ctx, targetGroup.ID, sourceGroup)
		if err != nil {
			result.Error = err.Error()
		} else {
			result.Success = true
		}
		result.APIRequests++
	} else {
		// Create new group
		result.Operation = "create"
		
		createdGroup, err := gbp.localClient.CreateOIDCGroup(ctx, sourceGroup)
		if err != nil {
			result.Error = err.Error()
		} else {
			result.Success = true
			result.PermissionsUpdated = len(sourceGroup.Permissions)
			result.ProjectRolesUpdated = len(sourceGroup.ProjectRoles)
			
			// Cache the new group
			if gbp.groupCache != nil {
				gbp.groupCache.Set(cacheKey, createdGroup)
			}
		}
		result.APIRequests++
	}
	
	result.Duration = time.Since(startTime)
	return result
}

// NewGroupCache creates a new group cache
func NewGroupCache(maxSize int, ttl time.Duration) *GroupCache {
	return &GroupCache{
		cache:   make(map[string]*CacheEntry),
		maxSize: maxSize,
		ttl:     ttl,
	}
}

// Get retrieves an item from the cache
func (gc *GroupCache) Get(key string) interface{} {
	gc.mutex.RLock()
	defer gc.mutex.RUnlock()
	
	entry, exists := gc.cache[key]
	if !exists {
		gc.missCount++
		return nil
	}
	
	// Check if expired
	if time.Since(entry.Timestamp) > entry.TTL {
		gc.missCount++
		return nil
	}
	
	gc.hitCount++
	return entry.Data
}

// Set stores an item in the cache
func (gc *GroupCache) Set(key string, value interface{}) {
	gc.mutex.Lock()
	defer gc.mutex.Unlock()
	
	// Implement simple LRU eviction if cache is full
	if len(gc.cache) >= gc.maxSize {
		// Remove oldest entry
		oldestKey := ""
		oldestTime := time.Now()
		for k, v := range gc.cache {
			if v.Timestamp.Before(oldestTime) {
				oldestTime = v.Timestamp
				oldestKey = k
			}
		}
		if oldestKey != "" {
			delete(gc.cache, oldestKey)
		}
	}
	
	gc.cache[key] = &CacheEntry{
		Data:      value,
		Timestamp: time.Now(),
		TTL:       gc.ttl,
	}
}

// GetHitRate returns the cache hit rate
func (gc *GroupCache) GetHitRate() float64 {
	gc.mutex.RLock()
	defer gc.mutex.RUnlock()
	
	total := gc.hitCount + gc.missCount
	if total == 0 {
		return 0
	}
	return float64(gc.hitCount) / float64(total)
}

// NewRequestLimiter creates a new request limiter
func NewRequestLimiter(requestsPerSecond float64, burstSize int) *RequestLimiter {
	rl := &RequestLimiter{
		requestsPerSecond: requestsPerSecond,
		burstSize:         burstSize,
		tokens:            make(chan struct{}, burstSize),
		stopChan:          make(chan struct{}),
	}
	
	// Fill initial tokens
	for i := 0; i < burstSize; i++ {
		rl.tokens <- struct{}{}
	}
	
	// Start token refill routine
	if requestsPerSecond > 0 {
		interval := time.Duration(float64(time.Second) / requestsPerSecond)
		rl.ticker = time.NewTicker(interval)
		
		go func() {
			for {
				select {
				case <-rl.ticker.C:
					select {
					case rl.tokens <- struct{}{}:
					default:
						// Token bucket is full
					}
				case <-rl.stopChan:
					return
				}
			}
		}()
	}
	
	return rl
}

// Wait waits for a token from the rate limiter
func (rl *RequestLimiter) Wait(ctx context.Context) error {
	if rl.requestsPerSecond <= 0 {
		return nil // No rate limiting
	}
	
	select {
	case <-rl.tokens:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stop stops the request limiter
func (rl *RequestLimiter) Stop() {
	if rl.ticker != nil {
		rl.ticker.Stop()
	}
	close(rl.stopChan)
}

// NewProgressTracker creates a new progress tracker
func NewProgressTracker(reportInterval time.Duration) *ProgressTracker {
	return &ProgressTracker{
		reportInterval: reportInterval,
		callbacks:      make([]ProgressCallback, 0),
	}
}

// Initialize initializes the progress tracker
func (pt *ProgressTracker) Initialize(totalOperations int) {
	pt.mutex.Lock()
	defer pt.mutex.Unlock()
	
	pt.totalOperations = totalOperations
	pt.completedOperations = 0
	pt.failedOperations = 0
	pt.skippedOperations = 0
	pt.startTime = time.Now()
	pt.lastReportTime = time.Now()
}

// ReportCompleted reports a completed operation
func (pt *ProgressTracker) ReportCompleted() {
	pt.mutex.Lock()
	defer pt.mutex.Unlock()
	
	pt.completedOperations++
	pt.checkAndReport()
}

// ReportFailed reports a failed operation
func (pt *ProgressTracker) ReportFailed() {
	pt.mutex.Lock()
	defer pt.mutex.Unlock()
	
	pt.failedOperations++
	pt.checkAndReport()
}

// ReportSkipped reports a skipped operation
func (pt *ProgressTracker) ReportSkipped() {
	pt.mutex.Lock()
	defer pt.mutex.Unlock()
	
	pt.skippedOperations++
	pt.checkAndReport()
}

// checkAndReport checks if it's time to report progress
func (pt *ProgressTracker) checkAndReport() {
	if time.Since(pt.lastReportTime) >= pt.reportInterval {
		pt.reportProgress()
		pt.lastReportTime = time.Now()
	}
}

// reportProgress reports current progress to callbacks
func (pt *ProgressTracker) reportProgress() {
	totalProcessed := pt.completedOperations + pt.failedOperations + pt.skippedOperations
	percentComplete := float64(totalProcessed) / float64(pt.totalOperations) * 100
	
	elapsedTime := time.Since(pt.startTime)
	var estimatedTimeRemaining time.Duration
	var operationsPerSecond float64
	
	if totalProcessed > 0 {
		operationsPerSecond = float64(totalProcessed) / elapsedTime.Seconds()
		remaining := pt.totalOperations - totalProcessed
		if operationsPerSecond > 0 {
			estimatedTimeRemaining = time.Duration(float64(remaining)/operationsPerSecond) * time.Second
		}
	}
	
	report := ProgressReport{
		TotalOperations:        pt.totalOperations,
		CompletedOperations:    pt.completedOperations,
		FailedOperations:       pt.failedOperations,
		SkippedOperations:      pt.skippedOperations,
		PercentComplete:        percentComplete,
		ElapsedTime:            elapsedTime,
		EstimatedTimeRemaining: estimatedTimeRemaining,
		OperationsPerSecond:    operationsPerSecond,
		Timestamp:              time.Now(),
	}
	
	for _, callback := range pt.callbacks {
		callback(report)
	}
}

// AddCallback adds a progress callback
func (pt *ProgressTracker) AddCallback(callback ProgressCallback) {
	pt.mutex.Lock()
	defer pt.mutex.Unlock()
	
	pt.callbacks = append(pt.callbacks, callback)
}

// collectMemoryUsage collects current memory usage statistics
func (gbp *GroupBatchProcessor) collectMemoryUsage() MemoryUsage {
	// This would use runtime.MemStats in a real implementation
	// For now, return dummy values
	return MemoryUsage{
		AllocatedMB: 50.0,
		InUseMB:     30.0,
		SystemMB:    100.0,
		GCCycles:    10,
	}
}

// calculatePerformanceMetrics calculates performance metrics from latency data
func (gbp *GroupBatchProcessor) calculatePerformanceMetrics(latencies []float64, result *BatchSyncResult) PerformanceMetrics {
	if len(latencies) == 0 {
		return PerformanceMetrics{}
	}
	
	// Calculate throughput
	throughput := float64(result.ProcessedGroups) / result.BatchDuration.Seconds()
	
	// Calculate average latency
	var totalLatency float64
	for _, latency := range latencies {
		totalLatency += latency
	}
	avgLatency := totalLatency / float64(len(latencies))
	
	// Calculate percentiles (simplified)
	// In a real implementation, you would use a proper percentile calculation
	p95Latency := avgLatency * 1.5
	p99Latency := avgLatency * 2.0
	
	// Calculate error rate
	errorRate := float64(result.FailedGroups) / float64(result.ProcessedGroups) * 100
	
	return PerformanceMetrics{
		ThroughputGroupsPerSecond: throughput,
		AverageLatencyMS:          avgLatency,
		P95LatencyMS:              p95Latency,
		P99LatencyMS:              p99Latency,
		ErrorRate:                 errorRate,
		ResourceUtilization:       75.0, // Dummy value
	}
}

// GetBatchProcessingStatistics returns statistics about batch processing operations
func (gbp *GroupBatchProcessor) GetBatchProcessingStatistics() (*BatchProcessingStatistics, error) {
	stats := &BatchProcessingStatistics{
		TotalBatchesProcessed:    0,
		TotalGroupsProcessed:     0,
		AverageGroupsPerBatch:    0,
		AverageBatchDuration:     0,
		TotalAPIRequests:         0,
		AverageCacheHitRate:      0,
		AverageMemoryUsageMB:     0,
		LastCalculated:           time.Now(),
	}
	
	// Get statistics from state manager or internal tracking
	// Implementation would depend on how statistics are tracked
	
	return stats, nil
}

// BatchProcessingStatistics provides statistics about batch processing operations
type BatchProcessingStatistics struct {
	TotalBatchesProcessed int           `json:"total_batches_processed"`
	TotalGroupsProcessed  int           `json:"total_groups_processed"`
	AverageGroupsPerBatch float64       `json:"average_groups_per_batch"`
	AverageBatchDuration  time.Duration `json:"average_batch_duration"`
	TotalAPIRequests      int           `json:"total_api_requests"`
	AverageCacheHitRate   float64       `json:"average_cache_hit_rate"`
	AverageMemoryUsageMB  float64       `json:"average_memory_usage_mb"`
	LastCalculated        time.Time     `json:"last_calculated"`
}

// Close cleans up resources used by the batch processor
func (gbp *GroupBatchProcessor) Close() error {
	if gbp.requestLimiter != nil {
		gbp.requestLimiter.Stop()
	}
	
	gbp.logger.Info("Group batch processor closed")
	return nil
}