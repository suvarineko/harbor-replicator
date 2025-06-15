package sync

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// ObservabilityConfig defines configuration for observability features
type ObservabilityConfig struct {
	MetricsEnabled           bool          `yaml:"metrics_enabled" json:"metrics_enabled"`
	TracingEnabled           bool          `yaml:"tracing_enabled" json:"tracing_enabled"`
	StructuredLoggingEnabled bool          `yaml:"structured_logging_enabled" json:"structured_logging_enabled"`
	MetricsPrefix            string        `yaml:"metrics_prefix" json:"metrics_prefix"`
	SampleRate               float64       `yaml:"sample_rate" json:"sample_rate"`
	RetentionPeriod          time.Duration `yaml:"retention_period" json:"retention_period"`
}

// DefaultObservabilityConfig returns a sensible default configuration
func DefaultObservabilityConfig() *ObservabilityConfig {
	return &ObservabilityConfig{
		MetricsEnabled:           true,
		TracingEnabled:           true,
		StructuredLoggingEnabled: true,
		MetricsPrefix:            "harbor_replicator_sync",
		SampleRate:               1.0,
		RetentionPeriod:          24 * time.Hour,
	}
}

// RetryObservabilityCollector collects comprehensive metrics and traces for retry operations
type RetryObservabilityCollector struct {
	config *ObservabilityConfig
	
	// OpenTelemetry components
	tracer trace.Tracer
	meter  metric.Meter
	
	// Metrics
	retryAttemptCounter       metric.Int64Counter
	retrySuccessCounter       metric.Int64Counter
	retryFailureCounter       metric.Int64Counter
	retryDurationHistogram    metric.Float64Histogram
	retryDelayHistogram       metric.Float64Histogram
	circuitBreakerStateGauge  metric.Int64ObservableGauge
	errorRateGauge            metric.Float64ObservableGauge
	
	// Circuit breaker metrics
	circuitBreakerTripCounter metric.Int64Counter
	circuitBreakerResetCounter metric.Int64Counter
	
	// Internal state for metrics calculation
	errorCounts           map[string]*ErrorMetrics
	operationMetrics      map[string]*OperationMetrics
	circuitBreakerStates  map[string]CircuitBreakerState
	mutex                 sync.RWMutex
	
	// Correlation ID generator
	correlationIDCounter uint64
	
	// Logger
	logger *slog.Logger
}

// ErrorMetrics tracks error-specific metrics
type ErrorMetrics struct {
	Count            int64     `json:"count"`
	LastOccurrence   time.Time `json:"last_occurrence"`
	Category         string    `json:"category"`
	RetrySuccessRate float64   `json:"retry_success_rate"`
}

// OperationMetrics tracks operation-specific metrics
type OperationMetrics struct {
	TotalAttempts        int64         `json:"total_attempts"`
	SuccessfulAttempts   int64         `json:"successful_attempts"`
	FailedAttempts       int64         `json:"failed_attempts"`
	AverageLatency       time.Duration `json:"average_latency"`
	MaxLatency           time.Duration `json:"max_latency"`
	MinLatency           time.Duration `json:"min_latency"`
	TotalLatency         time.Duration `json:"total_latency"`
	LastAttempt          time.Time     `json:"last_attempt"`
	RetryDistribution    map[int]int64 `json:"retry_distribution"` // attempt number -> count
}

// CorrelationContext contains correlation information for tracking retry chains
type CorrelationContext struct {
	CorrelationID string            `json:"correlation_id"`
	OperationName string            `json:"operation_name"`
	StartTime     time.Time         `json:"start_time"`
	Metadata      map[string]string `json:"metadata"`
}

