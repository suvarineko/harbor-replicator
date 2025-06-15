package sync

import (
	"context"
	"fmt"
	"math"
	"net"
	"net/http"
	"time"
)

// RetryConfig defines comprehensive retry configuration with jitter and customization
type RetryConfig struct {
	// Basic retry settings
	MaxRetries   int           `yaml:"max_retries" json:"max_retries"`
	InitialDelay time.Duration `yaml:"initial_delay" json:"initial_delay"`
	MaxDelay     time.Duration `yaml:"max_delay" json:"max_delay"`
	Multiplier   float64       `yaml:"multiplier" json:"multiplier"`

	// Jitter configuration
	JitterEnabled bool          `yaml:"jitter_enabled" json:"jitter_enabled"`
	JitterType    JitterType    `yaml:"jitter_type" json:"jitter_type"`
	JitterFactor  float64       `yaml:"jitter_factor" json:"jitter_factor"` // 0.0 to 1.0
	
	// Backoff strategy
	BackoffStrategy BackoffStrategy `yaml:"backoff_strategy" json:"backoff_strategy"`
	
	// Context deadline awareness
	RespectDeadline bool `yaml:"respect_deadline" json:"respect_deadline"`
	
	// Retry-After header support
	RespectRetryAfter bool          `yaml:"respect_retry_after" json:"respect_retry_after"`
	MaxRetryAfter     time.Duration `yaml:"max_retry_after" json:"max_retry_after"`
	
	// Custom retry predicate
	RetryPredicate RetryPredicate `yaml:"-" json:"-"`
	
	// Per-operation settings
	OperationConfigs map[string]*OperationRetryConfig `yaml:"operation_configs" json:"operation_configs"`
	
	// Circuit breaker integration
	CircuitBreakerEnabled bool `yaml:"circuit_breaker_enabled" json:"circuit_breaker_enabled"`
}

// OperationRetryConfig provides operation-specific retry configuration
type OperationRetryConfig struct {
	MaxRetries      *int           `yaml:"max_retries,omitempty" json:"max_retries,omitempty"`
	InitialDelay    *time.Duration `yaml:"initial_delay,omitempty" json:"initial_delay,omitempty"`
	MaxDelay        *time.Duration `yaml:"max_delay,omitempty" json:"max_delay,omitempty"`
	Multiplier      *float64       `yaml:"multiplier,omitempty" json:"multiplier,omitempty"`
	JitterEnabled   *bool          `yaml:"jitter_enabled,omitempty" json:"jitter_enabled,omitempty"`
	JitterFactor    *float64       `yaml:"jitter_factor,omitempty" json:"jitter_factor,omitempty"`
	BackoffStrategy *BackoffStrategy `yaml:"backoff_strategy,omitempty" json:"backoff_strategy,omitempty"`
	RetryPredicate  RetryPredicate `yaml:"-" json:"-"`
}

// JitterType defines different jitter calculation methods
type JitterType string

const (
	JitterTypeNone       JitterType = "none"
	JitterTypeFull       JitterType = "full"       // Full jitter: [0, delay]
	JitterTypeEqual      JitterType = "equal"      // Equal jitter: [delay/2, delay]
	JitterTypeDecorelated JitterType = "decorrelated" // Decorrelated jitter
)

// BackoffStrategy defines different backoff calculation strategies
type BackoffStrategy string

const (
	BackoffExponential BackoffStrategy = "exponential"
	BackoffLinear      BackoffStrategy = "linear"
	BackoffFixed       BackoffStrategy = "fixed"
	BackoffFibonacci   BackoffStrategy = "fibonacci"
	BackoffPolynomial  BackoffStrategy = "polynomial"
)

// RetryPredicate is a function that determines if an error should be retried
type RetryPredicate func(err error, attempt int, elapsed time.Duration) bool

// DefaultRetryConfig returns a sensible default retry configuration
func DefaultRetryConfig() *RetryConfig {
	return &RetryConfig{
		MaxRetries:            3,
		InitialDelay:          1 * time.Second,
		MaxDelay:              30 * time.Second,
		Multiplier:            2.0,
		JitterEnabled:         true,
		JitterType:            JitterTypeEqual,
		JitterFactor:          0.25,
		BackoffStrategy:       BackoffExponential,
		RespectDeadline:       true,
		RespectRetryAfter:     true,
		MaxRetryAfter:         5 * time.Minute,
		CircuitBreakerEnabled: false,
		OperationConfigs:      make(map[string]*OperationRetryConfig),
	}
}

