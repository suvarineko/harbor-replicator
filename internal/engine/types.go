package engine

import (
	"context"
	"time"
)

// SyncStatus represents the status of a synchronization operation
type SyncStatus string

const (
	SyncStatusPending    SyncStatus = "pending"
	SyncStatusRunning    SyncStatus = "running"
	SyncStatusCompleted  SyncStatus = "completed"
	SyncStatusFailed     SyncStatus = "failed"
	SyncStatusCancelled  SyncStatus = "cancelled"
	SyncStatusPartial    SyncStatus = "partial"
)

// SyncResult represents the result of a synchronization operation
type SyncResult struct {
	SyncID           string                 `json:"sync_id"`
	ResourceType     string                 `json:"resource_type"`
	SourceInstance   string                 `json:"source_instance"`
	TargetInstance   string                 `json:"target_instance"`
	StartTime        time.Time              `json:"start_time"`
	EndTime          time.Time              `json:"end_time"`
	Duration         time.Duration          `json:"duration"`
	Status           SyncStatus             `json:"status"`
	SuccessCount     int                    `json:"success_count"`
	ErrorCount       int                    `json:"error_count"`
	SkippedCount     int                    `json:"skipped_count"`
	ConflictCount    int                    `json:"conflict_count"`
	Errors           []SyncError            `json:"errors,omitempty"`
	Warnings         []string               `json:"warnings,omitempty"`
	Metrics          SyncMetrics            `json:"metrics"`
	Details          map[string]interface{} `json:"details,omitempty"`
}

// SyncError represents an error that occurred during synchronization
type SyncError struct {
	ResourceType     string                 `json:"resource_type"`
	ResourceID       string                 `json:"resource_id"`
	Operation        string                 `json:"operation"`
	ErrorType        string                 `json:"error_type"`
	ErrorMessage     string                 `json:"error_message"`
	ErrorDetails     map[string]interface{} `json:"error_details,omitempty"`
	Timestamp        time.Time              `json:"timestamp"`
	Retryable        bool                   `json:"retryable"`
	RetryAttempts    int                    `json:"retry_attempts"`
	LastRetryAt      time.Time              `json:"last_retry_at,omitempty"`
}

// SyncMetrics contains performance metrics for synchronization operations
type SyncMetrics struct {
	TotalResources      int           `json:"total_resources"`
	ProcessedResources  int           `json:"processed_resources"`
	AverageLatency      time.Duration `json:"average_latency"`
	Throughput          float64       `json:"throughput"` // resources per second
	MemoryUsage         int64         `json:"memory_usage"`
	CPUUsage            float64       `json:"cpu_usage"`
	NetworkRequests     int           `json:"network_requests"`
	CacheHits           int           `json:"cache_hits"`
	CacheMisses         int           `json:"cache_misses"`
}

// SyncContext provides context information for synchronization operations
type SyncContext struct {
	SyncID           string                 `json:"sync_id"`
	SourceInstance   string                 `json:"source_instance"`
	TargetInstance   string                 `json:"target_instance"`
	ResourceType     string                 `json:"resource_type"`
	DryRun           bool                   `json:"dry_run"`
	StartTime        time.Time              `json:"start_time"`
	Timeout          time.Duration          `json:"timeout"`
	MaxRetries       int                    `json:"max_retries"`
	BatchSize        int                    `json:"batch_size"`
	Concurrency      int                    `json:"concurrency"`
	Tags             map[string]string      `json:"tags,omitempty"`
	Metadata         map[string]interface{} `json:"metadata,omitempty"`
}

// SyncProgress represents the current progress of a synchronization operation
type SyncProgress struct {
	SyncID           string        `json:"sync_id"`
	ResourceType     string        `json:"resource_type"`
	TotalResources   int           `json:"total_resources"`
	ProcessedResources int         `json:"processed_resources"`
	SuccessfulResources int        `json:"successful_resources"`
	FailedResources  int           `json:"failed_resources"`
	SkippedResources int           `json:"skipped_resources"`
	CurrentPhase     string        `json:"current_phase"`
	PercentComplete  float64       `json:"percent_complete"`
	EstimatedTimeLeft time.Duration `json:"estimated_time_left"`
	StartTime        time.Time     `json:"start_time"`
	LastUpdateTime   time.Time     `json:"last_update_time"`
	Details          map[string]interface{} `json:"details,omitempty"`
}

