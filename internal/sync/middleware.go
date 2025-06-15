package sync

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// MiddlewareConfig defines configuration for retry and circuit breaker middleware
type MiddlewareConfig struct {
	// HTTP configuration
	HTTPRetryConfig       *RetryConfig             `yaml:"http_retry_config" json:"http_retry_config"`
	HTTPCircuitBreaker    *CircuitBreakerConfig    `yaml:"http_circuit_breaker" json:"http_circuit_breaker"`
	
	// gRPC configuration  
	GRPCRetryConfig       *RetryConfig             `yaml:"grpc_retry_config" json:"grpc_retry_config"`
	GRPCCircuitBreaker    *CircuitBreakerConfig    `yaml:"grpc_circuit_breaker" json:"grpc_circuit_breaker"`
	
	// Observability
	ObservabilityConfig   *ObservabilityConfig     `yaml:"observability_config" json:"observability_config"`
	
	// Hot reloading
	EnableHotReload       bool                     `yaml:"enable_hot_reload" json:"enable_hot_reload"`
	ConfigReloadInterval  time.Duration            `yaml:"config_reload_interval" json:"config_reload_interval"`
	
	// Request hedging
	EnableHedging         bool                     `yaml:"enable_hedging" json:"enable_hedging"`
	HedgingDelay          time.Duration            `yaml:"hedging_delay" json:"hedging_delay"`
	MaxHedgedRequests     int                      `yaml:"max_hedged_requests" json:"max_hedged_requests"`
	
	// Distributed coordination
	EnableDistributed     bool                     `yaml:"enable_distributed" json:"enable_distributed"`
	RedisAddress          string                   `yaml:"redis_address" json:"redis_address"`
	DistributedKeyPrefix  string                   `yaml:"distributed_key_prefix" json:"distributed_key_prefix"`
}

// DefaultMiddlewareConfig returns sensible default configuration
func DefaultMiddlewareConfig() *MiddlewareConfig {
	return &MiddlewareConfig{
		HTTPRetryConfig:      NewWebServiceRetryConfig(),
		HTTPCircuitBreaker:   DefaultCircuitBreakerConfig("http_default"),
		GRPCRetryConfig:      NewWebServiceRetryConfig(),
		GRPCCircuitBreaker:   DefaultCircuitBreakerConfig("grpc_default"),
		ObservabilityConfig:  DefaultObservabilityConfig(),
		EnableHotReload:      true,
		ConfigReloadInterval: 30 * time.Second,
		EnableHedging:        false,
		HedgingDelay:         10 * time.Millisecond,
		MaxHedgedRequests:    2,
		EnableDistributed:    false,
		DistributedKeyPrefix: "harbor_sync_cb",
	}
}

// IntegrationLayer provides comprehensive middleware for HTTP and gRPC
type IntegrationLayer struct {
	config              *MiddlewareConfig
	httpTransport       *HTTPRetryTransport
	grpcInterceptor     *GRPCRetryInterceptor
	adaptiveEngine      *AdaptiveRetryEngine
	observability       *RetryObservabilityCollector
	configManager       *ConfigManager
	distributedBreakers *DistributedCircuitBreakerManager
	
	// Hot reload
	stopConfigReload chan struct{}
	configWG         sync.WaitGroup
	
	// Synchronization
	mutex sync.RWMutex
}

