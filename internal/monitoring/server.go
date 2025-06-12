package monitoring

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/pprof"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
)

// MetricsServerManager manages the complete metrics server infrastructure
type MetricsServerManager struct {
	config           *ServerConfig
	logger           *zap.Logger
	server           *http.Server
	customRegistry   *CustomMetricsRegistry
	metricsServer    *MetricsServer
	
	// Server state
	mu               sync.RWMutex
	started          bool
	startTime        time.Time
	requestsServed   int64
	lastRequestTime  time.Time
}

// ServerStats contains server statistics
type ServerStats struct {
	Started         bool      `json:"started"`
	StartTime       time.Time `json:"start_time"`
	Uptime          string    `json:"uptime"`
	RequestsServed  int64     `json:"requests_served"`
	LastRequestTime time.Time `json:"last_request_time"`
	Address         string    `json:"address"`
	TLSEnabled      bool      `json:"tls_enabled"`
	AuthEnabled     bool      `json:"auth_enabled"`
}

// NewMetricsServerManager creates a new metrics server manager
func NewMetricsServerManager(config *ServerConfig, logger *zap.Logger, customRegistry *CustomMetricsRegistry) *MetricsServerManager {
	if logger == nil {
		logger = zap.NewNop()
	}

	if customRegistry == nil {
		customRegistry = NewCustomMetricsRegistry(logger)
	}

	return &MetricsServerManager{
		config:         config,
		logger:         logger,
		customRegistry: customRegistry,
	}
}

