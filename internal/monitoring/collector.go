package monitoring

import (
	"context"
	"time"

	"go.uber.org/zap"
)

// MetricsCollector defines the interface for collecting synchronization metrics
type MetricsCollector interface {
	// Sync operation metrics
	RecordSyncStart(ctx context.Context, metadata SyncMetadata) error
	RecordSyncComplete(ctx context.Context, metadata SyncMetadata, result SyncResult) error
	RecordSyncError(ctx context.Context, metadata SyncMetadata, err error) error

	// Resource metrics
	RecordResourceSynced(ctx context.Context, resource ResourceMetadata, operation string) error
	RecordResourceSkipped(ctx context.Context, resource ResourceMetadata, reason string) error
	RecordResourceError(ctx context.Context, resource ResourceMetadata, err error) error

	// Performance metrics
	RecordLatency(ctx context.Context, operation string, duration time.Duration) error
	RecordThroughput(ctx context.Context, resourceType string, count int, duration time.Duration) error

	// Health metrics
	RecordHealthStatus(ctx context.Context, component string, healthy bool) error
	RecordQuotaUsage(ctx context.Context, instance, resourceType string, usage float64) error

	// Custom metrics
	RecordCustomGauge(ctx context.Context, name string, value float64, labels map[string]string) error
	RecordCustomCounter(ctx context.Context, name string, increment float64, labels map[string]string) error
}

// SyncMetadata contains metadata about a sync operation
type SyncMetadata struct {
	SyncID        string            `json:"sync_id"`
	ResourceType  string            `json:"resource_type"`
	Source        string            `json:"source"`
	Target        string            `json:"target"`
	StartTime     time.Time         `json:"start_time"`
	Labels        map[string]string `json:"labels"`
	OperationType string            `json:"operation_type"` // full, incremental, partial
}

// SyncResult contains the result of a sync operation
type SyncResult struct {
	Status         SyncStatus        `json:"status"`
	Duration       time.Duration     `json:"duration"`
	ResourcesTotal int               `json:"resources_total"`
	ResourcesOK    int               `json:"resources_ok"`
	ResourcesError int               `json:"resources_error"`
	ResourcesSkip  int               `json:"resources_skip"`
	Errors         []SyncError       `json:"errors"`
	Summary        string            `json:"summary"`
	Metadata       map[string]string `json:"metadata"`
}

// SyncStatus represents the status of a sync operation
type SyncStatus string

const (
	SyncStatusSuccess    SyncStatus = "success"
	SyncStatusPartial    SyncStatus = "partial"
	SyncStatusFailed     SyncStatus = "failed"
	SyncStatusCancelled  SyncStatus = "cancelled"
	SyncStatusInProgress SyncStatus = "in_progress"
)

// SyncError represents an error during sync
type SyncError struct {
	Type      string    `json:"type"`
	Message   string    `json:"message"`
	Resource  string    `json:"resource"`
	Retryable bool      `json:"retryable"`
	Timestamp time.Time `json:"timestamp"`
}

// ResourceMetadata contains metadata about a resource being synchronized
type ResourceMetadata struct {
	Type       string            `json:"type"`
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Source     string            `json:"source"`
	Target     string            `json:"target"`
	Version    string            `json:"version"`
	Labels     map[string]string `json:"labels"`
	Size       int64             `json:"size"`
	Checksum   string            `json:"checksum"`
	Timestamp  time.Time         `json:"timestamp"`
}

// PrometheusCollector implements MetricsCollector using Prometheus metrics
type PrometheusCollector struct {
	logger        *zap.Logger
	activeSyncs   map[string]time.Time
	syncCounter   int64
}

// NewPrometheusCollector creates a new Prometheus-based metrics collector
func NewPrometheusCollector(logger *zap.Logger) *PrometheusCollector {
	if logger == nil {
		logger = zap.NewNop()
	}

	return &PrometheusCollector{
		logger:      logger,
		activeSyncs: make(map[string]time.Time),
	}
}

// RecordSyncStart records the start of a sync operation
func (pc *PrometheusCollector) RecordSyncStart(ctx context.Context, metadata SyncMetadata) error {
	pc.logger.Info("Sync operation started",
		zap.String("sync_id", metadata.SyncID),
		zap.String("resource_type", metadata.ResourceType),
		zap.String("source", metadata.Source),
		zap.String("target", metadata.Target),
		zap.String("operation_type", metadata.OperationType),
	)

	// Track active sync
	pc.activeSyncs[metadata.SyncID] = metadata.StartTime

	// Update metrics
	SetActiveSyncOperations(float64(len(pc.activeSyncs)))
	RecordSyncOperation("started")

	return nil
}

