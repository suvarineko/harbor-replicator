package engine

import (
	"context"
	"runtime"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.uber.org/zap"
)

// PrometheusMetricsCollector implements the MetricsCollector interface using Prometheus
type PrometheusMetricsCollector struct {
	mu       sync.RWMutex
	logger   *zap.Logger
	registry *prometheus.Registry
	
	// Sync operation metrics
	syncDuration     *prometheus.HistogramVec
	syncCounter      *prometheus.CounterVec
	syncErrors       *prometheus.CounterVec
	activeSync       prometheus.Gauge
	syncProgress     *prometheus.GaugeVec
	
	// Resource metrics
	resourcesTotal     *prometheus.GaugeVec
	resourcesProcessed *prometheus.CounterVec
	resourcesErrors    *prometheus.CounterVec
	resourcesSkipped   *prometheus.CounterVec
	
	// Engine metrics
	workerPoolSize      prometheus.Gauge
	workerPoolActive    prometheus.Gauge
	workerPoolQueue     prometheus.Gauge
	schedulerQueue      prometheus.Gauge
	
	// Performance metrics
	throughput         *prometheus.GaugeVec
	latency            *prometheus.HistogramVec
	memoryUsage        prometheus.Gauge
	cpuUsage           prometheus.Gauge
	goroutines         prometheus.Gauge
	
	// Circuit breaker metrics
	circuitBreakerState *prometheus.GaugeVec
	circuitBreakerCalls *prometheus.CounterVec
	
	// Health metrics
	healthStatus       *prometheus.GaugeVec
	healthCheckDuration *prometheus.HistogramVec
	
	// Custom metrics
	customGauges     map[string]*prometheus.GaugeVec
	customCounters   map[string]*prometheus.CounterVec
	customHistograms map[string]*prometheus.HistogramVec
	
	// Background collectors
	runtimeCollector *RuntimeMetricsCollector
	started          bool
	stopCh           chan struct{}
}

// RuntimeMetricsCollector collects runtime metrics in the background
type RuntimeMetricsCollector struct {
	collector *PrometheusMetricsCollector
	interval  time.Duration
	stopCh    chan struct{}
}

// NewPrometheusMetricsCollector creates a new Prometheus-based metrics collector
func NewPrometheusMetricsCollector(logger *zap.Logger, registry *prometheus.Registry) *PrometheusMetricsCollector {
	if registry == nil {
		registry = prometheus.NewRegistry()
	}

	mc := &PrometheusMetricsCollector{
		logger:           logger,
		registry:         registry,
		customGauges:     make(map[string]*prometheus.GaugeVec),
		customCounters:   make(map[string]*prometheus.CounterVec),
		customHistograms: make(map[string]*prometheus.HistogramVec),
		stopCh:           make(chan struct{}),
	}

	mc.initializeMetrics()
	mc.runtimeCollector = NewRuntimeMetricsCollector(mc, 30*time.Second)
	
	return mc
}

