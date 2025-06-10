package client

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"harbor-replicator/pkg/config"
)

type RateLimiterMiddleware struct {
	limiter       *rate.Limiter
	config        *config.RateLimitConfig
	metrics       RateLimitMetrics
	mu            sync.RWMutex
}

type RateLimitMetrics struct {
	TotalRequests     int64
	AllowedRequests   int64
	RateLimitedRequests int64
	WaitTime          time.Duration
	LastUpdate        time.Time
}

func NewRateLimiter(cfg *config.RateLimitConfig) *RateLimiterMiddleware {
	limiter := rate.NewLimiter(rate.Limit(cfg.RequestsPerSecond), cfg.BurstSize)
	
	return &RateLimiterMiddleware{
		limiter: limiter,
		config:  cfg,
		metrics: RateLimitMetrics{
			LastUpdate: time.Now(),
		},
	}
}

func (rl *RateLimiterMiddleware) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	
	rl.mu.Lock()
	rl.metrics.TotalRequests++
	rl.mu.Unlock()

	ctx := req.Context()
	if rl.config.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, rl.config.Timeout)
		defer cancel()
	}

	err := rl.limiter.Wait(ctx)
	if err != nil {
		rl.mu.Lock()
		rl.metrics.RateLimitedRequests++
		rl.metrics.LastUpdate = time.Now()
		rl.mu.Unlock()
		
		return nil, &RateLimitError{
			Message:   "rate limit exceeded",
			RetryAfter: rl.getRetryAfter(),
		}
	}

	waitTime := time.Since(start)
	
	rl.mu.Lock()
	rl.metrics.AllowedRequests++
	rl.metrics.WaitTime += waitTime
	rl.metrics.LastUpdate = time.Now()
	rl.mu.Unlock()

	transport := getNextTransport(req)
	if transport == nil {
		return nil, fmt.Errorf("no transport available")
	}

	return transport.RoundTrip(req)
}

func (rl *RateLimiterMiddleware) Allow() bool {
	allowed := rl.limiter.Allow()
	
	rl.mu.Lock()
	rl.metrics.TotalRequests++
	if allowed {
		rl.metrics.AllowedRequests++
	} else {
		rl.metrics.RateLimitedRequests++
	}
	rl.metrics.LastUpdate = time.Now()
	rl.mu.Unlock()
	
	return allowed
}

func (rl *RateLimiterMiddleware) AllowN(n int) bool {
	allowed := rl.limiter.AllowN(time.Now(), n)
	
	rl.mu.Lock()
	rl.metrics.TotalRequests += int64(n)
	if allowed {
		rl.metrics.AllowedRequests += int64(n)
	} else {
		rl.metrics.RateLimitedRequests += int64(n)
	}
	rl.metrics.LastUpdate = time.Now()
	rl.mu.Unlock()
	
	return allowed
}

func (rl *RateLimiterMiddleware) Wait(ctx context.Context) error {
	start := time.Now()
	
	rl.mu.Lock()
	rl.metrics.TotalRequests++
	rl.mu.Unlock()

	err := rl.limiter.Wait(ctx)
	waitTime := time.Since(start)
	
	rl.mu.Lock()
	if err != nil {
		rl.metrics.RateLimitedRequests++
	} else {
		rl.metrics.AllowedRequests++
		rl.metrics.WaitTime += waitTime
	}
	rl.metrics.LastUpdate = time.Now()
	rl.mu.Unlock()
	
	return err
}

func (rl *RateLimiterMiddleware) GetMetrics() RateLimitMetrics {
	rl.mu.RLock()
	defer rl.mu.RUnlock()
	
	return rl.metrics
}

func (rl *RateLimiterMiddleware) ResetMetrics() {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	
	rl.metrics = RateLimitMetrics{
		LastUpdate: time.Now(),
	}
}

func (rl *RateLimiterMiddleware) UpdateConfig(cfg *config.RateLimitConfig) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	
	rl.config = cfg
	rl.limiter.SetLimit(rate.Limit(cfg.RequestsPerSecond))
	rl.limiter.SetBurst(cfg.BurstSize)
}

func (rl *RateLimiterMiddleware) getRetryAfter() time.Duration {
	reservation := rl.limiter.Reserve()
	defer reservation.Cancel()
	
	return reservation.Delay()
}

func (rl *RateLimiterMiddleware) GetCurrentRate() float64 {
	return float64(rl.limiter.Limit())
}

func (rl *RateLimiterMiddleware) GetBurstSize() int {
	return rl.limiter.Burst()
}

func (rl *RateLimiterMiddleware) GetAvailableTokens() int {
	reservation := rl.limiter.Reserve()
	defer reservation.Cancel()
	
	if reservation.OK() {
		return rl.limiter.Burst()
	}
	return 0
}

type RateLimitError struct {
	Message    string
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return e.Message
}

func (e *RateLimitError) IsRetryable() bool {
	return true
}

type AdaptiveRateLimiter struct {
	*RateLimiterMiddleware
	successWindow    time.Duration
	adjustmentFactor float64
	minRate          float64
	maxRate          float64
	successCount     int64
	errorCount       int64
	lastAdjustment   time.Time
	mu               sync.RWMutex
}