// RecordSyncComplete records the completion of a sync operation
func (pc *PrometheusCollector) RecordSyncComplete(ctx context.Context, metadata SyncMetadata, result SyncResult) error {
	pc.logger.Info("Sync operation completed",
		zap.String("sync_id", metadata.SyncID),
		zap.String("resource_type", metadata.ResourceType),
		zap.String("status", string(result.Status)),
		zap.Duration("duration", result.Duration),
		zap.Int("resources_total", result.ResourcesTotal),
		zap.Int("resources_ok", result.ResourcesOK),
		zap.Int("resources_error", result.ResourcesError),
		zap.Int("resources_skip", result.ResourcesSkip),
	)

	// Remove from active syncs
	delete(pc.activeSyncs, metadata.SyncID)

	// Update metrics
	SetActiveSyncOperations(float64(len(pc.activeSyncs)))
	RecordSyncOperation(string(result.Status))
	RecordSyncDuration(metadata.Source, metadata.ResourceType, result.Duration)

	// Record resource metrics
	for i := 0; i < result.ResourcesOK; i++ {
		RecordResourceSynced(metadata.ResourceType, metadata.Source, "success")
	}

	// Update last successful sync timestamp if successful
	if result.Status == SyncStatusSuccess || result.Status == SyncStatusPartial {
		SetLastSuccessfulSync(metadata.ResourceType, metadata.Source)
	}

	// Record errors
	for _, syncErr := range result.Errors {
		RecordSyncError(syncErr.Type, metadata.ResourceType, metadata.Source)
	}

	return nil
}

// RecordSyncError records an error during sync operation
func (pc *PrometheusCollector) RecordSyncError(ctx context.Context, metadata SyncMetadata, err error) error {
	pc.logger.Error("Sync operation error",
		zap.String("sync_id", metadata.SyncID),
		zap.String("resource_type", metadata.ResourceType),
		zap.String("source", metadata.Source),
		zap.Error(err),
	)

	// Categorize error type
	errorType := categorizeError(err)
	RecordSyncError(errorType, metadata.ResourceType, metadata.Source)

	return nil
}

// RecordResourceSynced records a successfully synchronized resource
func (pc *PrometheusCollector) RecordResourceSynced(ctx context.Context, resource ResourceMetadata, operation string) error {
	pc.logger.Debug("Resource synchronized",
		zap.String("type", resource.Type),
		zap.String("name", resource.Name),
		zap.String("operation", operation),
		zap.String("source", resource.Source),
		zap.String("target", resource.Target),
	)

	RecordResourceSynced(resource.Type, resource.Source, operation)
	return nil
}

// RecordResourceSkipped records a skipped resource
func (pc *PrometheusCollector) RecordResourceSkipped(ctx context.Context, resource ResourceMetadata, reason string) error {
	pc.logger.Debug("Resource skipped",
		zap.String("type", resource.Type),
		zap.String("name", resource.Name),
		zap.String("reason", reason),
		zap.String("source", resource.Source),
	)

	// We'll use the existing queue size metric to track skipped resources
	// In a real implementation, you might want a dedicated skipped resources metric
	return nil
}

// RecordResourceError records an error processing a resource
func (pc *PrometheusCollector) RecordResourceError(ctx context.Context, resource ResourceMetadata, err error) error {
	pc.logger.Error("Resource processing error",
		zap.String("type", resource.Type),
		zap.String("name", resource.Name),
		zap.String("source", resource.Source),
		zap.Error(err),
	)

	errorType := categorizeError(err)
	RecordSyncError(errorType, resource.Type, resource.Source)

	return nil
}

// RecordLatency records operation latency
func (pc *PrometheusCollector) RecordLatency(ctx context.Context, operation string, duration time.Duration) error {
	pc.logger.Debug("Operation latency recorded",
		zap.String("operation", operation),
		zap.Duration("duration", duration),
	)

	// Use API call latency metric for general latency tracking
	RecordAPICallLatency("harbor", operation, "GET", duration)
	return nil
}

// RecordThroughput records throughput metrics
func (pc *PrometheusCollector) RecordThroughput(ctx context.Context, resourceType string, count int, duration time.Duration) error {
	if duration == 0 {
		return nil
	}

	throughput := float64(count) / duration.Seconds()
	pc.logger.Debug("Throughput recorded",
		zap.String("resource_type", resourceType),
		zap.Int("count", count),
		zap.Duration("duration", duration),
		zap.Float64("throughput", throughput),
	)

	// Record throughput using existing resource metrics
	for i := 0; i < count; i++ {
		RecordResourceSynced(resourceType, "unknown", "throughput")
	}

	return nil
}

// RecordHealthStatus records component health status
func (pc *PrometheusCollector) RecordHealthStatus(ctx context.Context, component string, healthy bool) error {
	pc.logger.Debug("Health status recorded",
		zap.String("component", component),
		zap.Bool("healthy", healthy),
	)

	SetHarborInstanceHealth(component, "component", healthy)
	return nil
}

