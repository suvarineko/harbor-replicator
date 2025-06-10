package client

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"

	"harbor-replicator/pkg/config"
)

// Error types for categorization
type ErrorType int

const (
	ErrorTypeTransient ErrorType = iota
	ErrorTypePermanent
	ErrorTypeRateLimit
	ErrorTypeAuth
	ErrorTypeNetwork
	ErrorTypeTimeout
	ErrorTypeCircuitBreaker
)

func (e ErrorType) String() string {
	switch e {
	case ErrorTypeTransient:
		return "transient"
	case ErrorTypePermanent:
		return "permanent"
	case ErrorTypeRateLimit:
		return "rate_limit"
	case ErrorTypeAuth:
		return "auth"
	case ErrorTypeNetwork:
		return "network"
	case ErrorTypeTimeout:
		return "timeout"
	case ErrorTypeCircuitBreaker:
		return "circuit_breaker"
	default:
		return "unknown"
	}
}

// HarborError wraps errors with additional context and categorization
type HarborError struct {
	Type          ErrorType
	OriginalError error
	StatusCode    int
	Message       string
	Retryable     bool
	RetryAfter    time.Duration
	Operation     string
	Timestamp     time.Time
}

func (e *HarborError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.OriginalError != nil {
		return e.OriginalError.Error()
	}
	return fmt.Sprintf("harbor error: type=%s, operation=%s", e.Type.String(), e.Operation)
}

func (e *HarborError) Unwrap() error {
	return e.OriginalError
}

func (e *HarborError) IsRetryable() bool {
	return e.Retryable
}

func (e *HarborError) GetRetryAfter() time.Duration {
	return e.RetryAfter
}

// NewHarborError creates a new HarborError with proper categorization
func NewHarborError(err error, operation string) *HarborError {
	if err == nil {
		return nil
	}

	harborErr := &HarborError{
		OriginalError: err,
		Operation:     operation,
		Timestamp:     time.Now(),
	}

	categorizeError(harborErr)
	return harborErr
}

// NewHarborHTTPError creates a HarborError from an HTTP response
func NewHarborHTTPError(resp *http.Response, operation string) *HarborError {
	if resp == nil {
		return &HarborError{
			Type:      ErrorTypeNetwork,
			Message:   "nil HTTP response",
			Operation: operation,
			Retryable: true,
			Timestamp: time.Now(),
		}
	}

	harborErr := &HarborError{
		StatusCode: resp.StatusCode,
		Operation:  operation,
		Timestamp:  time.Now(),
	}

	categorizeHTTPError(harborErr, resp)
	return harborErr
}

// categorizeError determines the error type and retry behavior
func categorizeError(harborErr *HarborError) {
	err := harborErr.OriginalError

	// Check for circuit breaker errors
	if err == ErrCircuitBreakerOpen || err == ErrTooManyRequests {
		harborErr.Type = ErrorTypeCircuitBreaker
		harborErr.Retryable = true
		harborErr.RetryAfter = 30 * time.Second
		return
	}

	// Check for rate limit errors
	if rateLimitErr, ok := err.(*RateLimitError); ok {
		harborErr.Type = ErrorTypeRateLimit
		harborErr.Retryable = true
		harborErr.RetryAfter = rateLimitErr.RetryAfter
		return
	}

	// Check for context errors
	if err == context.DeadlineExceeded || err == context.Canceled {
		harborErr.Type = ErrorTypeTimeout
		harborErr.Retryable = true
		return
	}

	// Check for network errors
	if isNetworkError(err) {
		harborErr.Type = ErrorTypeNetwork
		harborErr.Retryable = true
		return
	}

	// Default to permanent error for unknown error types
	harborErr.Type = ErrorTypePermanent
	harborErr.Retryable = false
}