// GetOperationConfig returns the effective configuration for a specific operation
func (rc *RetryConfig) GetOperationConfig(operation string) *RetryConfig {
	if opConfig, exists := rc.OperationConfigs[operation]; exists {
		// Create a copy of the base config
		effective := *rc
		
		// Override with operation-specific values
		if opConfig.MaxRetries != nil {
			effective.MaxRetries = *opConfig.MaxRetries
		}
		if opConfig.InitialDelay != nil {
			effective.InitialDelay = *opConfig.InitialDelay
		}
		if opConfig.MaxDelay != nil {
			effective.MaxDelay = *opConfig.MaxDelay
		}
		if opConfig.Multiplier != nil {
			effective.Multiplier = *opConfig.Multiplier
		}
		if opConfig.JitterEnabled != nil {
			effective.JitterEnabled = *opConfig.JitterEnabled
		}
		if opConfig.JitterFactor != nil {
			effective.JitterFactor = *opConfig.JitterFactor
		}
		if opConfig.BackoffStrategy != nil {
			effective.BackoffStrategy = *opConfig.BackoffStrategy
		}
		if opConfig.RetryPredicate != nil {
			effective.RetryPredicate = opConfig.RetryPredicate
		}
		
		return &effective
	}
	
	return rc
}

// EnhancedRetryer provides advanced retry functionality with jitter and customization
type EnhancedRetryer struct {
	config     *RetryConfig
	classifier ErrorClassifier
	jitterCalc JitterCalculator
	backoffCalc BackoffCalculator
	metrics    RetryMetrics
}

// RetryMetrics tracks retry statistics
type RetryMetrics struct {
	TotalAttempts        int64         `json:"total_attempts"`
	SuccessfulRetries    int64         `json:"successful_retries"`
	FailedRetries        int64         `json:"failed_retries"`
	AverageAttempts      float64       `json:"average_attempts"`
	AverageDelay         time.Duration `json:"average_delay"`
	MaxDelay             time.Duration `json:"max_delay"`
	TotalDelay           time.Duration `json:"total_delay"`
	ErrorsByCategory     map[ErrorCategory]int64 `json:"errors_by_category"`
	SuccessByAttempt     map[int]int64   `json:"success_by_attempt"`
	LastUpdate           time.Time     `json:"last_update"`
}

// NewRetryMetrics creates a new RetryMetrics instance
func NewRetryMetrics() *RetryMetrics {
	return &RetryMetrics{
		ErrorsByCategory: make(map[ErrorCategory]int64),
		SuccessByAttempt: make(map[int]int64),
		LastUpdate:       time.Now(),
	}
}

// JitterCalculator interface for different jitter strategies
type JitterCalculator interface {
	Calculate(delay time.Duration, jitterType JitterType, factor float64) time.Duration
}

// BackoffCalculator interface for different backoff strategies
type BackoffCalculator interface {
	Calculate(attempt int, baseDelay time.Duration, strategy BackoffStrategy, multiplier float64) time.Duration
}

// DefaultJitterCalculator implements standard jitter calculations
type DefaultJitterCalculator struct {
	previousDelay time.Duration // For decorrelated jitter
}

// Calculate computes jitter based on the specified type
func (djc *DefaultJitterCalculator) Calculate(delay time.Duration, jitterType JitterType, factor float64) time.Duration {
	if delay <= 0 || factor <= 0 {
		return delay
	}
	
	switch jitterType {
	case JitterTypeNone:
		return delay
		
	case JitterTypeFull:
		// Random delay between 0 and original delay
		jitter := time.Duration(float64(delay) * randomFloat() * factor)
		return time.Duration(float64(delay) * (1.0 - factor)) + jitter
		
	case JitterTypeEqual:
		// Random delay between delay/2 and delay
		halfDelay := delay / 2
		jitter := time.Duration(float64(halfDelay) * randomFloat() * factor)
		return halfDelay + jitter
		
	case JitterTypeDecorelated:
		// Decorrelated jitter: delay = random(0, min(maxDelay, 3 * previousDelay))
		maxJitterDelay := time.Duration(float64(delay) * 3.0)
		if djc.previousDelay > 0 && djc.previousDelay < maxJitterDelay {
			maxJitterDelay = djc.previousDelay
		}
		newDelay := time.Duration(float64(maxJitterDelay) * randomFloat())
		djc.previousDelay = newDelay
		return newDelay
		
	default:
		return delay
	}
}

