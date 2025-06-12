package monitoring

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
)

// CustomMetricsRegistry manages application-specific metrics and health indicators
type CustomMetricsRegistry struct {
	logger   *zap.Logger
	registry *prometheus.Registry
	mu       sync.RWMutex

	// Harbor instance health tracking
	harborInstances    map[string]*HarborInstanceMetrics
	healthChecks       map[string]*HealthCheck
	
	// Configuration drift detection
	driftDetectors     map[string]*DriftDetector
	
	// Resource quota tracking
	quotaTrackers      map[string]*QuotaTracker
	
	// Custom application metrics
	customMetrics      map[string]prometheus.Collector
	
	// Background monitoring
	monitoringTicker   *time.Ticker
	monitoringStop     chan struct{}
	monitoringStarted  bool
}

// HarborInstanceMetrics tracks metrics for a Harbor instance
type HarborInstanceMetrics struct {
	Name         string
	URL          string
	Type         string // source, target
	LastHealthCheck time.Time
	IsHealthy    bool
	ResponseTime time.Duration
	ErrorCount   int64
	SuccessCount int64
	Version      string
	
	// Prometheus metrics
	healthGauge     prometheus.Gauge
	responseTime    prometheus.Histogram
	requestCounter  *prometheus.CounterVec
}

// HealthCheck represents a health check configuration
type HealthCheck struct {
	Name        string
	Endpoint    string
	Interval    time.Duration
	Timeout     time.Duration
	Retries     int
	LastCheck   time.Time
	LastResult  bool
	LastError   error
	
	// Prometheus metrics
	statusGauge    prometheus.Gauge
	durationHist   prometheus.Histogram
	checksCounter  *prometheus.CounterVec
}

// DriftDetector detects configuration drift between Harbor instances
type DriftDetector struct {
	SourceInstance string
	TargetInstance string
	ResourceType   string
	LastCheck      time.Time
	DriftCount     int
	MaxDrift       int
	
	// Prometheus metrics
	driftGauge     prometheus.Gauge
	checksCounter  prometheus.Counter
}

// QuotaTracker tracks resource quota usage
type QuotaTracker struct {
	Instance     string
	ResourceType string
	UsedQuota    float64
	TotalQuota   float64
	LastUpdate   time.Time
	
	// Prometheus metrics
	usageGauge   prometheus.Gauge
	totalGauge   prometheus.Gauge
}

// NewCustomMetricsRegistry creates a new custom metrics registry
func NewCustomMetricsRegistry(logger *zap.Logger) *CustomMetricsRegistry {
	if logger == nil {
		logger = zap.NewNop()
	}

	registry := prometheus.NewRegistry()
	
	return &CustomMetricsRegistry{
		logger:          logger,
		registry:        registry,
		harborInstances: make(map[string]*HarborInstanceMetrics),
		healthChecks:    make(map[string]*HealthCheck),
		driftDetectors:  make(map[string]*DriftDetector),
		quotaTrackers:   make(map[string]*QuotaTracker),
		customMetrics:   make(map[string]prometheus.Collector),
		monitoringStop:  make(chan struct{}),
	}
}

// RegisterHarborInstance registers a Harbor instance for monitoring
func (cmr *CustomMetricsRegistry) RegisterHarborInstance(name, url, instanceType string) error {
	cmr.mu.Lock()
	defer cmr.mu.Unlock()

	if _, exists := cmr.harborInstances[name]; exists {
		return fmt.Errorf("harbor instance %s already registered", name)
	}

	// Create Prometheus metrics for this instance
	healthGauge := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "harbor_replicator",
		Subsystem: "instance",
		Name:      "health_status",
		Help:      "Health status of Harbor instance (1=healthy, 0=unhealthy)",
		ConstLabels: prometheus.Labels{
			"instance": name,
			"type":     instanceType,
			"url":      url,
		},
	})

	responseTime := prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "harbor_replicator",
		Subsystem: "instance",
		Name:      "response_time_seconds",
		Help:      "Response time for Harbor instance health checks",
		Buckets:   []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
		ConstLabels: prometheus.Labels{
			"instance": name,
			"type":     instanceType,
		},
	})

	requestCounter := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "harbor_replicator",
			Subsystem: "instance",
			Name:      "requests_total",
			Help:      "Total number of requests to Harbor instance",
			ConstLabels: prometheus.Labels{
				"instance": name,
				"type":     instanceType,
			},
		},
		[]string{"status"},
	)

	// Register metrics
	cmr.registry.MustRegister(healthGauge, responseTime, requestCounter)

	instance := &HarborInstanceMetrics{
		Name:            name,
		URL:             url,
		Type:            instanceType,
		LastHealthCheck: time.Now(),
		IsHealthy:       false,
		healthGauge:     healthGauge,
		responseTime:    responseTime,
		requestCounter:  requestCounter,
	}

	cmr.harborInstances[name] = instance
	
	cmr.logger.Info("Registered Harbor instance for monitoring",
		zap.String("name", name),
		zap.String("url", url),
		zap.String("type", instanceType),
	)

	return nil
}

