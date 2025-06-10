package client

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"harbor-replicator/pkg/config"
)

// RetryMiddleware implements HTTP retry logic as middleware
type RetryMiddleware struct {
	retryer   *Retryer
	transport http.RoundTripper
	metrics   RetryMetrics
	mutex     sync.RWMutex
}

// RetryMetrics tracks retry statistics
type RetryMetrics struct {
	TotalRequests     int64
	RetriedRequests   int64
	FailedRequests    int64
	AverageAttempts   float64
	RetryReasons      map[ErrorType]int64
	LastUpdate        time.Time
}

// NewRetryMiddleware creates a new retry middleware
func NewRetryMiddleware(cfg *config.RetryConfig, transport http.RoundTripper) *RetryMiddleware {
	if transport == nil {
		transport = http.DefaultTransport
	}

	return &RetryMiddleware{
		retryer:   NewRetryer(cfg),
		transport: transport,
		metrics: RetryMetrics{
			RetryReasons: make(map[ErrorType]int64),
			LastUpdate:   time.Now(),
		},
	}
}

// RoundTrip implements the http.RoundTripper interface with retry logic
func (rm *RetryMiddleware) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone request for potential retries
	reqClone, err := cloneRequest(req)
	if err != nil {
		return nil, fmt.Errorf("failed to clone request: %w", err)
	}

	var lastResp *http.Response
	var attemptCount int

	// Track metrics
	rm.mutex.Lock()
	rm.metrics.TotalRequests++
	rm.mutex.Unlock()

	operation := func() error {
		attemptCount++

		// Use original request for first attempt, cloned for retries
		currentReq := req
		if attemptCount > 1 {
			currentReq = reqClone
			// Reset body for retry if it exists
			if err := resetRequestBody(currentReq); err != nil {
				return NewHarborError(err, "reset_request_body")
			}
		}

		resp, err := rm.transport.RoundTrip(currentReq)
		if err != nil {
			return NewHarborError(err, "http_request")
		}

		// Check for HTTP error status codes
		if resp.StatusCode >= 400 {
			lastResp = resp
			harborErr := NewHarborHTTPError(resp, "http_request")
			return harborErr
		}

		lastResp = resp
		return nil
	}

	// Execute with retry logic
	ctx := req.Context()
	err = rm.retryer.Execute(ctx, operation, "http_request")

	// Update metrics
	rm.updateMetrics(attemptCount, err)

	if err != nil {
		return lastResp, err
	}

	return lastResp, nil
}

// updateMetrics updates retry statistics
func (rm *RetryMiddleware) updateMetrics(attemptCount int, finalErr error) {
	rm.mutex.Lock()
	defer rm.mutex.Unlock()

	if attemptCount > 1 {
		rm.metrics.RetriedRequests++
	}

	if finalErr != nil {
		rm.metrics.FailedRequests++
		if harborErr, ok := finalErr.(*HarborError); ok {
			rm.metrics.RetryReasons[harborErr.Type]++
		}
	}

	// Update average attempts
	totalAttempts := rm.metrics.AverageAttempts*float64(rm.metrics.TotalRequests-1) + float64(attemptCount)
	rm.metrics.AverageAttempts = totalAttempts / float64(rm.metrics.TotalRequests)
	rm.metrics.LastUpdate = time.Now()
}

// GetMetrics returns current retry metrics
func (rm *RetryMiddleware) GetMetrics() RetryMetrics {
	rm.mutex.RLock()
	defer rm.mutex.RUnlock()

	// Create a copy to avoid data races
	metrics := rm.metrics
	metrics.RetryReasons = make(map[ErrorType]int64)
	for k, v := range rm.metrics.RetryReasons {
		metrics.RetryReasons[k] = v
	}

	return metrics
}

// ResetMetrics resets all retry metrics
func (rm *RetryMiddleware) ResetMetrics() {
	rm.mutex.Lock()
	defer rm.mutex.Unlock()

	rm.metrics = RetryMetrics{
		RetryReasons: make(map[ErrorType]int64),
		LastUpdate:   time.Now(),
	}
}