// RecordQuotaUsage records quota usage metrics
func (pc *PrometheusCollector) RecordQuotaUsage(ctx context.Context, instance, resourceType string, usage float64) error {
	pc.logger.Debug("Quota usage recorded",
		zap.String("instance", instance),
		zap.String("resource_type", resourceType),
		zap.Float64("usage", usage),
	)

	SetResourceQuotaUsage(instance, resourceType, usage)
	return nil
}

// RecordCustomGauge records a custom gauge metric
func (pc *PrometheusCollector) RecordCustomGauge(ctx context.Context, name string, value float64, labels map[string]string) error {
	pc.logger.Debug("Custom gauge recorded",
		zap.String("name", name),
		zap.Float64("value", value),
		zap.Any("labels", labels),
	)

	// For custom gauges, we would need to implement dynamic metric registration
	// For now, we'll log the metric
	return nil
}

// RecordCustomCounter records a custom counter metric
func (pc *PrometheusCollector) RecordCustomCounter(ctx context.Context, name string, increment float64, labels map[string]string) error {
	pc.logger.Debug("Custom counter recorded",
		zap.String("name", name),
		zap.Float64("increment", increment),
		zap.Any("labels", labels),
	)

	// For custom counters, we would need to implement dynamic metric registration
	// For now, we'll log the metric
	return nil
}

// NoOpCollector provides a no-operation implementation of MetricsCollector
type NoOpCollector struct{}

// NewNoOpCollector creates a new no-op metrics collector
func NewNoOpCollector() *NoOpCollector {
	return &NoOpCollector{}
}

// RecordSyncStart is a no-op implementation
func (nc *NoOpCollector) RecordSyncStart(ctx context.Context, metadata SyncMetadata) error {
	return nil
}

// RecordSyncComplete is a no-op implementation
func (nc *NoOpCollector) RecordSyncComplete(ctx context.Context, metadata SyncMetadata, result SyncResult) error {
	return nil
}

// RecordSyncError is a no-op implementation
func (nc *NoOpCollector) RecordSyncError(ctx context.Context, metadata SyncMetadata, err error) error {
	return nil
}

// RecordResourceSynced is a no-op implementation
func (nc *NoOpCollector) RecordResourceSynced(ctx context.Context, resource ResourceMetadata, operation string) error {
	return nil
}

// RecordResourceSkipped is a no-op implementation
func (nc *NoOpCollector) RecordResourceSkipped(ctx context.Context, resource ResourceMetadata, reason string) error {
	return nil
}

// RecordResourceError is a no-op implementation
func (nc *NoOpCollector) RecordResourceError(ctx context.Context, resource ResourceMetadata, err error) error {
	return nil
}

// RecordLatency is a no-op implementation
func (nc *NoOpCollector) RecordLatency(ctx context.Context, operation string, duration time.Duration) error {
	return nil
}

// RecordThroughput is a no-op implementation
func (nc *NoOpCollector) RecordThroughput(ctx context.Context, resourceType string, count int, duration time.Duration) error {
	return nil
}

// RecordHealthStatus is a no-op implementation
func (nc *NoOpCollector) RecordHealthStatus(ctx context.Context, component string, healthy bool) error {
	return nil
}

// RecordQuotaUsage is a no-op implementation
func (nc *NoOpCollector) RecordQuotaUsage(ctx context.Context, instance, resourceType string, usage float64) error {
	return nil
}

// RecordCustomGauge is a no-op implementation
func (nc *NoOpCollector) RecordCustomGauge(ctx context.Context, name string, value float64, labels map[string]string) error {
	return nil
}

// RecordCustomCounter is a no-op implementation
func (nc *NoOpCollector) RecordCustomCounter(ctx context.Context, name string, increment float64, labels map[string]string) error {
	return nil
}

// Helper functions

// categorizeError categorizes an error for metrics purposes
func categorizeError(err error) string {
	if err == nil {
		return "unknown"
	}

	errStr := err.Error()
	switch {
	case contains(errStr, "timeout"):
		return "timeout"
	case contains(errStr, "connection"):
		return "connection"
	case contains(errStr, "authentication"):
		return "auth"
	case contains(errStr, "authorization"):
		return "auth"
	case contains(errStr, "not found"):
		return "not_found"
	case contains(errStr, "conflict"):
		return "conflict"
	case contains(errStr, "validation"):
		return "validation"
	case contains(errStr, "quota"):
		return "quota"
	case contains(errStr, "rate limit"):
		return "rate_limit"
	default:
		return "generic"
	}
}

// contains checks if a string contains a substring (case-insensitive)
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || 
		(len(s) > len(substr) && 
			(s[:len(substr)] == substr || 
			 s[len(s)-len(substr):] == substr ||
			 indexSubstring(s, substr) >= 0)))
}

// indexSubstring finds the index of substring in string
func indexSubstring(s, substr string) int {
	if len(substr) == 0 {
		return 0
	}
	if len(substr) > len(s) {
		return -1
	}
	
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}