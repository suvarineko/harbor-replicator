package engine

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

// WorkerPoolImpl implements the WorkerPool interface
type WorkerPoolImpl struct {
	config          WorkerPoolConfig
	workers         []*worker
	jobQueue        chan *SyncJob
	resultQueue     chan *jobResult
	rateLimiter     *rate.Limiter
	stats           *workerPoolStats
	wg              sync.WaitGroup
	ctx             context.Context
	cancel          context.CancelFunc
	started         int32
	stopped         int32
	mu              sync.RWMutex
}

// worker represents a single worker in the pool
type worker struct {
	id          int
	pool        *WorkerPoolImpl
	jobQueue    <-chan *SyncJob
	resultQueue chan<- *jobResult
	quit        chan bool
	active      int32
	ctx         context.Context
}

// jobResult represents the result of a job execution
type jobResult struct {
	job       *SyncJob
	result    *SyncResult
	err       error
	duration  time.Duration
	timestamp time.Time
}

// workerPoolStats implements WorkerPoolStats with atomic counters
type workerPoolStats struct {
	size                int64
	activeWorkers       int64
	queuedJobs          int64
	completedJobs       int64
	failedJobs          int64
	totalWaitTime       int64 // in nanoseconds
	totalProcessingTime int64 // in nanoseconds
	jobsSubmitted       int64
}

// NewWorkerPool creates a new worker pool with the given configuration
func NewWorkerPool(config WorkerPoolConfig) *WorkerPoolImpl {
	if config.Size <= 0 {
		config.Size = 5
	}
	if config.QueueSize <= 0 {
		config.QueueSize = 100
	}
	if config.MaxRetries <= 0 {
		config.MaxRetries = 3
	}
	if config.RetryInterval <= 0 {
		config.RetryInterval = 5 * time.Second
	}
	if config.GracefulTimeout <= 0 {
		config.GracefulTimeout = 30 * time.Second
	}

	var rateLimiter *rate.Limiter
	if config.RateLimit.RequestsPerSecond > 0 {
		rateLimiter = rate.NewLimiter(
			rate.Limit(config.RateLimit.RequestsPerSecond),
			config.RateLimit.BurstSize,
		)
	}

	return &WorkerPoolImpl{
		config:      config,
		jobQueue:    make(chan *SyncJob, config.QueueSize),
		resultQueue: make(chan *jobResult, config.QueueSize),
		rateLimiter: rateLimiter,
		stats: &workerPoolStats{
			size: int64(config.Size),
		},
	}
}

// Start initializes and starts the worker pool
func (wp *WorkerPoolImpl) Start(ctx context.Context) error {
	if !atomic.CompareAndSwapInt32(&wp.started, 0, 1) {
		return fmt.Errorf("worker pool already started")
	}

	wp.ctx, wp.cancel = context.WithCancel(ctx)
	wp.workers = make([]*worker, wp.config.Size)

	// Start workers
	for i := 0; i < wp.config.Size; i++ {
		wp.workers[i] = &worker{
			id:          i,
			pool:        wp,
			jobQueue:    wp.jobQueue,
			resultQueue: wp.resultQueue,
			quit:        make(chan bool),
			ctx:         wp.ctx,
		}
		wp.wg.Add(1)
		go wp.workers[i].start()
	}

	// Start result processor
	wp.wg.Add(1)
	go wp.processResults()

	return nil
}

// Stop gracefully stops the worker pool
func (wp *WorkerPoolImpl) Stop(ctx context.Context) error {
	if !atomic.CompareAndSwapInt32(&wp.stopped, 0, 1) {
		return fmt.Errorf("worker pool already stopped")
	}

	// Create a timeout context for graceful shutdown
	shutdownCtx, shutdownCancel := context.WithTimeout(ctx, wp.config.GracefulTimeout)
	defer shutdownCancel()

	// Signal all workers to stop
	for _, worker := range wp.workers {
		close(worker.quit)
	}

	// Wait for workers to finish or timeout
	done := make(chan struct{})
	go func() {
		wp.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// All workers stopped gracefully
	case <-shutdownCtx.Done():
		// Timeout reached, force stop
		wp.cancel()
		// Give a small grace period for forced shutdown
		forceCtx, forceCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer forceCancel()
		
		select {
		case <-done:
		case <-forceCtx.Done():
			return fmt.Errorf("worker pool failed to stop within timeout")
		}
	}

	// Close channels
	close(wp.jobQueue)
	close(wp.resultQueue)

	return nil
}

