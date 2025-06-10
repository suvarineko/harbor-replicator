package client

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"harbor-replicator/pkg/config"
)

var (
	ErrCircuitBreakerOpen     = errors.New("circuit breaker is open")
	ErrCircuitBreakerTimeout  = errors.New("circuit breaker timeout")
	ErrTooManyRequests        = errors.New("too many requests in half-open state")
)

type CircuitBreakerMiddleware struct {
	config     *config.CircuitBreakerConfig
	state      config.CircuitBreakerState
	counts     config.CircuitBreakerCounts
	expiry     time.Time
	generation uint64
	mutex      sync.RWMutex
	onStateChange func(name string, from config.CircuitBreakerState, to config.CircuitBreakerState)
	isFailure  func(error) bool
}

func NewCircuitBreaker(cfg *config.CircuitBreakerConfig) *CircuitBreakerMiddleware {
	cb := &CircuitBreakerMiddleware{
		config:     cfg,
		state:      config.CircuitBreakerClosed,
		generation: 0,
	}

	// Set default failure predicate if not provided
	if cb.isFailure == nil {
		cb.isFailure = func(err error) bool {
			return err != nil
		}
	}

	return cb
}

func (cb *CircuitBreakerMiddleware) RoundTrip(req *http.Request) (*http.Response, error) {
	generation, err := cb.beforeRequest()
	if err != nil {
		return nil, err
	}

	transport := getNextTransport(req)
	if transport == nil {
		cb.afterRequest(generation, false)
		return nil, errors.New("no transport available")
	}

	resp, err := transport.RoundTrip(req)
	
	// Determine if this is a failure
	success := err == nil && (resp == nil || resp.StatusCode < 500)
	cb.afterRequest(generation, success)

	return resp, err
}

func (cb *CircuitBreakerMiddleware) Execute(ctx context.Context, fn func() (interface{}, error)) (interface{}, error) {
	generation, err := cb.beforeRequest()
	if err != nil {
		return nil, err
	}

	result, err := fn()
	success := !cb.isFailure(err)
	cb.afterRequest(generation, success)

	return result, err
}

func (cb *CircuitBreakerMiddleware) beforeRequest() (uint64, error) {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()

	now := time.Now()
	state, generation := cb.currentState(now)

	if state == config.CircuitBreakerOpen {
		return generation, ErrCircuitBreakerOpen
	} else if state == config.CircuitBreakerHalfOpen && cb.counts.Requests >= cb.config.MaxRequests {
		return generation, ErrTooManyRequests
	}

	cb.counts.OnRequest()
	return generation, nil
}

func (cb *CircuitBreakerMiddleware) afterRequest(before uint64, success bool) {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()

	now := time.Now()
	state, generation := cb.currentState(now)
	if generation != before {
		return
	}

	if success {
		cb.onSuccess(state, now)
	} else {
		cb.onFailure(state, now)
	}
}

func (cb *CircuitBreakerMiddleware) onSuccess(state config.CircuitBreakerState, now time.Time) {
	cb.counts.OnSuccess()

	if state == config.CircuitBreakerHalfOpen && cb.counts.ConsecutiveSuccesses >= cb.config.SuccessThreshold {
		cb.setState(config.CircuitBreakerClosed, now)
	}
}

func (cb *CircuitBreakerMiddleware) onFailure(state config.CircuitBreakerState, now time.Time) {
	cb.counts.OnFailure()

	switch state {
	case config.CircuitBreakerClosed:
		if cb.readyToTrip() {
			cb.setState(config.CircuitBreakerOpen, now)
		}
	case config.CircuitBreakerHalfOpen:
		cb.setState(config.CircuitBreakerOpen, now)
	}
}

func (cb *CircuitBreakerMiddleware) readyToTrip() bool {
	return cb.counts.ConsecutiveFailures >= cb.config.FailureThreshold
}

func (cb *CircuitBreakerMiddleware) setState(state config.CircuitBreakerState, now time.Time) {
	if cb.state == state {
		return
	}

	prev := cb.state
	cb.state = state

	cb.toNewGeneration(now)

	if cb.onStateChange != nil {
		cb.onStateChange(cb.config.Name, prev, state)
	}
}

func (cb *CircuitBreakerMiddleware) currentState(now time.Time) (config.CircuitBreakerState, uint64) {
	switch cb.state {
	case config.CircuitBreakerClosed:
		if !cb.expiry.IsZero() && cb.expiry.Before(now) {
			cb.toNewGeneration(now)
		}
	case config.CircuitBreakerOpen:
		if cb.expiry.Before(now) {
			cb.setState(config.CircuitBreakerHalfOpen, now)
		}
	}
	return cb.state, cb.generation
}

func (cb *CircuitBreakerMiddleware) toNewGeneration(now time.Time) {
	cb.generation++
	cb.counts.Clear()

	var zero time.Time
	switch cb.state {
	case config.CircuitBreakerClosed:
		if cb.config.Interval == 0 {
			cb.expiry = zero
		} else {
			cb.expiry = now.Add(cb.config.Interval)
		}
	case config.CircuitBreakerOpen:
		cb.expiry = now.Add(cb.config.Timeout)
	default: // CircuitBreakerHalfOpen
		cb.expiry = zero
	}
}