// categorizeHTTPError categorizes errors based on HTTP status codes
func categorizeHTTPError(harborErr *HarborError, resp *http.Response) {
	statusCode := resp.StatusCode

	switch {
	case statusCode >= 500 && statusCode < 600:
		// 5xx errors are typically transient server errors
		harborErr.Type = ErrorTypeTransient
		harborErr.Retryable = true
		harborErr.Message = fmt.Sprintf("server error: %d %s", statusCode, resp.Status)
		
		// Check for Retry-After header
		if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "" {
			if duration, err := time.ParseDuration(retryAfter + "s"); err == nil {
				harborErr.RetryAfter = duration
			}
		}

	case statusCode == 429:
		// Rate limiting
		harborErr.Type = ErrorTypeRateLimit
		harborErr.Retryable = true
		harborErr.Message = "rate limit exceeded"
		
		if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "" {
			if duration, err := time.ParseDuration(retryAfter + "s"); err == nil {
				harborErr.RetryAfter = duration
			}
		}
		if harborErr.RetryAfter == 0 {
			harborErr.RetryAfter = 60 * time.Second // Default retry after
		}

	case statusCode == 401 || statusCode == 403:
		// Authentication/authorization errors
		harborErr.Type = ErrorTypeAuth
		harborErr.Retryable = false
		harborErr.Message = fmt.Sprintf("authentication error: %d %s", statusCode, resp.Status)

	case statusCode >= 400 && statusCode < 500:
		// Other 4xx errors are typically permanent client errors
		harborErr.Type = ErrorTypePermanent
		harborErr.Retryable = false
		harborErr.Message = fmt.Sprintf("client error: %d %s", statusCode, resp.Status)

	case statusCode >= 200 && statusCode < 300:
		// Success status codes shouldn't create errors, but handle just in case
		harborErr.Type = ErrorTypePermanent
		harborErr.Retryable = false
		harborErr.Message = fmt.Sprintf("unexpected success status in error: %d %s", statusCode, resp.Status)

	default:
		// Unknown status codes
		harborErr.Type = ErrorTypeTransient
		harborErr.Retryable = true
		harborErr.Message = fmt.Sprintf("unknown status code: %d %s", statusCode, resp.Status)
	}
}

// isNetworkError checks if an error is a network-related error
func isNetworkError(err error) bool {
	if err == nil {
		return false
	}

	// Check for common network error types
	if netErr, ok := err.(net.Error); ok {
		return netErr.Timeout() || netErr.Temporary()
	}

	// Check for specific network error types
	if _, ok := err.(*net.OpError); ok {
		return true
	}

	if _, ok := err.(*net.DNSError); ok {
		return true
	}

	// Check for syscall errors
	if opErr, ok := err.(*net.OpError); ok {
		if syscallErr, ok := opErr.Err.(*syscall.Errno); ok {
			switch *syscallErr {
			case syscall.ECONNREFUSED, syscall.ECONNRESET, syscall.ETIMEDOUT:
				return true
			}
		}
	}

	// Check for common error messages
	errMsg := strings.ToLower(err.Error())
	networkKeywords := []string{
		"connection refused",
		"connection reset",
		"connection timeout",
		"network unreachable",
		"host unreachable",
		"no route to host",
		"broken pipe",
		"i/o timeout",
	}

	for _, keyword := range networkKeywords {
		if strings.Contains(errMsg, keyword) {
			return true
		}
	}

	return false
}

// RetryableOperation represents an operation that can be retried
type RetryableOperation func() error

// RetryableOperationWithResult represents an operation that can be retried and returns a result
type RetryableOperationWithResult func() (interface{}, error)

// Retryer handles retry logic with exponential backoff
type Retryer struct {
	config      *config.RetryConfig
	maxAttempts int
	baseDelay   time.Duration
	maxDelay    time.Duration
	multiplier  float64
	jitter      bool
}

// NewRetryer creates a new Retryer with the given configuration
func NewRetryer(cfg *config.RetryConfig) *Retryer {
	if cfg == nil {
		cfg = &config.RetryConfig{
			MaxAttempts:  3,
			InitialDelay: 1 * time.Second,
			MaxDelay:     30 * time.Second,
			Multiplier:   2.0,
			Jitter:       true,
		}
	}

	return &Retryer{
		config:      cfg,
		maxAttempts: cfg.MaxAttempts,
		baseDelay:   cfg.InitialDelay,
		maxDelay:    cfg.MaxDelay,
		multiplier:  cfg.Multiplier,
		jitter:      cfg.Jitter,
	}
}

// Execute executes an operation with retry logic
func (r *Retryer) Execute(ctx context.Context, operation RetryableOperation, operationName string) error {
	var lastErr error

	for attempt := 1; attempt <= r.maxAttempts; attempt++ {
		err := operation()
		if err == nil {
			return nil
		}

		lastErr = err

		// Create or wrap error with Harbor error context
		var harborErr *HarborError
		if he, ok := err.(*HarborError); ok {
			harborErr = he
		} else {
			harborErr = NewHarborError(err, operationName)
		}

		// Don't retry permanent errors
		if !harborErr.IsRetryable() {
			return harborErr
		}

		// Don't retry if this is the last attempt
		if attempt == r.maxAttempts {
			break
		}

		// Calculate retry delay
		delay := r.calculateDelay(attempt, harborErr.GetRetryAfter())

		// Wait for retry delay or context cancellation
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
			// Continue to next attempt
		}
	}

	// Return the last error if all retries failed
	if harborErr, ok := lastErr.(*HarborError); ok {
		return harborErr
	}
	return NewHarborError(lastErr, operationName)
}