// NewRetryObservabilityCollector creates a new observability collector
func NewRetryObservabilityCollector(config *ObservabilityConfig, logger *slog.Logger) (*RetryObservabilityCollector, error) {
	if config == nil {
		config = DefaultObservabilityConfig()
	}
	
	if logger == nil {
		logger = slog.Default()
	}
	
	collector := &RetryObservabilityCollector{
		config:               config,
		errorCounts:          make(map[string]*ErrorMetrics),
		operationMetrics:     make(map[string]*OperationMetrics),
		circuitBreakerStates: make(map[string]CircuitBreakerState),
		logger:               logger,
	}
	
	if config.TracingEnabled {
		collector.tracer = otel.Tracer("harbor-replicator/sync")
	}
	
	if config.MetricsEnabled {
		if err := collector.initializeMetrics(); err != nil {
			return nil, fmt.Errorf("failed to initialize metrics: %w", err)
		}
	}
	
	return collector, nil
}

// initializeMetrics initializes OpenTelemetry metrics
func (roc *RetryObservabilityCollector) initializeMetrics() error {
	roc.meter = otel.Meter("harbor-replicator/sync")
	
	var err error
	
	// Retry attempt metrics
	roc.retryAttemptCounter, err = roc.meter.Int64Counter(
		roc.config.MetricsPrefix+"_retry_attempts_total",
		metric.WithDescription("Total number of retry attempts"),
	)
	if err != nil {
		return fmt.Errorf("failed to create retry attempt counter: %w", err)
	}
	
	roc.retrySuccessCounter, err = roc.meter.Int64Counter(
		roc.config.MetricsPrefix+"_retry_success_total",
		metric.WithDescription("Total number of successful retries"),
	)
	if err != nil {
		return fmt.Errorf("failed to create retry success counter: %w", err)
	}
	
	roc.retryFailureCounter, err = roc.meter.Int64Counter(
		roc.config.MetricsPrefix+"_retry_failures_total",
		metric.WithDescription("Total number of failed retries"),
	)
	if err != nil {
		return fmt.Errorf("failed to create retry failure counter: %w", err)
	}
	
	// Duration metrics
	roc.retryDurationHistogram, err = roc.meter.Float64Histogram(
		roc.config.MetricsPrefix+"_retry_duration_seconds",
		metric.WithDescription("Duration of retry operations"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return fmt.Errorf("failed to create retry duration histogram: %w", err)
	}
	
	roc.retryDelayHistogram, err = roc.meter.Float64Histogram(
		roc.config.MetricsPrefix+"_retry_delay_seconds",
		metric.WithDescription("Delay between retry attempts"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return fmt.Errorf("failed to create retry delay histogram: %w", err)
	}
	
	// Circuit breaker metrics
	roc.circuitBreakerTripCounter, err = roc.meter.Int64Counter(
		roc.config.MetricsPrefix+"_circuit_breaker_trips_total",
		metric.WithDescription("Total number of circuit breaker trips"),
	)
	if err != nil {
		return fmt.Errorf("failed to create circuit breaker trip counter: %w", err)
	}
	
	roc.circuitBreakerResetCounter, err = roc.meter.Int64Counter(
		roc.config.MetricsPrefix+"_circuit_breaker_resets_total",
		metric.WithDescription("Total number of circuit breaker resets"),
	)
	if err != nil {
		return fmt.Errorf("failed to create circuit breaker reset counter: %w", err)
	}
	
	// Observable gauges
	roc.circuitBreakerStateGauge, err = roc.meter.Int64ObservableGauge(
		roc.config.MetricsPrefix+"_circuit_breaker_state",
		metric.WithDescription("Current state of circuit breakers (0=closed, 1=open, 2=half-open)"),
	)
	if err != nil {
		return fmt.Errorf("failed to create circuit breaker state gauge: %w", err)
	}
	
	roc.errorRateGauge, err = roc.meter.Float64ObservableGauge(
		roc.config.MetricsPrefix+"_error_rate",
		metric.WithDescription("Current error rate for operations"),
	)
	if err != nil {
		return fmt.Errorf("failed to create error rate gauge: %w", err)
	}
	
	// Register callbacks for observable metrics
	_, err = roc.meter.RegisterCallback(
		roc.collectObservableMetrics,
		roc.circuitBreakerStateGauge,
		roc.errorRateGauge,
	)
	if err != nil {
		return fmt.Errorf("failed to register metric callbacks: %w", err)
	}
	
	return nil
}

// collectObservableMetrics collects observable metrics
func (roc *RetryObservabilityCollector) collectObservableMetrics(ctx context.Context, observer metric.Observer) error {
	roc.mutex.RLock()
	defer roc.mutex.RUnlock()
	
	// Collect circuit breaker states
	for name, state := range roc.circuitBreakerStates {
		observer.ObserveInt64(roc.circuitBreakerStateGauge, int64(state),
			metric.WithAttributes(attribute.String("circuit_breaker", name)))
	}
	
	// Collect error rates
	for operation, metrics := range roc.operationMetrics {
		if metrics.TotalAttempts > 0 {
			errorRate := float64(metrics.FailedAttempts) / float64(metrics.TotalAttempts)
			observer.ObserveFloat64(roc.errorRateGauge, errorRate,
				metric.WithAttributes(attribute.String("operation", operation)))
		}
	}
	
	return nil
}

// GenerateCorrelationID generates a unique correlation ID for tracking retry chains
func (roc *RetryObservabilityCollector) GenerateCorrelationID() string {
	id := atomic.AddUint64(&roc.correlationIDCounter, 1)
	return fmt.Sprintf("retry-%d-%d", time.Now().UnixNano(), id)
}

// StartRetryChain starts a new retry chain with tracing and correlation
func (roc *RetryObservabilityCollector) StartRetryChain(
	ctx context.Context,
	operationName string,
	metadata map[string]string,
) (context.Context, *CorrelationContext) {
	
	correlationID := roc.GenerateCorrelationID()
	correlationCtx := &CorrelationContext{
		CorrelationID: correlationID,
		OperationName: operationName,
		StartTime:     time.Now(),
		Metadata:      metadata,
	}
	
	// Add correlation ID to context
	ctx = context.WithValue(ctx, "correlation_id", correlationID)
	
	// Start tracing span if enabled
	if roc.config.TracingEnabled {
		var span trace.Span
		ctx, span = roc.tracer.Start(ctx, fmt.Sprintf("retry_chain_%s", operationName),
			trace.WithAttributes(
				attribute.String("correlation_id", correlationID),
				attribute.String("operation", operationName),
			))
		
		// Add metadata as span attributes
		for key, value := range metadata {
			span.SetAttributes(attribute.String(fmt.Sprintf("metadata.%s", key), value))
		}
		
		// Store span in context for later finishing
		ctx = context.WithValue(ctx, "retry_span", span)
	}
	
	// Structured logging
	if roc.config.StructuredLoggingEnabled {
		roc.logger.InfoContext(ctx, "Starting retry chain",
			slog.String("correlation_id", correlationID),
			slog.String("operation", operationName),
			slog.Any("metadata", metadata),
		)
	}
	
	return ctx, correlationCtx
}

// RecordRetryAttempt records a retry attempt with all relevant metrics
func (roc *RetryObservabilityCollector) RecordRetryAttempt(
	ctx context.Context,
	correlationCtx *CorrelationContext,
	attempt int,
	err error,
	duration time.Duration,
	delay time.Duration,
) {
	
	// Record metrics if enabled
	if roc.config.MetricsEnabled {
		attributes := []attribute.KeyValue{
			attribute.String("operation", correlationCtx.OperationName),
			attribute.String("correlation_id", correlationCtx.CorrelationID),
			attribute.Int("attempt", attempt),
		}
		
		if err != nil {
			// Classify error
			classifier := NewDefaultErrorClassifier()
			classified := classifier.Classify(err, correlationCtx.OperationName)
			
			attributes = append(attributes,
				attribute.String("error_category", classified.Category.String()),
				attribute.String("error_type", fmt.Sprintf("%T", err)),
				attribute.Bool("retryable", classified.IsRetryable()),
			)
			
			// Record failure metrics
			roc.retryFailureCounter.Add(ctx, 1, metric.WithAttributes(attributes...))
			
			// Update error metrics
			roc.updateErrorMetrics(classified.Category.String(), classified.IsRetryable())
		} else {
			// Record success metrics
			roc.retrySuccessCounter.Add(ctx, 1, metric.WithAttributes(attributes...))
		}
		
		// Always record attempt
		roc.retryAttemptCounter.Add(ctx, 1, metric.WithAttributes(attributes...))
		
		// Record duration
		roc.retryDurationHistogram.Record(ctx, duration.Seconds(), metric.WithAttributes(attributes...))
		
		// Record delay if this isn't the first attempt
		if attempt > 1 && delay > 0 {
			roc.retryDelayHistogram.Record(ctx, delay.Seconds(), metric.WithAttributes(attributes...))
		}
	}
	
	// Update operation metrics
	roc.updateOperationMetrics(correlationCtx.OperationName, attempt, err != nil, duration)
	
	// Add tracing information
	if roc.config.TracingEnabled {
		if span, ok := ctx.Value("retry_span").(trace.Span); ok {
			span.AddEvent(fmt.Sprintf("retry_attempt_%d", attempt),
				trace.WithAttributes(
					attribute.Int("attempt", attempt),
					attribute.Float64("duration_seconds", duration.Seconds()),
					attribute.Bool("success", err == nil),
				))
			
			if err != nil {
				span.SetStatus(codes.Error, err.Error())
				span.RecordError(err)
			}
		}
	}
	
	// Structured logging
	if roc.config.StructuredLoggingEnabled {
		logLevel := slog.LevelInfo
		if err != nil {
			logLevel = slog.LevelWarn
		}
		
		roc.logger.Log(ctx, logLevel, "Retry attempt completed",
			slog.String("correlation_id", correlationCtx.CorrelationID),
			slog.String("operation", correlationCtx.OperationName),
			slog.Int("attempt", attempt),
			slog.Duration("duration", duration),
			slog.Duration("delay", delay),
			slog.Bool("success", err == nil),
			slog.Any("error", err),
		)
	}
}

// FinishRetryChain finishes a retry chain and records final metrics
func (roc *RetryObservabilityCollector) FinishRetryChain(
	ctx context.Context,
	correlationCtx *CorrelationContext,
	finalResult error,
	totalAttempts int,
) {
	
	totalDuration := time.Since(correlationCtx.StartTime)
	
	// Finish tracing span if enabled
	if roc.config.TracingEnabled {
		if span, ok := ctx.Value("retry_span").(trace.Span); ok {
			span.SetAttributes(
				attribute.Int("total_attempts", totalAttempts),
				attribute.Float64("total_duration_seconds", totalDuration.Seconds()),
				attribute.Bool("final_success", finalResult == nil),
			)
			
			if finalResult != nil {
				span.SetStatus(codes.Error, finalResult.Error())
			} else {
				span.SetStatus(codes.Ok, "Retry chain completed successfully")
			}
			
			span.End()
		}
	}
	
	// Final structured logging
	if roc.config.StructuredLoggingEnabled {
		logLevel := slog.LevelInfo
		if finalResult != nil {
			logLevel = slog.LevelError
		}
		
		roc.logger.Log(ctx, logLevel, "Retry chain finished",
			slog.String("correlation_id", correlationCtx.CorrelationID),
			slog.String("operation", correlationCtx.OperationName),
			slog.Int("total_attempts", totalAttempts),
			slog.Duration("total_duration", totalDuration),
			slog.Bool("success", finalResult == nil),
			slog.Any("final_error", finalResult),
		)
	}
}

// RecordCircuitBreakerStateChange records circuit breaker state changes
func (roc *RetryObservabilityCollector) RecordCircuitBreakerStateChange(
	ctx context.Context,
	name string,
	fromState, toState CircuitBreakerState,
	reason string,
) {
	
	roc.mutex.Lock()
	roc.circuitBreakerStates[name] = toState
	roc.mutex.Unlock()
	
	if roc.config.MetricsEnabled {
		attributes := []attribute.KeyValue{
			attribute.String("circuit_breaker", name),
			attribute.String("from_state", fromState.String()),
			attribute.String("to_state", toState.String()),
			attribute.String("reason", reason),
		}
		
		if toState == StateOpen {
			roc.circuitBreakerTripCounter.Add(ctx, 1, metric.WithAttributes(attributes...))
		} else if toState == StateClosed && fromState != StateClosed {
			roc.circuitBreakerResetCounter.Add(ctx, 1, metric.WithAttributes(attributes...))
		}
	}
	
	if roc.config.TracingEnabled {
		span := trace.SpanFromContext(ctx)
		span.AddEvent("circuit_breaker_state_change",
			trace.WithAttributes(
				attribute.String("circuit_breaker", name),
				attribute.String("from_state", fromState.String()),
				attribute.String("to_state", toState.String()),
				attribute.String("reason", reason),
			))
	}
	
	if roc.config.StructuredLoggingEnabled {
		roc.logger.InfoContext(ctx, "Circuit breaker state changed",
			slog.String("circuit_breaker", name),
			slog.String("from_state", fromState.String()),
			slog.String("to_state", toState.String()),
			slog.String("reason", reason),
		)
	}
}

// updateErrorMetrics updates error-specific metrics
func (roc *RetryObservabilityCollector) updateErrorMetrics(errorCategory string, retryable bool) {
	roc.mutex.Lock()
	defer roc.mutex.Unlock()
	
	if metrics, exists := roc.errorCounts[errorCategory]; exists {
		metrics.Count++
		metrics.LastOccurrence = time.Now()
		
		// Update retry success rate (simplified calculation)
		if retryable {
			metrics.RetrySuccessRate = 0.8 // Simplified - in reality, track actual success rate
		}
	} else {
		roc.errorCounts[errorCategory] = &ErrorMetrics{
			Count:            1,
			LastOccurrence:   time.Now(),
			Category:         errorCategory,
			RetrySuccessRate: 0.8,
		}
	}
}

// updateOperationMetrics updates operation-specific metrics
func (roc *RetryObservabilityCollector) updateOperationMetrics(
	operation string,
	attempt int,
	failed bool,
	duration time.Duration,
) {
	roc.mutex.Lock()
	defer roc.mutex.Unlock()
	
	if metrics, exists := roc.operationMetrics[operation]; exists {
		metrics.TotalAttempts++
		metrics.LastAttempt = time.Now()
		metrics.TotalLatency += duration
		
		if failed {
			metrics.FailedAttempts++
		} else {
			metrics.SuccessfulAttempts++
		}
		
		// Update latency statistics
		if duration > metrics.MaxLatency {
			metrics.MaxLatency = duration
		}
		if metrics.MinLatency == 0 || duration < metrics.MinLatency {
			metrics.MinLatency = duration
		}
		metrics.AverageLatency = metrics.TotalLatency / time.Duration(metrics.TotalAttempts)
		
		// Update retry distribution
		if metrics.RetryDistribution == nil {
			metrics.RetryDistribution = make(map[int]int64)
		}
		metrics.RetryDistribution[attempt]++
	} else {
		newMetrics := &OperationMetrics{
			TotalAttempts:     1,
			LastAttempt:       time.Now(),
			TotalLatency:      duration,
			MaxLatency:        duration,
			MinLatency:        duration,
			AverageLatency:    duration,
			RetryDistribution: make(map[int]int64),
		}
		
		if failed {
			newMetrics.FailedAttempts = 1
		} else {
			newMetrics.SuccessfulAttempts = 1
		}
		
		newMetrics.RetryDistribution[attempt] = 1
		roc.operationMetrics[operation] = newMetrics
	}
}

// GetErrorMetrics returns current error metrics
func (roc *RetryObservabilityCollector) GetErrorMetrics() map[string]*ErrorMetrics {
	roc.mutex.RLock()
	defer roc.mutex.RUnlock()
	
	result := make(map[string]*ErrorMetrics)
	for k, v := range roc.errorCounts {
		// Create copy to avoid mutations
		result[k] = &ErrorMetrics{
			Count:            v.Count,
			LastOccurrence:   v.LastOccurrence,
			Category:         v.Category,
			RetrySuccessRate: v.RetrySuccessRate,
		}
	}
	
	return result
}

// GetOperationMetrics returns current operation metrics
func (roc *RetryObservabilityCollector) GetOperationMetrics() map[string]*OperationMetrics {
	roc.mutex.RLock()
	defer roc.mutex.RUnlock()
	
	result := make(map[string]*OperationMetrics)
	for k, v := range roc.operationMetrics {
		// Create copy to avoid mutations
		retryDist := make(map[int]int64)
		for attempt, count := range v.RetryDistribution {
			retryDist[attempt] = count
		}
		
		result[k] = &OperationMetrics{
			TotalAttempts:     v.TotalAttempts,
			SuccessfulAttempts: v.SuccessfulAttempts,
			FailedAttempts:    v.FailedAttempts,
			AverageLatency:    v.AverageLatency,
			MaxLatency:        v.MaxLatency,
			MinLatency:        v.MinLatency,
			TotalLatency:      v.TotalLatency,
			LastAttempt:       v.LastAttempt,
			RetryDistribution: retryDist,
		}
	}
	
	return result
}

// GetCircuitBreakerStates returns current circuit breaker states
func (roc *RetryObservabilityCollector) GetCircuitBreakerStates() map[string]CircuitBreakerState {
	roc.mutex.RLock()
	defer roc.mutex.RUnlock()
	
	result := make(map[string]CircuitBreakerState)
	for k, v := range roc.circuitBreakerStates {
		result[k] = v
	}
	
	return result
}

// Reset resets all collected metrics (useful for testing)
func (roc *RetryObservabilityCollector) Reset() {
	roc.mutex.Lock()
	defer roc.mutex.Unlock()
	
	roc.errorCounts = make(map[string]*ErrorMetrics)
	roc.operationMetrics = make(map[string]*OperationMetrics)
	roc.circuitBreakerStates = make(map[string]CircuitBreakerState)
	atomic.StoreUint64(&roc.correlationIDCounter, 0)
}

// ObservableRetryWrapper wraps the EnhancedRetryer with observability
type ObservableRetryWrapper struct {
	retryer   *EnhancedRetryer
	collector *RetryObservabilityCollector
}

// NewObservableRetryWrapper creates a new observable retry wrapper
func NewObservableRetryWrapper(
	retryer *EnhancedRetryer,
	collector *RetryObservabilityCollector,
) *ObservableRetryWrapper {
	return &ObservableRetryWrapper{
		retryer:   retryer,
		collector: collector,
	}
}

// Execute executes an operation with full observability
func (orw *ObservableRetryWrapper) Execute(
	ctx context.Context,
	operation func() error,
	operationName string,
) error {
	
	// Start retry chain
	ctx, correlationCtx := orw.collector.StartRetryChain(ctx, operationName, map[string]string{
		"component": "retry_wrapper",
	})
	
	var totalAttempts int
	var finalError error
	
	// Wrap the operation to capture attempt-level metrics
	wrappedOperation := func() error {
		totalAttempts++
		start := time.Now()
		
		err := operation()
		duration := time.Since(start)
		
		// Calculate delay (simplified - in reality would track actual delay)
		var delay time.Duration
		if totalAttempts > 1 {
			delay = time.Second * time.Duration(totalAttempts-1) // Simplified delay calculation
		}
		
		// Record the attempt
		orw.collector.RecordRetryAttempt(ctx, correlationCtx, totalAttempts, err, duration, delay)
		
		return err
	}
	
	// Execute with retry
	finalError = orw.retryer.Execute(ctx, wrappedOperation, operationName)
	
	// Finish retry chain
	orw.collector.FinishRetryChain(ctx, correlationCtx, finalError, totalAttempts)
	
	return finalError
}

// Health check metrics for the observability system
func (roc *RetryObservabilityCollector) HealthCheck() map[string]interface{} {
	health := map[string]interface{}{
		"metrics_enabled":            roc.config.MetricsEnabled,
		"tracing_enabled":            roc.config.TracingEnabled,
		"structured_logging_enabled": roc.config.StructuredLoggingEnabled,
		"total_operations_tracked":   len(roc.operationMetrics),
		"total_error_types_tracked":  len(roc.errorCounts),
		"circuit_breakers_tracked":   len(roc.circuitBreakerStates),
	}
	
	return health
}