func NewAdaptiveRateLimiter(cfg *config.RateLimitConfig, options ...AdaptiveOption) *AdaptiveRateLimiter {
	baseRL := NewRateLimiter(cfg)
	
	arl := &AdaptiveRateLimiter{
		RateLimiterMiddleware: baseRL,
		successWindow:         30 * time.Second,
		adjustmentFactor:      0.1,
		minRate:               1.0,
		maxRate:               cfg.RequestsPerSecond * 2,
		lastAdjustment:        time.Now(),
	}
	
	for _, option := range options {
		option(arl)
	}
	
	go arl.adjustmentLoop()
	
	return arl
}

type AdaptiveOption func(*AdaptiveRateLimiter)

func WithSuccessWindow(window time.Duration) AdaptiveOption {
	return func(arl *AdaptiveRateLimiter) {
		arl.successWindow = window
	}
}

func WithAdjustmentFactor(factor float64) AdaptiveOption {
	return func(arl *AdaptiveRateLimiter) {
		arl.adjustmentFactor = factor
	}
}

func WithRateRange(min, max float64) AdaptiveOption {
	return func(arl *AdaptiveRateLimiter) {
		arl.minRate = min
		arl.maxRate = max
	}
}

func (arl *AdaptiveRateLimiter) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := arl.RateLimiterMiddleware.RoundTrip(req)
	
	arl.mu.Lock()
	if err != nil {
		arl.errorCount++
	} else if resp != nil && resp.StatusCode < 400 {
		arl.successCount++
	} else {
		arl.errorCount++
	}
	arl.mu.Unlock()
	
	return resp, err
}

func (arl *AdaptiveRateLimiter) adjustmentLoop() {
	ticker := time.NewTicker(arl.successWindow)
	defer ticker.Stop()
	
	for range ticker.C {
		arl.adjustRate()
	}
}

func (arl *AdaptiveRateLimiter) adjustRate() {
	arl.mu.Lock()
	successCount := arl.successCount
	errorCount := arl.errorCount
	arl.successCount = 0
	arl.errorCount = 0
	arl.mu.Unlock()
	
	if successCount+errorCount == 0 {
		return
	}
	
	successRate := float64(successCount) / float64(successCount+errorCount)
	currentRate := arl.GetCurrentRate()
	
	var newRate float64
	
	if successRate > 0.95 {
		newRate = currentRate * (1 + arl.adjustmentFactor)
	} else if successRate < 0.8 {
		newRate = currentRate * (1 - arl.adjustmentFactor)
	} else {
		return
	}
	
	if newRate < arl.minRate {
		newRate = arl.minRate
	} else if newRate > arl.maxRate {
		newRate = arl.maxRate
	}
	
	if newRate != currentRate {
		arl.limiter.SetLimit(rate.Limit(newRate))
		arl.lastAdjustment = time.Now()
	}
}

func (arl *AdaptiveRateLimiter) GetAdaptiveStats() AdaptiveStats {
	arl.mu.RLock()
	defer arl.mu.RUnlock()
	
	return AdaptiveStats{
		CurrentRate:      arl.GetCurrentRate(),
		MinRate:          arl.minRate,
		MaxRate:          arl.maxRate,
		SuccessCount:     arl.successCount,
		ErrorCount:       arl.errorCount,
		LastAdjustment:   arl.lastAdjustment,
		AdjustmentFactor: arl.adjustmentFactor,
	}
}

type AdaptiveStats struct {
	CurrentRate      float64
	MinRate          float64
	MaxRate          float64
	SuccessCount     int64
	ErrorCount       int64
	LastAdjustment   time.Time
	AdjustmentFactor float64
}

func getNextTransport(req *http.Request) http.RoundTripper {
	return http.DefaultTransport
}

type PerEndpointRateLimiter struct {
	limiters map[string]*RateLimiterMiddleware
	config   *config.RateLimitConfig
	mu       sync.RWMutex
}

func NewPerEndpointRateLimiter(cfg *config.RateLimitConfig) *PerEndpointRateLimiter {
	return &PerEndpointRateLimiter{
		limiters: make(map[string]*RateLimiterMiddleware),
		config:   cfg,
	}
}

func (per *PerEndpointRateLimiter) RoundTrip(req *http.Request) (*http.Response, error) {
	endpoint := req.URL.Path
	
	per.mu.RLock()
	limiter, exists := per.limiters[endpoint]
	per.mu.RUnlock()
	
	if !exists {
		per.mu.Lock()
		if limiter, exists = per.limiters[endpoint]; !exists {
			limiter = NewRateLimiter(per.config)
			per.limiters[endpoint] = limiter
		}
		per.mu.Unlock()
	}
	
	return limiter.RoundTrip(req)
}

func (per *PerEndpointRateLimiter) GetEndpointMetrics() map[string]RateLimitMetrics {
	per.mu.RLock()
	defer per.mu.RUnlock()
	
	metrics := make(map[string]RateLimitMetrics)
	for endpoint, limiter := range per.limiters {
		metrics[endpoint] = limiter.GetMetrics()
	}
	
	return metrics
}