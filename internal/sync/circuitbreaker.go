package sync

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

var (
	// Circuit breaker specific errors
	ErrCircuitBreakerOpen         = errors.New("circuit breaker is open")
	ErrCircuitBreakerHalfOpenBusy = errors.New("circuit breaker is half-open and busy")
	ErrCircuitBreakerTimeout      = errors.New("circuit breaker operation timeout")
)

// CircuitBreakerState represents the state of a circuit breaker
type CircuitBreakerState int32

const (
	// StateClosed - Normal operation, requests are allowed
	StateClosed CircuitBreakerState = iota
	// StateOpen - Circuit is open, requests are rejected immediately
	StateOpen
	// StateHalfOpen - Testing state, limited requests allowed to test recovery
	StateHalfOpen
)

func (s CircuitBreakerState) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// CircuitBreakerConfig defines configuration for circuit breaker
type CircuitBreakerConfig struct {
	// Name identifies the circuit breaker
	Name string `yaml:"name" json:"name"`
	
	// Failure threshold settings
	FailureThreshold     uint32        `yaml:"failure_threshold" json:"failure_threshold"`         // Number of failures to trip
	FailureRateThreshold float64       `yaml:"failure_rate_threshold" json:"failure_rate_threshold"` // Percentage of failures to trip (0.0-1.0)
	MinRequestVolume     uint32        `yaml:"min_request_volume" json:"min_request_volume"`       // Minimum requests before considering failure rate
	
	// Success threshold for recovery
	SuccessThreshold uint32 `yaml:"success_threshold" json:"success_threshold"` // Consecutive successes to close from half-open
	
	// Timeout settings
	OpenTimeout    time.Duration `yaml:"open_timeout" json:"open_timeout"`       // Time before transitioning to half-open
	HalfOpenTimeout time.Duration `yaml:"half_open_timeout" json:"half_open_timeout"` // Timeout for half-open operations
	
	// Sliding window configuration
	SlidingWindowType WindowType   `yaml:"sliding_window_type" json:"sliding_window_type"`
	SlidingWindowSize int          `yaml:"sliding_window_size" json:"sliding_window_size"` // Size of the sliding window
	WindowInterval    time.Duration `yaml:"window_interval" json:"window_interval"`         // Interval for time-based windows
	
	// Request limiting in half-open state
	MaxRequestsInHalfOpen uint32 `yaml:"max_requests_in_half_open" json:"max_requests_in_half_open"`
	
	// Failure predicate - determines what constitutes a failure
	FailurePredicate func(error) bool `yaml:"-" json:"-"`
	
	// State change notifications
	OnStateChange func(name string, from, to CircuitBreakerState) `yaml:"-" json:"-"`
	
	// Metrics collection
	MetricsEnabled bool `yaml:"metrics_enabled" json:"metrics_enabled"`
}

// WindowType defines the type of sliding window
type WindowType string

const (
	WindowTypeCount WindowType = "count" // Count-based sliding window
	WindowTypeTime  WindowType = "time"  // Time-based sliding window
)

// DefaultCircuitBreakerConfig returns a sensible default configuration
func DefaultCircuitBreakerConfig(name string) *CircuitBreakerConfig {
	return &CircuitBreakerConfig{
		Name:                  name,
		FailureThreshold:      5,
		FailureRateThreshold:  0.5, // 50%
		MinRequestVolume:      10,
		SuccessThreshold:      3,
		OpenTimeout:           30 * time.Second,
		HalfOpenTimeout:       5 * time.Second,
		SlidingWindowType:     WindowTypeCount,
		SlidingWindowSize:     100,
		WindowInterval:        60 * time.Second,
		MaxRequestsInHalfOpen: 3,
		MetricsEnabled:        true,
		FailurePredicate: func(err error) bool {
			return err != nil
		},
	}
}

// CircuitBreakerMetrics tracks circuit breaker statistics
type CircuitBreakerMetrics struct {
	TotalRequests     uint64    `json:"total_requests"`
	SuccessfulRequests uint64   `json:"successful_requests"`
	FailedRequests    uint64    `json:"failed_requests"`
	RejectedRequests  uint64    `json:"rejected_requests"`
	StateTransitions  uint64    `json:"state_transitions"`
	LastStateChange   time.Time `json:"last_state_change"`
	TotalUptime       time.Duration `json:"total_uptime"`
	TotalDowntime     time.Duration `json:"total_downtime"`
	LastFailure       time.Time `json:"last_failure"`
	LastSuccess       time.Time `json:"last_success"`
}