// NewIntegrationLayer creates a new integration layer
func NewIntegrationLayer(config *MiddlewareConfig) (*IntegrationLayer, error) {
	if config == nil {
		config = DefaultMiddlewareConfig()
	}
	
	// Initialize observability
	observability, err := NewRetryObservabilityCollector(config.ObservabilityConfig, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create observability collector: %w", err)
	}
	
	// Initialize adaptive engine
	baseRetryer := NewEnhancedRetryer(config.HTTPRetryConfig)
	adaptiveEngine := NewAdaptiveRetryEngine(DefaultAdaptiveRetryConfig(), baseRetryer, observability)
	
	il := &IntegrationLayer{
		config:           config,
		adaptiveEngine:   adaptiveEngine,
		observability:    observability,
		stopConfigReload: make(chan struct{}),
	}
	
	// Initialize HTTP transport
	il.httpTransport = NewHTTPRetryTransport(config, adaptiveEngine, observability)
	
	// Initialize gRPC interceptor
	il.grpcInterceptor = NewGRPCRetryInterceptor(config, adaptiveEngine, observability)
	
	// Initialize config manager for hot reloading
	if config.EnableHotReload {
		il.configManager = NewConfigManager(config, il.onConfigUpdate)
		il.startConfigReloading()
	}
	
	// Initialize distributed circuit breakers if enabled
	if config.EnableDistributed {
		il.distributedBreakers, err = NewDistributedCircuitBreakerManager(config)
		if err != nil {
			return nil, fmt.Errorf("failed to create distributed circuit breaker manager: %w", err)
		}
	}
	
	return il, nil
}

// HTTPRetryTransport implements http.RoundTripper with retry and circuit breaker support
type HTTPRetryTransport struct {
	config         *MiddlewareConfig
	baseTransport  http.RoundTripper
	adaptiveEngine *AdaptiveRetryEngine
	observability  *RetryObservabilityCollector
	circuitBreakers map[string]*CircuitBreaker
	mutex          sync.RWMutex
}

// NewHTTPRetryTransport creates a new HTTP retry transport
func NewHTTPRetryTransport(
	config *MiddlewareConfig,
	adaptiveEngine *AdaptiveRetryEngine,
	observability *RetryObservabilityCollector,
) *HTTPRetryTransport {
	return &HTTPRetryTransport{
		config:          config,
		baseTransport:   http.DefaultTransport,
		adaptiveEngine:  adaptiveEngine,
		observability:   observability,
		circuitBreakers: make(map[string]*CircuitBreaker),
	}
}

// RoundTrip implements http.RoundTripper with comprehensive retry logic
func (hrt *HTTPRetryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Extract operation metadata
	operationType := "http_request"
	endpoint := fmt.Sprintf("%s://%s", req.URL.Scheme, req.URL.Host)
	priority := PriorityNormal
	
	// Check for priority header
	if priorityHeader := req.Header.Get("X-Retry-Priority"); priorityHeader != "" {
		switch strings.ToLower(priorityHeader) {
		case "critical":
			priority = PriorityCritical
		case "high":
			priority = PriorityHigh
		case "low":
			priority = PriorityLow
		}
	}
	
	// Prepare metadata
	metadata := map[string]interface{}{
		"method":    req.Method,
		"url":       req.URL.String(),
		"endpoint":  endpoint,
		"user_agent": req.Header.Get("User-Agent"),
	}
	
	// Clone request for retries
	clonedReq, err := hrt.cloneRequest(req)
	if err != nil {
		return nil, fmt.Errorf("failed to clone request: %w", err)
	}
	
	// Get or create circuit breaker for this endpoint
	circuitBreaker := hrt.getCircuitBreaker(endpoint)
	
	// Execute with hedging if enabled
	if hrt.config.EnableHedging && isIdempotent(req) {
		return hrt.executeWithHedging(req.Context(), clonedReq, operationType, endpoint, priority, metadata, circuitBreaker)
	}
	
	// Regular execution with adaptive retry
	var lastResp *http.Response
	operation := func() error {
		return circuitBreaker.Execute(func() error {
			resp, err := hrt.baseTransport.RoundTrip(clonedReq)
			if err != nil {
				return err
			}
			
			// Check for HTTP error status codes
			if resp.StatusCode >= 400 {
				lastResp = resp
				return NewHTTPError(resp.StatusCode, resp.Status, "", req.Method, req.URL.String())
			}
			
			lastResp = resp
			return nil
		})
	}
	
	err = hrt.adaptiveEngine.ExecuteWithAdaptation(
		req.Context(),
		operation,
		operationType,
		endpoint,
		priority,
		metadata,
	)
	
	if err != nil {
		return lastResp, err
	}
	
	return lastResp, nil
}

