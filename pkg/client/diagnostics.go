package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"sync"
	"time"

	"harbor-replicator/pkg/config"
)

// DiagnosticsCollector provides comprehensive diagnostics for the Harbor client
type DiagnosticsCollector struct {
	client  *HarborClientWrapper
	mu      sync.RWMutex
	history []HealthCheckResult
}

// NewDiagnosticsCollector creates a new diagnostics collector
func NewDiagnosticsCollector(client *HarborClientWrapper) *DiagnosticsCollector {
	return &DiagnosticsCollector{
		client:  client,
		history: make([]HealthCheckResult, 0, 100), // Keep last 100 health checks
	}
}

// HealthCheckResult represents the result of a health check
type HealthCheckResult struct {
	Healthy        bool                   `json:"healthy"`
	Timestamp      time.Time              `json:"timestamp"`
	Latency        time.Duration          `json:"latency"`
	Version        string                 `json:"version,omitempty"`
	Details        map[string]interface{} `json:"details"`
	Errors         []string               `json:"errors,omitempty"`
	Warnings       []string               `json:"warnings,omitempty"`
	ComponentChecks map[string]ComponentHealth `json:"component_checks"`
}

// ComponentHealth represents the health status of a specific component
type ComponentHealth struct {
	Healthy     bool          `json:"healthy"`
	Status      string        `json:"status"`
	LastChecked time.Time     `json:"last_checked"`
	Latency     time.Duration `json:"latency,omitempty"`
	Error       string        `json:"error,omitempty"`
	Details     interface{}   `json:"details,omitempty"`
}

// DiagnosticsReport provides comprehensive system diagnostics
type DiagnosticsReport struct {
	Timestamp           time.Time                     `json:"timestamp"`
	OverallHealth       bool                          `json:"overall_health"`
	Client              ClientDiagnostics             `json:"client"`
	Connectivity        ConnectivityDiagnostics       `json:"connectivity"`
	Performance         PerformanceDiagnostics        `json:"performance"`
	Middleware          MiddlewareDiagnostics         `json:"middleware"`
	Configuration       ConfigurationDiagnostics      `json:"configuration"`
	Resources           ResourceDiagnostics           `json:"resources"`
	HealthHistory       []HealthCheckResult           `json:"health_history"`
	Recommendations     []string                      `json:"recommendations"`
}

// ClientDiagnostics contains client-specific diagnostic information
type ClientDiagnostics struct {
	Version         string        `json:"version"`
	UserAgent       string        `json:"user_agent"`
	BaseURL         string        `json:"base_url"`
	IsHealthy       bool          `json:"is_healthy"`
	LastHealthCheck time.Time     `json:"last_health_check"`
	Uptime          time.Duration `json:"uptime"`
	Stats           ClientStats   `json:"stats"`
}

// ConnectivityDiagnostics contains connectivity diagnostic information
type ConnectivityDiagnostics struct {
	CanConnect      bool            `json:"can_connect"`
	DNSResolution   ComponentHealth `json:"dns_resolution"`
	TCPConnection   ComponentHealth `json:"tcp_connection"`
	TLSHandshake    ComponentHealth `json:"tls_handshake"`
	HTTPResponse    ComponentHealth `json:"http_response"`
	Authentication  ComponentHealth `json:"authentication"`
	APIReachability ComponentHealth `json:"api_reachability"`
}

// PerformanceDiagnostics contains performance diagnostic information
type PerformanceDiagnostics struct {
	AverageLatency      time.Duration                 `json:"average_latency"`
	P95Latency          time.Duration                 `json:"p95_latency"`
	P99Latency          time.Duration                 `json:"p99_latency"`
	Throughput          float64                       `json:"throughput_rps"`
	ErrorRate           float64                       `json:"error_rate"`
	EndpointPerformance map[string]EndpointPerformance `json:"endpoint_performance"`
}

// EndpointPerformance contains performance metrics for specific endpoints
type EndpointPerformance struct {
	RequestCount    int64         `json:"request_count"`
	AverageLatency  time.Duration `json:"average_latency"`
	ErrorCount      int64         `json:"error_count"`
	ErrorRate       float64       `json:"error_rate"`
	LastActivity    time.Time     `json:"last_activity"`
}

// MiddlewareDiagnostics contains middleware diagnostic information
type MiddlewareDiagnostics struct {
	RateLimiter    ComponentHealth `json:"rate_limiter"`
	CircuitBreaker ComponentHealth `json:"circuit_breaker"`
	Retry          ComponentHealth `json:"retry"`
	Transport      ComponentHealth `json:"transport"`
}