// Start starts the metrics server with all endpoints
func (msm *MetricsServerManager) Start(ctx context.Context) error {
	msm.mu.Lock()
	defer msm.mu.Unlock()

	if msm.started {
		return fmt.Errorf("metrics server already started")
	}

	// Create the main metrics server
	msm.metricsServer = SetupMetricsServer(*msm.config, msm.logger)

	// Create enhanced server with additional endpoints
	mux := http.NewServeMux()

	// Add metrics endpoints
	metricsHandler := msm.customRegistry.GetMetricsHandler()
	if msm.config.Auth != nil && msm.config.Auth.Enabled {
		metricsHandler = msm.basicAuthMiddleware(metricsHandler)
	}
	mux.Handle("/metrics", msm.loggingMiddleware(metricsHandler))

	// Add Prometheus default metrics (if using default registry)
	mux.Handle("/metrics/default", msm.loggingMiddleware(promhttp.Handler()))

	// Add health and readiness endpoints
	mux.HandleFunc("/health", msm.healthHandler)
	mux.HandleFunc("/ready", msm.readinessHandler)
	mux.HandleFunc("/live", msm.livenessHandler)

	// Add summary endpoint
	mux.Handle("/summary", msm.loggingMiddleware(msm.customRegistry.GetSummaryEndpoint()))

	// Add server stats endpoint
	mux.HandleFunc("/stats", msm.statsHandler)

	// Add pprof endpoints if enabled
	if msm.config.EnablePprof {
		msm.addPprofEndpoints(mux)
	}

	// Add debug endpoints
	mux.HandleFunc("/debug/config", msm.configHandler)
	mux.HandleFunc("/debug/registry", msm.registryHandler)

	// Create HTTP server with enhanced configuration
	msm.server = &http.Server{
		Addr:         fmt.Sprintf("%s:%d", msm.config.BindAddr, msm.config.Port),
		Handler:      msm.middlewareStack(mux),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Configure TLS if enabled
	if msm.config.TLS != nil && msm.config.TLS.Enabled {
		tlsConfig := &tls.Config{
			MinVersion: tls.VersionTLS12,
			CipherSuites: []uint16{
				tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
				tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
				tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			},
		}
		msm.server.TLSConfig = tlsConfig
	}

	// Start the server
	go func() {
		var err error
		if msm.config.TLS != nil && msm.config.TLS.Enabled {
			msm.logger.Info("Starting secure metrics server",
				zap.String("addr", msm.server.Addr),
				zap.String("cert", msm.config.TLS.CertFile),
				zap.String("key", msm.config.TLS.KeyFile))
			err = msm.server.ListenAndServeTLS(msm.config.TLS.CertFile, msm.config.TLS.KeyFile)
		} else {
			msm.logger.Info("Starting metrics server", zap.String("addr", msm.server.Addr))
			err = msm.server.ListenAndServe()
		}

		if err != nil && err != http.ErrServerClosed {
			msm.logger.Fatal("metrics server failed", zap.Error(err))
		}
	}()

	msm.started = true
	msm.startTime = time.Now()
	msm.logger.Info("Metrics server manager started successfully")

	return nil
}

// Stop gracefully stops the metrics server
func (msm *MetricsServerManager) Stop(ctx context.Context) error {
	msm.mu.Lock()
	defer msm.mu.Unlock()

	if !msm.started {
		return nil
	}

	msm.logger.Info("Stopping metrics server manager...")

	// Stop custom registry background monitoring
	msm.customRegistry.StopBackgroundMonitoring()

	// Stop main server
	if msm.server != nil {
		if err := msm.server.Shutdown(ctx); err != nil {
			msm.logger.Error("Error stopping metrics server", zap.Error(err))
			return err
		}
	}

	// Stop metrics server
	if msm.metricsServer != nil {
		if err := msm.metricsServer.Stop(ctx); err != nil {
			msm.logger.Error("Error stopping metrics server", zap.Error(err))
			return err
		}
	}

	msm.started = false
	msm.logger.Info("Metrics server manager stopped successfully")
	return nil
}

// IsStarted returns whether the server is started
func (msm *MetricsServerManager) IsStarted() bool {
	msm.mu.RLock()
	defer msm.mu.RUnlock()
	return msm.started
}

// GetCustomRegistry returns the custom metrics registry
func (msm *MetricsServerManager) GetCustomRegistry() *CustomMetricsRegistry {
	return msm.customRegistry
}

// GetStats returns server statistics
func (msm *MetricsServerManager) GetStats() ServerStats {
	msm.mu.RLock()
	defer msm.mu.RUnlock()

	var uptime string
	if msm.started {
		uptime = time.Since(msm.startTime).String()
	}

	return ServerStats{
		Started:         msm.started,
		StartTime:       msm.startTime,
		Uptime:          uptime,
		RequestsServed:  msm.requestsServed,
		LastRequestTime: msm.lastRequestTime,
		Address:         msm.server.Addr,
		TLSEnabled:      msm.config.TLS != nil && msm.config.TLS.Enabled,
		AuthEnabled:     msm.config.Auth != nil && msm.config.Auth.Enabled,
	}
}

// Middleware functions

// middlewareStack applies all middleware to the handler
func (msm *MetricsServerManager) middlewareStack(handler http.Handler) http.Handler {
	// Apply middleware in reverse order
	handler = msm.panicRecoveryMiddleware(handler)
	handler = msm.requestCountingMiddleware(handler)
	handler = msm.loggingMiddleware(handler)
	return handler
}

// loggingMiddleware logs HTTP requests
func (msm *MetricsServerManager) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		
		wrapped := &responseWriter{
			ResponseWriter: w,
			statusCode:     200,
		}
		
		next.ServeHTTP(wrapped, r)
		
		duration := time.Since(start)
		msm.logger.Debug("HTTP request",
			zap.String("method", r.Method),
			zap.String("path", r.URL.Path),
			zap.String("remote_addr", r.RemoteAddr),
			zap.Int("status_code", wrapped.statusCode),
			zap.Duration("duration", duration),
		)
	})
}

// panicRecoveryMiddleware recovers from panics
func (msm *MetricsServerManager) panicRecoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				msm.logger.Error("Panic in HTTP handler",
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

// requestCountingMiddleware counts requests
func (msm *MetricsServerManager) requestCountingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		msm.mu.Lock()
		msm.requestsServed++
		msm.lastRequestTime = time.Now()
		msm.mu.Unlock()
		
		next.ServeHTTP(w, r)
	})
}

