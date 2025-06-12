package metrics

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// MetricsCollector defines the interface for collecting Harbor client metrics
type MetricsCollector interface {
	RecordRequest(method, endpoint string, duration time.Duration, statusCode int)
	RecordError(method, endpoint string, errorType string)
	RecordRateLimit(limited bool)
	RecordCircuitBreakerState(state string)
}

// ExtendedMetricsCollector provides additional metrics collection capabilities
type ExtendedMetricsCollector interface {
	MetricsCollector
	RecordConnectionPoolStats(stats map[string]float64)
	RecordRetryAttempt(method, endpoint string, attempt int, success bool)
	RecordQueuedRequests(count int)
	RecordResponseSize(method, endpoint string, size int64)
	StartRequestTimer(method, endpoint string) func()
	GetMetricsSummary() MetricsSummary
}

// MetricsSummary contains aggregated metrics information
type MetricsSummary struct {
	TotalRequests       int64                 `json:"total_requests"`
	TotalErrors         int64                 `json:"total_errors"`
	AverageResponseTime time.Duration         `json:"average_response_time"`
	ErrorRate           float64               `json:"error_rate"`
	RateLimitHits       int64                 `json:"rate_limit_hits"`
	CircuitBreakerState string                `json:"circuit_breaker_state"`
	LastActivity        time.Time             `json:"last_activity"`
	ErrorBreakdown      map[string]int64      `json:"error_breakdown"`
	EndpointStats       map[string]EndpointStat `json:"endpoint_stats"`
}

// EndpointStat contains statistics for a specific endpoint
type EndpointStat struct {
	RequestCount    int64         `json:"request_count"`
	ErrorCount      int64         `json:"error_count"`
	AverageLatency  time.Duration `json:"average_latency"`
	LastActivity    time.Time     `json:"last_activity"`
}

// ComprehensiveMetrics implements ExtendedMetricsCollector with full observability
type ComprehensiveMetrics struct {
	namespace string
	subsystem string
	
	// Prometheus metrics
	requestsTotal       *prometheus.CounterVec
	requestDuration     *prometheus.HistogramVec
	requestSize         *prometheus.HistogramVec
	responseSize        *prometheus.HistogramVec
	errorsTotal         *prometheus.CounterVec
	rateLimitHits       prometheus.Counter
	circuitBreakerState *prometheus.GaugeVec
	connectionPoolStats *prometheus.GaugeVec
	retryAttempts       *prometheus.CounterVec
	queuedRequests      prometheus.Gauge
	
	// Internal tracking
	mu             sync.RWMutex
	requestTimers  map[string]time.Time
	summary        MetricsSummary
	endpointStats  map[string]*EndpointStat
	errorBreakdown map[string]int64
}

// NewComprehensiveMetrics creates a new comprehensive metrics collector
func NewComprehensiveMetrics(namespace, subsystem string) *ComprehensiveMetrics {
	if namespace == "" {
		namespace = "harbor_replicator"
	}
	if subsystem == "" {
		subsystem = "client"
	}

	c := &ComprehensiveMetrics{
		namespace:      namespace,
		subsystem:      subsystem,
		requestTimers:  make(map[string]time.Time),
		endpointStats:  make(map[string]*EndpointStat),
		errorBreakdown: make(map[string]int64),
		summary: MetricsSummary{
			ErrorBreakdown: make(map[string]int64),
			EndpointStats:  make(map[string]EndpointStat),
		},
	}

	c.initializePrometheusMetrics()
	return c
}