// initializeMetrics creates all the Prometheus metrics
func (mc *PrometheusMetricsCollector) initializeMetrics() {
	// Sync operation metrics
	mc.syncDuration = promauto.With(mc.registry).NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "harbor_replicator",
			Subsystem: "sync",
			Name:      "duration_seconds",
			Help:      "Duration of sync operations in seconds",
			Buckets:   prometheus.ExponentialBuckets(0.1, 2, 10),
		},
		[]string{"resource_type", "source_instance", "target_instance", "status"},
	)

	mc.syncCounter = promauto.With(mc.registry).NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "harbor_replicator",
			Subsystem: "sync",
			Name:      "operations_total",
			Help:      "Total number of sync operations",
		},
		[]string{"resource_type", "source_instance", "target_instance", "status"},
	)

	mc.syncErrors = promauto.With(mc.registry).NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "harbor_replicator",
			Subsystem: "sync",
			Name:      "errors_total",
			Help:      "Total number of sync errors",
		},
		[]string{"resource_type", "error_type", "retryable"},
	)

	mc.activeSync = promauto.With(mc.registry).NewGauge(
		prometheus.GaugeOpts{
			Namespace: "harbor_replicator",
			Subsystem: "sync",
			Name:      "active_operations",
			Help:      "Number of currently active sync operations",
		},
	)

	mc.syncProgress = promauto.With(mc.registry).NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "harbor_replicator",
			Subsystem: "sync",
			Name:      "progress_percent",
			Help:      "Progress percentage of active sync operations",
		},
		[]string{"sync_id", "resource_type", "phase"},
	)

	// Resource metrics
	mc.resourcesTotal = promauto.With(mc.registry).NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "harbor_replicator",
			Subsystem: "resources",
			Name:      "total",
			Help:      "Total number of resources to synchronize",
		},
		[]string{"resource_type", "source_instance"},
	)

	mc.resourcesProcessed = promauto.With(mc.registry).NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "harbor_replicator",
			Subsystem: "resources",
			Name:      "processed_total",
			Help:      "Total number of resources processed",
		},
		[]string{"resource_type", "operation", "status"},
	)

	mc.resourcesErrors = promauto.With(mc.registry).NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "harbor_replicator",
			Subsystem: "resources",
			Name:      "errors_total",
			Help:      "Total number of resource processing errors",
		},
		[]string{"resource_type", "error_type"},
	)

	mc.resourcesSkipped = promauto.With(mc.registry).NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "harbor_replicator",
			Subsystem: "resources",
			Name:      "skipped_total",
			Help:      "Total number of resources skipped",
		},
		[]string{"resource_type", "reason"},
	)

	// Engine metrics
	mc.workerPoolSize = promauto.With(mc.registry).NewGauge(
		prometheus.GaugeOpts{
			Namespace: "harbor_replicator",
			Subsystem: "engine",
			Name:      "worker_pool_size",
			Help:      "Current worker pool size",
		},
	)

	mc.workerPoolActive = promauto.With(mc.registry).NewGauge(
		prometheus.GaugeOpts{
			Namespace: "harbor_replicator",
			Subsystem: "engine",
			Name:      "worker_pool_active",
			Help:      "Number of active workers",
		},
	)

	mc.workerPoolQueue = promauto.With(mc.registry).NewGauge(
		prometheus.GaugeOpts{
			Namespace: "harbor_replicator",
			Subsystem: "engine",
			Name:      "worker_pool_queue_size",
			Help:      "Number of queued jobs in worker pool",
		},
	)

	mc.schedulerQueue = promauto.With(mc.registry).NewGauge(
		prometheus.GaugeOpts{
			Namespace: "harbor_replicator",
			Subsystem: "engine",
			Name:      "scheduler_queue_size",
			Help:      "Number of scheduled jobs in scheduler",
		},
	)

	// Performance metrics
	mc.throughput = promauto.With(mc.registry).NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "harbor_replicator",
			Subsystem: "performance",
			Name:      "throughput_resources_per_second",
			Help:      "Current throughput in resources per second",
		},
		[]string{"resource_type", "operation"},
	)

	mc.latency = promauto.With(mc.registry).NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "harbor_replicator",
			Subsystem: "performance",
			Name:      "latency_seconds",
			Help:      "Latency of operations in seconds",
			Buckets:   prometheus.ExponentialBuckets(0.001, 2, 10),
		},
		[]string{"operation", "resource_type"},
	)

	mc.memoryUsage = promauto.With(mc.registry).NewGauge(
		prometheus.GaugeOpts{
			Namespace: "harbor_replicator",
			Subsystem: "performance",
			Name:      "memory_usage_bytes",
			Help:      "Current memory usage in bytes",
		},
	)

	mc.cpuUsage = promauto.With(mc.registry).NewGauge(
		prometheus.GaugeOpts{
			Namespace: "harbor_replicator",
			Subsystem: "performance",
			Name:      "cpu_usage_percent",
			Help:      "Current CPU usage percentage",
		},
	)

	mc.goroutines = promauto.With(mc.registry).NewGauge(
		prometheus.GaugeOpts{
			Namespace: "harbor_replicator",
			Subsystem: "performance",
			Name:      "goroutines",
			Help:      "Current number of goroutines",
		},
	)

	// Circuit breaker metrics
	mc.circuitBreakerState = promauto.With(mc.registry).NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "harbor_replicator",
			Subsystem: "circuit_breaker",
			Name:      "state",
			Help:      "Circuit breaker state (0=closed, 1=open, 2=half-open)",
		},
		[]string{"name"},
	)

	mc.circuitBreakerCalls = promauto.With(mc.registry).NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "harbor_replicator",
			Subsystem: "circuit_breaker",
			Name:      "calls_total",
			Help:      "Total number of circuit breaker calls",
		},
		[]string{"name", "state", "result"},
	)

	// Health metrics
	mc.healthStatus = promauto.With(mc.registry).NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "harbor_replicator",
			Subsystem: "health",
			Name:      "status",
			Help:      "Health status (1=healthy, 0=unhealthy)",
		},
		[]string{"component"},
	)

	mc.healthCheckDuration = promauto.With(mc.registry).NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "harbor_replicator",
			Subsystem: "health",
			Name:      "check_duration_seconds",
			Help:      "Duration of health checks in seconds",
			Buckets:   prometheus.ExponentialBuckets(0.001, 2, 8),
		},
		[]string{"component"},
	)
}