func (cb *CircuitBreakerMiddleware) State() config.CircuitBreakerState {
	cb.mutex.RLock()
	defer cb.mutex.RUnlock()

	now := time.Now()
	state, _ := cb.currentState(now)
	return state
}

func (cb *CircuitBreakerMiddleware) Counts() config.CircuitBreakerCounts {
	cb.mutex.RLock()
	defer cb.mutex.RUnlock()

	return cb.counts
}

func (cb *CircuitBreakerMiddleware) Name() string {
	return cb.config.Name
}

func (cb *CircuitBreakerMiddleware) OnStateChange(fn func(name string, from config.CircuitBreakerState, to config.CircuitBreakerState)) {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()

	cb.onStateChange = fn
}

func (cb *CircuitBreakerMiddleware) Reset() {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()

	cb.toNewGeneration(time.Now())
	cb.setState(config.CircuitBreakerClosed, time.Now())
}

func (cb *CircuitBreakerMiddleware) Trip() {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()

	cb.setState(config.CircuitBreakerOpen, time.Now())
}


// TwoStepCircuitBreaker is a circuit breaker that supports manual success/failure reporting
type TwoStepCircuitBreaker struct {
	*CircuitBreakerMiddleware
}

func NewTwoStepCircuitBreaker(cfg *config.CircuitBreakerConfig) *TwoStepCircuitBreaker {
	return &TwoStepCircuitBreaker{
		CircuitBreakerMiddleware: NewCircuitBreaker(cfg),
	}
}

func (tscb *TwoStepCircuitBreaker) Allow() (done func(success bool), err error) {
	generation, err := tscb.beforeRequest()
	if err != nil {
		return nil, err
	}

	return func(success bool) {
		tscb.afterRequest(generation, success)
	}, nil
}

// MultiCircuitBreaker manages multiple circuit breakers for different operations
type MultiCircuitBreaker struct {
	breakers map[string]*CircuitBreakerMiddleware
	config   *config.CircuitBreakerConfig
	mutex    sync.RWMutex
}

func NewMultiCircuitBreaker(cfg *config.CircuitBreakerConfig) *MultiCircuitBreaker {
	return &MultiCircuitBreaker{
		breakers: make(map[string]*CircuitBreakerMiddleware),
		config:   cfg,
	}
}

func (mcb *MultiCircuitBreaker) GetBreaker(name string) *CircuitBreakerMiddleware {
	mcb.mutex.RLock()
	breaker, exists := mcb.breakers[name]
	mcb.mutex.RUnlock()

	if !exists {
		mcb.mutex.Lock()
		if breaker, exists = mcb.breakers[name]; !exists {
			// Create config copy with specific name
			config := *mcb.config
			config.Name = name
			breaker = NewCircuitBreaker(&config)
			mcb.breakers[name] = breaker
		}
		mcb.mutex.Unlock()
	}

	return breaker
}

func (mcb *MultiCircuitBreaker) Execute(name string, fn func() (interface{}, error)) (interface{}, error) {
	breaker := mcb.GetBreaker(name)
	return breaker.Execute(context.Background(), fn)
}

func (mcb *MultiCircuitBreaker) GetStates() map[string]config.CircuitBreakerState {
	mcb.mutex.RLock()
	defer mcb.mutex.RUnlock()

	states := make(map[string]config.CircuitBreakerState)
	for name, breaker := range mcb.breakers {
		states[name] = breaker.State()
	}
	return states
}

func (mcb *MultiCircuitBreaker) GetCounts() map[string]config.CircuitBreakerCounts {
	mcb.mutex.RLock()
	defer mcb.mutex.RUnlock()

	counts := make(map[string]config.CircuitBreakerCounts)
	for name, breaker := range mcb.breakers {
		counts[name] = breaker.Counts()
	}
	return counts
}

func (mcb *MultiCircuitBreaker) Reset(name string) {
	if breaker := mcb.GetBreaker(name); breaker != nil {
		breaker.Reset()
	}
}

func (mcb *MultiCircuitBreaker) ResetAll() {
	mcb.mutex.RLock()
	defer mcb.mutex.RUnlock()

	for _, breaker := range mcb.breakers {
		breaker.Reset()
	}
}

// Helper function to determine if an HTTP response should be considered a failure
func IsHTTPFailure(resp *http.Response, err error) bool {
	if err != nil {
		return true
	}
	if resp != nil && resp.StatusCode >= 500 {
		return true
	}
	return false
}

// Predefined failure predicates
var (
	// FailOnError treats any error as failure
	FailOnError = func(err error) bool {
		return err != nil
	}

	// FailOnHTTPError treats HTTP 5xx status codes and errors as failures
	FailOnHTTPError = func(err error) bool {
		if err != nil {
			return true
		}
		// This would need to be used in conjunction with response inspection
		return false
	}

	// FailOnTimeout treats timeout errors as failures
	FailOnTimeout = func(err error) bool {
		if err == nil {
			return false
		}
		// Check for timeout errors
		if err == context.DeadlineExceeded {
			return true
		}
		// Could also check for other timeout-related errors
		return false
	}
)