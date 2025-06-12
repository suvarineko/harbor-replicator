package engine

import (
	"context"
	"time"

	"harbor-replicator/pkg/config"
	"harbor-replicator/pkg/state"
)

// SyncEngine defines the interface for the main synchronization engine
type SyncEngine interface {
	// Lifecycle management
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Restart(ctx context.Context) error
	
	// Sync operations
	RunSyncCycle(ctx context.Context) (*SyncCycleResult, error)
	TriggerSync(resourceType string) error
	CancelSync(syncID string) error
	
	// Configuration management
	UpdateConfig(config *config.ReplicatorConfig) error
	GetConfig() *config.ReplicatorConfig
	ValidateConfig(config *config.ReplicatorConfig) error
	
	// Status and monitoring
	GetStatus() *EngineStatus
	GetProgress(syncID string) (*SyncProgress, error)
	ListActiveSyncs() []string
	GetMetrics() map[string]interface{}
	
	// Health checking
	HealthCheck(ctx context.Context) (*HealthStatus, error)
	
	// Resource synchronizer management
	RegisterSynchronizer(synchronizer ResourceSynchronizer) error
	UnregisterSynchronizer(resourceType string) error
	GetSynchronizer(resourceType string) (ResourceSynchronizer, error)
	ListSynchronizers() []string
}

// EngineStatus represents the current status of the sync engine
type EngineStatus struct {
	State                EngineState           `json:"state"`
	StartTime            time.Time             `json:"start_time,omitempty"`
	LastSyncTime         time.Time             `json:"last_sync_time,omitempty"`
	NextSyncTime         time.Time             `json:"next_sync_time,omitempty"`
	ActiveSyncs          int                   `json:"active_syncs"`
	TotalSyncsCompleted  int64                 `json:"total_syncs_completed"`
	TotalSyncsFailed     int64                 `json:"total_syncs_failed"`
	RegisteredSynchronizers []string           `json:"registered_synchronizers"`
	WorkerPoolStats      *WorkerPoolStats      `json:"worker_pool_stats,omitempty"`
	SchedulerStats       *SchedulerStats       `json:"scheduler_stats,omitempty"`
	ResourceStatuses     map[string]SyncStatus `json:"resource_statuses"`
	LastError            string                `json:"last_error,omitempty"`
	Version              string                `json:"version"`
	Uptime               time.Duration         `json:"uptime"`
}

// EngineState represents the state of the sync engine
type EngineState string

const (
	EngineStateInitializing EngineState = "initializing"
	EngineStateStarting     EngineState = "starting"
	EngineStateRunning      EngineState = "running"
	EngineStateStopping     EngineState = "stopping"
	EngineStateStopped      EngineState = "stopped"
	EngineStateError        EngineState = "error"
)

// SchedulerStats contains statistics about the scheduler
type SchedulerStats struct {
	ScheduledJobs    int       `json:"scheduled_jobs"`
	CompletedJobs    int64     `json:"completed_jobs"`
	FailedJobs       int64     `json:"failed_jobs"`
	AverageInterval  time.Duration `json:"average_interval"`
	LastScheduleTime time.Time `json:"last_schedule_time"`
	NextScheduleTime time.Time `json:"next_schedule_time"`
}

// SyncEngineConfig contains configuration specific to the sync engine
type SyncEngineConfig struct {
	// Core configuration
	Config           *config.ReplicatorConfig `json:"config"`
	
	// Engine-specific settings
	MaxConcurrentSyncs   int           `json:"max_concurrent_syncs"`
	SyncTimeout          time.Duration `json:"sync_timeout"`
	ShutdownTimeout      time.Duration `json:"shutdown_timeout"`
	HealthCheckInterval  time.Duration `json:"health_check_interval"`
	
	// Worker pool configuration
	WorkerPool           WorkerPoolConfig `json:"worker_pool"`
	
	// Scheduler configuration
	Scheduler            SchedulerConfig `json:"scheduler"`
	
	// Circuit breaker configuration
	CircuitBreaker       CircuitBreakerConfig `json:"circuit_breaker"`
	
	// Progress tracking configuration
	ProgressTracking     ProgressTrackingConfig `json:"progress_tracking"`
	
	// Metrics configuration
	Metrics              MetricsConfig `json:"metrics"`
}