// DefaultBackoffCalculator implements standard backoff calculations
type DefaultBackoffCalculator struct {
	fibonacciCache []int // Cache for fibonacci sequence
}

// Calculate computes backoff delay based on strategy
func (dbc *DefaultBackoffCalculator) Calculate(attempt int, baseDelay time.Duration, strategy BackoffStrategy, multiplier float64) time.Duration {
	if attempt <= 0 {
		return baseDelay
	}
	
	switch strategy {
	case BackoffFixed:
		return baseDelay
		
	case BackoffLinear:
		return time.Duration(int64(baseDelay) * int64(attempt))
		
	case BackoffExponential:
		return time.Duration(float64(baseDelay) * math.Pow(multiplier, float64(attempt-1)))
		
	case BackoffFibonacci:
		fib := dbc.fibonacci(attempt)
		return time.Duration(int64(baseDelay) * int64(fib))
		
	case BackoffPolynomial:
		// Quadratic backoff: baseDelay * attempt^2
		return time.Duration(int64(baseDelay) * int64(attempt*attempt))
		
	default:
		return time.Duration(float64(baseDelay) * math.Pow(multiplier, float64(attempt-1)))
	}
}

// fibonacci calculates the nth fibonacci number with caching
func (dbc *DefaultBackoffCalculator) fibonacci(n int) int {
	if n <= 0 {
		return 1
	}
	if n == 1 {
		return 1
	}
	
	// Extend cache if necessary
	for len(dbc.fibonacciCache) <= n {
		if len(dbc.fibonacciCache) == 0 {
			dbc.fibonacciCache = []int{1, 1}
		} else {
			next := dbc.fibonacciCache[len(dbc.fibonacciCache)-1] + dbc.fibonacciCache[len(dbc.fibonacciCache)-2]
			dbc.fibonacciCache = append(dbc.fibonacciCache, next)
		}
	}
	
	return dbc.fibonacciCache[n]
}

// NewEnhancedRetryer creates a new enhanced retryer
func NewEnhancedRetryer(config *RetryConfig) *EnhancedRetryer {
	if config == nil {
		config = DefaultRetryConfig()
	}
	
	return &EnhancedRetryer{
		config:      config,
		classifier:  NewDefaultErrorClassifier(),
		jitterCalc:  &DefaultJitterCalculator{},
		backoffCalc: &DefaultBackoffCalculator{},
		metrics:     *NewRetryMetrics(),
	}
}

// WithRetry executes an operation with retry logic
func WithRetry(ctx context.Context, config RetryConfig, operation func() error) error {
	retryer := NewEnhancedRetryer(&config)
	return retryer.Execute(ctx, operation, "generic_operation")
}

// Execute executes an operation with comprehensive retry logic
func (er *EnhancedRetryer) Execute(ctx context.Context, operation func() error, operationName string) error {
	startTime := time.Now()
	opConfig := er.config.GetOperationConfig(operationName)
	
	var lastErr error
	var totalDelay time.Duration
	
	for attempt := 1; attempt <= opConfig.MaxRetries+1; attempt++ {
		attemptStart := time.Now()
		
		// Execute the operation
		err := operation()
		
		// Update metrics
		er.metrics.TotalAttempts++
		
		if err == nil {
			// Success
			er.metrics.SuccessfulRetries++
			er.metrics.SuccessByAttempt[attempt]++
			er.updateAverages(attempt, totalDelay)
			return nil
		}
		
		lastErr = err
		
		// Classify the error
		classified := er.classifier.Classify(err, operationName)
		er.metrics.ErrorsByCategory[classified.Category]++
		
		// Check if we should retry
		shouldRetry := er.shouldRetry(classified, attempt, opConfig, time.Since(startTime))
		if !shouldRetry || attempt > opConfig.MaxRetries {
			er.metrics.FailedRetries++
			break
		}
		
		// Calculate retry delay
		delay := er.calculateDelay(attempt, classified, opConfig, time.Since(startTime))
		if delay <= 0 {
			continue // Immediate retry
		}
		
		totalDelay += delay
		er.updateDelayMetrics(delay)
		
		// Check context deadline
		if opConfig.RespectDeadline {
			if deadline, ok := ctx.Deadline(); ok {
				if time.Now().Add(delay).After(deadline) {
					// Would exceed deadline, don't retry
					break
				}
			}
		}
		
		// Wait for retry delay or context cancellation
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
			// Continue to next attempt
		}
	}
	
	er.updateAverages(opConfig.MaxRetries+1, totalDelay)
	return lastErr
}