// Submit submits a job to the worker pool
func (wp *WorkerPoolImpl) Submit(job *SyncJob) error {
	if atomic.LoadInt32(&wp.stopped) == 1 {
		return fmt.Errorf("worker pool is stopped")
	}

	// Apply rate limiting if configured
	if wp.rateLimiter != nil {
		rateLimitCtx := wp.ctx
		if wp.config.RateLimit.Timeout > 0 {
			var cancel context.CancelFunc
			rateLimitCtx, cancel = context.WithTimeout(wp.ctx, wp.config.RateLimit.Timeout)
			defer cancel()
		}

		if err := wp.rateLimiter.Wait(rateLimitCtx); err != nil {
			return fmt.Errorf("rate limit exceeded: %w", err)
		}
	}

	select {
	case wp.jobQueue <- job:
		atomic.AddInt64(&wp.stats.queuedJobs, 1)
		atomic.AddInt64(&wp.stats.jobsSubmitted, 1)
		return nil
	case <-wp.ctx.Done():
		return fmt.Errorf("worker pool context cancelled")
	default:
		return fmt.Errorf("job queue is full")
	}
}

// GetStats returns worker pool statistics
func (wp *WorkerPoolImpl) GetStats() WorkerPoolStats {
	wp.mu.RLock()
	defer wp.mu.RUnlock()

	totalJobs := atomic.LoadInt64(&wp.stats.completedJobs) + atomic.LoadInt64(&wp.stats.failedJobs)
	var avgWaitTime, avgProcessingTime time.Duration

	if totalJobs > 0 {
		avgWaitTime = time.Duration(atomic.LoadInt64(&wp.stats.totalWaitTime) / totalJobs)
		avgProcessingTime = time.Duration(atomic.LoadInt64(&wp.stats.totalProcessingTime) / totalJobs)
	}

	return WorkerPoolStats{
		Size:                int(atomic.LoadInt64(&wp.stats.size)),
		ActiveWorkers:       int(atomic.LoadInt64(&wp.stats.activeWorkers)),
		QueuedJobs:          int(atomic.LoadInt64(&wp.stats.queuedJobs)),
		CompletedJobs:       int(atomic.LoadInt64(&wp.stats.completedJobs)),
		FailedJobs:          int(atomic.LoadInt64(&wp.stats.failedJobs)),
		AverageWaitTime:     avgWaitTime,
		TotalProcessingTime: avgProcessingTime,
	}
}

// Resize changes the number of workers
func (wp *WorkerPoolImpl) Resize(size int) error {
	if size <= 0 {
		return fmt.Errorf("worker pool size must be positive")
	}

	wp.mu.Lock()
	defer wp.mu.Unlock()

	currentSize := len(wp.workers)
	if size == currentSize {
		return nil // No change needed
	}

	if size > currentSize {
		// Add new workers
		for i := currentSize; i < size; i++ {
			worker := &worker{
				id:          i,
				pool:        wp,
				jobQueue:    wp.jobQueue,
				resultQueue: wp.resultQueue,
				quit:        make(chan bool),
				ctx:         wp.ctx,
			}
			wp.workers = append(wp.workers, worker)
			wp.wg.Add(1)
			go worker.start()
		}
	} else {
		// Remove workers
		for i := size; i < currentSize; i++ {
			close(wp.workers[i].quit)
		}
		wp.workers = wp.workers[:size]
	}

	atomic.StoreInt64(&wp.stats.size, int64(size))
	wp.config.Size = size

	return nil
}

// start begins the worker's job processing loop
func (w *worker) start() {
	defer w.pool.wg.Done()

	for {
		select {
		case job := <-w.jobQueue:
			if job == nil {
				return // Channel closed
			}
			w.processJob(job)
			
		case <-w.quit:
			return
			
		case <-w.ctx.Done():
			return
		}
	}
}