// executeWithHedging executes request with hedging for latency-sensitive operations
func (hrt *HTTPRetryTransport) executeWithHedging(
	ctx context.Context,
	req *http.Request,
	operationType, endpoint string,
	priority OperationPriority,
	metadata map[string]interface{},
	circuitBreaker *CircuitBreaker,
) (*http.Response, error) {
	
	type hedgedResult struct {
		resp *http.Response
		err  error
	}
	
	resultChan := make(chan hedgedResult, hrt.config.MaxHedgedRequests)
	hedgeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	
	// Start primary request
	go func() {
		clonedReq := req.Clone(hedgeCtx)
		resp, err := hrt.baseTransport.RoundTrip(clonedReq)
		select {
		case resultChan <- hedgedResult{resp: resp, err: err}:
		case <-hedgeCtx.Done():
		}
	}()
	
	// Start hedged requests with delay
	for i := 1; i < hrt.config.MaxHedgedRequests; i++ {
		select {
		case <-hedgeCtx.Done():
			return nil, ctx.Err()
		case <-time.After(hrt.config.HedgingDelay):
			go func() {
				clonedReq := req.Clone(hedgeCtx)
				resp, err := hrt.baseTransport.RoundTrip(clonedReq)
				select {
				case resultChan <- hedgedResult{resp: resp, err: err}:
				case <-hedgeCtx.Done():
				}
			}()
		case result := <-resultChan:
			// Got result from primary request
			cancel() // Cancel other requests
			return result.resp, result.err
		}
	}
	
	// Wait for any result
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-resultChan:
		cancel() // Cancel remaining requests
		return result.resp, result.err
	}
}

// getCircuitBreaker gets or creates a circuit breaker for an endpoint
func (hrt *HTTPRetryTransport) getCircuitBreaker(endpoint string) *CircuitBreaker {
	hrt.mutex.RLock()
	cb, exists := hrt.circuitBreakers[endpoint]
	hrt.mutex.RUnlock()
	
	if exists {
		return cb
	}
	
	hrt.mutex.Lock()
	defer hrt.mutex.Unlock()
	
	// Double-check after acquiring write lock
	if cb, exists = hrt.circuitBreakers[endpoint]; exists {
		return cb
	}
	
	// Create new circuit breaker
	config := *hrt.config.HTTPCircuitBreaker
	config.Name = fmt.Sprintf("http_%s", endpoint)
	cb = NewCircuitBreaker(&config)
	hrt.circuitBreakers[endpoint] = cb
	
	// Register with adaptive engine
	hrt.adaptiveEngine.RegisterCircuitBreaker(config.Name, cb)
	
	return cb
}

// cloneRequest creates a deep copy of an HTTP request
func (hrt *HTTPRetryTransport) cloneRequest(req *http.Request) (*http.Request, error) {
	cloned := req.Clone(req.Context())
	
	if req.Body != nil {
		bodyBytes, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		
		// Reset original body
		req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		
		// Set cloned body
		cloned.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	}
	
	return cloned, nil
}

// isIdempotent checks if a request is idempotent and safe for hedging
func isIdempotent(req *http.Request) bool {
	switch req.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	case http.MethodPut, http.MethodDelete:
		// These are idempotent but may have side effects, be conservative
		return false
	default:
		return false
	}
}

// GRPCRetryInterceptor provides gRPC unary and streaming interceptors with retry support
type GRPCRetryInterceptor struct {
	config         *MiddlewareConfig
	adaptiveEngine *AdaptiveRetryEngine
	observability  *RetryObservabilityCollector
	circuitBreakers map[string]*CircuitBreaker
	mutex          sync.RWMutex
}