// initializePrometheusMetrics initializes all Prometheus metrics
func (c *ComprehensiveMetrics) initializePrometheusMetrics() {
	c.requestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: c.namespace,
			Subsystem: c.subsystem,
			Name:      "requests_total",
			Help:      "Total number of HTTP requests made to Harbor API",
		},
		[]string{"method", "endpoint", "status_code"},
	)

	c.requestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: c.namespace,
			Subsystem: c.subsystem,
			Name:      "request_duration_seconds",
			Help:      "Duration of HTTP requests to Harbor API",
			Buckets:   []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30},
		},
		[]string{"method", "endpoint"},
	)

	c.requestSize = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: c.namespace,
			Subsystem: c.subsystem,
			Name:      "request_size_bytes",
			Help:      "Size of HTTP request bodies in bytes",
			Buckets:   prometheus.ExponentialBuckets(100, 10, 6), // 100B to 10MB
		},
		[]string{"method", "endpoint"},
	)

	c.responseSize = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: c.namespace,
			Subsystem: c.subsystem,
			Name:      "response_size_bytes",
			Help:      "Size of HTTP response bodies in bytes",
			Buckets:   prometheus.ExponentialBuckets(100, 10, 7), // 100B to 100MB
		},
		[]string{"method", "endpoint"},
	)

	c.errorsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: c.namespace,
			Subsystem: c.subsystem,
			Name:      "errors_total",
			Help:      "Total number of errors from Harbor API requests",
		},
		[]string{"method", "endpoint", "error_type"},
	)

	c.rateLimitHits = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: c.namespace,
			Subsystem: c.subsystem,
			Name:      "rate_limit_hits_total",
			Help:      "Total number of rate limit hits",
		},
	)

	c.circuitBreakerState = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: c.namespace,
			Subsystem: c.subsystem,
			Name:      "circuit_breaker_state",
			Help:      "Current state of the circuit breaker (0=closed, 1=half-open, 2=open)",
		},
		[]string{"state"},
	)

	c.connectionPoolStats = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: c.namespace,
			Subsystem: c.subsystem,
			Name:      "connection_pool_stats",
			Help:      "Connection pool statistics",
		},
		[]string{"stat_type"},
	)

	c.retryAttempts = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: c.namespace,
			Subsystem: c.subsystem,
			Name:      "retry_attempts_total",
			Help:      "Total number of retry attempts",
		},
		[]string{"method", "endpoint", "attempt", "success"},
	)

	c.queuedRequests = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: c.namespace,
			Subsystem: c.subsystem,
			Name:      "queued_requests",
			Help:      "Number of requests currently queued",
		},
	)
}

// RecordRequest records an HTTP request with its duration and status
func (c *ComprehensiveMetrics) RecordRequest(method, endpoint string, duration time.Duration, statusCode int) {
	statusStr := getStatusCodeString(statusCode)
	
	c.requestsTotal.WithLabelValues(method, endpoint, statusStr).Inc()
	c.requestDuration.WithLabelValues(method, endpoint).Observe(duration.Seconds())
	
	// Update internal tracking
	c.mu.Lock()
	defer c.mu.Unlock()
	
	c.summary.TotalRequests++
	c.summary.LastActivity = time.Now()
	
	// Update endpoint stats
	key := method + " " + endpoint
	if c.endpointStats[key] == nil {
		c.endpointStats[key] = &EndpointStat{}
	}
	stat := c.endpointStats[key]
	stat.RequestCount++
	stat.LastActivity = time.Now()
	
	// Update average latency
	if stat.AverageLatency == 0 {
		stat.AverageLatency = duration
	} else {
		stat.AverageLatency = (stat.AverageLatency + duration) / 2
	}
	
	// Update overall average response time
	if c.summary.AverageResponseTime == 0 {
		c.summary.AverageResponseTime = duration
	} else {
		c.summary.AverageResponseTime = (c.summary.AverageResponseTime + duration) / 2
	}
	
	// Update error rate
	if statusCode >= 400 {
		c.summary.TotalErrors++
		stat.ErrorCount++
	}
	
	if c.summary.TotalRequests > 0 {
		c.summary.ErrorRate = float64(c.summary.TotalErrors) / float64(c.summary.TotalRequests)
	}
}

// RecordError records an error with its type
func (c *ComprehensiveMetrics) RecordError(method, endpoint string, errorType string) {
	c.errorsTotal.WithLabelValues(method, endpoint, errorType).Inc()
	
	c.mu.Lock()
	defer c.mu.Unlock()
	
	c.summary.TotalErrors++
	c.errorBreakdown[errorType]++
	c.summary.ErrorBreakdown[errorType] = c.errorBreakdown[errorType]
	
	// Update endpoint stats
	key := method + " " + endpoint
	if c.endpointStats[key] == nil {
		c.endpointStats[key] = &EndpointStat{}
	}
	c.endpointStats[key].ErrorCount++
	
	// Update error rate
	if c.summary.TotalRequests > 0 {
		c.summary.ErrorRate = float64(c.summary.TotalErrors) / float64(c.summary.TotalRequests)
	}
}

// RecordRateLimit records a rate limit event
func (c *ComprehensiveMetrics) RecordRateLimit(limited bool) {
	if limited {
		c.rateLimitHits.Inc()
		
		c.mu.Lock()
		c.summary.RateLimitHits++
		c.mu.Unlock()
	}
}

// RecordCircuitBreakerState records the current circuit breaker state
func (c *ComprehensiveMetrics) RecordCircuitBreakerState(state string) {
	// Reset all states to 0
	c.circuitBreakerState.WithLabelValues("closed").Set(0)
	c.circuitBreakerState.WithLabelValues("half_open").Set(0)
	c.circuitBreakerState.WithLabelValues("open").Set(0)
	
	// Set current state to 1
	normalizedState := normalizeCircuitBreakerState(state)
	c.circuitBreakerState.WithLabelValues(normalizedState).Set(1)
	
	c.mu.Lock()
	c.summary.CircuitBreakerState = normalizedState
	c.mu.Unlock()
}