// basicAuthMiddleware provides basic authentication
func (msm *MetricsServerManager) basicAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if msm.config.Auth == nil || !msm.config.Auth.Enabled {
			next.ServeHTTP(w, r)
			return
		}

		username, password, ok := r.BasicAuth()
		if !ok || username != msm.config.Auth.Username || password != msm.config.Auth.Password {
			w.Header().Set("WWW-Authenticate", `Basic realm="Metrics"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// HTTP handler functions

// healthHandler handles health check requests
func (msm *MetricsServerManager) healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	
	uptime := time.Since(msm.startTime)
	response := fmt.Sprintf(`{
		"status": "healthy",
		"uptime": "%s",
		"timestamp": "%s",
		"version": "1.0.0"
	}`, uptime, time.Now().Format(time.RFC3339))
	
	w.Write([]byte(response))
}

// readinessHandler handles readiness check requests
func (msm *MetricsServerManager) readinessHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	
	response := fmt.Sprintf(`{
		"status": "ready",
		"timestamp": "%s",
		"server_started": %t
	}`, time.Now().Format(time.RFC3339), msm.started)
	
	w.Write([]byte(response))
}

// livenessHandler handles liveness check requests
func (msm *MetricsServerManager) livenessHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	
	response := fmt.Sprintf(`{
		"status": "alive",
		"timestamp": "%s"
	}`, time.Now().Format(time.RFC3339))
	
	w.Write([]byte(response))
}

// statsHandler provides server statistics
func (msm *MetricsServerManager) statsHandler(w http.ResponseWriter, r *http.Request) {
	stats := msm.GetStats()
	
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	
	if err := json.NewEncoder(w).Encode(stats); err != nil {
		msm.logger.Error("Failed to encode stats response", zap.Error(err))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

// configHandler provides configuration information (debug endpoint)
func (msm *MetricsServerManager) configHandler(w http.ResponseWriter, r *http.Request) {
	// Only show safe configuration information
	config := map[string]interface{}{
		"port":         msm.config.Port,
		"bind_addr":    msm.config.BindAddr,
		"tls_enabled":  msm.config.TLS != nil && msm.config.TLS.Enabled,
		"auth_enabled": msm.config.Auth != nil && msm.config.Auth.Enabled,
		"pprof_enabled": msm.config.EnablePprof,
		"timestamp":    time.Now().Format(time.RFC3339),
	}
	
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	
	if err := json.NewEncoder(w).Encode(config); err != nil {
		msm.logger.Error("Failed to encode config response", zap.Error(err))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

// registryHandler provides information about registered metrics
func (msm *MetricsServerManager) registryHandler(w http.ResponseWriter, r *http.Request) {
	// Get metric families from registry
	metricFamilies, err := msm.customRegistry.GetRegistry().Gather()
	if err != nil {
		msm.logger.Error("Failed to gather metrics", zap.Error(err))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	
	info := map[string]interface{}{
		"metric_families": len(metricFamilies),
		"timestamp":       time.Now().Format(time.RFC3339),
	}
	
	// Add summary of metric families
	familyNames := make([]string, len(metricFamilies))
	for i, family := range metricFamilies {
		familyNames[i] = family.GetName()
	}
	info["families"] = familyNames
	
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	
	if err := json.NewEncoder(w).Encode(info); err != nil {
		msm.logger.Error("Failed to encode registry response", zap.Error(err))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

// addPprofEndpoints adds pprof debugging endpoints
func (msm *MetricsServerManager) addPprofEndpoints(mux *http.ServeMux) {
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	
	// Add additional pprof handlers
	mux.Handle("/debug/pprof/heap", pprof.Handler("heap"))
	mux.Handle("/debug/pprof/goroutine", pprof.Handler("goroutine"))
	mux.Handle("/debug/pprof/block", pprof.Handler("block"))
	mux.Handle("/debug/pprof/mutex", pprof.Handler("mutex"))
	
	msm.logger.Info("Added pprof debug endpoints")
}

// StartBackgroundMonitoring starts background monitoring for the custom registry
func (msm *MetricsServerManager) StartBackgroundMonitoring(ctx context.Context, interval time.Duration) error {
	return msm.customRegistry.StartBackgroundMonitoring(ctx, interval)
}

// RegisterHarborInstance is a convenience method to register a Harbor instance
func (msm *MetricsServerManager) RegisterHarborInstance(name, url, instanceType string) error {
	return msm.customRegistry.RegisterHarborInstance(name, url, instanceType)
}

// RegisterHealthCheck is a convenience method to register a health check
func (msm *MetricsServerManager) RegisterHealthCheck(name, endpoint string, interval, timeout time.Duration) error {
	return msm.customRegistry.RegisterHealthCheck(name, endpoint, interval, timeout)
}