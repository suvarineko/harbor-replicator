package monitoring

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"
)

// SyncMetricsIntegration provides integration between sync operations and metrics collection
type SyncMetricsIntegration struct {
	collector MetricsCollector
	logger    *zap.Logger
}

// NewSyncMetricsIntegration creates a new sync metrics integration
func NewSyncMetricsIntegration(collector MetricsCollector, logger *zap.Logger) *SyncMetricsIntegration {
	return &SyncMetricsIntegration{
		collector: collector,
		logger:    logger,
	}
}

// WrapSyncOperation wraps a sync operation with metrics collection
func (smi *SyncMetricsIntegration) WrapSyncOperation(
	ctx context.Context,
	syncID string,
	resourceType string,
	source string,
	target string,
	operationType string,
	operation func(ctx context.Context) error,
) error {
	// Create sync metadata
	metadata := SyncMetadata{
		SyncID:        syncID,
		ResourceType:  resourceType,
		Source:        source,
		Target:        target,
		StartTime:     time.Now(),
		OperationType: operationType,
		Labels: map[string]string{
			"sync_id":        syncID,
			"resource_type":  resourceType,
			"source":         source,
			"target":         target,
			"operation_type": operationType,
		},
	}

	// Record sync start
	if err := smi.collector.RecordSyncStart(ctx, metadata); err != nil {
		smi.logger.Warn("Failed to record sync start", zap.Error(err))
	}

	// Execute the operation
	startTime := time.Now()
	err := operation(ctx)
	duration := time.Since(startTime)

	// Determine result status
	var status SyncStatus
	if err != nil {
		status = SyncStatusFailed
		// Record the error
		if recordErr := smi.collector.RecordSyncError(ctx, metadata, err); recordErr != nil {
			smi.logger.Warn("Failed to record sync error", zap.Error(recordErr))
		}
	} else {
		status = SyncStatusSuccess
	}

	// Create sync result
	result := SyncResult{
		Status:   status,
		Duration: duration,
		Summary:  string(status),
		Metadata: metadata.Labels,
	}

	if err != nil {
		result.Errors = []SyncError{
			{
				Type:      categorizeError(err),
				Message:   err.Error(),
				Resource:  resourceType,
				Retryable: isRetryableError(err),
				Timestamp: time.Now(),
			},
		}
		result.ResourcesError = 1
	} else {
		result.ResourcesOK = 1
	}
	result.ResourcesTotal = 1

	// Record sync completion
	if recordErr := smi.collector.RecordSyncComplete(ctx, metadata, result); recordErr != nil {
		smi.logger.Warn("Failed to record sync complete", zap.Error(recordErr))
	}

	return err
}

// WrapResourceOperation wraps a resource operation with metrics collection
func (smi *SyncMetricsIntegration) WrapResourceOperation(
	ctx context.Context,
	resourceType string,
	resourceID string,
	resourceName string,
	source string,
	target string,
	operation func(ctx context.Context) (string, error), // returns operation type
) error {
	// Create resource metadata
	resource := ResourceMetadata{
		Type:      resourceType,
		ID:        resourceID,
		Name:      resourceName,
		Source:    source,
		Target:    target,
		Timestamp: time.Now(),
		Labels: map[string]string{
			"resource_type": resourceType,
			"resource_id":   resourceID,
			"source":        source,
			"target":        target,
		},
	}

	// Execute the operation
	startTime := time.Now()
	operationType, err := operation(ctx)
	duration := time.Since(startTime)

	// Record latency
	if latencyErr := smi.collector.RecordLatency(ctx, operationType, duration); latencyErr != nil {
		smi.logger.Warn("Failed to record latency", zap.Error(latencyErr))
	}

	if err != nil {
		// Record resource error
		if recordErr := smi.collector.RecordResourceError(ctx, resource, err); recordErr != nil {
			smi.logger.Warn("Failed to record resource error", zap.Error(recordErr))
		}
		return err
	}

	// Record successful resource sync
	if recordErr := smi.collector.RecordResourceSynced(ctx, resource, operationType); recordErr != nil {
		smi.logger.Warn("Failed to record resource synced", zap.Error(recordErr))
	}

	return nil
}

// RecordHealthCheck records a health check result
func (smi *SyncMetricsIntegration) RecordHealthCheck(ctx context.Context, component string, healthy bool) {
	if err := smi.collector.RecordHealthStatus(ctx, component, healthy); err != nil {
		smi.logger.Warn("Failed to record health status", zap.Error(err))
	}
}

// RecordThroughputMetrics records throughput metrics for a batch operation
func (smi *SyncMetricsIntegration) RecordThroughputMetrics(ctx context.Context, resourceType string, count int, duration time.Duration) {
	if err := smi.collector.RecordThroughput(ctx, resourceType, count, duration); err != nil {
		smi.logger.Warn("Failed to record throughput", zap.Error(err))
	}
}