// Start begins collecting metrics
func (mc *PrometheusMetricsCollector) Start(ctx context.Context) error {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	if mc.started {
		return nil
	}

	mc.started = true
	mc.runtimeCollector.Start()

	mc.logger.Info("Metrics collector started")
	return nil
}

// Stop stops collecting metrics
func (mc *PrometheusMetricsCollector) Stop() error {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	if !mc.started {
		return nil
	}

	close(mc.stopCh)
	mc.runtimeCollector.Stop()
	mc.started = false

	mc.logger.Info("Metrics collector stopped")
	return nil
}

// RecordSyncDuration records the duration of a sync operation
func (mc *PrometheusMetricsCollector) RecordSyncDuration(resourceType string, duration time.Duration) {
	mc.syncDuration.WithLabelValues(resourceType, "", "", "completed").Observe(duration.Seconds())
}

// RecordSyncDurationWithLabels records sync duration with full label set
func (mc *PrometheusMetricsCollector) RecordSyncDurationWithLabels(resourceType, sourceInstance, targetInstance, status string, duration time.Duration) {
	mc.syncDuration.WithLabelValues(resourceType, sourceInstance, targetInstance, status).Observe(duration.Seconds())
}

// IncSyncCounter increments a sync counter
func (mc *PrometheusMetricsCollector) IncSyncCounter(resourceType, status string) {
	mc.syncCounter.WithLabelValues(resourceType, "", "", status).Inc()
}

// IncSyncCounterWithLabels increments sync counter with full label set
func (mc *PrometheusMetricsCollector) IncSyncCounterWithLabels(resourceType, sourceInstance, targetInstance, status string) {
	mc.syncCounter.WithLabelValues(resourceType, sourceInstance, targetInstance, status).Inc()
}

// RecordSyncError records a sync error
func (mc *PrometheusMetricsCollector) RecordSyncError(resourceType, errorType string, retryable bool) {
	retryableStr := "false"
	if retryable {
		retryableStr = "true"
	}
	mc.syncErrors.WithLabelValues(resourceType, errorType, retryableStr).Inc()
}

// SetActiveSync sets the number of active sync operations
func (mc *PrometheusMetricsCollector) SetActiveSync(count float64) {
	mc.activeSync.Set(count)
}

// UpdateSyncProgress updates sync progress metrics
func (mc *PrometheusMetricsCollector) UpdateSyncProgress(syncID, resourceType, phase string, percent float64) {
	mc.syncProgress.WithLabelValues(syncID, resourceType, phase).Set(percent)
}

// RecordResourceCount records resource counts
func (mc *PrometheusMetricsCollector) RecordResourceCount(resourceType, sourceInstance string, count float64) {
	mc.resourcesTotal.WithLabelValues(resourceType, sourceInstance).Set(count)
}

// IncResourceProcessed increments processed resource counter
func (mc *PrometheusMetricsCollector) IncResourceProcessed(resourceType, operation, status string) {
	mc.resourcesProcessed.WithLabelValues(resourceType, operation, status).Inc()
}

// IncResourceError increments resource error counter
func (mc *PrometheusMetricsCollector) IncResourceError(resourceType, errorType string) {
	mc.resourcesErrors.WithLabelValues(resourceType, errorType).Inc()
}

// IncResourceSkipped increments skipped resource counter
func (mc *PrometheusMetricsCollector) IncResourceSkipped(resourceType, reason string) {
	mc.resourcesSkipped.WithLabelValues(resourceType, reason).Inc()
}

// UpdateWorkerPoolMetrics updates worker pool metrics
func (mc *PrometheusMetricsCollector) UpdateWorkerPoolMetrics(size, active, queued float64) {
	mc.workerPoolSize.Set(size)
	mc.workerPoolActive.Set(active)
	mc.workerPoolQueue.Set(queued)
}

// SetSchedulerQueue sets scheduler queue size
func (mc *PrometheusMetricsCollector) SetSchedulerQueue(size float64) {
	mc.schedulerQueue.Set(size)
}

// SetThroughput sets throughput metrics
func (mc *PrometheusMetricsCollector) SetThroughput(resourceType, operation string, value float64) {
	mc.throughput.WithLabelValues(resourceType, operation).Set(value)
}

// RecordLatency records operation latency
func (mc *PrometheusMetricsCollector) RecordLatency(operation, resourceType string, duration time.Duration) {
	mc.latency.WithLabelValues(operation, resourceType).Observe(duration.Seconds())
}