// NewGRPCRetryInterceptor creates a new gRPC retry interceptor
func NewGRPCRetryInterceptor(
	config *MiddlewareConfig,
	adaptiveEngine *AdaptiveRetryEngine,
	observability *RetryObservabilityCollector,
) *GRPCRetryInterceptor {
	return &GRPCRetryInterceptor{
		config:          config,
		adaptiveEngine:  adaptiveEngine,
		observability:   observability,
		circuitBreakers: make(map[string]*CircuitBreaker),
	}
}

// UnaryClientInterceptor returns a gRPC unary client interceptor with retry support
func (gri *GRPCRetryInterceptor) UnaryClientInterceptor() grpc.UnaryClientInterceptor {
	return func(
		ctx context.Context,
		method string,
		req, reply interface{},
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		
		// Extract metadata
		operationType := "grpc_unary"
		endpoint := cc.Target()
		priority := gri.extractPriority(ctx)
		
		metadata := map[string]interface{}{
			"method":   method,
			"target":   endpoint,
			"type":     "unary",
		}
		
		// Get circuit breaker
		circuitBreaker := gri.getCircuitBreaker(endpoint, method)
		
		// Execute with adaptive retry
		operation := func() error {
			return circuitBreaker.Execute(func() error {
				err := invoker(ctx, method, req, reply, cc, opts...)
				
				// Check for retryable gRPC errors
				if err != nil {
					grpcCode := status.Code(err)
					if !isRetryableGRPCCode(grpcCode) {
						return NewClassifiedError(
							err,
							ErrorCategoryPermanent,
							RetryNotEligible,
							int(grpcCode),
							0,
							operationType,
							map[string]interface{}{"grpc_code": grpcCode.String()},
							time.Now(),
							fmt.Sprintf("GRPC_%s", grpcCode.String()),
						)
					}
				}
				
				return err
			})
		}
		
		return gri.adaptiveEngine.ExecuteWithAdaptation(
			ctx,
			operation,
			operationType,
			endpoint,
			priority,
			metadata,
		)
	}
}

// StreamClientInterceptor returns a gRPC streaming client interceptor with retry support
func (gri *GRPCRetryInterceptor) StreamClientInterceptor() grpc.StreamClientInterceptor {
	return func(
		ctx context.Context,
		desc *grpc.StreamDesc,
		cc *grpc.ClientConn,
		method string,
		streamer grpc.Streamer,
		opts ...grpc.CallOption,
	) (grpc.ClientStream, error) {
		
		// For streaming, we implement retry at the stream level
		operationType := "grpc_stream"
		endpoint := cc.Target()
		priority := gri.extractPriority(ctx)
		
		metadata := map[string]interface{}{
			"method":     method,
			"target":     endpoint,
			"type":       "stream",
			"stream_type": getStreamType(desc),
		}
		
		// Get circuit breaker
		circuitBreaker := gri.getCircuitBreaker(endpoint, method)
		
		var stream grpc.ClientStream
		operation := func() error {
			return circuitBreaker.Execute(func() error {
				var err error
				stream, err = streamer(ctx, desc, cc, method, opts...)
				return err
			})
		}
		
		err := gri.adaptiveEngine.ExecuteWithAdaptation(
			ctx,
			operation,
			operationType,
			endpoint,
			priority,
			metadata,
		)
		
		if err != nil {
			return nil, err
		}
		
		// Wrap the stream to handle retries on stream operations
		return &retryableClientStream{
			ClientStream:   stream,
			interceptor:    gri,
			method:        method,
			endpoint:      endpoint,
			operationType: operationType,
		}, nil
	}
}

// extractPriority extracts priority from gRPC context
func (gri *GRPCRetryInterceptor) extractPriority(ctx context.Context) OperationPriority {
	if md, ok := metadata.FromOutgoingContext(ctx); ok {
		if priorities := md.Get("x-retry-priority"); len(priorities) > 0 {
			switch strings.ToLower(priorities[0]) {
			case "critical":
				return PriorityCritical
			case "high":
				return PriorityHigh
			case "low":
				return PriorityLow
			}
		}
	}
	return PriorityNormal
}

