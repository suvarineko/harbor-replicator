package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"harbor-replicator/pkg/config"
)

// HarborClientWrapper wraps the Harbor client with middleware and additional functionality
type HarborClientWrapper struct {
	config         *config.HarborInstanceConfig
	httpClient     *http.Client
	transport      *Transport
	rateLimiter    *RateLimiterMiddleware
	circuitBreaker *CircuitBreakerMiddleware
	retryer        *Retryer
	logger         Logger
	metrics        MetricsCollector
	userAgent      string
	baseURL        string
	authHeader     string
	mu             sync.RWMutex
	stats          ClientStats
	isHealthy      bool
	lastHealthCheck time.Time
}

// NewHarborClient creates a new Harbor client with all middleware components
func NewHarborClient(cfg *config.HarborInstanceConfig, logger Logger, metrics MetricsCollector) (*HarborClientWrapper, error) {
	if cfg == nil {
		return nil, fmt.Errorf("configuration is required")
	}

	if logger == nil {
		return nil, fmt.Errorf("logger is required")
	}

	// Validate configuration
	if err := validateConfig(cfg); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	// Parse and validate base URL
	baseURL, err := parseAndValidateURL(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("invalid Harbor URL: %w", err)
	}

	client := &HarborClientWrapper{
		config:    cfg,
		logger:    logger,
		metrics:   metrics,
		baseURL:   baseURL,
		userAgent: fmt.Sprintf("harbor-replicator/%s", "1.0.0"), // TODO: Get version from build
		stats:     ClientStats{},
	}

	// Initialize authentication
	if err := client.initializeAuth(); err != nil {
		return nil, fmt.Errorf("failed to initialize authentication: %w", err)
	}

	// Build HTTP client with middleware stack
	if err := client.buildHTTPClient(); err != nil {
		return nil, fmt.Errorf("failed to build HTTP client: %w", err)
	}

	// Test connectivity
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := client.testConnectivity(ctx); err != nil {
		return nil, fmt.Errorf("failed to connect to Harbor: %w", err)
	}

	client.isHealthy = true
	client.lastHealthCheck = time.Now()

	logger.Info("Harbor client initialized successfully", 
		"url", client.baseURL,
		"username", cfg.Username)

	return client, nil
}

// validateConfig validates the Harbor configuration
func validateConfig(cfg *config.HarborInstanceConfig) error {
	if cfg.URL == "" {
		return fmt.Errorf("URL is required")
	}

	if cfg.Username == "" {
		return fmt.Errorf("username is required")
	}

	if cfg.Password == "" {
		return fmt.Errorf("password is required")
	}

	// Validate timeout values
	if cfg.Timeout > 0 && cfg.Timeout < time.Second {
		return fmt.Errorf("timeout must be at least 1 second")
	}

	// Validate rate limiting config
	if cfg.RateLimit.RequestsPerSecond < 0 {
		return fmt.Errorf("requests per second cannot be negative")
	}

	if cfg.RateLimit.BurstSize < 1 {
		return fmt.Errorf("burst size must be at least 1")
	}

	return nil
}

// parseAndValidateURL parses and validates the Harbor URL
func parseAndValidateURL(rawURL string) (string, error) {
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		rawURL = "https://" + rawURL
	}

	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid URL format: %w", err)
	}

	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return "", fmt.Errorf("URL must use http or https scheme")
	}

	if parsedURL.Host == "" {
		return "", fmt.Errorf("URL must include a host")
	}

	// Ensure path ends with /api/v2.0 for Harbor API
	if !strings.HasSuffix(parsedURL.Path, "/api/v2.0") {
		if parsedURL.Path == "" || parsedURL.Path == "/" {
			parsedURL.Path = "/api/v2.0"
		} else {
			parsedURL.Path = strings.TrimSuffix(parsedURL.Path, "/") + "/api/v2.0"
		}
	}

	return parsedURL.String(), nil
}

// initializeAuth sets up authentication headers
func (h *HarborClientWrapper) initializeAuth() error {
	// For now, we'll use basic auth. In the future, we can add support for other auth methods
	h.authHeader = "Basic " + basicAuth(h.config.Username, h.config.Password)
	return nil
}