// UpdateCircuitBreakerState updates circuit breaker state
func (mc *PrometheusMetricsCollector) UpdateCircuitBreakerState(name string, state CircuitBreakerState) {
	mc.circuitBreakerState.WithLabelValues(name).Set(float64(state))
}

// RecordCircuitBreakerCall records a circuit breaker call
func (mc *PrometheusMetricsCollector) RecordCircuitBreakerCall(name, state, result string) {
	mc.circuitBreakerCalls.WithLabelValues(name, state, result).Inc()
}

// UpdateHealthStatus updates health status
func (mc *PrometheusMetricsCollector) UpdateHealthStatus(component string, healthy bool) {
	value := 0.0
	if healthy {
		value = 1.0
	}
	mc.healthStatus.WithLabelValues(component).Set(value)
}

// RecordHealthCheckDuration records health check duration
func (mc *PrometheusMetricsCollector) RecordHealthCheckDuration(component string, duration time.Duration) {
	mc.healthCheckDuration.WithLabelValues(component).Observe(duration.Seconds())
}

// SetGauge sets a gauge metric
func (mc *PrometheusMetricsCollector) SetGauge(name string, value float64, labels map[string]string) {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	gauge, exists := mc.customGauges[name]
	if !exists {
		labelNames := make([]string, 0, len(labels))
		for k := range labels {
			labelNames = append(labelNames, k)
		}

		gauge = promauto.With(mc.registry).NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "harbor_replicator",
				Subsystem: "custom",
				Name:      name,
				Help:      "Custom gauge metric: " + name,
			},
			labelNames,
		)
		mc.customGauges[name] = gauge
	}

	labelValues := make([]string, 0, len(labels))
	for _, v := range labels {
		labelValues = append(labelValues, v)
	}

	gauge.WithLabelValues(labelValues...).Set(value)
}

// RecordHistogram records a histogram metric
func (mc *PrometheusMetricsCollector) RecordHistogram(name string, value float64, labels map[string]string) {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	histogram, exists := mc.customHistograms[name]
	if !exists {
		labelNames := make([]string, 0, len(labels))
		for k := range labels {
			labelNames = append(labelNames, k)
		}

		histogram = promauto.With(mc.registry).NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "harbor_replicator",
				Subsystem: "custom",
				Name:      name,
				Help:      "Custom histogram metric: " + name,
				Buckets:   prometheus.DefBuckets,
			},
			labelNames,
		)
		mc.customHistograms[name] = histogram
	}

	labelValues := make([]string, 0, len(labels))
	for _, v := range labels {
		labelValues = append(labelValues, v)
	}

	histogram.WithLabelValues(labelValues...).Observe(value)
}

// GetMetrics returns all collected metrics
func (mc *PrometheusMetricsCollector) GetMetrics() map[string]interface{} {
	metrics := make(map[string]interface{})
	
	// This would typically serialize all Prometheus metrics
	// For now, we'll return a placeholder
	metrics["prometheus_registry"] = mc.registry
	metrics["collector_type"] = "prometheus"
	metrics["started"] = mc.started
	
	return metrics
}

// GetRegistry returns the Prometheus registry
func (mc *PrometheusMetricsCollector) GetRegistry() *prometheus.Registry {
	return mc.registry
}

// NewRuntimeMetricsCollector creates a new runtime metrics collector
func NewRuntimeMetricsCollector(mc *PrometheusMetricsCollector, interval time.Duration) *RuntimeMetricsCollector {
	return &RuntimeMetricsCollector{
		collector: mc,
		interval:  interval,
		stopCh:    make(chan struct{}),
	}
}

// Start begins collecting runtime metrics
func (rmc *RuntimeMetricsCollector) Start() {
	go rmc.collectLoop()
}

// Stop stops collecting runtime metrics
func (rmc *RuntimeMetricsCollector) Stop() {
	close(rmc.stopCh)
}

// collectLoop runs the collection loop
func (rmc *RuntimeMetricsCollector) collectLoop() {
	ticker := time.NewTicker(rmc.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			rmc.collectRuntimeMetrics()
		case <-rmc.stopCh:
			return
		}
	}
}

// collectRuntimeMetrics collects Go runtime metrics
func (rmc *RuntimeMetricsCollector) collectRuntimeMetrics() {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	// Memory metrics
	rmc.collector.memoryUsage.Set(float64(m.Alloc))

	// Goroutine count
	rmc.collector.goroutines.Set(float64(runtime.NumGoroutine()))

	// CPU usage would require additional OS-specific implementation
	// For now, we'll set it to 0 as a placeholder
	rmc.collector.cpuUsage.Set(0.0)
}