// ConfigurationDiagnostics contains configuration diagnostic information
type ConfigurationDiagnostics struct {
	Valid           bool                   `json:"valid"`
	Settings        map[string]interface{} `json:"settings"`
	SecurityChecks  map[string]bool        `json:"security_checks"`
	Recommendations []string               `json:"recommendations"`
}

// ResourceDiagnostics contains resource usage diagnostic information
type ResourceDiagnostics struct {
	ConnectionPool ConnectionPoolDiagnostics `json:"connection_pool"`
	Memory         MemoryDiagnostics         `json:"memory"`
	GoRoutines     int                       `json:"go_routines"`
	GOMAXPROCS     int                       `json:"gomaxprocs"`
}

// ConnectionPoolDiagnostics contains connection pool diagnostic information
type ConnectionPoolDiagnostics struct {
	ActiveConnections int                    `json:"active_connections"`
	IdleConnections   int                    `json:"idle_connections"`
	MaxConnections    int                    `json:"max_connections"`
	PoolUtilization   float64                `json:"pool_utilization"`
	Stats             map[string]interface{} `json:"stats"`
}

// MemoryDiagnostics contains memory usage diagnostic information
type MemoryDiagnostics struct {
	AllocatedMB      float64 `json:"allocated_mb"`
	SystemMB         float64 `json:"system_mb"`
	GCCycles         uint32  `json:"gc_cycles"`
	LastGC           time.Time `json:"last_gc"`
	HeapAllocatedMB  float64 `json:"heap_allocated_mb"`
	HeapReleasedMB   float64 `json:"heap_released_mb"`
}

// PerformHealthCheck performs a comprehensive health check
func (h *HarborClientWrapper) PerformHealthCheck(ctx context.Context) (*HealthCheckResult, error) {
	start := time.Now()
	
	result := &HealthCheckResult{
		Timestamp:       start,
		Details:         make(map[string]interface{}),
		ComponentChecks: make(map[string]ComponentHealth),
		Errors:          make([]string, 0),
		Warnings:        make([]string, 0),
	}

	// Perform connectivity check
	connectivityHealth := h.checkConnectivity(ctx)
	result.ComponentChecks["connectivity"] = connectivityHealth
	if !connectivityHealth.Healthy {
		result.Errors = append(result.Errors, connectivityHealth.Error)
	}

	// Check authentication
	authHealth := h.checkAuthentication(ctx)
	result.ComponentChecks["authentication"] = authHealth
	if !authHealth.Healthy {
		result.Errors = append(result.Errors, authHealth.Error)
	}

	// Check middleware components
	middlewareHealth := h.checkMiddlewareHealth()
	for name, health := range middlewareHealth {
		result.ComponentChecks[name] = health
		if !health.Healthy {
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s: %s", name, health.Error))
		}
	}

	// Check system info endpoint
	systemHealth := h.checkSystemInfo(ctx)
	result.ComponentChecks["system_info"] = systemHealth
	if systemHealth.Healthy {
		if version, ok := systemHealth.Details.(map[string]interface{})["harbor_version"]; ok {
			result.Version = version.(string)
		}
	}

	// Calculate overall health
	result.Healthy = connectivityHealth.Healthy && authHealth.Healthy
	result.Latency = time.Since(start)

	// Add performance statistics
	h.mu.RLock()
	result.Details["total_requests"] = h.stats.TotalRequests
	result.Details["successful_requests"] = h.stats.SuccessfulRequests
	result.Details["failed_requests"] = h.stats.FailedRequests
	result.Details["average_latency"] = h.stats.AverageLatency.String()
	result.Details["last_activity"] = h.stats.LastActivity
	result.Details["circuit_breaker_trips"] = h.stats.CircuitBreakerTrips
	result.Details["rate_limit_hits"] = h.stats.RateLimitHits
	h.mu.RUnlock()

	// Update internal state
	h.mu.Lock()
	h.isHealthy = result.Healthy
	h.lastHealthCheck = start
	h.mu.Unlock()

	return result, nil
}

