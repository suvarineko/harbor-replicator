package monitoring

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
)

var (
	syncTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "harbor_replicator_sync_total",
			Help: "Total number of synchronization cycles",
		},
		[]string{"status"},
	)

	resourcesSynced = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "harbor_replicator_resources_synced",
			Help: "Total resources synchronized",
		},
		[]string{"type", "source", "operation"},
	)

	syncDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "harbor_replicator_sync_duration_seconds",
			Help:    "Duration of sync operations",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"source", "resource_type"},
	)

	// Additional metrics for comprehensive monitoring
	syncErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "harbor_replicator_sync_errors_total",
			Help: "Total number of sync errors by error type",
		},
		[]string{"error_type", "resource_type", "source"},
	)

	activeSyncOperations = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "harbor_replicator_active_sync_operations",
			Help: "Number of currently active sync operations",
		},
	)

	resourceQueueSize = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "harbor_replicator_resource_queue_size",
			Help: "Number of resources queued for synchronization",
		},
		[]string{"resource_type"},
	)

	apiCallLatency = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "harbor_replicator_api_call_latency_seconds",
			Help:    "Latency of API calls to Harbor instances",
			Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
		},
		[]string{"source", "endpoint", "method"},
	)

	lastSuccessfulSync = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "harbor_replicator_last_successful_sync_timestamp",
			Help: "Timestamp of last successful sync operation",
		},
		[]string{"resource_type", "source"},
	)

	// Health and configuration metrics
	harborInstanceHealth = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "harbor_replicator_harbor_instance_health",
			Help: "Health status of Harbor instances (1=healthy, 0=unhealthy)",
		},
		[]string{"instance", "type"},
	)

	replicationLag = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "harbor_replicator_replication_lag_seconds",
			Help: "Time lag between source and target synchronization",
		},
		[]string{"source", "target", "resource_type"},
	)

	configurationDrift = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "harbor_replicator_configuration_drift",
			Help: "Number of configuration differences detected",
		},
		[]string{"source", "target", "resource_type"},
	)

	resourceQuotaUsage = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "harbor_replicator_resource_quota_usage_percent",
			Help: "Resource quota usage percentage",
		},
		[]string{"instance", "resource_type"},
	)
)

func init() {
	prometheus.MustRegister(
		syncTotal,
		resourcesSynced,
		syncDuration,
		syncErrors,
		activeSyncOperations,
		resourceQueueSize,
		apiCallLatency,
		lastSuccessfulSync,
		harborInstanceHealth,
		replicationLag,
		configurationDrift,
		resourceQuotaUsage,
	)
}

// MetricsServer represents the HTTP server for exposing metrics
type MetricsServer struct {
	server     *http.Server
	logger     *zap.Logger
	started    bool
	mu         sync.RWMutex
	startTime  time.Time
	tlsConfig  *TLSConfig
	authConfig *AuthConfig
}

// TLSConfig contains TLS configuration for the metrics server
type TLSConfig struct {
	Enabled  bool   `yaml:"enabled"`
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
}

// AuthConfig contains authentication configuration for the metrics server
type AuthConfig struct {
	Enabled  bool              `yaml:"enabled"`
	Username string            `yaml:"username"`
	Password string            `yaml:"password"`
	Headers  map[string]string `yaml:"headers"`
}

// ServerConfig contains the full configuration for the metrics server
type ServerConfig struct {
	Port       int         `yaml:"port"`
	BindAddr   string      `yaml:"bind_addr"`
	TLS        *TLSConfig  `yaml:"tls"`
	Auth       *AuthConfig `yaml:"auth"`
	EnablePprof bool       `yaml:"enable_pprof"`
}