// shouldRetry determines if an error should be retried
func (er *EnhancedRetryer) shouldRetry(classified *ClassifiedError, attempt int, config *RetryConfig, elapsed time.Duration) bool {
	// Check custom retry predicate first
	if config.RetryPredicate != nil {
		return config.RetryPredicate(classified.OriginalError, attempt, elapsed)
	}
	
	// Use classification-based retry logic
	return classified.IsRetryable()
}

// calculateDelay computes the delay before next retry attempt
func (er *EnhancedRetryer) calculateDelay(attempt int, classified *ClassifiedError, config *RetryConfig, elapsed time.Duration) time.Duration {
	// Check for explicit retry-after from error
	if config.RespectRetryAfter && classified.RetryAfter > 0 {
		if classified.RetryAfter <= config.MaxRetryAfter {
			return classified.RetryAfter
		}
	}
	
	// Calculate base delay using backoff strategy
	baseDelay := er.backoffCalc.Calculate(attempt, config.InitialDelay, config.BackoffStrategy, config.Multiplier)
	
	// Cap at max delay
	if baseDelay > config.MaxDelay {
		baseDelay = config.MaxDelay
	}
	
	// Apply jitter if enabled
	if config.JitterEnabled {
		baseDelay = er.jitterCalc.Calculate(baseDelay, config.JitterType, config.JitterFactor)
	}
	
	return baseDelay
}

// updateAverages updates average metrics
func (er *EnhancedRetryer) updateAverages(attempts int, totalDelay time.Duration) {
	if er.metrics.TotalAttempts > 0 {
		er.metrics.AverageAttempts = float64(er.metrics.TotalAttempts) / float64(er.metrics.SuccessfulRetries + er.metrics.FailedRetries)
	}
	
	er.metrics.TotalDelay += totalDelay
	if er.metrics.TotalAttempts > 0 {
		er.metrics.AverageDelay = er.metrics.TotalDelay / time.Duration(er.metrics.TotalAttempts)
	}
	
	er.metrics.LastUpdate = time.Now()
}

// updateDelayMetrics updates delay-related metrics
func (er *EnhancedRetryer) updateDelayMetrics(delay time.Duration) {
	if delay > er.metrics.MaxDelay {
		er.metrics.MaxDelay = delay
	}
}

// GetMetrics returns current retry metrics
func (er *EnhancedRetryer) GetMetrics() RetryMetrics {
	return er.metrics
}

// ResetMetrics resets all retry metrics
func (er *EnhancedRetryer) ResetMetrics() {
	er.metrics = *NewRetryMetrics()
}

// SetErrorClassifier sets a custom error classifier
func (er *EnhancedRetryer) SetErrorClassifier(classifier ErrorClassifier) {
	er.classifier = classifier
}

// SetJitterCalculator sets a custom jitter calculator
func (er *EnhancedRetryer) SetJitterCalculator(calc JitterCalculator) {
	er.jitterCalc = calc
}

// SetBackoffCalculator sets a custom backoff calculator
func (er *EnhancedRetryer) SetBackoffCalculator(calc BackoffCalculator) {
	er.backoffCalc = calc
}

// Predefined retry predicates

// RetryOnTransientErrors retries only on transient errors
func RetryOnTransientErrors(err error, attempt int, elapsed time.Duration) bool {
	classifier := NewDefaultErrorClassifier()
	classified := classifier.Classify(err, "unknown")
	return classified.Category == ErrorCategoryTransient || 
		   classified.Category == ErrorCategoryNetwork ||
		   classified.Category == ErrorCategoryTimeout
}