// UpdateHarborInstanceHealth updates the health status of a Harbor instance
func (cmr *CustomMetricsRegistry) UpdateHarborInstanceHealth(name string, healthy bool, responseTime time.Duration) error {
	cmr.mu.Lock()
	defer cmr.mu.Unlock()

	instance, exists := cmr.harborInstances[name]
	if !exists {
		return fmt.Errorf("harbor instance %s not registered", name)
	}

	instance.LastHealthCheck = time.Now()
	instance.IsHealthy = healthy
	instance.ResponseTime = responseTime

	// Update Prometheus metrics
	if healthy {
		instance.healthGauge.Set(1)
		instance.SuccessCount++
		instance.requestCounter.WithLabelValues("success").Inc()
	} else {
		instance.healthGauge.Set(0)
		instance.ErrorCount++
		instance.requestCounter.WithLabelValues("error").Inc()
	}

	instance.responseTime.Observe(responseTime.Seconds())

	return nil
}

// RegisterHealthCheck registers a custom health check
func (cmr *CustomMetricsRegistry) RegisterHealthCheck(name, endpoint string, interval, timeout time.Duration) error {
	cmr.mu.Lock()
	defer cmr.mu.Unlock()

	if _, exists := cmr.healthChecks[name]; exists {
		return fmt.Errorf("health check %s already registered", name)
	}

	// Create Prometheus metrics for this health check
	statusGauge := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "harbor_replicator",
		Subsystem: "health",
		Name:      "check_status",
		Help:      "Health check status (1=passing, 0=failing)",
		ConstLabels: prometheus.Labels{
			"check_name": name,
			"endpoint":   endpoint,
		},
	})

	durationHist := prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "harbor_replicator",
		Subsystem: "health",
		Name:      "check_duration_seconds",
		Help:      "Duration of health checks",
		Buckets:   prometheus.ExponentialBuckets(0.001, 2, 10),
		ConstLabels: prometheus.Labels{
			"check_name": name,
		},
	})

	checksCounter := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "harbor_replicator",
			Subsystem: "health",
			Name:      "checks_total",
			Help:      "Total number of health checks performed",
			ConstLabels: prometheus.Labels{
				"check_name": name,
			},
		},
		[]string{"result"},
	)

	// Register metrics
	cmr.registry.MustRegister(statusGauge, durationHist, checksCounter)

	healthCheck := &HealthCheck{
		Name:           name,
		Endpoint:       endpoint,
		Interval:       interval,
		Timeout:        timeout,
		statusGauge:    statusGauge,
		durationHist:   durationHist,
		checksCounter:  checksCounter,
	}

	cmr.healthChecks[name] = healthCheck

	cmr.logger.Info("Registered health check",
		zap.String("name", name),
		zap.String("endpoint", endpoint),
		zap.Duration("interval", interval),
	)

	return nil
}

// UpdateHealthCheck updates a health check result
func (cmr *CustomMetricsRegistry) UpdateHealthCheck(name string, success bool, duration time.Duration, err error) error {
	cmr.mu.Lock()
	defer cmr.mu.Unlock()

	healthCheck, exists := cmr.healthChecks[name]
	if !exists {
		return fmt.Errorf("health check %s not registered", name)
	}

	healthCheck.LastCheck = time.Now()
	healthCheck.LastResult = success
	healthCheck.LastError = err

	// Update Prometheus metrics
	if success {
		healthCheck.statusGauge.Set(1)
		healthCheck.checksCounter.WithLabelValues("success").Inc()
	} else {
		healthCheck.statusGauge.Set(0)
		healthCheck.checksCounter.WithLabelValues("failure").Inc()
	}

	healthCheck.durationHist.Observe(duration.Seconds())

	return nil
}