// CircuitBreakerConfig defines circuit breaker configuration
type CircuitBreakerConfig struct {
	Enabled              bool          `json:"enabled"`
	FailureThreshold     uint32        `json:"failure_threshold"`
	SuccessThreshold     uint32        `json:"success_threshold"`
	Timeout              time.Duration `json:"timeout"`
	MaxRequests          uint32        `json:"max_requests"`
	Interval             time.Duration `json:"interval"`
}

// ProgressTrackingConfig defines progress tracking configuration
type ProgressTrackingConfig struct {
	Enabled              bool          `json:"enabled"`
	UpdateInterval       time.Duration `json:"update_interval"`
	RetentionDuration    time.Duration `json:"retention_duration"`
	PersistProgress      bool          `json:"persist_progress"`
	EnableNotifications  bool          `json:"enable_notifications"`
}

// MetricsConfig defines metrics collection configuration
type MetricsConfig struct {
	Enabled              bool          `json:"enabled"`
	CollectionInterval   time.Duration `json:"collection_interval"`
	RetentionDuration    time.Duration `json:"retention_duration"`
	EnablePrometheus     bool          `json:"enable_prometheus"`
	EnableCustomMetrics  bool          `json:"enable_custom_metrics"`
}

// SyncEngineOptions contains options for creating a new sync engine
type SyncEngineOptions struct {
	Config           *SyncEngineConfig    `json:"config"`
	StateManager     *state.StateManager  `json:"state_manager,omitempty"`
	Logger           interface{}          `json:"logger,omitempty"` // Should be *zap.Logger
	MetricsCollector MetricsCollector     `json:"metrics_collector,omitempty"`
	CircuitBreakers  map[string]CircuitBreaker `json:"circuit_breakers,omitempty"`
}

// NewSyncEngine creates a new sync engine instance
func NewSyncEngine(options *SyncEngineOptions) (SyncEngine, error) {
	// This will be implemented when we create the concrete implementation
	return nil, nil
}

// DefaultSyncEngineConfig returns a default configuration for the sync engine
func DefaultSyncEngineConfig() *SyncEngineConfig {
	return &SyncEngineConfig{
		MaxConcurrentSyncs:  5,
		SyncTimeout:         30 * time.Minute,
		ShutdownTimeout:     30 * time.Second,
		HealthCheckInterval: 30 * time.Second,
		
		WorkerPool: WorkerPoolConfig{
			Size:            10,
			QueueSize:       100,
			MaxRetries:      3,
			RetryInterval:   5 * time.Second,
			GracefulTimeout: 30 * time.Second,
			RateLimit: RateLimitConfig{
				RequestsPerSecond: 10.0,
				BurstSize:         20,
				Timeout:           10 * time.Second,
			},
		},
		
		Scheduler: SchedulerConfig{
			Interval:               5 * time.Minute,
			Jitter:                 30 * time.Second,
			MaxConcurrentSyncs:     3,
			EnableImmediateTrigger: true,
			SkipIfRunning:          true,
		},
		
		CircuitBreaker: CircuitBreakerConfig{
			Enabled:          true,
			FailureThreshold: 5,
			SuccessThreshold: 3,
			Timeout:          60 * time.Second,
			MaxRequests:      10,
			Interval:         30 * time.Second,
		},
		
		ProgressTracking: ProgressTrackingConfig{
			Enabled:             true,
			UpdateInterval:      5 * time.Second,
			RetentionDuration:   24 * time.Hour,
			PersistProgress:     true,
			EnableNotifications: false,
		},
		
		Metrics: MetricsConfig{
			Enabled:             true,
			CollectionInterval:  10 * time.Second,
			RetentionDuration:   7 * 24 * time.Hour,
			EnablePrometheus:    true,
			EnableCustomMetrics: true,
		},
	}
}