// SlidingWindow interface for tracking requests over a window
type SlidingWindow interface {
	RecordSuccess()
	RecordFailure()
	GetCounts() (successes, failures uint32)
	GetFailureRate() float64
	Clear()
}

// CountBasedWindow implements a count-based sliding window
type CountBasedWindow struct {
	size       int
	successes  []bool
	current    int
	full       bool
	mutex      sync.RWMutex
}

// NewCountBasedWindow creates a new count-based sliding window
func NewCountBasedWindow(size int) *CountBasedWindow {
	return &CountBasedWindow{
		size:      size,
		successes: make([]bool, size),
		current:   0,
		full:      false,
	}
}

func (cbw *CountBasedWindow) RecordSuccess() {
	cbw.mutex.Lock()
	defer cbw.mutex.Unlock()
	
	cbw.successes[cbw.current] = true
	cbw.advance()
}

func (cbw *CountBasedWindow) RecordFailure() {
	cbw.mutex.Lock()
	defer cbw.mutex.Unlock()
	
	cbw.successes[cbw.current] = false
	cbw.advance()
}

func (cbw *CountBasedWindow) advance() {
	cbw.current++
	if cbw.current >= cbw.size {
		cbw.current = 0
		cbw.full = true
	}
}

func (cbw *CountBasedWindow) GetCounts() (successes, failures uint32) {
	cbw.mutex.RLock()
	defer cbw.mutex.RUnlock()
	
	limit := cbw.current
	if cbw.full {
		limit = cbw.size
	}
	
	for i := 0; i < limit; i++ {
		if cbw.successes[i] {
			successes++
		} else {
			failures++
		}
	}
	
	return successes, failures
}

func (cbw *CountBasedWindow) GetFailureRate() float64 {
	successes, failures := cbw.GetCounts()
	total := successes + failures
	
	if total == 0 {
		return 0.0
	}
	
	return float64(failures) / float64(total)
}

func (cbw *CountBasedWindow) Clear() {
	cbw.mutex.Lock()
	defer cbw.mutex.Unlock()
	
	for i := range cbw.successes {
		cbw.successes[i] = false
	}
	cbw.current = 0
	cbw.full = false
}

// TimeBasedWindow implements a time-based sliding window
type TimeBasedWindow struct {
	interval    time.Duration
	buckets     []bucketData
	currentPos  int
	lastUpdate  time.Time
	mutex       sync.RWMutex
}

type bucketData struct {
	successes uint32
	failures  uint32
	timestamp time.Time
}

// NewTimeBasedWindow creates a new time-based sliding window
func NewTimeBasedWindow(interval time.Duration, buckets int) *TimeBasedWindow {
	return &TimeBasedWindow{
		interval:   interval,
		buckets:    make([]bucketData, buckets),
		currentPos: 0,
		lastUpdate: time.Now(),
	}
}

func (tbw *TimeBasedWindow) RecordSuccess() {
	tbw.mutex.Lock()
	defer tbw.mutex.Unlock()
	
	tbw.updateCurrentBucket()
	tbw.buckets[tbw.currentPos].successes++
}

func (tbw *TimeBasedWindow) RecordFailure() {
	tbw.mutex.Lock()
	defer tbw.mutex.Unlock()
	
	tbw.updateCurrentBucket()
	tbw.buckets[tbw.currentPos].failures++
}

func (tbw *TimeBasedWindow) updateCurrentBucket() {
	now := time.Now()
	if now.Sub(tbw.lastUpdate) >= tbw.interval {
		tbw.currentPos = (tbw.currentPos + 1) % len(tbw.buckets)
		tbw.buckets[tbw.currentPos] = bucketData{timestamp: now}
		tbw.lastUpdate = now
	}
}

func (tbw *TimeBasedWindow) GetCounts() (successes, failures uint32) {
	tbw.mutex.RLock()
	defer tbw.mutex.RUnlock()
	
	cutoff := time.Now().Add(-tbw.interval * time.Duration(len(tbw.buckets)))
	
	for _, bucket := range tbw.buckets {
		if bucket.timestamp.After(cutoff) {
			successes += bucket.successes
			failures += bucket.failures
		}
	}
	
	return successes, failures
}