// checkConnectivity performs a basic connectivity check
func (h *HarborClientWrapper) checkConnectivity(ctx context.Context) ComponentHealth {
	start := time.Now()
	
	req, err := http.NewRequestWithContext(ctx, "GET", h.baseURL+"/systeminfo", nil)
	if err != nil {
		return ComponentHealth{
			Healthy:     false,
			Status:      "error",
			LastChecked: start,
			Error:       fmt.Sprintf("failed to create request: %v", err),
		}
	}

	req.Header.Set("User-Agent", h.userAgent)
	
	// Use a simple client without middleware for basic connectivity check
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return ComponentHealth{
			Healthy:     false,
			Status:      "unreachable",
			LastChecked: start,
			Latency:     time.Since(start),
			Error:       fmt.Sprintf("connection failed: %v", err),
		}
	}
	defer resp.Body.Close()

	return ComponentHealth{
		Healthy:     true,
		Status:      "reachable",
		LastChecked: start,
		Latency:     time.Since(start),
		Details:     map[string]interface{}{"status_code": resp.StatusCode},
	}
}

// checkAuthentication checks if authentication is working
func (h *HarborClientWrapper) checkAuthentication(ctx context.Context) ComponentHealth {
	start := time.Now()
	
	req, err := http.NewRequestWithContext(ctx, "GET", h.baseURL+"/systeminfo", nil)
	if err != nil {
		return ComponentHealth{
			Healthy:     false,
			Status:      "error",
			LastChecked: start,
			Error:       fmt.Sprintf("failed to create request: %v", err),
		}
	}

	req.Header.Set("Authorization", h.authHeader)
	req.Header.Set("User-Agent", h.userAgent)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return ComponentHealth{
			Healthy:     false,
			Status:      "error",
			LastChecked: start,
			Latency:     time.Since(start),
			Error:       fmt.Sprintf("request failed: %v", err),
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 {
		return ComponentHealth{
			Healthy:     false,
			Status:      "authentication_failed",
			LastChecked: start,
			Latency:     time.Since(start),
			Error:       "invalid credentials",
		}
	}

	if resp.StatusCode >= 400 {
		return ComponentHealth{
			Healthy:     false,
			Status:      "http_error",
			LastChecked: start,
			Latency:     time.Since(start),
			Error:       fmt.Sprintf("HTTP %d", resp.StatusCode),
		}
	}

	return ComponentHealth{
		Healthy:     true,
		Status:      "authenticated",
		LastChecked: start,
		Latency:     time.Since(start),
	}
}

// checkMiddlewareHealth checks the health of middleware components
func (h *HarborClientWrapper) checkMiddlewareHealth() map[string]ComponentHealth {
	checks := make(map[string]ComponentHealth)
	now := time.Now()

	// Check circuit breaker
	if h.circuitBreaker != nil {
		state := h.circuitBreaker.State()
		checks["circuit_breaker"] = ComponentHealth{
			Healthy:     state == config.CircuitBreakerClosed,
			Status:      state.String(),
			LastChecked: now,
			Details:     h.circuitBreaker.Counts(),
		}
	}

	// Check rate limiter
	if h.rateLimiter != nil {
		metrics := h.rateLimiter.GetMetrics()
		checks["rate_limiter"] = ComponentHealth{
			Healthy:     true,
			Status:      "active",
			LastChecked: now,
			Details:     metrics,
		}
	}

	// Check transport
	checks["transport"] = ComponentHealth{
		Healthy:     h.transport != nil,
		Status:      "active",
		LastChecked: now,
		Details: map[string]interface{}{
			"max_idle_conns":     h.transport.maxIdleConns,
			"max_conns_per_host": h.transport.maxConnsPerHost,
			"timeout":            h.transport.timeout.String(),
		},
	}

	return checks
}

// checkSystemInfo checks the Harbor system info endpoint
func (h *HarborClientWrapper) checkSystemInfo(ctx context.Context) ComponentHealth {
	start := time.Now()
	
	resp, err := h.Get(ctx, "/systeminfo")
	if err != nil {
		return ComponentHealth{
			Healthy:     false,
			Status:      "error",
			LastChecked: start,
			Latency:     time.Since(start),
			Error:       fmt.Sprintf("system info request failed: %v", err),
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return ComponentHealth{
			Healthy:     false,
			Status:      "http_error",
			LastChecked: start,
			Latency:     time.Since(start),
			Error:       fmt.Sprintf("HTTP %d", resp.StatusCode),
		}
	}

	// Try to parse system info for additional details
	var systemInfo map[string]interface{}
	if err := parseJSONResponse(resp, &systemInfo); err == nil {
		return ComponentHealth{
			Healthy:     true,
			Status:      "available",
			LastChecked: start,
			Latency:     time.Since(start),
			Details:     systemInfo,
		}
	}

	return ComponentHealth{
		Healthy:     true,
		Status:      "available",
		LastChecked: start,
		Latency:     time.Since(start),
	}
}

// GetDiagnostics returns comprehensive diagnostic information
func (h *HarborClientWrapper) GetDiagnostics(ctx context.Context) (*DiagnosticsReport, error) {
	start := time.Now()
	
	report := &DiagnosticsReport{
		Timestamp:       start,
		Recommendations: make([]string, 0),
	}

	// Collect client diagnostics
	report.Client = h.getClientDiagnostics()

	// Collect connectivity diagnostics
	report.Connectivity = h.getConnectivityDiagnostics(ctx)

	// Collect performance diagnostics
	report.Performance = h.getPerformanceDiagnostics()

	// Collect middleware diagnostics
	report.Middleware = h.getMiddlewareDiagnostics()

	// Collect configuration diagnostics
	report.Configuration = h.getConfigurationDiagnostics()

	// Collect resource diagnostics
	report.Resources = h.getResourceDiagnostics()

	// Perform health check and add to history
	healthResult, err := h.PerformHealthCheck(ctx)
	if err == nil {
		report.HealthHistory = []HealthCheckResult{*healthResult}
	}

	// Determine overall health
	report.OverallHealth = report.Client.IsHealthy && 
						   report.Connectivity.CanConnect && 
						   report.Middleware.CircuitBreaker.Healthy

	// Generate recommendations
	report.Recommendations = h.generateRecommendations(report)

	return report, nil
}

// getClientDiagnostics collects client-specific diagnostics
func (h *HarborClientWrapper) getClientDiagnostics() ClientDiagnostics {
	h.mu.RLock()
	defer h.mu.RUnlock()

	return ClientDiagnostics{
		Version:         "1.0.0", // TODO: Get from build
		UserAgent:       h.userAgent,
		BaseURL:         h.baseURL,
		IsHealthy:       h.isHealthy,
		LastHealthCheck: h.lastHealthCheck,
		Uptime:          time.Since(h.lastHealthCheck), // Approximate
		Stats:           h.stats,
	}
}

// getConnectivityDiagnostics collects connectivity diagnostics
func (h *HarborClientWrapper) getConnectivityDiagnostics(ctx context.Context) ConnectivityDiagnostics {
	diagnostics := ConnectivityDiagnostics{}
	
	// Basic connectivity
	diagnostics.TCPConnection = h.checkConnectivity(ctx)
	diagnostics.CanConnect = diagnostics.TCPConnection.Healthy

	// Authentication
	diagnostics.Authentication = h.checkAuthentication(ctx)

	// API reachability
	diagnostics.APIReachability = h.checkSystemInfo(ctx)

	return diagnostics
}

// getPerformanceDiagnostics collects performance diagnostics
func (h *HarborClientWrapper) getPerformanceDiagnostics() PerformanceDiagnostics {
	h.mu.RLock()
	defer h.mu.RUnlock()

	var throughput float64
	if h.stats.LastActivity.Sub(time.Now()) < time.Hour && h.stats.TotalRequests > 0 {
		throughput = float64(h.stats.TotalRequests) / time.Since(h.stats.LastActivity).Hours() / 3600
	}

	errorRate := float64(0)
	if h.stats.TotalRequests > 0 {
		errorRate = float64(h.stats.FailedRequests) / float64(h.stats.TotalRequests)
	}

	return PerformanceDiagnostics{
		AverageLatency:      h.stats.AverageLatency,
		P95Latency:          h.stats.AverageLatency * 2, // Rough estimate
		P99Latency:          h.stats.AverageLatency * 3, // Rough estimate
		Throughput:          throughput,
		ErrorRate:           errorRate,
		EndpointPerformance: make(map[string]EndpointPerformance),
	}
}

// getMiddlewareDiagnostics collects middleware diagnostics
func (h *HarborClientWrapper) getMiddlewareDiagnostics() MiddlewareDiagnostics {
	middlewareChecks := h.checkMiddlewareHealth()
	
	diagnostics := MiddlewareDiagnostics{}
	
	if cb, exists := middlewareChecks["circuit_breaker"]; exists {
		diagnostics.CircuitBreaker = cb
	}
	
	if rl, exists := middlewareChecks["rate_limiter"]; exists {
		diagnostics.RateLimiter = rl
	}
	
	if transport, exists := middlewareChecks["transport"]; exists {
		diagnostics.Transport = transport
	}

	return diagnostics
}

// getConfigurationDiagnostics collects configuration diagnostics
func (h *HarborClientWrapper) getConfigurationDiagnostics() ConfigurationDiagnostics {
	h.mu.RLock()
	cfg := h.config
	h.mu.RUnlock()

	settings := map[string]interface{}{
		"url":                      cfg.URL,
		"username":                 cfg.Username,
		"timeout":                  cfg.Timeout.String(),
		"max_idle_conns":           cfg.MaxIdleConns,
		"max_idle_conns_per_host":  cfg.MaxIdleConnsPerHost,
		"insecure_skip_verify":     cfg.InsecureSkipVerify,
		"rate_limit_enabled":       cfg.RateLimit.RequestsPerSecond > 0,
		"circuit_breaker_enabled":  cfg.CircuitBreaker.Enabled,
		"retry_enabled":            cfg.Retry.MaxAttempts > 0,
	}

	securityChecks := map[string]bool{
		"tls_enabled":           !cfg.InsecureSkipVerify,
		"authentication_set":    cfg.Username != "" && cfg.Password != "",
		"timeout_configured":    cfg.Timeout > 0,
	}

	recommendations := make([]string, 0)
	if cfg.InsecureSkipVerify {
		recommendations = append(recommendations, "Consider enabling TLS verification for production use")
	}
	if cfg.Timeout == 0 {
		recommendations = append(recommendations, "Consider setting a timeout to prevent indefinite blocking")
	}

	return ConfigurationDiagnostics{
		Valid:           validateConfig(cfg) == nil,
		Settings:        settings,
		SecurityChecks:  securityChecks,
		Recommendations: recommendations,
	}
}

// getResourceDiagnostics collects resource usage diagnostics
func (h *HarborClientWrapper) getResourceDiagnostics() ResourceDiagnostics {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	connectionPool := ConnectionPoolDiagnostics{
		MaxConnections: h.config.MaxIdleConns,
		Stats: map[string]interface{}{
			"max_idle_conns":          h.config.MaxIdleConns,
			"max_idle_conns_per_host": h.config.MaxIdleConnsPerHost,
		},
	}

	if h.config.MaxIdleConns > 0 {
		// Rough estimate of utilization based on request patterns
		connectionPool.PoolUtilization = float64(h.stats.TotalRequests%100) / 100
	}

	memory := MemoryDiagnostics{
		AllocatedMB:     float64(m.Alloc) / 1024 / 1024,
		SystemMB:        float64(m.Sys) / 1024 / 1024,
		GCCycles:        m.NumGC,
		HeapAllocatedMB: float64(m.HeapAlloc) / 1024 / 1024,
		HeapReleasedMB:  float64(m.HeapReleased) / 1024 / 1024,
	}

	if m.LastGC > 0 {
		memory.LastGC = time.Unix(0, int64(m.LastGC))
	}

	return ResourceDiagnostics{
		ConnectionPool: connectionPool,
		Memory:         memory,
		GoRoutines:     runtime.NumGoroutine(),
		GOMAXPROCS:     runtime.GOMAXPROCS(0),
	}
}

// generateRecommendations generates recommendations based on diagnostics
func (h *HarborClientWrapper) generateRecommendations(report *DiagnosticsReport) []string {
	recommendations := make([]string, 0)

	// Performance recommendations
	if report.Performance.ErrorRate > 0.1 {
		recommendations = append(recommendations, "High error rate detected - consider checking network connectivity and Harbor instance health")
	}

	if report.Performance.AverageLatency > 5*time.Second {
		recommendations = append(recommendations, "High latency detected - consider increasing timeout values or checking network performance")
	}

	// Circuit breaker recommendations
	if !report.Middleware.CircuitBreaker.Healthy {
		recommendations = append(recommendations, "Circuit breaker is open - Harbor instance may be experiencing issues")
	}

	// Resource recommendations
	if report.Resources.GoRoutines > 1000 {
		recommendations = append(recommendations, "High number of goroutines detected - consider investigating potential goroutine leaks")
	}

	if report.Resources.Memory.AllocatedMB > 500 {
		recommendations = append(recommendations, "High memory usage detected - consider monitoring memory consumption")
	}

	// Configuration recommendations
	recommendations = append(recommendations, report.Configuration.Recommendations...)

	return recommendations
}

// parseJSONResponse parses JSON response body into the provided interface
func parseJSONResponse(resp *http.Response, v interface{}) error {
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(v)
}