// cloneRequest creates a deep copy of an HTTP request
func cloneRequest(req *http.Request) (*http.Request, error) {
	if req == nil {
		return nil, fmt.Errorf("request is nil")
	}

	// Create a new request
	clone := &http.Request{
		Method:        req.Method,
		URL:           req.URL,
		Proto:         req.Proto,
		ProtoMajor:    req.ProtoMajor,
		ProtoMinor:    req.ProtoMinor,
		Header:        make(http.Header),
		Host:          req.Host,
		RemoteAddr:    req.RemoteAddr,
		RequestURI:    req.RequestURI,
		TLS:           req.TLS,
		Cancel:        req.Cancel,
		Response:      req.Response,
	}

	// Clone headers
	for key, values := range req.Header {
		clone.Header[key] = append([]string(nil), values...)
	}

	// Clone body if it exists
	if req.Body != nil {
		bodyBytes, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to read request body: %w", err)
		}

		// Reset original body
		req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		
		// Set clone body
		clone.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		clone.ContentLength = req.ContentLength
	}

	// Clone context
	clone = clone.WithContext(req.Context())

	return clone, nil
}

// resetRequestBody resets the request body for retries
func resetRequestBody(req *http.Request) error {
	if req.Body == nil {
		return nil
	}

	// If the body is already a bytes.Reader, we can reset it
	if seeker, ok := req.Body.(io.Seeker); ok {
		_, err := seeker.Seek(0, io.SeekStart)
		return err
	}

	// For other body types, we need to read and recreate
	// This assumes the body was cloned earlier
	return nil
}

// RetryableHTTPClient wraps an HTTP client with retry functionality
type RetryableHTTPClient struct {
	client      *http.Client
	retryer     *Retryer
	baseTimeout time.Duration
}

// NewRetryableHTTPClient creates a new retryable HTTP client
func NewRetryableHTTPClient(cfg *config.RetryConfig, timeout time.Duration) *RetryableHTTPClient {
	client := &http.Client{
		Timeout: timeout,
	}

	return &RetryableHTTPClient{
		client:      client,
		retryer:     NewRetryer(cfg),
		baseTimeout: timeout,
	}
}

// Do executes an HTTP request with retry logic
func (rc *RetryableHTTPClient) Do(req *http.Request) (*http.Response, error) {
	operation := func() (interface{}, error) {
		// Clone request for each attempt
		reqClone, err := cloneRequest(req)
		if err != nil {
			return nil, NewHarborError(err, "clone_request")
		}

		resp, err := rc.client.Do(reqClone)
		if err != nil {
			return nil, NewHarborError(err, "http_do")
		}

		// Check for HTTP error status codes
		if resp.StatusCode >= 400 {
			harborErr := NewHarborHTTPError(resp, "http_do")
			return resp, harborErr
		}

		return resp, nil
	}

	ctx := req.Context()
	result, err := rc.retryer.ExecuteWithResult(ctx, operation, "http_do")
	if err != nil {
		return nil, err
	}
	
	if resp, ok := result.(*http.Response); ok {
		return resp, nil
	}
	
	return nil, NewHarborError(fmt.Errorf("unexpected result type"), "http_do")
}

// Get executes a GET request with retry logic
func (rc *RetryableHTTPClient) Get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, NewHarborError(err, "create_get_request")
	}

	return rc.Do(req)
}

// Post executes a POST request with retry logic
func (rc *RetryableHTTPClient) Post(ctx context.Context, url, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", url, body)
	if err != nil {
		return nil, NewHarborError(err, "create_post_request")
	}

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	return rc.Do(req)
}

// Put executes a PUT request with retry logic
func (rc *RetryableHTTPClient) Put(ctx context.Context, url, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "PUT", url, body)
	if err != nil {
		return nil, NewHarborError(err, "create_put_request")
	}

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	return rc.Do(req)
}

// Delete executes a DELETE request with retry logic
func (rc *RetryableHTTPClient) Delete(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "DELETE", url, nil)
	if err != nil {
		return nil, NewHarborError(err, "create_delete_request")
	}

	return rc.Do(req)
}

// SetTimeout updates the client timeout
func (rc *RetryableHTTPClient) SetTimeout(timeout time.Duration) {
	rc.client.Timeout = timeout
	rc.baseTimeout = timeout
}

// GetClient returns the underlying HTTP client
func (rc *RetryableHTTPClient) GetClient() *http.Client {
	return rc.client
}

// Close closes the underlying HTTP client
func (rc *RetryableHTTPClient) Close() error {
	if rc.client != nil {
		rc.client.CloseIdleConnections()
	}
	return nil
}

// BackoffStrategy represents different backoff strategies
type BackoffStrategy int

const (
	BackoffExponential BackoffStrategy = iota
	BackoffLinear
	BackoffFixed
)