// processJob processes a single job with retry logic
func (w *worker) processJob(job *SyncJob) {
	atomic.AddInt64(&w.pool.stats.activeWorkers, 1)
	atomic.AddInt64(&w.pool.stats.queuedJobs, -1)
	atomic.StoreInt32(&w.active, 1)
	
	defer func() {
		atomic.AddInt64(&w.pool.stats.activeWorkers, -1)
		atomic.StoreInt32(&w.active, 0)
	}()

	startTime := time.Now()
	waitTime := startTime.Sub(job.ScheduledAt)
	
	var result *SyncResult
	var err error
	
	// Execute job with retry logic
	for attempt := 0; attempt <= w.pool.config.MaxRetries; attempt++ {
		if attempt > 0 {
			// Wait before retry
			select {
			case <-time.After(w.pool.config.RetryInterval):
			case <-w.ctx.Done():
				return
			}
		}
		
		jobCtx := w.ctx
		if job.Context != nil && job.Context.Timeout > 0 {
			var cancel context.CancelFunc
			jobCtx, cancel = context.WithTimeout(w.ctx, job.Context.Timeout)
			defer cancel()
		}
		
		result, err = w.executeJob(jobCtx, job)
		if err == nil || !isRetryableError(err) {
			break
		}
		
		job.RetryCount = attempt + 1
	}

	duration := time.Since(startTime)
	
	// Update statistics
	atomic.AddInt64(&w.pool.stats.totalWaitTime, waitTime.Nanoseconds())
	atomic.AddInt64(&w.pool.stats.totalProcessingTime, duration.Nanoseconds())
	
	if err != nil {
		atomic.AddInt64(&w.pool.stats.failedJobs, 1)
	} else {
		atomic.AddInt64(&w.pool.stats.completedJobs, 1)
	}

	// Send result
	jobResult := &jobResult{
		job:       job,
		result:    result,
		err:       err,
		duration:  duration,
		timestamp: time.Now(),
	}

	select {
	case w.resultQueue <- jobResult:
	case <-w.ctx.Done():
		return
	default:
		// Result queue is full, log and continue
		// In a real implementation, this should be logged
	}
}

// executeJob executes the actual job logic
func (w *worker) executeJob(ctx context.Context, job *SyncJob) (*SyncResult, error) {
	// This is a placeholder - in the real implementation, this would call
	// the appropriate resource synchronizer based on job.ResourceType
	
	// For now, we'll create a mock result
	result := &SyncResult{
		SyncID:         job.ID,
		ResourceType:   job.ResourceType,
		SourceInstance: job.SourceInstance,
		TargetInstance: job.TargetInstance,
		StartTime:      time.Now(),
		Status:         SyncStatusCompleted,
		SuccessCount:   1,
		ErrorCount:     0,
		SkippedCount:   0,
		ConflictCount:  0,
		Errors:         []SyncError{},
		Warnings:       []string{},
		Metrics: SyncMetrics{
			TotalResources:     1,
			ProcessedResources: 1,
			AverageLatency:     100 * time.Millisecond,
			Throughput:         10.0,
		},
	}
	
	result.EndTime = time.Now()
	result.Duration = result.EndTime.Sub(result.StartTime)
	
	return result, nil
}

// processResults processes job results
func (wp *WorkerPoolImpl) processResults() {
	defer wp.wg.Done()

	for {
		select {
		case result := <-wp.resultQueue:
			if result == nil {
				return // Channel closed
			}
			wp.handleJobResult(result)
			
		case <-wp.ctx.Done():
			return
		}
	}
}

// handleJobResult processes a job result
func (wp *WorkerPoolImpl) handleJobResult(result *jobResult) {
	// In a real implementation, this would:
	// 1. Update state manager with sync results
	// 2. Send metrics to metrics collector
	// 3. Update progress tracking
	// 4. Handle error reporting and alerting
	// 5. Trigger any post-sync hooks
	
	// For now, we'll just log the result (in a real implementation)
	// fmt.Printf("Job %s completed: success=%v, duration=%v\n", 
	//     result.job.ID, result.err == nil, result.duration)
}

// isRetryableError determines if an error is retryable
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	
	// In a real implementation, this would check for specific error types
	// that indicate temporary failures (network timeouts, rate limits, etc.)
	
	// For now, we'll consider all errors as potentially retryable
	return true
}