// RetryOnHTTPErrors retries on specific HTTP status codes
func RetryOnHTTPErrors(retryCodes []int) RetryPredicate {
	return func(err error, attempt int, elapsed time.Duration) bool {
		classifier := NewDefaultErrorClassifier()
		statusCode := classifier.ExtractStatusCode(err)
		
		for _, code := range retryCodes {
			if statusCode == code {
				return true
			}
		}
		
		return false
	}
}

// RetryWithMaxElapsed retries until maximum elapsed time
func RetryWithMaxElapsed(maxElapsed time.Duration, basePredicate RetryPredicate) RetryPredicate {
	return func(err error, attempt int, elapsed time.Duration) bool {
		if elapsed >= maxElapsed {
			return false
		}
		return basePredicate(err, attempt, elapsed)
	}
}

// RetryWithMaxAttempts retries up to maximum attempts
func RetryWithMaxAttempts(maxAttempts int, basePredicate RetryPredicate) RetryPredicate {
	return func(err error, attempt int, elapsed time.Duration) bool {
		if attempt > maxAttempts {
			return false
		}
		return basePredicate(err, attempt, elapsed)
	}
}

// Helper functions

// isRetryable checks if an error is retryable using legacy logic for backwards compatibility
func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	
	// Check for network errors
	if netErr, ok := err.(net.Error); ok && netErr.Temporary() {
		return true
	}
	
	// Check for HTTP 5xx errors
	if httpErr, ok := err.(*HTTPError); ok {
		return httpErr.StatusCode >= 500 && httpErr.StatusCode < 600
	}
	
	// Check for context timeout (but not cancellation)
	if err == context.DeadlineExceeded {
		return true
	}
	
	return false
}

// randomFloat returns a pseudo-random float between 0 and 1
func randomFloat() float64 {
	// Simple pseudo-random implementation for jitter
	// In production, consider using crypto/rand for better randomness
	seed := time.Now().UnixNano()
	return float64((seed%10000)) / 10000.0
}

// Configuration builders for common scenarios

// NewWebServiceRetryConfig creates retry config optimized for web services
func NewWebServiceRetryConfig() *RetryConfig {
	config := DefaultRetryConfig()
	config.MaxRetries = 5
	config.InitialDelay = 500 * time.Millisecond
	config.MaxDelay = 60 * time.Second
	config.JitterType = JitterTypeEqual
	config.JitterFactor = 0.1
	config.BackoffStrategy = BackoffExponential
	config.Multiplier = 1.5
	config.RespectRetryAfter = true
	config.MaxRetryAfter = 5 * time.Minute
	
	// Add operation-specific configs
	config.OperationConfigs["harbor_api"] = &OperationRetryConfig{
		MaxRetries:   intPtr(3),
		InitialDelay: durationPtr(1 * time.Second),
		Multiplier:   float64Ptr(2.0),
	}
	
	config.OperationConfigs["database"] = &OperationRetryConfig{
		MaxRetries:   intPtr(5),
		InitialDelay: durationPtr(100 * time.Millisecond),
		MaxDelay:     durationPtr(5 * time.Second),
	}
	
	return config
}

// NewDatabaseRetryConfig creates retry config optimized for database operations
func NewDatabaseRetryConfig() *RetryConfig {
	config := DefaultRetryConfig()
	config.MaxRetries = 10
	config.InitialDelay = 100 * time.Millisecond
	config.MaxDelay = 5 * time.Second
	config.BackoffStrategy = BackoffLinear
	config.JitterType = JitterTypeFull
	config.JitterFactor = 0.25
	
	return config
}

// NewCriticalOperationRetryConfig creates retry config for critical operations
func NewCriticalOperationRetryConfig() *RetryConfig {
	config := DefaultRetryConfig()
	config.MaxRetries = 10
	config.InitialDelay = 2 * time.Second
	config.MaxDelay = 2 * time.Minute
	config.BackoffStrategy = BackoffExponential
	config.Multiplier = 1.5
	config.JitterEnabled = true
	config.JitterType = JitterTypeDecorelated
	config.JitterFactor = 0.1
	
	return config
}

// Helper functions for pointer creation
func intPtr(i int) *int {
	return &i
}

func durationPtr(d time.Duration) *time.Duration {
	return &d
}

func float64Ptr(f float64) *float64 {
	return &f
}

func boolPtr(b bool) *bool {
	return &b
}