// buildHTTPClient constructs the HTTP client with middleware stack
func (h *HarborClientWrapper) buildHTTPClient() error {
	// Start with base transport
	baseTransport := &http.Transport{
		MaxIdleConns:          h.config.MaxIdleConns,
		MaxIdleConnsPerHost:   h.config.MaxIdleConnsPerHost,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	// Configure TLS
	if h.config.InsecureSkipVerify {
		baseTransport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}

	// Wrap with our custom transport
	h.transport = &Transport{
		baseTransport:   baseTransport,
		timeout:         h.config.Timeout,
		maxIdleConns:    h.config.MaxIdleConns,
		maxConnsPerHost: h.config.MaxIdleConnsPerHost,
	}

	var transport http.RoundTripper = h.transport

	// Add circuit breaker middleware
	if h.config.CircuitBreaker.Enabled {
		h.circuitBreaker = NewCircuitBreaker(&h.config.CircuitBreaker)
		h.circuitBreaker.OnStateChange(h.onCircuitBreakerStateChange)
		transport = &circuitBreakerTransport{
			next:           transport,
			circuitBreaker: h.circuitBreaker,
		}
	}

	// Add rate limiting middleware
	if h.config.RateLimit.RequestsPerSecond > 0 {
		h.rateLimiter = NewRateLimiter(&h.config.RateLimit)
		transport = &rateLimiterTransport{
			next:        transport,
			rateLimiter: h.rateLimiter,
		}
	}

	// Add retry middleware
	retryTransport := NewRetryMiddleware(&h.config.Retry, transport)
	h.retryer = retryTransport.retryer

	// Create HTTP client
	timeout := h.config.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	h.httpClient = &http.Client{
		Transport: retryTransport,
		Timeout:   timeout,
	}

	return nil
}

// testConnectivity verifies that we can connect to Harbor
func (h *HarborClientWrapper) testConnectivity(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", h.baseURL+"/systeminfo", nil)
	if err != nil {
		return fmt.Errorf("failed to create test request: %w", err)
	}

	req.Header.Set("Authorization", h.authHeader)
	req.Header.Set("User-Agent", h.userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to connect to Harbor: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 {
		return fmt.Errorf("authentication failed: invalid credentials")
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("Harbor returned error status: %d", resp.StatusCode)
	}

	return nil
}

// onCircuitBreakerStateChange handles circuit breaker state changes
func (h *HarborClientWrapper) onCircuitBreakerStateChange(name string, from, to config.CircuitBreakerState) {
	h.logger.Info("Circuit breaker state changed",
		"name", name,
		"from", from.String(),
		"to", to.String())

	if h.metrics != nil {
		h.metrics.RecordCircuitBreakerState(to.String())
	}

	h.mu.Lock()
	if to == config.CircuitBreakerOpen {
		h.stats.CircuitBreakerTrips++
		h.isHealthy = false
	} else if to == config.CircuitBreakerClosed {
		h.isHealthy = true
	}
	h.mu.Unlock()
}

// Get executes a GET request
func (h *HarborClientWrapper) Get(ctx context.Context, path string, options ...RequestOption) (*http.Response, error) {
	return h.makeRequest(ctx, "GET", path, nil, options...)
}

// Post executes a POST request
func (h *HarborClientWrapper) Post(ctx context.Context, path string, body interface{}, options ...RequestOption) (*http.Response, error) {
	return h.makeRequest(ctx, "POST", path, body, options...)
}

// Put executes a PUT request
func (h *HarborClientWrapper) Put(ctx context.Context, path string, body interface{}, options ...RequestOption) (*http.Response, error) {
	return h.makeRequest(ctx, "PUT", path, body, options...)
}

// Delete executes a DELETE request
func (h *HarborClientWrapper) Delete(ctx context.Context, path string, options ...RequestOption) (*http.Response, error) {
	return h.makeRequest(ctx, "DELETE", path, nil, options...)
}

// makeRequest is the core request method that handles all HTTP operations
func (h *HarborClientWrapper) makeRequest(ctx context.Context, method, path string, body interface{}, options ...RequestOption) (*http.Response, error) {
	start := time.Now()

	// Apply options
	reqOptions := &RequestOptions{
		Method:      method,
		Path:        path,
		Body:        body,
		Headers:     make(map[string]string),
		QueryParams: make(map[string]string),
		Retryable:   true,
	}

	for _, option := range options {
		option(reqOptions)
	}

	// Build URL
	fullURL := h.baseURL + path
	if len(reqOptions.QueryParams) > 0 {
		urlObj, _ := url.Parse(fullURL)
		q := urlObj.Query()
		for k, v := range reqOptions.QueryParams {
			q.Set(k, v)
		}
		urlObj.RawQuery = q.Encode()
		fullURL = urlObj.String()
	}

	// Create request
	req, err := h.createHTTPRequest(ctx, method, fullURL, body, reqOptions)
	if err != nil {
		return nil, NewHarborError(err, "create_request")
	}

	// Execute request
	resp, err := h.httpClient.Do(req)

	// Update stats
	duration := time.Since(start)
	h.updateStats(method, path, duration, resp, err)

	// Record metrics
	if h.metrics != nil {
		statusCode := 0
		if resp != nil {
			statusCode = resp.StatusCode
		}
		h.metrics.RecordRequest(method, path, duration, statusCode)
		if err != nil {
			errorType := "unknown"
			if harborErr, ok := err.(*HarborError); ok {
				errorType = harborErr.Type.String()
			}
			h.metrics.RecordError(method, path, errorType)
		}
	}

	if err != nil {
		return resp, err
	}

	return resp, nil
}

// createHTTPRequest creates an HTTP request with proper headers
func (h *HarborClientWrapper) createHTTPRequest(ctx context.Context, method, url string, body interface{}, options *RequestOptions) (*http.Request, error) {
	var reqBody io.Reader
	if body != nil {
		switch v := body.(type) {
		case io.Reader:
			reqBody = v
		case string:
			reqBody = strings.NewReader(v)
		case []byte:
			reqBody = bytes.NewReader(v)
		default:
			// Assume JSON
			jsonData, err := json.Marshal(body)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal request body: %w", err)
			}
			reqBody = bytes.NewReader(jsonData)
			options.Headers["Content-Type"] = "application/json"
		}
	}

	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, err
	}

	// Set default headers
	req.Header.Set("Authorization", h.authHeader)
	req.Header.Set("User-Agent", h.userAgent)
	req.Header.Set("Accept", "application/json")

	// Set custom headers
	for key, value := range options.Headers {
		req.Header.Set(key, value)
	}

	return req, nil
}