// SetupMetricsServer creates and configures the metrics HTTP server
func SetupMetricsServer(config ServerConfig, logger *zap.Logger) *MetricsServer {
	if logger == nil {
		logger = zap.NewNop()
	}

	// Default configuration
	if config.Port == 0 {
		config.Port = 8080
	}
	if config.BindAddr == "" {
		config.BindAddr = "0.0.0.0"
	}

	ms := &MetricsServer{
		logger:     logger,
		startTime:  time.Now(),
		tlsConfig:  config.TLS,
		authConfig: config.Auth,
	}

	mux := http.NewServeMux()

	// Add metrics endpoint
	metricsHandler := promhttp.Handler()
	if config.Auth != nil && config.Auth.Enabled {
		metricsHandler = ms.basicAuthMiddleware(metricsHandler)
	}
	mux.Handle("/metrics", ms.loggingMiddleware(metricsHandler))

	// Add health endpoints
	mux.HandleFunc("/health", ms.healthHandler)
	mux.HandleFunc("/ready", ms.readinessHandler)

	// Add pprof endpoints if enabled
	if config.EnablePprof {
		mux.HandleFunc("/debug/pprof/", ms.pprofHandler)
	}

	ms.server = &http.Server{
		Addr:         fmt.Sprintf("%s:%d", config.BindAddr, config.Port),
		Handler:      ms.panicRecoveryMiddleware(mux),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	return ms
}

// Start starts the metrics server
func (ms *MetricsServer) Start() error {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	if ms.started {
		return fmt.Errorf("metrics server already started")
	}

	go func() {
		var err error
		if ms.tlsConfig != nil && ms.tlsConfig.Enabled {
			ms.logger.Info("Starting metrics server with TLS",
				zap.String("addr", ms.server.Addr),
				zap.String("cert", ms.tlsConfig.CertFile),
				zap.String("key", ms.tlsConfig.KeyFile))
			err = ms.server.ListenAndServeTLS(ms.tlsConfig.CertFile, ms.tlsConfig.KeyFile)
		} else {
			ms.logger.Info("Starting metrics server", zap.String("addr", ms.server.Addr))
			err = ms.server.ListenAndServe()
		}

		if err != nil && err != http.ErrServerClosed {
			ms.logger.Fatal("metrics server failed", zap.Error(err))
		}
	}()

	ms.started = true
	ms.logger.Info("Metrics server started successfully")
	return nil
}

// Stop gracefully stops the metrics server
func (ms *MetricsServer) Stop(ctx context.Context) error {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	if !ms.started {
		return nil
	}

	ms.logger.Info("Stopping metrics server...")

	err := ms.server.Shutdown(ctx)
	if err != nil {
		ms.logger.Error("Error stopping metrics server", zap.Error(err))
		return err
	}

	ms.started = false
	ms.logger.Info("Metrics server stopped successfully")
	return nil
}

// IsStarted returns whether the server is started
func (ms *MetricsServer) IsStarted() bool {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	return ms.started
}

// GetAddr returns the server address
func (ms *MetricsServer) GetAddr() string {
	return ms.server.Addr
}

// Middleware functions

// loggingMiddleware logs HTTP requests
func (ms *MetricsServer) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		
		wrapped := &responseWriter{
			ResponseWriter: w,
			statusCode:     200,
		}
		
		next.ServeHTTP(wrapped, r)
		
		duration := time.Since(start)
		ms.logger.Debug("HTTP request",
			zap.String("method", r.Method),
			zap.String("path", r.URL.Path),
			zap.String("remote_addr", r.RemoteAddr),
			zap.Int("status_code", wrapped.statusCode),
			zap.Duration("duration", duration),
		)
	})
}