func (tbw *TimeBasedWindow) GetFailureRate() float64 {
	successes, failures := tbw.GetCounts()
	total := successes + failures
	
	if total == 0 {
		return 0.0
	}
	
	return float64(failures) / float64(total)
}

func (tbw *TimeBasedWindow) Clear() {
	tbw.mutex.Lock()
	defer tbw.mutex.Unlock()
	
	for i := range tbw.buckets {
		tbw.buckets[i] = bucketData{}
	}
	tbw.currentPos = 0
	tbw.lastUpdate = time.Now()
}

// CircuitBreaker implements the circuit breaker pattern
type CircuitBreaker struct {
	config       *CircuitBreakerConfig
	state        int32 // atomic access to CircuitBreakerState
	window       SlidingWindow
	metrics      CircuitBreakerMetrics
	lastStateChange time.Time
	
	// Half-open state management
	halfOpenRequests uint32 // atomic counter for half-open requests
	consecutiveSuccesses uint32 // atomic counter for consecutive successes in half-open
	
	// Synchronization
	mutex sync.RWMutex
	
	// Generation counter to handle race conditions
	generation uint64
}

// NewCircuitBreaker creates a new circuit breaker with the given configuration
func NewCircuitBreaker(config *CircuitBreakerConfig) *CircuitBreaker {
	if config == nil {
		config = DefaultCircuitBreakerConfig("default")
	}
	
	var window SlidingWindow
	switch config.SlidingWindowType {
	case WindowTypeTime:
		window = NewTimeBasedWindow(config.WindowInterval, config.SlidingWindowSize)
	default:
		window = NewCountBasedWindow(config.SlidingWindowSize)
	}
	
	cb := &CircuitBreaker{
		config:          config,
		state:           int32(StateClosed),
		window:          window,
		lastStateChange: time.Now(),
		generation:      0,
	}
	
	if config.MetricsEnabled {
		cb.metrics = CircuitBreakerMetrics{
			LastStateChange: time.Now(),
		}
	}
	
	return cb
}

// Execute executes a function with circuit breaker protection
func (cb *CircuitBreaker) Execute(fn func() error) error {
	generation, err := cb.beforeRequest()
	if err != nil {
		return err
	}
	
	// Execute the function
	start := time.Now()
	err = fn()
	duration := time.Since(start)
	
	// Record the result
	cb.afterRequest(generation, err, duration)
	
	return err
}

// ExecuteWithTimeout executes a function with circuit breaker protection and timeout
func (cb *CircuitBreaker) ExecuteWithTimeout(ctx context.Context, timeout time.Duration, fn func(context.Context) error) error {
	generation, err := cb.beforeRequest()
	if err != nil {
		return err
	}
	
	// Create context with timeout
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	
	// Execute the function
	start := time.Now()
	err = fn(ctx)
	duration := time.Since(start)
	
	// Check for timeout
	if ctx.Err() == context.DeadlineExceeded {
		err = ErrCircuitBreakerTimeout
	}
	
	// Record the result
	cb.afterRequest(generation, err, duration)
	
	return err
}

// beforeRequest handles pre-request logic and state checks
func (cb *CircuitBreaker) beforeRequest() (uint64, error) {
	state := CircuitBreakerState(atomic.LoadInt32(&cb.state))
	
	switch state {
	case StateClosed:
		// Allow request in closed state
		if cb.config.MetricsEnabled {
			atomic.AddUint64(&cb.metrics.TotalRequests, 1)
		}
		return cb.generation, nil
		
	case StateOpen:
		// Check if we should transition to half-open
		cb.mutex.RLock()
		shouldTransition := time.Since(cb.lastStateChange) >= cb.config.OpenTimeout
		cb.mutex.RUnlock()
		
		if shouldTransition {
			cb.mutex.Lock()
			// Double-check after acquiring lock
			if time.Since(cb.lastStateChange) >= cb.config.OpenTimeout {
				cb.transitionToHalfOpen()
			}
			cb.mutex.Unlock()
			
			// Allow this request as the first test in half-open
			if cb.config.MetricsEnabled {
				atomic.AddUint64(&cb.metrics.TotalRequests, 1)
			}
			return cb.generation, nil
		}
		
		// Reject request in open state
		if cb.config.MetricsEnabled {
			atomic.AddUint64(&cb.metrics.RejectedRequests, 1)
		}
		return 0, ErrCircuitBreakerOpen
		
	case StateHalfOpen:
		// Check if we're within the limit for half-open requests
		current := atomic.LoadUint32(&cb.halfOpenRequests)
		if current >= cb.config.MaxRequestsInHalfOpen {
			if cb.config.MetricsEnabled {
				atomic.AddUint64(&cb.metrics.RejectedRequests, 1)
			}
			return 0, ErrCircuitBreakerHalfOpenBusy
		}
		
		// Increment half-open request counter
		atomic.AddUint32(&cb.halfOpenRequests, 1)
		if cb.config.MetricsEnabled {
			atomic.AddUint64(&cb.metrics.TotalRequests, 1)
		}
		return cb.generation, nil
		
	default:
		return 0, fmt.Errorf("unknown circuit breaker state: %v", state)
	}
}