// updateStats updates client statistics
func (h *HarborClientWrapper) updateStats(method, path string, duration time.Duration, resp *http.Response, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.stats.TotalRequests++
	h.stats.LastActivity = time.Now()

	if err != nil {
		h.stats.FailedRequests++
	} else if resp != nil && resp.StatusCode < 400 {
		h.stats.SuccessfulRequests++
	} else {
		h.stats.FailedRequests++
	}

	// Update average latency
	totalLatency := h.stats.AverageLatency*time.Duration(h.stats.TotalRequests-1) + duration
	h.stats.AverageLatency = totalLatency / time.Duration(h.stats.TotalRequests)
}

// HealthCheck performs a health check of the Harbor connection
func (h *HarborClientWrapper) HealthCheck(ctx context.Context) (*HealthCheckResult, error) {
	start := time.Now()

	result := &HealthCheckResult{
		Healthy:        false,
		Timestamp:      start,
		Details:        make(map[string]interface{}),
		ComponentChecks: make(map[string]ComponentHealth),
		Errors:         make([]string, 0),
		Warnings:       make([]string, 0),
	}

	// Test basic connectivity
	err := h.testConnectivity(ctx)
	if err != nil {
		result.Details["error"] = err.Error()
		return result, err
	}

	result.Healthy = true
	result.Latency = time.Since(start)

	// Add additional health information
	h.mu.RLock()
	result.Details["total_requests"] = h.stats.TotalRequests
	result.Details["successful_requests"] = h.stats.SuccessfulRequests
	result.Details["failed_requests"] = h.stats.FailedRequests
	result.Details["average_latency"] = h.stats.AverageLatency.String()
	result.Details["last_activity"] = h.stats.LastActivity
	h.mu.RUnlock()

	// Add circuit breaker status
	if h.circuitBreaker != nil {
		result.Details["circuit_breaker_state"] = h.circuitBreaker.State().String()
		result.Details["circuit_breaker_counts"] = h.circuitBreaker.Counts()
	}

	// Add rate limiter status
	if h.rateLimiter != nil {
		result.Details["rate_limiter_metrics"] = h.rateLimiter.GetMetrics()
	}

	h.mu.Lock()
	h.isHealthy = result.Healthy
	h.lastHealthCheck = start
	h.mu.Unlock()

	return result, nil
}

// GetStats returns current client statistics
func (h *HarborClientWrapper) GetStats() ClientStats {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.stats
}

// IsHealthy returns whether the client is currently healthy
func (h *HarborClientWrapper) IsHealthy() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.isHealthy
}

// UpdateConfig updates the client configuration
func (h *HarborClientWrapper) UpdateConfig(newConfig *config.HarborInstanceConfig) error {
	if newConfig == nil {
		return fmt.Errorf("configuration cannot be nil")
	}

	if err := validateConfig(newConfig); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	h.config = newConfig

	// Update authentication if credentials changed
	if err := h.initializeAuth(); err != nil {
		return fmt.Errorf("failed to update authentication: %w", err)
	}

	// Update rate limiter if configuration changed
	if h.rateLimiter != nil {
		h.rateLimiter.UpdateConfig(&newConfig.RateLimit)
	}

	h.logger.Info("Harbor client configuration updated")
	return nil
}

// Close closes the client and cleans up resources
func (h *HarborClientWrapper) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.httpClient != nil {
		h.httpClient.CloseIdleConnections()
	}

	h.logger.Info("Harbor client closed")
	return nil
}

// GetBaseURL returns the base URL for the Harbor API
func (h *HarborClientWrapper) GetBaseURL() string {
	return h.baseURL
}

// GetConfig returns a copy of the current configuration
func (h *HarborClientWrapper) GetConfig() *config.HarborInstanceConfig {
	h.mu.RLock()
	defer h.mu.RUnlock()

	// Return a copy to prevent external modification
	configCopy := *h.config
	return &configCopy
}

// Helper transport wrappers for middleware integration

type circuitBreakerTransport struct {
	next           http.RoundTripper
	circuitBreaker *CircuitBreakerMiddleware
}

func (t *circuitBreakerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.circuitBreaker.RoundTrip(req)
}

type rateLimiterTransport struct {
	next        http.RoundTripper
	rateLimiter *RateLimiterMiddleware
}

func (t *rateLimiterTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.rateLimiter.RoundTrip(req)
}

// Utility functions

// basicAuth creates a basic auth header value
func basicAuth(username, password string) string {
	auth := username + ":" + password
	return base64.StdEncoding.EncodeToString([]byte(auth))
}