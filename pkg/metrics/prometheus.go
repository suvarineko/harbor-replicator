package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// PrometheusMetrics implements MetricsCollector for Prometheus
type PrometheusMetrics struct {
	requestsTotal    *prometheus.CounterVec
	requestDuration  *prometheus.HistogramVec
	errorsTotal      *prometheus.CounterVec
	rateLimitHits    prometheus.Counter
	circuitBreakerState *prometheus.GaugeVec
	connectionPoolStats *prometheus.GaugeVec
}

// NewPrometheusMetrics creates a new Prometheus metrics collector
func NewPrometheusMetrics(namespace, subsystem string) *PrometheusMetrics {
	if namespace == "" {
		namespace = "harbor_replicator"
	}
	if subsystem == "" {
		subsystem = "client"
	}

	return &PrometheusMetrics{
		requestsTotal: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "requests_total",
				Help:      "Total number of HTTP requests made to Harbor API",
			},
			[]string{"method", "endpoint", "status_code"},
		),
		requestDuration: promauto.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "request_duration_seconds",
				Help:      "Duration of HTTP requests to Harbor API",
				Buckets:   prometheus.DefBuckets, // Default buckets: .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10
			},
			[]string{"method", "endpoint"},
		),
		errorsTotal: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "errors_total",
				Help:      "Total number of errors from Harbor API requests",
			},
			[]string{"method", "endpoint", "error_type"},
		),
		rateLimitHits: promauto.NewCounter(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "rate_limit_hits_total",
				Help:      "Total number of rate limit hits",
			},
		),
		circuitBreakerState: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "circuit_breaker_state",
				Help:      "Current state of the circuit breaker (0=closed, 1=half-open, 2=open)",
			},
			[]string{"state"},
		),
		connectionPoolStats: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "connection_pool_stats",
				Help:      "Connection pool statistics",
			},
			[]string{"stat_type"},
		),
	}
}

// RecordRequest records an HTTP request with its duration and status
func (p *PrometheusMetrics) RecordRequest(method, endpoint string, duration time.Duration, statusCode int) {
	statusStr := getStatusCodeString(statusCode)
	
	p.requestsTotal.WithLabelValues(method, endpoint, statusStr).Inc()
	p.requestDuration.WithLabelValues(method, endpoint).Observe(duration.Seconds())
}

// RecordError records an error with its type
func (p *PrometheusMetrics) RecordError(method, endpoint string, errorType string) {
	p.errorsTotal.WithLabelValues(method, endpoint, errorType).Inc()
}

// RecordRateLimit records a rate limit event
func (p *PrometheusMetrics) RecordRateLimit(limited bool) {
	if limited {
		p.rateLimitHits.Inc()
	}
}

// RecordCircuitBreakerState records the current circuit breaker state
func (p *PrometheusMetrics) RecordCircuitBreakerState(state string) {
	// Reset all states to 0
	p.circuitBreakerState.WithLabelValues("closed").Set(0)
	p.circuitBreakerState.WithLabelValues("half_open").Set(0)
	p.circuitBreakerState.WithLabelValues("open").Set(0)
	
	// Set current state to 1
	p.circuitBreakerState.WithLabelValues(state).Set(1)
}

// RecordConnectionPoolStats records connection pool statistics
func (p *PrometheusMetrics) RecordConnectionPoolStats(stats map[string]float64) {
	for statType, value := range stats {
		p.connectionPoolStats.WithLabelValues(statType).Set(value)
	}
}


// NoOpMetrics provides a no-op implementation of MetricsCollector
type NoOpMetrics struct{}

// NewNoOpMetrics creates a new no-op metrics collector
func NewNoOpMetrics() *NoOpMetrics {
	return &NoOpMetrics{}
}

// RecordRequest is a no-op implementation
func (n *NoOpMetrics) RecordRequest(method, endpoint string, duration time.Duration, statusCode int) {
	// No-op
}

// RecordError is a no-op implementation
func (n *NoOpMetrics) RecordError(method, endpoint string, errorType string) {
	// No-op
}

// RecordRateLimit is a no-op implementation
func (n *NoOpMetrics) RecordRateLimit(limited bool) {
	// No-op
}

// RecordCircuitBreakerState is a no-op implementation
func (n *NoOpMetrics) RecordCircuitBreakerState(state string) {
	// No-op
}