// afterRequest handles post-request logic and state transitions
func (cb *CircuitBreaker) afterRequest(generation uint64, err error, duration time.Duration) {
	// Check if generation matches (avoid race conditions)
	if generation != cb.generation {
		return
	}
	
	isFailure := cb.config.FailurePredicate(err)
	
	// Update metrics
	if cb.config.MetricsEnabled {
		if isFailure {
			atomic.AddUint64(&cb.metrics.FailedRequests, 1)
			cb.metrics.LastFailure = time.Now()
		} else {
			atomic.AddUint64(&cb.metrics.SuccessfulRequests, 1)
			cb.metrics.LastSuccess = time.Now()
		}
	}
	
	// Update sliding window
	if isFailure {
		cb.window.RecordFailure()
	} else {
		cb.window.RecordSuccess()
	}
	
	state := CircuitBreakerState(atomic.LoadInt32(&cb.state))
	
	switch state {
	case StateClosed:
		if isFailure {
			cb.checkTransitionToOpen()
		}
		
	case StateHalfOpen:
		if isFailure {
			// Any failure in half-open state transitions back to open
			cb.mutex.Lock()
			cb.transitionToOpen()
			cb.mutex.Unlock()
		} else {
			// Success in half-open state
			successes := atomic.AddUint32(&cb.consecutiveSuccesses, 1)
			if successes >= cb.config.SuccessThreshold {
				cb.mutex.Lock()
				cb.transitionToClosed()
				cb.mutex.Unlock()
			}
		}
		
		// Decrement half-open request counter
		atomic.AddUint32(&cb.halfOpenRequests, ^uint32(0)) // Subtract 1
		
	case StateOpen:
		// Requests shouldn't reach here, but handle gracefully
		break
	}
}

// checkTransitionToOpen checks if circuit breaker should transition to open state
func (cb *CircuitBreaker) checkTransitionToOpen() {
	successes, failures := cb.window.GetCounts()
	totalRequests := successes + failures
	
	// Check minimum request volume
	if totalRequests < cb.config.MinRequestVolume {
		return
	}
	
	// Check failure threshold (absolute count)
	if failures >= cb.config.FailureThreshold {
		cb.mutex.Lock()
		cb.transitionToOpen()
		cb.mutex.Unlock()
		return
	}
	
	// Check failure rate threshold
	failureRate := cb.window.GetFailureRate()
	if failureRate >= cb.config.FailureRateThreshold {
		cb.mutex.Lock()
		cb.transitionToOpen()
		cb.mutex.Unlock()
	}
}

// transitionToOpen transitions circuit breaker to open state
func (cb *CircuitBreaker) transitionToOpen() {
	oldState := CircuitBreakerState(atomic.SwapInt32(&cb.state, int32(StateOpen)))
	cb.lastStateChange = time.Now()
	cb.generation++
	
	// Reset counters
	atomic.StoreUint32(&cb.halfOpenRequests, 0)
	atomic.StoreUint32(&cb.consecutiveSuccesses, 0)
	
	// Update metrics
	if cb.config.MetricsEnabled {
		atomic.AddUint64(&cb.metrics.StateTransitions, 1)
		cb.metrics.LastStateChange = time.Now()
	}
	
	// Notify state change
	if cb.config.OnStateChange != nil {
		cb.config.OnStateChange(cb.config.Name, oldState, StateOpen)
	}
}