// CustomRetryer provides more advanced retry functionality
type CustomRetryer struct {
	*Retryer
	strategy          BackoffStrategy
	shouldRetryFunc   func(error) bool
	onRetryFunc       func(attempt int, err error, delay time.Duration)
	maxJitterPercent  float64
}

// CustomRetryerOption configures a CustomRetryer
type CustomRetryerOption func(*CustomRetryer)

// WithBackoffStrategy sets the backoff strategy
func WithBackoffStrategy(strategy BackoffStrategy) CustomRetryerOption {
	return func(cr *CustomRetryer) {
		cr.strategy = strategy
	}
}

// WithShouldRetryFunc sets a custom function to determine if an error should be retried
func WithShouldRetryFunc(fn func(error) bool) CustomRetryerOption {
	return func(cr *CustomRetryer) {
		cr.shouldRetryFunc = fn
	}
}

// WithOnRetryFunc sets a callback function called on each retry attempt
func WithOnRetryFunc(fn func(attempt int, err error, delay time.Duration)) CustomRetryerOption {
	return func(cr *CustomRetryer) {
		cr.onRetryFunc = fn
	}
}

// WithMaxJitterPercent sets the maximum jitter percentage (0.0 to 1.0)
func WithMaxJitterPercent(percent float64) CustomRetryerOption {
	return func(cr *CustomRetryer) {
		if percent >= 0 && percent <= 1 {
			cr.maxJitterPercent = percent
		}
	}
}

// NewCustomRetryer creates a new CustomRetryer with options
func NewCustomRetryer(cfg *config.RetryConfig, options ...CustomRetryerOption) *CustomRetryer {
	cr := &CustomRetryer{
		Retryer:          NewRetryer(cfg),
		strategy:         BackoffExponential,
		maxJitterPercent: 0.25, // 25% default jitter
	}

	for _, option := range options {
		option(cr)
	}

	return cr
}

// Execute executes an operation with custom retry logic
func (cr *CustomRetryer) Execute(ctx context.Context, operation RetryableOperation, operationName string) error {
	var lastErr error

	for attempt := 1; attempt <= cr.maxAttempts; attempt++ {
		err := operation()
		if err == nil {
			return nil
		}

		lastErr = err

		// Use custom retry function if provided
		shouldRetry := cr.shouldRetryFunc != nil && cr.shouldRetryFunc(err)
		if !shouldRetry {
			// Fall back to default Harbor error checking
			if harborErr, ok := err.(*HarborError); ok {
				shouldRetry = harborErr.IsRetryable()
			} else {
				shouldRetry = IsRetryableError(err)
			}
		}

		if !shouldRetry || attempt == cr.maxAttempts {
			break
		}

		// Calculate delay using custom strategy
		delay := cr.calculateCustomDelay(attempt, err)

		// Call retry callback if provided
		if cr.onRetryFunc != nil {
			cr.onRetryFunc(attempt, err, delay)
		}

		// Wait for retry delay or context cancellation
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
			// Continue to next attempt
		}
	}

	// Return the last error
	if harborErr, ok := lastErr.(*HarborError); ok {
		return harborErr
	}
	return NewHarborError(lastErr, operationName)
}

// calculateCustomDelay calculates delay based on the configured strategy
func (cr *CustomRetryer) calculateCustomDelay(attempt int, err error) time.Duration {
	var baseDelay time.Duration

	// Check if error provides a retry-after duration
	if harborErr, ok := err.(*HarborError); ok {
		if retryAfter := harborErr.GetRetryAfter(); retryAfter > 0 && retryAfter <= cr.maxDelay {
			return retryAfter
		}
	}

	// Calculate base delay based on strategy
	switch cr.strategy {
	case BackoffLinear:
		baseDelay = time.Duration(int64(cr.baseDelay) * int64(attempt))
	case BackoffFixed:
		baseDelay = cr.baseDelay
	case BackoffExponential:
		fallthrough
	default:
		baseDelay = time.Duration(float64(cr.baseDelay) * pow(cr.multiplier, float64(attempt-1)))
	}

	// Cap at max delay
	if baseDelay > cr.maxDelay {
		baseDelay = cr.maxDelay
	}

	// Add jitter if enabled
	if cr.jitter && baseDelay > 0 {
		jitterAmount := time.Duration(float64(baseDelay) * cr.maxJitterPercent)
		jitter := time.Duration(float64(jitterAmount) * (2*randomFloat() - 1))
		baseDelay += jitter

		// Ensure delay is not negative
		if baseDelay < 0 {
			baseDelay = cr.baseDelay
		}
	}

	return baseDelay
}