// getCircuitBreaker gets or creates a circuit breaker for a gRPC endpoint/method
func (gri *GRPCRetryInterceptor) getCircuitBreaker(endpoint, method string) *CircuitBreaker {
	key := fmt.Sprintf("%s:%s", endpoint, method)
	
	gri.mutex.RLock()
	cb, exists := gri.circuitBreakers[key]
	gri.mutex.RUnlock()
	
	if exists {
		return cb
	}
	
	gri.mutex.Lock()
	defer gri.mutex.Unlock()
	
	// Double-check after acquiring write lock
	if cb, exists = gri.circuitBreakers[key]; exists {
		return cb
	}
	
	// Create new circuit breaker
	config := *gri.config.GRPCCircuitBreaker
	config.Name = fmt.Sprintf("grpc_%s_%s", endpoint, method)
	cb = NewCircuitBreaker(&config)
	gri.circuitBreakers[key] = cb
	
	// Register with adaptive engine
	gri.adaptiveEngine.RegisterCircuitBreaker(config.Name, cb)
	
	return cb
}

// retryableClientStream wraps a gRPC client stream with retry capability
type retryableClientStream struct {
	grpc.ClientStream
	interceptor   *GRPCRetryInterceptor
	method        string
	endpoint      string
	operationType string
}

// SendMsg sends a message with retry logic
func (rcs *retryableClientStream) SendMsg(m interface{}) error {
	operation := func() error {
		return rcs.ClientStream.SendMsg(m)
	}
	
	return rcs.interceptor.adaptiveEngine.ExecuteWithAdaptation(
		rcs.Context(),
		operation,
		rcs.operationType+"_send",
		rcs.endpoint,
		PriorityNormal,
		map[string]interface{}{
			"method": rcs.method,
			"operation": "send_msg",
		},
	)
}

// RecvMsg receives a message with retry logic
func (rcs *retryableClientStream) RecvMsg(m interface{}) error {
	operation := func() error {
		return rcs.ClientStream.RecvMsg(m)
	}
	
	return rcs.interceptor.adaptiveEngine.ExecuteWithAdaptation(
		rcs.Context(),
		operation,
		rcs.operationType+"_recv",
		rcs.endpoint,
		PriorityNormal,
		map[string]interface{}{
			"method": rcs.method,
			"operation": "recv_msg",
		},
	)
}

// ConfigManager handles hot reloading of configuration
type ConfigManager struct {
	config       *MiddlewareConfig
	updateFunc   func(*MiddlewareConfig)
	lastModified time.Time
	mutex        sync.RWMutex
}

// NewConfigManager creates a new config manager
func NewConfigManager(config *MiddlewareConfig, updateFunc func(*MiddlewareConfig)) *ConfigManager {
	return &ConfigManager{
		config:       config,
		updateFunc:   updateFunc,
		lastModified: time.Now(),
	}
}

// DistributedCircuitBreakerManager manages circuit breakers across multiple instances
type DistributedCircuitBreakerManager struct {
	config      *MiddlewareConfig
	localStates map[string]CircuitBreakerState
	mutex       sync.RWMutex
	// In a real implementation, this would include Redis client
}

// NewDistributedCircuitBreakerManager creates a new distributed circuit breaker manager
func NewDistributedCircuitBreakerManager(config *MiddlewareConfig) (*DistributedCircuitBreakerManager, error) {
	return &DistributedCircuitBreakerManager{
		config:      config,
		localStates: make(map[string]CircuitBreakerState),
	}, nil
}

// Integration layer methods

// GetHTTPTransport returns the HTTP transport with retry capabilities
func (il *IntegrationLayer) GetHTTPTransport() http.RoundTripper {
	return il.httpTransport
}

// GetGRPCUnaryInterceptor returns the gRPC unary client interceptor
func (il *IntegrationLayer) GetGRPCUnaryInterceptor() grpc.UnaryClientInterceptor {
	return il.grpcInterceptor.UnaryClientInterceptor()
}