// transitionToHalfOpen transitions circuit breaker to half-open state
func (cb *CircuitBreaker) transitionToHalfOpen() {
	oldState := CircuitBreakerState(atomic.SwapInt32(&cb.state, int32(StateHalfOpen)))
	cb.lastStateChange = time.Now()
	cb.generation++
	
	// Reset counters
	atomic.StoreUint32(&cb.halfOpenRequests, 0)
	atomic.StoreUint32(&cb.consecutiveSuccesses, 0)
	
	// Update metrics
	if cb.config.MetricsEnabled {
		atomic.AddUint64(&cb.metrics.StateTransitions, 1)
		cb.metrics.LastStateChange = time.Now()
	}
	
	// Notify state change
	if cb.config.OnStateChange != nil {
		cb.config.OnStateChange(cb.config.Name, oldState, StateHalfOpen)
	}
}

// transitionToClosed transitions circuit breaker to closed state
func (cb *CircuitBreaker) transitionToClosed() {
	oldState := CircuitBreakerState(atomic.SwapInt32(&cb.state, int32(StateClosed)))
	cb.lastStateChange = time.Now()
	cb.generation++
	
	// Reset counters and sliding window
	atomic.StoreUint32(&cb.halfOpenRequests, 0)
	atomic.StoreUint32(&cb.consecutiveSuccesses, 0)
	cb.window.Clear()
	
	// Update metrics
	if cb.config.MetricsEnabled {
		atomic.AddUint64(&cb.metrics.StateTransitions, 1)
		cb.metrics.LastStateChange = time.Now()
	}
	
	// Notify state change
	if cb.config.OnStateChange != nil {
		cb.config.OnStateChange(cb.config.Name, oldState, StateClosed)
	}
}

// State returns the current state of the circuit breaker
func (cb *CircuitBreaker) State() CircuitBreakerState {
	return CircuitBreakerState(atomic.LoadInt32(&cb.state))
}

// GetMetrics returns current circuit breaker metrics
func (cb *CircuitBreaker) GetMetrics() CircuitBreakerMetrics {
	if !cb.config.MetricsEnabled {
		return CircuitBreakerMetrics{}
	}
	
	cb.mutex.RLock()
	defer cb.mutex.RUnlock()
	
	metrics := cb.metrics
	metrics.TotalRequests = atomic.LoadUint64(&cb.metrics.TotalRequests)
	metrics.SuccessfulRequests = atomic.LoadUint64(&cb.metrics.SuccessfulRequests)
	metrics.FailedRequests = atomic.LoadUint64(&cb.metrics.FailedRequests)
	metrics.RejectedRequests = atomic.LoadUint64(&cb.metrics.RejectedRequests)
	metrics.StateTransitions = atomic.LoadUint64(&cb.metrics.StateTransitions)
	
	return metrics
}

// GetCounts returns current success and failure counts from the sliding window
func (cb *CircuitBreaker) GetCounts() (successes, failures uint32) {
	return cb.window.GetCounts()
}

// GetFailureRate returns the current failure rate from the sliding window
func (cb *CircuitBreaker) GetFailureRate() float64 {
	return cb.window.GetFailureRate()
}

// Reset manually resets the circuit breaker to closed state
func (cb *CircuitBreaker) Reset() {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()
	
	oldState := CircuitBreakerState(atomic.SwapInt32(&cb.state, int32(StateClosed)))
	cb.lastStateChange = time.Now()
	cb.generation++
	
	// Reset all counters and window
	atomic.StoreUint32(&cb.halfOpenRequests, 0)
	atomic.StoreUint32(&cb.consecutiveSuccesses, 0)
	cb.window.Clear()
	
	// Reset metrics
	if cb.config.MetricsEnabled {
		cb.metrics = CircuitBreakerMetrics{
			LastStateChange: time.Now(),
		}
	}
	
	// Notify state change
	if cb.config.OnStateChange != nil {
		cb.config.OnStateChange(cb.config.Name, oldState, StateClosed)
	}
}

// ForceOpen manually forces the circuit breaker to open state
func (cb *CircuitBreaker) ForceOpen() {
	cb.mutex.Lock()
	cb.transitionToOpen()
	cb.mutex.Unlock()
}

// Name returns the name of the circuit breaker
func (cb *CircuitBreaker) Name() string {
	return cb.config.Name
}

// CircuitBreakerManager manages multiple circuit breakers
type CircuitBreakerManager struct {
	breakers map[string]*CircuitBreaker
	config   *CircuitBreakerConfig
	mutex    sync.RWMutex
}