// WorkerPoolConfig defines configuration for the worker pool
type WorkerPoolConfig struct {
	Size            int           `json:"size"`
	QueueSize       int           `json:"queue_size"`
	MaxRetries      int           `json:"max_retries"`
	RetryInterval   time.Duration `json:"retry_interval"`
	GracefulTimeout time.Duration `json:"graceful_timeout"`
	RateLimit       RateLimitConfig `json:"rate_limit"`
}

// RateLimitConfig defines rate limiting configuration
type RateLimitConfig struct {
	RequestsPerSecond float64       `json:"requests_per_second"`
	BurstSize         int           `json:"burst_size"`
	Timeout           time.Duration `json:"timeout"`
}

// SchedulerConfig defines configuration for the sync scheduler
type SchedulerConfig struct {
	Interval        time.Duration `json:"interval"`
	Jitter          time.Duration `json:"jitter"`
	MaxConcurrentSyncs int        `json:"max_concurrent_syncs"`
	EnableImmediateTrigger bool   `json:"enable_immediate_trigger"`
	SkipIfRunning   bool          `json:"skip_if_running"`
}

// SyncJob represents a synchronization job to be executed
type SyncJob struct {
	ID             string                 `json:"id"`
	ResourceType   string                 `json:"resource_type"`
	SourceInstance string                 `json:"source_instance"`
	TargetInstance string                 `json:"target_instance"`
	Priority       int                    `json:"priority"`
	ScheduledAt    time.Time              `json:"scheduled_at"`
	CreatedAt      time.Time              `json:"created_at"`
	MaxRetries     int                    `json:"max_retries"`
	RetryCount     int                    `json:"retry_count"`
	Context        *SyncContext           `json:"context"`
	Options        map[string]interface{} `json:"options,omitempty"`
}

// ResourceSynchronizer defines the interface that all resource synchronizers must implement
type ResourceSynchronizer interface {
	// GetResourceType returns the type of resource this synchronizer handles
	GetResourceType() string
	
	// Sync performs synchronization between source and target instances
	Sync(ctx context.Context, syncCtx *SyncContext) (*SyncResult, error)
	
	// ValidateConfig validates the synchronizer configuration
	ValidateConfig(config interface{}) error
	
	// GetStatus returns the current status of the synchronizer
	GetStatus() SyncStatus
	
	// Stop gracefully stops the synchronizer
	Stop(ctx context.Context) error
}

// WorkerPool defines the interface for managing concurrent workers
type WorkerPool interface {
	// Start initializes and starts the worker pool
	Start(ctx context.Context) error
	
	// Stop gracefully stops the worker pool
	Stop(ctx context.Context) error
	
	// Submit submits a job to the worker pool
	Submit(job *SyncJob) error
	
	// GetStats returns worker pool statistics
	GetStats() WorkerPoolStats
	
	// Resize changes the number of workers
	Resize(size int) error
}

// WorkerPoolStats contains statistics about the worker pool
type WorkerPoolStats struct {
	Size            int           `json:"size"`
	ActiveWorkers   int           `json:"active_workers"`
	QueuedJobs      int           `json:"queued_jobs"`
	CompletedJobs   int           `json:"completed_jobs"`
	FailedJobs      int           `json:"failed_jobs"`
	AverageWaitTime time.Duration `json:"average_wait_time"`
	TotalProcessingTime time.Duration `json:"total_processing_time"`
}

// Scheduler defines the interface for managing sync schedules
type Scheduler interface {
	// Start starts the scheduler
	Start(ctx context.Context) error
	
	// Stop stops the scheduler
	Stop(ctx context.Context) error
	
	// ScheduleSync schedules a sync operation
	ScheduleSync(job *SyncJob) error
	
	// CancelSync cancels a scheduled sync operation
	CancelSync(jobID string) error
	
	// TriggerImmediate triggers an immediate sync
	TriggerImmediate(resourceType string) error
	
	// GetSchedule returns the current schedule
	GetSchedule() []ScheduledJob
	
	// UpdateInterval updates the sync interval
	UpdateInterval(interval time.Duration) error
}

// ScheduledJob represents a scheduled synchronization job
type ScheduledJob struct {
	Job        *SyncJob  `json:"job"`
	NextRun    time.Time `json:"next_run"`
	LastRun    time.Time `json:"last_run,omitempty"`
	RunCount   int       `json:"run_count"`
	Enabled    bool      `json:"enabled"`
}