// GetGRPCStreamInterceptor returns the gRPC streaming client interceptor
func (il *IntegrationLayer) GetGRPCStreamInterceptor() grpc.StreamClientInterceptor {
	return il.grpcInterceptor.StreamClientInterceptor()
}

// UpdateConfig updates the configuration (for hot reloading)
func (il *IntegrationLayer) UpdateConfig(newConfig *MiddlewareConfig) {
	il.mutex.Lock()
	defer il.mutex.Unlock()
	
	il.config = newConfig
	// Update components with new config
	// In a full implementation, this would update all components
}

// onConfigUpdate handles configuration updates
func (il *IntegrationLayer) onConfigUpdate(newConfig *MiddlewareConfig) {
	il.UpdateConfig(newConfig)
}

// startConfigReloading starts the configuration reloading goroutine
func (il *IntegrationLayer) startConfigReloading() {
	il.configWG.Add(1)
	go func() {
		defer il.configWG.Done()
		
		ticker := time.NewTicker(il.config.ConfigReloadInterval)
		defer ticker.Stop()
		
		for {
			select {
			case <-il.stopConfigReload:
				return
			case <-ticker.C:
				// In a real implementation, this would check for config file changes
				// and reload the configuration
			}
		}
	}()
}

// GetMetrics returns comprehensive metrics from all components
func (il *IntegrationLayer) GetMetrics() map[string]interface{} {
	metrics := make(map[string]interface{})
	
	// Observability metrics
	if il.observability != nil {
		metrics["error_metrics"] = il.observability.GetErrorMetrics()
		metrics["operation_metrics"] = il.observability.GetOperationMetrics()
		metrics["circuit_breaker_states"] = il.observability.GetCircuitBreakerStates()
	}
	
	// Adaptive engine metrics
	if il.adaptiveEngine != nil {
		metrics["historical_data"] = il.adaptiveEngine.GetHistoricalData()
		metrics["system_load"] = il.adaptiveEngine.GetSystemLoad()
		metrics["adaptation_states"] = il.adaptiveEngine.GetAdaptationStates()
	}
	
	return metrics
}

// Stop gracefully stops the integration layer
func (il *IntegrationLayer) Stop() {
	// Stop config reloading
	if il.configManager != nil {
		close(il.stopConfigReload)
		il.configWG.Wait()
	}
	
	// Stop adaptive engine
	if il.adaptiveEngine != nil {
		il.adaptiveEngine.Stop()
	}
}

// Utility functions

// isRetryableGRPCCode checks if a gRPC code is retryable
func isRetryableGRPCCode(code codes.Code) bool {
	switch code {
	case codes.Unavailable, codes.DeadlineExceeded, codes.Internal, codes.Aborted, codes.ResourceExhausted:
		return true
	default:
		return false
	}
}

// getStreamType returns the type of gRPC stream
func getStreamType(desc *grpc.StreamDesc) string {
	if desc.ClientStreams && desc.ServerStreams {
		return "bidirectional"
	} else if desc.ClientStreams {
		return "client_streaming"
	} else if desc.ServerStreams {
		return "server_streaming"
	}
	return "unknown"
}

// CreateHTTPClientWithRetry creates an HTTP client with retry capabilities
func CreateHTTPClientWithRetry(config *MiddlewareConfig) (*http.Client, error) {
	il, err := NewIntegrationLayer(config)
	if err != nil {
		return nil, err
	}
	
	return &http.Client{
		Transport: il.GetHTTPTransport(),
		Timeout:   30 * time.Second,
	}, nil
}

// CreateGRPCDialOptions creates gRPC dial options with retry interceptors
func CreateGRPCDialOptions(config *MiddlewareConfig) ([]grpc.DialOption, error) {
	il, err := NewIntegrationLayer(config)
	if err != nil {
		return nil, err
	}
	
	return []grpc.DialOption{
		grpc.WithUnaryInterceptor(il.GetGRPCUnaryInterceptor()),
		grpc.WithStreamInterceptor(il.GetGRPCStreamInterceptor()),
	}, nil
}