// NewCircuitBreakerManager creates a new circuit breaker manager
func NewCircuitBreakerManager(defaultConfig *CircuitBreakerConfig) *CircuitBreakerManager {
	if defaultConfig == nil {
		defaultConfig = DefaultCircuitBreakerConfig("default")
	}
	
	return &CircuitBreakerManager{
		breakers: make(map[string]*CircuitBreaker),
		config:   defaultConfig,
	}
}

// GetCircuitBreaker returns a circuit breaker for the given name, creating one if it doesn't exist
func (cbm *CircuitBreakerManager) GetCircuitBreaker(name string) *CircuitBreaker {
	cbm.mutex.RLock()
	cb, exists := cbm.breakers[name]
	cbm.mutex.RUnlock()
	
	if exists {
		return cb
	}
	
	cbm.mutex.Lock()
	defer cbm.mutex.Unlock()
	
	// Double-check after acquiring write lock
	if cb, exists = cbm.breakers[name]; exists {
		return cb
	}
	
	// Create new circuit breaker with name-specific config
	config := *cbm.config
	config.Name = name
	cb = NewCircuitBreaker(&config)
	cbm.breakers[name] = cb
	
	return cb
}

// Execute executes a function using the named circuit breaker
func (cbm *CircuitBreakerManager) Execute(name string, fn func() error) error {
	cb := cbm.GetCircuitBreaker(name)
	return cb.Execute(fn)
}

// GetAllStates returns the states of all circuit breakers
func (cbm *CircuitBreakerManager) GetAllStates() map[string]CircuitBreakerState {
	cbm.mutex.RLock()
	defer cbm.mutex.RUnlock()
	
	states := make(map[string]CircuitBreakerState)
	for name, cb := range cbm.breakers {
		states[name] = cb.State()
	}
	
	return states
}

// GetAllMetrics returns metrics for all circuit breakers
func (cbm *CircuitBreakerManager) GetAllMetrics() map[string]CircuitBreakerMetrics {
	cbm.mutex.RLock()
	defer cbm.mutex.RUnlock()
	
	metrics := make(map[string]CircuitBreakerMetrics)
	for name, cb := range cbm.breakers {
		metrics[name] = cb.GetMetrics()
	}
	
	return metrics
}

// ResetAll resets all circuit breakers
func (cbm *CircuitBreakerManager) ResetAll() {
	cbm.mutex.RLock()
	defer cbm.mutex.RUnlock()
	
	for _, cb := range cbm.breakers {
		cb.Reset()
	}
}

// RemoveCircuitBreaker removes a circuit breaker from the manager
func (cbm *CircuitBreakerManager) RemoveCircuitBreaker(name string) {
	cbm.mutex.Lock()
	defer cbm.mutex.Unlock()
	
	delete(cbm.breakers, name)
}

// Predefined failure predicates for common scenarios

// FailOnHTTPErrors creates a failure predicate that fails on HTTP status codes >= threshold
func FailOnHTTPErrors(threshold int) func(error) bool {
	return func(err error) bool {
		if err == nil {
			return false
		}
		
		if httpErr, ok := err.(*HTTPError); ok {
			return httpErr.StatusCode >= threshold
		}
		
		classifier := NewDefaultErrorClassifier()
		statusCode := classifier.ExtractStatusCode(err)
		return statusCode >= threshold
	}
}

// FailOnSpecificErrors creates a failure predicate that fails on specific error types
func FailOnSpecificErrors(errorTypes ...ErrorCategory) func(error) bool {
	typeMap := make(map[ErrorCategory]bool)
	for _, et := range errorTypes {
		typeMap[et] = true
	}
	
	return func(err error) bool {
		if err == nil {
			return false
		}
		
		classifier := NewDefaultErrorClassifier()
		category := classifier.GetErrorCategory(err)
		return typeMap[category]
	}
}

// FailOnTimeout creates a failure predicate that fails on timeout errors
func FailOnTimeout() func(error) bool {
	return func(err error) bool {
		if err == nil {
			return false
		}
		
		if err == context.DeadlineExceeded || err == ErrCircuitBreakerTimeout {
			return true
		}
		
		classifier := NewDefaultErrorClassifier()
		category := classifier.GetErrorCategory(err)
		return category == ErrorCategoryTimeout
	}
}