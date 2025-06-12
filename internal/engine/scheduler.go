package engine

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// SchedulerImpl implements the Scheduler interface
type SchedulerImpl struct {
	config          SchedulerConfig
	workerPool      WorkerPool
	scheduledJobs   map[string]*ScheduledJob
	ticker          *time.Ticker
	triggerChan     chan string
	ctx             context.Context
	cancel          context.CancelFunc
	wg              sync.WaitGroup
	mu              sync.RWMutex
	started         int32
	stopped         int32
	stats           *schedulerStats
	nextJobID       int64
	activeSyncs     map[string]bool
	activeSyncsMu   sync.RWMutex
}

// schedulerStats tracks scheduler statistics
type schedulerStats struct {
	scheduledJobs    int64
	completedJobs    int64
	failedJobs       int64
	lastScheduleTime int64 // Unix timestamp in nanoseconds
	nextScheduleTime int64 // Unix timestamp in nanoseconds
}

// NewScheduler creates a new scheduler instance
func NewScheduler(config SchedulerConfig, workerPool WorkerPool) *SchedulerImpl {
	if config.Interval <= 0 {
		config.Interval = 5 * time.Minute
	}
	if config.Jitter < 0 {
		config.Jitter = 0
	}
	if config.MaxConcurrentSyncs <= 0 {
		config.MaxConcurrentSyncs = 3
	}

	return &SchedulerImpl{
		config:        config,
		workerPool:    workerPool,
		scheduledJobs: make(map[string]*ScheduledJob),
		triggerChan:   make(chan string, 100),
		stats:         &schedulerStats{},
		activeSyncs:   make(map[string]bool),
	}
}

// Start starts the scheduler
func (s *SchedulerImpl) Start(ctx context.Context) error {
	if !atomic.CompareAndSwapInt32(&s.started, 0, 1) {
		return fmt.Errorf("scheduler already started")
	}

	s.ctx, s.cancel = context.WithCancel(ctx)
	
	// Calculate next schedule time with jitter
	nextSchedule := s.calculateNextScheduleTime()
	atomic.StoreInt64(&s.stats.nextScheduleTime, nextSchedule.UnixNano())
	
	// Start scheduler loop
	s.wg.Add(1)
	go s.schedulerLoop()

	// Start trigger handler
	s.wg.Add(1)
	go s.triggerHandler()

	return nil
}

// Stop stops the scheduler
func (s *SchedulerImpl) Stop(ctx context.Context) error {
	if !atomic.CompareAndSwapInt32(&s.stopped, 0, 1) {
		return fmt.Errorf("scheduler already stopped")
	}

	s.cancel()
	
	// Wait for goroutines to finish
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	// Wait for graceful shutdown or timeout
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("scheduler shutdown timeout")
	}
}