// RegisterDriftDetector registers a configuration drift detector
func (cmr *CustomMetricsRegistry) RegisterDriftDetector(sourceInstance, targetInstance, resourceType string, maxDrift int) error {
	cmr.mu.Lock()
	defer cmr.mu.Unlock()

	key := fmt.Sprintf("%s:%s:%s", sourceInstance, targetInstance, resourceType)
	if _, exists := cmr.driftDetectors[key]; exists {
		return fmt.Errorf("drift detector %s already registered", key)
	}

	// Create Prometheus metrics
	driftGauge := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "harbor_replicator",
		Subsystem: "drift",
		Name:      "detected_differences",
		Help:      "Number of configuration differences detected",
		ConstLabels: prometheus.Labels{
			"source":        sourceInstance,
			"target":        targetInstance,
			"resource_type": resourceType,
		},
	})

	checksCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "harbor_replicator",
		Subsystem: "drift",
		Name:      "checks_total",
		Help:      "Total number of drift checks performed",
		ConstLabels: prometheus.Labels{
			"source":        sourceInstance,
			"target":        targetInstance,
			"resource_type": resourceType,
		},
	})

	// Register metrics
	cmr.registry.MustRegister(driftGauge, checksCounter)

	detector := &DriftDetector{
		SourceInstance: sourceInstance,
		TargetInstance: targetInstance,
		ResourceType:   resourceType,
		MaxDrift:       maxDrift,
		driftGauge:     driftGauge,
		checksCounter:  checksCounter,
	}

	cmr.driftDetectors[key] = detector

	cmr.logger.Info("Registered drift detector",
		zap.String("source", sourceInstance),
		zap.String("target", targetInstance),
		zap.String("resource_type", resourceType),
		zap.Int("max_drift", maxDrift),
	)

	return nil
}

// UpdateDriftDetection updates drift detection results
func (cmr *CustomMetricsRegistry) UpdateDriftDetection(sourceInstance, targetInstance, resourceType string, driftCount int) error {
	cmr.mu.Lock()
	defer cmr.mu.Unlock()

	key := fmt.Sprintf("%s:%s:%s", sourceInstance, targetInstance, resourceType)
	detector, exists := cmr.driftDetectors[key]
	if !exists {
		return fmt.Errorf("drift detector %s not registered", key)
	}

	detector.LastCheck = time.Now()
	detector.DriftCount = driftCount

	// Update Prometheus metrics
	detector.driftGauge.Set(float64(driftCount))
	detector.checksCounter.Inc()

	return nil
}

// RegisterQuotaTracker registers a resource quota tracker
func (cmr *CustomMetricsRegistry) RegisterQuotaTracker(instance, resourceType string) error {
	cmr.mu.Lock()
	defer cmr.mu.Unlock()

	key := fmt.Sprintf("%s:%s", instance, resourceType)
	if _, exists := cmr.quotaTrackers[key]; exists {
		return fmt.Errorf("quota tracker %s already registered", key)
	}

	// Create Prometheus metrics
	usageGauge := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "harbor_replicator",
		Subsystem: "quota",
		Name:      "usage_bytes",
		Help:      "Current quota usage in bytes",
		ConstLabels: prometheus.Labels{
			"instance":      instance,
			"resource_type": resourceType,
		},
	})

	totalGauge := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "harbor_replicator",
		Subsystem: "quota",
		Name:      "total_bytes",
		Help:      "Total quota limit in bytes",
		ConstLabels: prometheus.Labels{
			"instance":      instance,
			"resource_type": resourceType,
		},
	})

	// Register metrics
	cmr.registry.MustRegister(usageGauge, totalGauge)

	tracker := &QuotaTracker{
		Instance:     instance,
		ResourceType: resourceType,
		usageGauge:   usageGauge,
		totalGauge:   totalGauge,
	}

	cmr.quotaTrackers[key] = tracker

	cmr.logger.Info("Registered quota tracker",
		zap.String("instance", instance),
		zap.String("resource_type", resourceType),
	)

	return nil
}

// UpdateQuotaUsage updates quota usage metrics
func (cmr *CustomMetricsRegistry) UpdateQuotaUsage(instance, resourceType string, used, total float64) error {
	cmr.mu.Lock()
	defer cmr.mu.Unlock()

	key := fmt.Sprintf("%s:%s", instance, resourceType)
	tracker, exists := cmr.quotaTrackers[key]
	if !exists {
		return fmt.Errorf("quota tracker %s not registered", key)
	}

	tracker.UsedQuota = used
	tracker.TotalQuota = total
	tracker.LastUpdate = time.Now()

	// Update Prometheus metrics
	tracker.usageGauge.Set(used)
	tracker.totalGauge.Set(total)

	return nil
}

// RegisterCustomMetric registers a custom Prometheus metric
func (cmr *CustomMetricsRegistry) RegisterCustomMetric(name string, metric prometheus.Collector) error {
	cmr.mu.Lock()
	defer cmr.mu.Unlock()

	if _, exists := cmr.customMetrics[name]; exists {
		return fmt.Errorf("custom metric %s already registered", name)
	}

	cmr.registry.MustRegister(metric)
	cmr.customMetrics[name] = metric

	cmr.logger.Info("Registered custom metric", zap.String("name", name))
	return nil
}