// RecordConnectionPoolStats records connection pool statistics
func (c *ComprehensiveMetrics) RecordConnectionPoolStats(stats map[string]float64) {
	for statType, value := range stats {
		c.connectionPoolStats.WithLabelValues(statType).Set(value)
	}
}

// RecordRetryAttempt records a retry attempt
func (c *ComprehensiveMetrics) RecordRetryAttempt(method, endpoint string, attempt int, success bool) {
	attemptStr := "attempt_" + string(rune(attempt+'0'))
	successStr := "false"
	if success {
		successStr = "true"
	}
	
	c.retryAttempts.WithLabelValues(method, endpoint, attemptStr, successStr).Inc()
}

// RecordQueuedRequests records the number of queued requests
func (c *ComprehensiveMetrics) RecordQueuedRequests(count int) {
	c.queuedRequests.Set(float64(count))
}

// RecordResponseSize records the size of a response
func (c *ComprehensiveMetrics) RecordResponseSize(method, endpoint string, size int64) {
	c.responseSize.WithLabelValues(method, endpoint).Observe(float64(size))
}

// StartRequestTimer starts a timer for a request and returns a function to stop it
func (c *ComprehensiveMetrics) StartRequestTimer(method, endpoint string) func() {
	start := time.Now()
	key := method + "_" + endpoint + "_" + start.Format(time.RFC3339Nano)
	
	c.mu.Lock()
	c.requestTimers[key] = start
	c.mu.Unlock()
	
	return func() {
		c.mu.Lock()
		if startTime, exists := c.requestTimers[key]; exists {
			delete(c.requestTimers, key)
			c.mu.Unlock()
			
			duration := time.Since(startTime)
			c.requestDuration.WithLabelValues(method, endpoint).Observe(duration.Seconds())
		} else {
			c.mu.Unlock()
		}
	}
}

// GetMetricsSummary returns a summary of all collected metrics
func (c *ComprehensiveMetrics) GetMetricsSummary() MetricsSummary {
	c.mu.RLock()
	defer c.mu.RUnlock()
	
	// Create a copy to avoid race conditions
	summary := MetricsSummary{
		TotalRequests:       c.summary.TotalRequests,
		TotalErrors:         c.summary.TotalErrors,
		AverageResponseTime: c.summary.AverageResponseTime,
		ErrorRate:           c.summary.ErrorRate,
		RateLimitHits:       c.summary.RateLimitHits,
		CircuitBreakerState: c.summary.CircuitBreakerState,
		LastActivity:        c.summary.LastActivity,
		ErrorBreakdown:      make(map[string]int64),
		EndpointStats:       make(map[string]EndpointStat),
	}
	
	// Copy error breakdown
	for k, v := range c.errorBreakdown {
		summary.ErrorBreakdown[k] = v
	}
	
	// Copy endpoint stats
	for k, v := range c.endpointStats {
		summary.EndpointStats[k] = *v
	}
	
	return summary
}

// normalizeCircuitBreakerState normalizes circuit breaker state strings
func normalizeCircuitBreakerState(state string) string {
	switch state {
	case "closed", "Closed", "CLOSED":
		return "closed"
	case "half_open", "half-open", "HalfOpen", "HALF_OPEN":
		return "half_open"
	case "open", "Open", "OPEN":
		return "open"
	default:
		return state
	}
}

// Middleware function to add metrics to HTTP middleware stack
func (c *ComprehensiveMetrics) HTTPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		
		// Wrap the response writer to capture status code and response size
		wrapped := &responseWriter{
			ResponseWriter: w,
			statusCode:     200,
		}
		
		// Record request size
		if r.ContentLength > 0 {
			c.requestSize.WithLabelValues(r.Method, r.URL.Path).Observe(float64(r.ContentLength))
		}
		
		// Execute the request
		next.ServeHTTP(wrapped, r)
		
		// Record metrics
		duration := time.Since(start)
		c.RecordRequest(r.Method, r.URL.Path, duration, wrapped.statusCode)
		
		// Record response size
		if wrapped.bytesWritten > 0 {
			c.RecordResponseSize(r.Method, r.URL.Path, wrapped.bytesWritten)
		}
	})
}

// responseWriter wraps http.ResponseWriter to capture metrics
type responseWriter struct {
	http.ResponseWriter
	statusCode    int
	bytesWritten  int64
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	n, err := rw.ResponseWriter.Write(b)
	rw.bytesWritten += int64(n)
	return n, err
}

// getStatusCodeString converts status code to string for metrics
func getStatusCodeString(statusCode int) string {
	if statusCode == 0 {
		return "error"
	}
	return strconv.Itoa(statusCode)
}