// SyncOrchestrator defines the interface for orchestrating synchronization operations
type SyncOrchestrator interface {
	// Start starts the orchestrator
	Start(ctx context.Context) error
	
	// Stop stops the orchestrator
	Stop(ctx context.Context) error
	
	// RunSyncCycle executes a complete sync cycle
	RunSyncCycle(ctx context.Context) (*SyncCycleResult, error)
	
	// GetProgress returns the current sync progress
	GetProgress(syncID string) (*SyncProgress, error)
	
	// ListActiveSyncs returns all currently active sync operations
	ListActiveSyncs() []string
	
	// CancelSync cancels a running sync operation
	CancelSync(syncID string) error
}

// SyncCycleResult represents the result of a complete sync cycle
type SyncCycleResult struct {
	CycleID      string                    `json:"cycle_id"`
	StartTime    time.Time                 `json:"start_time"`
	EndTime      time.Time                 `json:"end_time"`
	Duration     time.Duration             `json:"duration"`
	TotalSyncs   int                       `json:"total_syncs"`
	SuccessfulSyncs int                    `json:"successful_syncs"`
	FailedSyncs  int                       `json:"failed_syncs"`
	Results      map[string]*SyncResult    `json:"results"`
	Errors       []SyncError               `json:"errors,omitempty"`
	Metrics      SyncMetrics               `json:"metrics"`
}

// ProgressTracker defines the interface for tracking sync progress
type ProgressTracker interface {
	// StartTracking starts tracking progress for a sync operation
	StartTracking(syncID string, totalResources int) error
	
	// UpdateProgress updates the progress for a sync operation
	UpdateProgress(syncID string, progress *SyncProgress) error
	
	// GetProgress retrieves the current progress for a sync operation
	GetProgress(syncID string) (*SyncProgress, error)
	
	// FinishTracking marks tracking as finished for a sync operation
	FinishTracking(syncID string) error
	
	// ListActiveProgress lists all currently tracked sync operations
	ListActiveProgress() ([]string, error)
	
	// Subscribe to progress updates
	Subscribe(syncID string) (<-chan *SyncProgress, error)
	
	// Unsubscribe from progress updates
	Unsubscribe(syncID string) error
}

// CircuitBreaker defines the interface for circuit breaker functionality
type CircuitBreaker interface {
	// Execute executes a function with circuit breaker protection
	Execute(fn func() error) error
	
	// State returns the current state of the circuit breaker
	State() CircuitBreakerState
	
	// Counts returns the current counts
	Counts() CircuitBreakerCounts
	
	// Name returns the name of the circuit breaker
	Name() string
}

// CircuitBreakerState represents the state of a circuit breaker
type CircuitBreakerState int

const (
	CircuitBreakerClosed CircuitBreakerState = iota
	CircuitBreakerHalfOpen
	CircuitBreakerOpen
)

// CircuitBreakerCounts represents the counts tracked by a circuit breaker
type CircuitBreakerCounts struct {
	Requests             uint32 `json:"requests"`
	TotalSuccesses       uint32 `json:"total_successes"`
	TotalFailures        uint32 `json:"total_failures"`
	ConsecutiveSuccesses uint32 `json:"consecutive_successes"`
	ConsecutiveFailures  uint32 `json:"consecutive_failures"`
}

// HealthChecker defines the interface for health checking
type HealthChecker interface {
	// Check performs a health check
	Check(ctx context.Context) (*HealthStatus, error)
	
	// GetStatus returns the current health status
	GetStatus() *HealthStatus
	
	// RegisterCheck registers a custom health check
	RegisterCheck(name string, check func(ctx context.Context) error)
	
	// UnregisterCheck unregisters a health check
	UnregisterCheck(name string)
}

// HealthStatus represents the health status of a component
type HealthStatus struct {
	Status    string            `json:"status"` // "healthy", "unhealthy", "unknown"
	Message   string            `json:"message,omitempty"`
	Details   map[string]string `json:"details,omitempty"`
	Timestamp time.Time         `json:"timestamp"`
	Duration  time.Duration     `json:"duration"`
}

// MetricsCollector defines the interface for collecting synchronization metrics
type MetricsCollector interface {
	// RecordSyncDuration records the duration of a sync operation
	RecordSyncDuration(resourceType string, duration time.Duration)
	
	// IncSyncCounter increments a sync counter
	IncSyncCounter(resourceType, status string)
	
	// SetGauge sets a gauge metric
	SetGauge(name string, value float64, labels map[string]string)
	
	// RecordHistogram records a histogram metric
	RecordHistogram(name string, value float64, labels map[string]string)
	
	// GetMetrics returns all collected metrics
	GetMetrics() map[string]interface{}
}