// GetRegistry returns the Prometheus registry
func (cmr *CustomMetricsRegistry) GetRegistry() *prometheus.Registry {
	return cmr.registry
}

// StartBackgroundMonitoring starts background monitoring tasks
func (cmr *CustomMetricsRegistry) StartBackgroundMonitoring(ctx context.Context, interval time.Duration) error {
	cmr.mu.Lock()
	defer cmr.mu.Unlock()

	if cmr.monitoringStarted {
		return fmt.Errorf("background monitoring already started")
	}

	cmr.monitoringTicker = time.NewTicker(interval)
	cmr.monitoringStarted = true

	go cmr.backgroundMonitoringLoop(ctx)

	cmr.logger.Info("Started background monitoring", zap.Duration("interval", interval))
	return nil
}

// StopBackgroundMonitoring stops background monitoring tasks
func (cmr *CustomMetricsRegistry) StopBackgroundMonitoring() {
	cmr.mu.Lock()
	defer cmr.mu.Unlock()

	if !cmr.monitoringStarted {
		return
	}

	close(cmr.monitoringStop)
	if cmr.monitoringTicker != nil {
		cmr.monitoringTicker.Stop()
	}
	cmr.monitoringStarted = false

	cmr.logger.Info("Stopped background monitoring")
}

// backgroundMonitoringLoop runs the background monitoring tasks
func (cmr *CustomMetricsRegistry) backgroundMonitoringLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			cmr.logger.Info("Background monitoring stopped due to context cancellation")
			return
		case <-cmr.monitoringStop:
			cmr.logger.Info("Background monitoring stopped")
			return
		case <-cmr.monitoringTicker.C:
			cmr.performBackgroundChecks(ctx)
		}
	}
}

// performBackgroundChecks performs periodic background health checks
func (cmr *CustomMetricsRegistry) performBackgroundChecks(ctx context.Context) {
	cmr.mu.RLock()
	defer cmr.mu.RUnlock()

	// Perform health checks
	for name, healthCheck := range cmr.healthChecks {
		go cmr.performHealthCheck(ctx, name, healthCheck)
	}
}

// performHealthCheck performs a single health check
func (cmr *CustomMetricsRegistry) performHealthCheck(ctx context.Context, name string, healthCheck *HealthCheck) {
	start := time.Now()
	
	// Create a timeout context for this health check
	checkCtx, cancel := context.WithTimeout(ctx, healthCheck.Timeout)
	defer cancel()

	// Perform the actual health check (simplified HTTP check)
	success := cmr.doHealthCheck(checkCtx, healthCheck.Endpoint)
	duration := time.Since(start)

	// Update the health check result
	var err error
	if !success {
		err = fmt.Errorf("health check failed for %s", healthCheck.Endpoint)
	}

	if updateErr := cmr.UpdateHealthCheck(name, success, duration, err); updateErr != nil {
		cmr.logger.Warn("Failed to update health check result",
			zap.String("name", name),
			zap.Error(updateErr),
		)
	}
}

// doHealthCheck performs the actual health check (simplified implementation)
func (cmr *CustomMetricsRegistry) doHealthCheck(ctx context.Context, endpoint string) bool {
	// This is a simplified implementation
	// In a real implementation, you would make an HTTP request to the endpoint
	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return false
	}

	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode >= 200 && resp.StatusCode < 400
}

// GetSummaryEndpoint returns a summary of all monitored metrics
func (cmr *CustomMetricsRegistry) GetSummaryEndpoint() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cmr.mu.RLock()
		defer cmr.mu.RUnlock()

		summary := map[string]interface{}{
			"harbor_instances": len(cmr.harborInstances),
			"health_checks":    len(cmr.healthChecks),
			"drift_detectors":  len(cmr.driftDetectors),
			"quota_trackers":   len(cmr.quotaTrackers),
			"custom_metrics":   len(cmr.customMetrics),
			"monitoring_active": cmr.monitoringStarted,
			"timestamp":        time.Now().Format(time.RFC3339),
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		if err := json.NewEncoder(w).Encode(summary); err != nil {
			cmr.logger.Error("Failed to encode summary response", zap.Error(err))
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
	})
}

// GetMetricsHandler returns the Prometheus metrics handler
func (cmr *CustomMetricsRegistry) GetMetricsHandler() http.Handler {
	return promhttp.HandlerFor(cmr.registry, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	})
}