// ExecuteWithResult executes an operation with retry logic and returns a result
func (r *Retryer) ExecuteWithResult(ctx context.Context, operation RetryableOperationWithResult, operationName string) (interface{}, error) {
	var lastErr error

	for attempt := 1; attempt <= r.maxAttempts; attempt++ {
		result, err := operation()
		if err == nil {
			return result, nil
		}

		lastErr = err

		// Create or wrap error with Harbor error context
		var harborErr *HarborError
		if he, ok := err.(*HarborError); ok {
			harborErr = he
		} else {
			harborErr = NewHarborError(err, operationName)
		}

		// Don't retry permanent errors
		if !harborErr.IsRetryable() {
			return nil, harborErr
		}

		// Don't retry if this is the last attempt
		if attempt == r.maxAttempts {
			break
		}

		// Calculate retry delay
		delay := r.calculateDelay(attempt, harborErr.GetRetryAfter())

		// Wait for retry delay or context cancellation
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
			// Continue to next attempt
		}
	}

	// Return the last error if all retries failed
	if harborErr, ok := lastErr.(*HarborError); ok {
		return nil, harborErr
	}
	return nil, NewHarborError(lastErr, operationName)
}

// calculateDelay calculates the delay before the next retry attempt
func (r *Retryer) calculateDelay(attempt int, retryAfter time.Duration) time.Duration {
	// Use explicit retry-after if provided and it's reasonable
	if retryAfter > 0 && retryAfter <= r.maxDelay {
		return retryAfter
	}

	// Calculate exponential backoff delay
	delay := float64(r.baseDelay) * pow(r.multiplier, float64(attempt-1))

	// Cap at max delay
	if time.Duration(delay) > r.maxDelay {
		delay = float64(r.maxDelay)
	}

	finalDelay := time.Duration(delay)

	// Add jitter if enabled
	if r.jitter && finalDelay > 0 {
		jitterAmount := finalDelay / 4 // 25% jitter
		finalDelay = finalDelay + time.Duration(float64(jitterAmount)*(2*randomFloat()-1))
		
		// Ensure delay is not negative
		if finalDelay < 0 {
			finalDelay = time.Duration(delay) / 2
		}
	}

	return finalDelay
}

// pow calculates x^y for float64 (simple implementation for our use case)
func pow(x, y float64) float64 {
	if y == 0 {
		return 1
	}
	result := x
	for i := 1; i < int(y); i++ {
		result *= x
	}
	return result
}

// randomFloat returns a random float between 0 and 1 (simple implementation)
func randomFloat() float64 {
	// Simple pseudo-random implementation for jitter
	// In production, you might want to use a better random source
	seed := time.Now().UnixNano()
	return float64((seed%1000))/1000.0
}

// IsRetryableError checks if an error is retryable
func IsRetryableError(err error) bool {
	if err == nil {
		return false
	}

	if harborErr, ok := err.(*HarborError); ok {
		return harborErr.IsRetryable()
	}

	// For non-Harbor errors, apply basic retry logic
	if err == context.DeadlineExceeded || err == context.Canceled {
		return false // Don't retry context cancellations
	}

	if isNetworkError(err) {
		return true
	}

	return false
}

// GetErrorType returns the type of a Harbor error
func GetErrorType(err error) ErrorType {
	if harborErr, ok := err.(*HarborError); ok {
		return harborErr.Type
	}
	return ErrorTypePermanent
}

// IsTemporaryError checks if an error is temporary and should be retried
func IsTemporaryError(err error) bool {
	if harborErr, ok := err.(*HarborError); ok {
		return harborErr.Type == ErrorTypeTransient || harborErr.Type == ErrorTypeNetwork
	}
	return isNetworkError(err)
}

// IsAuthError checks if an error is an authentication error
func IsAuthError(err error) bool {
	if harborErr, ok := err.(*HarborError); ok {
		return harborErr.Type == ErrorTypeAuth
	}
	return false
}

// IsRateLimitError checks if an error is a rate limiting error
func IsRateLimitError(err error) bool {
	if harborErr, ok := err.(*HarborError); ok {
		return harborErr.Type == ErrorTypeRateLimit
	}
	if _, ok := err.(*RateLimitError); ok {
		return true
	}
	return false
}