// RecordQuotaMetrics records quota usage metrics
func (smi *SyncMetricsIntegration) RecordQuotaMetrics(ctx context.Context, instance, resourceType string, usage float64) {
	if err := smi.collector.RecordQuotaUsage(ctx, instance, resourceType, usage); err != nil {
		smi.logger.Warn("Failed to record quota usage", zap.Error(err))
	}
}

// Helper functions for metrics integration

// isRetryableError determines if an error is retryable
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()
	// These error types are typically retryable
	retryablePatterns := []string{
		"timeout",
		"connection",
		"temporary",
		"rate limit",
		"service unavailable",
		"internal server error",
		"bad gateway",
		"gateway timeout",
	}

	for _, pattern := range retryablePatterns {
		if contains(errStr, pattern) {
			return true
		}
	}

	return false
}

// InstrumentedSyncWrapper provides a convenient way to add metrics to existing sync functions
type InstrumentedSyncWrapper struct {
	integration *SyncMetricsIntegration
	logger      *zap.Logger
}

// NewInstrumentedSyncWrapper creates a new instrumented sync wrapper
func NewInstrumentedSyncWrapper(integration *SyncMetricsIntegration, logger *zap.Logger) *InstrumentedSyncWrapper {
	return &InstrumentedSyncWrapper{
		integration: integration,
		logger:      logger,
	}
}

// WrapRobotSync wraps robot synchronization operations
func (isw *InstrumentedSyncWrapper) WrapRobotSync(
	ctx context.Context,
	source string,
	target string,
	robotName string,
	syncFunc func(ctx context.Context) error,
) error {
	syncID := fmt.Sprintf("robot-%s-%d", robotName, time.Now().Unix())
	return isw.integration.WrapSyncOperation(
		ctx,
		syncID,
		"robot",
		source,
		target,
		"sync",
		syncFunc,
	)
}

// WrapOIDCGroupSync wraps OIDC group synchronization operations
func (isw *InstrumentedSyncWrapper) WrapOIDCGroupSync(
	ctx context.Context,
	source string,
	target string,
	groupName string,
	syncFunc func(ctx context.Context) error,
) error {
	syncID := fmt.Sprintf("oidc-group-%s-%d", groupName, time.Now().Unix())
	return isw.integration.WrapSyncOperation(
		ctx,
		syncID,
		"oidc_group",
		source,
		target,
		"sync",
		syncFunc,
	)
}

// WrapBatchOperation wraps batch operations with throughput metrics
func (isw *InstrumentedSyncWrapper) WrapBatchOperation(
	ctx context.Context,
	resourceType string,
	batchSize int,
	batchFunc func(ctx context.Context) error,
) error {
	startTime := time.Now()
	err := batchFunc(ctx)
	duration := time.Since(startTime)

	// Record throughput metrics
	isw.integration.RecordThroughputMetrics(ctx, resourceType, batchSize, duration)

	return err
}

// GetCollector returns the underlying metrics collector
func (isw *InstrumentedSyncWrapper) GetCollector() MetricsCollector {
	return isw.integration.collector
}

// RecordCustomMetric is a convenience method for recording custom metrics
func (isw *InstrumentedSyncWrapper) RecordCustomMetric(ctx context.Context, name string, value float64, labels map[string]string) {
	if err := isw.integration.collector.RecordCustomGauge(ctx, name, value, labels); err != nil {
		isw.logger.Warn("Failed to record custom metric", zap.String("name", name), zap.Error(err))
	}
}

// MetricsAwareEngine interface for engines that support metrics collection
type MetricsAwareEngine interface {
	SetMetricsCollector(collector MetricsCollector)
	GetMetricsCollector() MetricsCollector
	RecordSyncMetrics(ctx context.Context, metadata SyncMetadata, result SyncResult) error
}

// DefaultMetricsAwareEngine provides a default implementation of MetricsAwareEngine
type DefaultMetricsAwareEngine struct {
	collector MetricsCollector
	logger    *zap.Logger
}

// NewDefaultMetricsAwareEngine creates a new default metrics-aware engine
func NewDefaultMetricsAwareEngine(logger *zap.Logger) *DefaultMetricsAwareEngine {
	return &DefaultMetricsAwareEngine{
		collector: NewNoOpCollector(),
		logger:    logger,
	}
}

// SetMetricsCollector sets the metrics collector
func (dmae *DefaultMetricsAwareEngine) SetMetricsCollector(collector MetricsCollector) {
	dmae.collector = collector
}

// GetMetricsCollector returns the metrics collector
func (dmae *DefaultMetricsAwareEngine) GetMetricsCollector() MetricsCollector {
	return dmae.collector
}

// RecordSyncMetrics records sync metrics
func (dmae *DefaultMetricsAwareEngine) RecordSyncMetrics(ctx context.Context, metadata SyncMetadata, result SyncResult) error {
	return dmae.collector.RecordSyncComplete(ctx, metadata, result)
}