// panicRecoveryMiddleware recovers from panics and logs them
func (ms *MetricsServer) panicRecoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				ms.logger.Error("Panic in HTTP handler",
					zap.Any("error", err),
					zap.String("path", r.URL.Path),
					zap.String("method", r.Method),
				)
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// basicAuthMiddleware provides basic authentication
func (ms *MetricsServer) basicAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ms.authConfig == nil || !ms.authConfig.Enabled {
			next.ServeHTTP(w, r)
			return
		}

		username, password, ok := r.BasicAuth()
		if !ok || username != ms.authConfig.Username || password != ms.authConfig.Password {
			w.Header().Set("WWW-Authenticate", `Basic realm="Metrics"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// HTTP handler functions

// healthHandler handles health check requests
func (ms *MetricsServer) healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	
	uptime := time.Since(ms.startTime)
	response := fmt.Sprintf(`{
		"status": "healthy",
		"uptime": "%s",
		"timestamp": "%s"
	}`, uptime, time.Now().Format(time.RFC3339))
	
	w.Write([]byte(response))
}

// readinessHandler handles readiness check requests
func (ms *MetricsServer) readinessHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	
	response := fmt.Sprintf(`{
		"status": "ready",
		"timestamp": "%s"
	}`, time.Now().Format(time.RFC3339))
	
	w.Write([]byte(response))
}

// pprofHandler provides access to pprof debugging endpoints
func (ms *MetricsServer) pprofHandler(w http.ResponseWriter, r *http.Request) {
	// This would typically import and use net/http/pprof
	// For security, we'll provide a simple status response
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("pprof endpoints available at /debug/pprof/\n"))
}

// responseWriter wraps http.ResponseWriter to capture status code
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// Metric recording functions

// RecordSyncOperation records a sync operation
func RecordSyncOperation(status string) {
	syncTotal.WithLabelValues(status).Inc()
}

// RecordResourceSynced records a synchronized resource
func RecordResourceSynced(resourceType, source, operation string) {
	resourcesSynced.WithLabelValues(resourceType, source, operation).Inc()
}

// RecordSyncDuration records the duration of a sync operation
func RecordSyncDuration(source, resourceType string, duration time.Duration) {
	syncDuration.WithLabelValues(source, resourceType).Observe(duration.Seconds())
}

// RecordSyncError records a sync error
func RecordSyncError(errorType, resourceType, source string) {
	syncErrors.WithLabelValues(errorType, resourceType, source).Inc()
}

// SetActiveSyncOperations sets the number of active sync operations
func SetActiveSyncOperations(count float64) {
	activeSyncOperations.Set(count)
}

// SetResourceQueueSize sets the resource queue size
func SetResourceQueueSize(resourceType string, size float64) {
	resourceQueueSize.WithLabelValues(resourceType).Set(size)
}

// RecordAPICallLatency records API call latency
func RecordAPICallLatency(source, endpoint, method string, duration time.Duration) {
	apiCallLatency.WithLabelValues(source, endpoint, method).Observe(duration.Seconds())
}

// SetLastSuccessfulSync updates the timestamp of last successful sync
func SetLastSuccessfulSync(resourceType, source string) {
	lastSuccessfulSync.WithLabelValues(resourceType, source).SetToCurrentTime()
}

// SetHarborInstanceHealth sets the health status of a Harbor instance
func SetHarborInstanceHealth(instance, instanceType string, healthy bool) {
	value := 0.0
	if healthy {
		value = 1.0
	}
	harborInstanceHealth.WithLabelValues(instance, instanceType).Set(value)
}

// SetReplicationLag sets the replication lag metric
func SetReplicationLag(source, target, resourceType string, lag time.Duration) {
	replicationLag.WithLabelValues(source, target, resourceType).Set(lag.Seconds())
}

// SetConfigurationDrift sets the configuration drift metric
func SetConfigurationDrift(source, target, resourceType string, driftCount float64) {
	configurationDrift.WithLabelValues(source, target, resourceType).Set(driftCount)
}

// SetResourceQuotaUsage sets the resource quota usage percentage
func SetResourceQuotaUsage(instance, resourceType string, usage float64) {
	resourceQuotaUsage.WithLabelValues(instance, resourceType).Set(usage)
}

// GetMetricsRegistry returns the default Prometheus registry
func GetMetricsRegistry() *prometheus.Registry {
	return prometheus.DefaultRegisterer.(*prometheus.Registry)
}