// ScheduleSync schedules a sync operation
func (s *SchedulerImpl) ScheduleSync(job *SyncJob) error {
	if atomic.LoadInt32(&s.stopped) == 1 {
		return fmt.Errorf("scheduler is stopped")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	scheduledJob := &ScheduledJob{
		Job:     job,
		NextRun: job.ScheduledAt,
		Enabled: true,
	}

	s.scheduledJobs[job.ID] = scheduledJob
	atomic.AddInt64(&s.stats.scheduledJobs, 1)

	return nil
}

// CancelSync cancels a scheduled sync operation
func (s *SchedulerImpl) CancelSync(jobID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.scheduledJobs[jobID]; !exists {
		return fmt.Errorf("job %s not found", jobID)
	}

	delete(s.scheduledJobs, jobID)
	atomic.AddInt64(&s.stats.scheduledJobs, -1)

	return nil
}

// TriggerImmediate triggers an immediate sync
func (s *SchedulerImpl) TriggerImmediate(resourceType string) error {
	if atomic.LoadInt32(&s.stopped) == 1 {
		return fmt.Errorf("scheduler is stopped")
	}

	if !s.config.EnableImmediateTrigger {
		return fmt.Errorf("immediate triggers are disabled")
	}

	select {
	case s.triggerChan <- resourceType:
		return nil
	default:
		return fmt.Errorf("trigger channel is full")
	}
}

// GetSchedule returns the current schedule
func (s *SchedulerImpl) GetSchedule() []ScheduledJob {
	s.mu.RLock()
	defer s.mu.RUnlock()

	schedule := make([]ScheduledJob, 0, len(s.scheduledJobs))
	for _, job := range s.scheduledJobs {
		schedule = append(schedule, *job)
	}

	// Sort by next run time
	sort.Slice(schedule, func(i, j int) bool {
		return schedule[i].NextRun.Before(schedule[j].NextRun)
	})

	return schedule
}

// UpdateInterval updates the sync interval
func (s *SchedulerImpl) UpdateInterval(interval time.Duration) error {
	if interval <= 0 {
		return fmt.Errorf("interval must be positive")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.config.Interval = interval

	// If ticker is running, restart it with new interval
	if s.ticker != nil {
		s.ticker.Stop()
		s.ticker = time.NewTicker(interval)
	}

	// Update next schedule time
	nextSchedule := s.calculateNextScheduleTime()
	atomic.StoreInt64(&s.stats.nextScheduleTime, nextSchedule.UnixNano())

	return nil
}

// schedulerLoop is the main scheduling loop
func (s *SchedulerImpl) schedulerLoop() {
	defer s.wg.Done()

	// Create ticker with jitter
	nextTick := s.calculateNextScheduleTime()
	initialDelay := time.Until(nextTick)
	
	timer := time.NewTimer(initialDelay)
	defer timer.Stop()

	for {
		select {
		case <-timer.C:
			s.executePendingJobs()
			
			// Schedule next execution
			nextTick = s.calculateNextScheduleTime()
			timer.Reset(time.Until(nextTick))
			atomic.StoreInt64(&s.stats.nextScheduleTime, nextTick.UnixNano())
			
		case <-s.ctx.Done():
			return
		}
	}
}

// triggerHandler handles immediate sync triggers
func (s *SchedulerImpl) triggerHandler() {
	defer s.wg.Done()

	for {
		select {
		case resourceType := <-s.triggerChan:
			s.executeImmediateTrigger(resourceType)
		case <-s.ctx.Done():
			return
		}
	}
}

// calculateNextScheduleTime calculates the next schedule time with jitter
func (s *SchedulerImpl) calculateNextScheduleTime() time.Time {
	baseTime := time.Now().Add(s.config.Interval)
	
	if s.config.Jitter > 0 {
		// Add random jitter to prevent thundering herd
		jitterNs := int64(s.config.Jitter)
		randomJitter := time.Duration(rand.Int63n(jitterNs))
		baseTime = baseTime.Add(randomJitter)
	}
	
	return baseTime
}

// executePendingJobs executes all pending scheduled jobs
func (s *SchedulerImpl) executePendingJobs() {
	now := time.Now()
	atomic.StoreInt64(&s.stats.lastScheduleTime, now.UnixNano())

	s.mu.RLock()
	pendingJobs := make([]*ScheduledJob, 0)
	
	for _, scheduledJob := range s.scheduledJobs {
		if scheduledJob.Enabled && scheduledJob.NextRun.Before(now) {
			pendingJobs = append(pendingJobs, scheduledJob)
		}
	}
	s.mu.RUnlock()

	// Check if we should skip execution due to already running syncs
	if s.config.SkipIfRunning && s.hasActiveSyncs() && len(pendingJobs) > 0 {
		// Skip this execution but update next run times
		s.updateNextRunTimes(pendingJobs)
		return
	}

	// Check concurrent sync limit
	activeSyncCount := s.getActiveSyncCount()
	if activeSyncCount >= s.config.MaxConcurrentSyncs {
		return
	}

	// Execute jobs up to the concurrent limit
	jobsToExecute := len(pendingJobs)
	if activeSyncCount+jobsToExecute > s.config.MaxConcurrentSyncs {
		jobsToExecute = s.config.MaxConcurrentSyncs - activeSyncCount
	}

	for i := 0; i < jobsToExecute; i++ {
		s.executeScheduledJob(pendingJobs[i])
	}
}

// executeScheduledJob executes a single scheduled job
func (s *SchedulerImpl) executeScheduledJob(scheduledJob *ScheduledJob) {
	jobID := scheduledJob.Job.ID
	
	// Mark sync as active
	s.activeSyncsMu.Lock()
	s.activeSyncs[jobID] = true
	s.activeSyncsMu.Unlock()

	// Submit job to worker pool
	err := s.workerPool.Submit(scheduledJob.Job)
	if err != nil {
		// Job submission failed
		s.activeSyncsMu.Lock()
		delete(s.activeSyncs, jobID)
		s.activeSyncsMu.Unlock()
		
		atomic.AddInt64(&s.stats.failedJobs, 1)
		return
	}

	// Update scheduled job
	s.mu.Lock()
	scheduledJob.LastRun = time.Now()
	scheduledJob.RunCount++
	scheduledJob.NextRun = s.calculateNextRunTime(scheduledJob)
	s.mu.Unlock()

	// Start a goroutine to track job completion
	go s.trackJobCompletion(jobID)
}

// executeImmediateTrigger executes an immediate sync trigger
func (s *SchedulerImpl) executeImmediateTrigger(resourceType string) {
	// Check concurrent sync limit
	if s.getActiveSyncCount() >= s.config.MaxConcurrentSyncs {
		return
	}

	// Create immediate job
	jobID := s.generateJobID()
	job := &SyncJob{
		ID:           jobID,
		ResourceType: resourceType,
		Priority:     1, // High priority for immediate triggers
		ScheduledAt:  time.Now(),
		CreatedAt:    time.Now(),
		MaxRetries:   3,
		Context: &SyncContext{
			SyncID:       jobID,
			ResourceType: resourceType,
			DryRun:       false,
			StartTime:    time.Now(),
			Timeout:      30 * time.Minute,
			MaxRetries:   3,
			BatchSize:    100,
			Concurrency:  5,
		},
	}

	// Mark sync as active
	s.activeSyncsMu.Lock()
	s.activeSyncs[jobID] = true
	s.activeSyncsMu.Unlock()

	// Submit job to worker pool
	err := s.workerPool.Submit(job)
	if err != nil {
		// Job submission failed
		s.activeSyncsMu.Lock()
		delete(s.activeSyncs, jobID)
		s.activeSyncsMu.Unlock()
		
		atomic.AddInt64(&s.stats.failedJobs, 1)
		return
	}

	// Start a goroutine to track job completion
	go s.trackJobCompletion(jobID)
}

// trackJobCompletion tracks the completion of a job and updates statistics
func (s *SchedulerImpl) trackJobCompletion(jobID string) {
	// In a real implementation, this would:
	// 1. Monitor the job status through the worker pool or state manager
	// 2. Update statistics when the job completes
	// 3. Remove the job from active syncs
	
	// For now, we'll simulate job completion after a delay
	time.Sleep(5 * time.Second) // Simulate job execution time
	
	s.activeSyncsMu.Lock()
	delete(s.activeSyncs, jobID)
	s.activeSyncsMu.Unlock()
	
	atomic.AddInt64(&s.stats.completedJobs, 1)
}

// calculateNextRunTime calculates the next run time for a scheduled job
func (s *SchedulerImpl) calculateNextRunTime(scheduledJob *ScheduledJob) time.Time {
	// For now, we'll use the global interval
	// In a real implementation, this could support per-job intervals
	return time.Now().Add(s.config.Interval)
}

// updateNextRunTimes updates the next run times for skipped jobs
func (s *SchedulerImpl) updateNextRunTimes(jobs []*ScheduledJob) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, job := range jobs {
		job.NextRun = s.calculateNextRunTime(job)
	}
}

// hasActiveSyncs checks if there are any active sync operations
func (s *SchedulerImpl) hasActiveSyncs() bool {
	s.activeSyncsMu.RLock()
	defer s.activeSyncsMu.RUnlock()
	
	return len(s.activeSyncs) > 0
}

// getActiveSyncCount returns the number of active sync operations
func (s *SchedulerImpl) getActiveSyncCount() int {
	s.activeSyncsMu.RLock()
	defer s.activeSyncsMu.RUnlock()
	
	return len(s.activeSyncs)
}

// generateJobID generates a unique job ID
func (s *SchedulerImpl) generateJobID() string {
	id := atomic.AddInt64(&s.nextJobID, 1)
	return fmt.Sprintf("sync-job-%d-%d", time.Now().Unix(), id)
}

// GetStats returns scheduler statistics
func (s *SchedulerImpl) GetStats() *SchedulerStats {
	totalJobs := atomic.LoadInt64(&s.stats.completedJobs) + atomic.LoadInt64(&s.stats.failedJobs)
	var avgInterval time.Duration
	if totalJobs > 0 {
		avgInterval = s.config.Interval
	}

	lastScheduleTime := atomic.LoadInt64(&s.stats.lastScheduleTime)
	nextScheduleTime := atomic.LoadInt64(&s.stats.nextScheduleTime)

	return &SchedulerStats{
		ScheduledJobs:    int(atomic.LoadInt64(&s.stats.scheduledJobs)),
		CompletedJobs:    atomic.LoadInt64(&s.stats.completedJobs),
		FailedJobs:       atomic.LoadInt64(&s.stats.failedJobs),
		AverageInterval:  avgInterval,
		LastScheduleTime: time.Unix(0, lastScheduleTime),
		NextScheduleTime: time.Unix(0, nextScheduleTime),
	}
}