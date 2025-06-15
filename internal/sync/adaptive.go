package sync

import (
	"context"
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// AdaptiveRetryConfig defines configuration for adaptive retry behavior
type AdaptiveRetryConfig struct {
	// Learning parameters
	LearningRate         float64       `yaml:"learning_rate" json:"learning_rate"`                   // Rate at which to adapt (0.0 to 1.0)
	HistoryWindow        time.Duration `yaml:"history_window" json:"history_window"`                 // How far back to consider for learning
	MinSamples           int           `yaml:"min_samples" json:"min_samples"`                       // Minimum samples before adapting
	AdaptationThreshold  float64       `yaml:"adaptation_threshold" json:"adaptation_threshold"`     // Success rate threshold for adaptation
	
	// Load awareness
	LoadThresholdHigh    float64       `yaml:"load_threshold_high" json:"load_threshold_high"`       // High load threshold (0.0 to 1.0)
	LoadThresholdMedium  float64       `yaml:"load_threshold_medium" json:"load_threshold_medium"`   // Medium load threshold
	LoadReductionFactor  float64       `yaml:"load_reduction_factor" json:"load_reduction_factor"`   // Factor to reduce retries under high load
	
	// Rate limiting
	TokenBucketCapacity  int           `yaml:"token_bucket_capacity" json:"token_bucket_capacity"`   // Token bucket capacity for rate limiting
	TokenRefillRate      float64       `yaml:"token_refill_rate" json:"token_refill_rate"`           // Tokens per second
	
	// Priority queuing
	EnablePriorityQueue  bool          `yaml:"enable_priority_queue" json:"enable_priority_queue"`   // Enable priority-based retry queuing
	MaxQueueSize         int           `yaml:"max_queue_size" json:"max_queue_size"`                 // Maximum queue size
	QueueTimeout         time.Duration `yaml:"queue_timeout" json:"queue_timeout"`                   // Timeout for queued operations
	
	// Feedback loops
	CircuitBreakerWeight float64       `yaml:"circuit_breaker_weight" json:"circuit_breaker_weight"` // Weight of circuit breaker state in decisions
	LatencyWeight        float64       `yaml:"latency_weight" json:"latency_weight"`                 // Weight of latency in decisions
	ErrorRateWeight      float64       `yaml:"error_rate_weight" json:"error_rate_weight"`           // Weight of error rate in decisions
}

// DefaultAdaptiveRetryConfig returns a sensible default configuration
func DefaultAdaptiveRetryConfig() *AdaptiveRetryConfig {
	return &AdaptiveRetryConfig{
		LearningRate:         0.1,
		HistoryWindow:        1 * time.Hour,
		MinSamples:           10,
		AdaptationThreshold:  0.7,
		LoadThresholdHigh:    0.8,
		LoadThresholdMedium:  0.6,
		LoadReductionFactor:  0.5,
		TokenBucketCapacity:  100,
		TokenRefillRate:      10.0,
		EnablePriorityQueue:  true,
		MaxQueueSize:         1000,
		QueueTimeout:         30 * time.Second,
		CircuitBreakerWeight: 0.4,
		LatencyWeight:        0.3,
		ErrorRateWeight:      0.3,
	}
}

// OperationPriority defines priority levels for operations
type OperationPriority int

const (
	PriorityLow OperationPriority = iota
	PriorityNormal
	PriorityHigh
	PriorityCritical
)

func (p OperationPriority) String() string {
	switch p {
	case PriorityLow:
		return "low"
	case PriorityNormal:
		return "normal"
	case PriorityHigh:
		return "high"
	case PriorityCritical:
		return "critical"
	default:
		return "unknown"
	}
}

// SystemLoadMetrics represents current system load metrics
type SystemLoadMetrics struct {
	CPUUsage     float64   `json:"cpu_usage"`
	MemoryUsage  float64   `json:"memory_usage"`
	NetworkUsage float64   `json:"network_usage"`
	QueueDepth   int       `json:"queue_depth"`
	ErrorRate    float64   `json:"error_rate"`
	Latency      time.Duration `json:"latency"`
	Timestamp    time.Time `json:"timestamp"`
}

// HistoricalData represents historical performance data for an operation
type HistoricalData struct {
	OperationType    string            `json:"operation_type"`
	Endpoint         string            `json:"endpoint"`
	ErrorType        string            `json:"error_type"`
	SuccessRate      float64           `json:"success_rate"`
	AverageLatency   time.Duration     `json:"average_latency"`
	SampleCount      int               `json:"sample_count"`
	LastUpdate       time.Time         `json:"last_update"`
	RetryDistribution map[int]float64   `json:"retry_distribution"` // attempt -> success rate
}

// TokenBucket implements a token bucket for rate limiting
type TokenBucket struct {
	capacity    int64
	tokens      int64
	refillRate  float64
	lastRefill  time.Time
	mutex       sync.Mutex
}

// NewTokenBucket creates a new token bucket
func NewTokenBucket(capacity int, refillRate float64) *TokenBucket {
	return &TokenBucket{
		capacity:   int64(capacity),
		tokens:     int64(capacity),
		refillRate: refillRate,
		lastRefill: time.Now(),
	}
}

// TryConsume attempts to consume a token from the bucket
func (tb *TokenBucket) TryConsume() bool {
	tb.mutex.Lock()
	defer tb.mutex.Unlock()
	
	tb.refill()
	
	if tb.tokens > 0 {
		tb.tokens--
		return true
	}
	
	return false
}

// refill adds tokens to the bucket based on elapsed time
func (tb *TokenBucket) refill() {
	now := time.Now()
	elapsed := now.Sub(tb.lastRefill).Seconds()
	tokensToAdd := int64(elapsed * tb.refillRate)
	
	if tokensToAdd > 0 {
		tb.tokens = min(tb.capacity, tb.tokens+tokensToAdd)
		tb.lastRefill = now
	}
}

// PriorityRetryOperation represents a retry operation with priority
type PriorityRetryOperation struct {
	ID          string                `json:"id"`
	Priority    OperationPriority     `json:"priority"`
	Operation   func() error          `json:"-"`
	Context     context.Context       `json:"-"`
	CreatedAt   time.Time            `json:"created_at"`
	Deadline    time.Time            `json:"deadline"`
	Metadata    map[string]interface{} `json:"metadata"`
	RetryCount  int                  `json:"retry_count"`
}

// PriorityQueue implements a priority queue for retry operations
type PriorityQueue struct {
	operations []*PriorityRetryOperation
	mutex      sync.RWMutex
	maxSize    int
}

// NewPriorityQueue creates a new priority queue
func NewPriorityQueue(maxSize int) *PriorityQueue {
	return &PriorityQueue{
		operations: make([]*PriorityRetryOperation, 0),
		maxSize:    maxSize,
	}
}

// Enqueue adds an operation to the priority queue
func (pq *PriorityQueue) Enqueue(operation *PriorityRetryOperation) bool {
	pq.mutex.Lock()
	defer pq.mutex.Unlock()
	
	if len(pq.operations) >= pq.maxSize {
		// Queue is full, reject low priority operations or evict oldest low priority
		if operation.Priority <= PriorityNormal {
			return false
		}
		
		// Try to evict a lower priority operation
		for i, op := range pq.operations {
			if op.Priority < operation.Priority {
				// Remove the lower priority operation
				pq.operations = append(pq.operations[:i], pq.operations[i+1:]...)
				break
			}
		}
		
		// If still full, reject
		if len(pq.operations) >= pq.maxSize {
			return false
		}
	}
	
	pq.operations = append(pq.operations, operation)
	pq.sort()
	
	return true
}

// Dequeue removes and returns the highest priority operation
func (pq *PriorityQueue) Dequeue() *PriorityRetryOperation {
	pq.mutex.Lock()
	defer pq.mutex.Unlock()
	
	if len(pq.operations) == 0 {
		return nil
	}
	
	// Remove expired operations
	pq.removeExpired()
	
	if len(pq.operations) == 0 {
		return nil
	}
	
	// Return highest priority operation (first in sorted slice)
	operation := pq.operations[0]
	pq.operations = pq.operations[1:]
	
	return operation
}

// sort sorts operations by priority (descending) and then by creation time (ascending)
func (pq *PriorityQueue) sort() {
	sort.Slice(pq.operations, func(i, j int) bool {
		if pq.operations[i].Priority == pq.operations[j].Priority {
			return pq.operations[i].CreatedAt.Before(pq.operations[j].CreatedAt)
		}
		return pq.operations[i].Priority > pq.operations[j].Priority
	})
}

// removeExpired removes expired operations from the queue
func (pq *PriorityQueue) removeExpired() {
	now := time.Now()
	validOps := make([]*PriorityRetryOperation, 0, len(pq.operations))
	
	for _, op := range pq.operations {
		if op.Deadline.After(now) {
			validOps = append(validOps, op)
		}
	}
	
	pq.operations = validOps
}

// Size returns the current queue size
func (pq *PriorityQueue) Size() int {
	pq.mutex.RLock()
	defer pq.mutex.RUnlock()
	return len(pq.operations)
}

// AdaptiveRetryEngine implements an intelligent retry system
type AdaptiveRetryEngine struct {
	config            *AdaptiveRetryConfig
	baseRetryer       *EnhancedRetryer
	historicalData    map[string]*HistoricalData // key: operation_type:endpoint:error_type
	circuitBreakers   map[string]*CircuitBreaker
	tokenBucket       *TokenBucket
	priorityQueue     *PriorityQueue
	loadMetrics       atomic.Value // stores *SystemLoadMetrics
	observability     *RetryObservabilityCollector
	
	// Adaptation state
	adaptationState   map[string]*AdaptationState
	
	// Synchronization
	mutex sync.RWMutex
	
	// Background processing
	stopChan chan struct{}
	wg       sync.WaitGroup
}

// AdaptationState tracks the current adaptation state for an operation type
type AdaptationState struct {
	CurrentMultiplier float64   `json:"current_multiplier"`
	CurrentDelay      time.Duration `json:"current_delay"`
	LastAdaptation    time.Time `json:"last_adaptation"`
	SuccessStreak     int       `json:"success_streak"`
	FailureStreak     int       `json:"failure_streak"`
}

// NewAdaptiveRetryEngine creates a new adaptive retry engine
func NewAdaptiveRetryEngine(
	config *AdaptiveRetryConfig,
	baseRetryer *EnhancedRetryer,
	observability *RetryObservabilityCollector,
) *AdaptiveRetryEngine {
	if config == nil {
		config = DefaultAdaptiveRetryConfig()
	}
	
	engine := &AdaptiveRetryEngine{
		config:          config,
		baseRetryer:     baseRetryer,
		historicalData:  make(map[string]*HistoricalData),
		circuitBreakers: make(map[string]*CircuitBreaker),
		tokenBucket:     NewTokenBucket(config.TokenBucketCapacity, config.TokenRefillRate),
		priorityQueue:   NewPriorityQueue(config.MaxQueueSize),
		adaptationState: make(map[string]*AdaptationState),
		observability:   observability,
		stopChan:        make(chan struct{}),
	}
	
	// Initialize with default load metrics
	engine.loadMetrics.Store(&SystemLoadMetrics{
		CPUUsage:     0.0,
		MemoryUsage:  0.0,
		NetworkUsage: 0.0,
		QueueDepth:   0,
		ErrorRate:    0.0,
		Latency:      0,
		Timestamp:    time.Now(),
	})
	
	// Start background processing
	engine.startBackgroundProcessing()
	
	return engine
}

// ExecuteWithAdaptation executes an operation with adaptive retry logic
func (are *AdaptiveRetryEngine) ExecuteWithAdaptation(
	ctx context.Context,
	operation func() error,
	operationType string,
	endpoint string,
	priority OperationPriority,
	metadata map[string]interface{},
) error {
	
	// Check if we should queue the operation based on load
	if are.shouldQueue(priority) {
		return are.enqueueOperation(ctx, operation, operationType, priority, metadata)
	}
	
	// Try to acquire token for rate limiting
	if !are.tokenBucket.TryConsume() {
		// Rate limited - queue if priority allows
		if priority >= PriorityHigh {
			return are.enqueueOperation(ctx, operation, operationType, priority, metadata)
		}
		return NewClassifiedError(
			nil,
			ErrorCategoryRateLimit,
			RetryEligible,
			0,
			60*time.Second,
			operationType,
			map[string]interface{}{"reason": "rate_limited"},
			time.Now(),
			"RATE_LIMITED",
		)
	}
	
	// Get adaptive configuration for this operation
	adaptiveConfig := are.getAdaptiveConfig(operationType, endpoint)
	
	// Execute with adaptive retry
	return are.executeWithAdaptiveRetry(ctx, operation, operationType, endpoint, adaptiveConfig)
}

// shouldQueue determines if an operation should be queued based on system load
func (are *AdaptiveRetryEngine) shouldQueue(priority OperationPriority) bool {
	if !are.config.EnablePriorityQueue {
		return false
	}
	
	loadMetrics := are.loadMetrics.Load().(*SystemLoadMetrics)
	
	// Calculate overall load score
	loadScore := (loadMetrics.CPUUsage + loadMetrics.MemoryUsage + loadMetrics.NetworkUsage) / 3.0
	
	// Queue based on load and priority
	switch {
	case loadScore >= are.config.LoadThresholdHigh:
		return priority < PriorityCritical
	case loadScore >= are.config.LoadThresholdMedium:
		return priority < PriorityHigh
	default:
		return false
	}
}

// enqueueOperation adds an operation to the priority queue
func (are *AdaptiveRetryEngine) enqueueOperation(
	ctx context.Context,
	operation func() error,
	operationType string,
	priority OperationPriority,
	metadata map[string]interface{},
) error {
	
	deadline := time.Now().Add(are.config.QueueTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	
	retryOp := &PriorityRetryOperation{
		ID:        generateOperationID(),
		Priority:  priority,
		Operation: operation,
		Context:   ctx,
		CreatedAt: time.Now(),
		Deadline:  deadline,
		Metadata:  metadata,
	}
	
	if !are.priorityQueue.Enqueue(retryOp) {
		return NewClassifiedError(
			nil,
			ErrorCategoryRateLimit,
			RetryNotEligible,
			0,
			0,
			operationType,
			map[string]interface{}{"reason": "queue_full"},
			time.Now(),
			"QUEUE_FULL",
		)
	}
	
	return nil
}

// getAdaptiveConfig returns an adaptive retry configuration for the operation
func (are *AdaptiveRetryEngine) getAdaptiveConfig(operationType, endpoint string) *RetryConfig {
	// Start with base configuration
	config := *are.baseRetryer.config
	
	// Get adaptation state
	are.mutex.RLock()
	adaptationKey := fmt.Sprintf("%s:%s", operationType, endpoint)
	adaptState, exists := are.adaptationState[adaptationKey]
	are.mutex.RUnlock()
	
	if !exists {
		// Initialize adaptation state
		adaptState = &AdaptationState{
			CurrentMultiplier: config.Multiplier,
			CurrentDelay:      config.InitialDelay,
			LastAdaptation:    time.Now(),
		}
		
		are.mutex.Lock()
		are.adaptationState[adaptationKey] = adaptState
		are.mutex.Unlock()
	}
	
	// Apply adaptive adjustments
	config.Multiplier = adaptState.CurrentMultiplier
	config.InitialDelay = adaptState.CurrentDelay
	
	// Adjust based on circuit breaker state
	if cb, exists := are.circuitBreakers[operationType]; exists {
		switch cb.State() {
		case StateOpen:
			// Reduce aggressiveness when circuit is open
			config.MaxRetries = int(float64(config.MaxRetries) * 0.5)
			config.InitialDelay = time.Duration(float64(config.InitialDelay) * 2.0)
		case StateHalfOpen:
			// Be more conservative in half-open state
			config.MaxRetries = min(config.MaxRetries, 2)
		}
	}
	
	// Adjust based on system load
	loadMetrics := are.loadMetrics.Load().(*SystemLoadMetrics)
	loadScore := (loadMetrics.CPUUsage + loadMetrics.MemoryUsage + loadMetrics.NetworkUsage) / 3.0
	
	if loadScore >= are.config.LoadThresholdHigh {
		// Reduce retry aggressiveness under high load
		config.MaxRetries = int(float64(config.MaxRetries) * are.config.LoadReductionFactor)
		config.InitialDelay = time.Duration(float64(config.InitialDelay) * (1.0 + loadScore))
	}
	
	return &config
}

// executeWithAdaptiveRetry executes operation with adaptive retry logic
func (are *AdaptiveRetryEngine) executeWithAdaptiveRetry(
	ctx context.Context,
	operation func() error,
	operationType, endpoint string,
	config *RetryConfig,
) error {
	
	startTime := time.Now()
	var attempts int
	var lastErr error
	
	// Create adaptive retryer with the adjusted config
	adaptiveRetryer := NewEnhancedRetryer(config)
	
	// Wrap operation to collect data for learning
	wrappedOperation := func() error {
		attempts++
		attemptStart := time.Now()
		
		err := operation()
		attemptDuration := time.Since(attemptStart)
		
		// Update historical data
		are.updateHistoricalData(operationType, endpoint, err, attemptDuration, attempts)
		
		return err
	}
	
	// Execute with adaptive retry
	lastErr = adaptiveRetryer.Execute(ctx, wrappedOperation, operationType)
	
	// Learn from the result
	totalDuration := time.Since(startTime)
	are.learnFromExecution(operationType, endpoint, lastErr, attempts, totalDuration)
	
	return lastErr
}

// updateHistoricalData updates historical performance data
func (are *AdaptiveRetryEngine) updateHistoricalData(
	operationType, endpoint string,
	err error,
	duration time.Duration,
	attempt int,
) {
	are.mutex.Lock()
	defer are.mutex.Unlock()
	
	errorType := "success"
	if err != nil {
		classifier := NewDefaultErrorClassifier()
		classified := classifier.Classify(err, operationType)
		errorType = classified.Category.String()
	}
	
	key := fmt.Sprintf("%s:%s:%s", operationType, endpoint, errorType)
	
	if data, exists := are.historicalData[key]; exists {
		// Update existing data with exponential moving average
		alpha := are.config.LearningRate
		data.AverageLatency = time.Duration(float64(data.AverageLatency)*(1-alpha) + float64(duration)*alpha)
		data.SampleCount++
		data.LastUpdate = time.Now()
		
		// Update success rate
		if err == nil {
			data.SuccessRate = data.SuccessRate*(1-alpha) + alpha
		} else {
			data.SuccessRate = data.SuccessRate * (1 - alpha)
		}
		
		// Update retry distribution
		if data.RetryDistribution == nil {
			data.RetryDistribution = make(map[int]float64)
		}
		if _, exists := data.RetryDistribution[attempt]; !exists {
			data.RetryDistribution[attempt] = 0
		}
		if err == nil {
			data.RetryDistribution[attempt] = data.RetryDistribution[attempt]*(1-alpha) + alpha
		} else {
			data.RetryDistribution[attempt] = data.RetryDistribution[attempt] * (1 - alpha)
		}
	} else {
		// Create new historical data
		successRate := 1.0
		if err != nil {
			successRate = 0.0
		}
		
		are.historicalData[key] = &HistoricalData{
			OperationType:     operationType,
			Endpoint:          endpoint,
			ErrorType:         errorType,
			SuccessRate:       successRate,
			AverageLatency:    duration,
			SampleCount:       1,
			LastUpdate:        time.Now(),
			RetryDistribution: map[int]float64{attempt: successRate},
		}
	}
}

// learnFromExecution adapts retry parameters based on execution results
func (are *AdaptiveRetryEngine) learnFromExecution(
	operationType, endpoint string,
	finalErr error,
	totalAttempts int,
	totalDuration time.Duration,
) {
	are.mutex.Lock()
	defer are.mutex.Unlock()
	
	adaptationKey := fmt.Sprintf("%s:%s", operationType, endpoint)
	adaptState := are.adaptationState[adaptationKey]
	
	if adaptState == nil {
		return // Should not happen, but handle gracefully
	}
	
	// Check if we have enough samples to adapt
	historyKey := fmt.Sprintf("%s:%s:success", operationType, endpoint)
	if histData, exists := are.historicalData[historyKey]; exists {
		if histData.SampleCount < are.config.MinSamples {
			return // Not enough samples yet
		}
		
		// Adapt based on success rate and performance
		if finalErr == nil {
			adaptState.SuccessStreak++
			adaptState.FailureStreak = 0
			
			// If success rate is high and we're performing well, be less aggressive
			if histData.SuccessRate >= are.config.AdaptationThreshold {
				if adaptState.SuccessStreak >= 5 {
					adaptState.CurrentMultiplier = math.Max(1.2, adaptState.CurrentMultiplier*0.95)
					adaptState.CurrentDelay = time.Duration(float64(adaptState.CurrentDelay) * 0.95)
				}
			}
		} else {
			adaptState.FailureStreak++
			adaptState.SuccessStreak = 0
			
			// If success rate is low, be more aggressive
			if histData.SuccessRate < are.config.AdaptationThreshold {
				if adaptState.FailureStreak >= 3 {
					adaptState.CurrentMultiplier = math.Min(3.0, adaptState.CurrentMultiplier*1.1)
					adaptState.CurrentDelay = time.Duration(float64(adaptState.CurrentDelay) * 1.1)
				}
			}
		}
		
		adaptState.LastAdaptation = time.Now()
	}
}

// UpdateSystemLoad updates the current system load metrics
func (are *AdaptiveRetryEngine) UpdateSystemLoad(metrics *SystemLoadMetrics) {
	are.loadMetrics.Store(metrics)
}

// RegisterCircuitBreaker registers a circuit breaker for feedback
func (are *AdaptiveRetryEngine) RegisterCircuitBreaker(name string, cb *CircuitBreaker) {
	are.mutex.Lock()
	defer are.mutex.Unlock()
	are.circuitBreakers[name] = cb
}

// startBackgroundProcessing starts background goroutines for queue processing
func (are *AdaptiveRetryEngine) startBackgroundProcessing() {
	// Start queue processor
	are.wg.Add(1)
	go are.processQueue()
	
	// Start metrics collector
	are.wg.Add(1)
	go are.collectMetrics()
}

// processQueue processes operations from the priority queue
func (are *AdaptiveRetryEngine) processQueue() {
	defer are.wg.Done()
	
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	
	for {
		select {
		case <-are.stopChan:
			return
		case <-ticker.C:
			if operation := are.priorityQueue.Dequeue(); operation != nil {
				// Execute the queued operation
				go func(op *PriorityRetryOperation) {
					if op.Context.Err() != nil {
						return // Context already cancelled
					}
					
					// Try to acquire token
					if !are.tokenBucket.TryConsume() {
						// Still rate limited, re-queue if deadline allows
						if time.Now().Before(op.Deadline) {
							are.priorityQueue.Enqueue(op)
						}
						return
					}
					
					// Execute the operation
					op.RetryCount++
					_ = op.Operation() // Ignore error as it's handled in the operation itself
				}(operation)
			}
		}
	}
}

// collectMetrics periodically collects and updates metrics
func (are *AdaptiveRetryEngine) collectMetrics() {
	defer are.wg.Done()
	
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	
	for {
		select {
		case <-are.stopChan:
			return
		case <-ticker.C:
			are.cleanupOldData()
			are.updateLoadMetrics()
		}
	}
}

// cleanupOldData removes old historical data outside the history window
func (are *AdaptiveRetryEngine) cleanupOldData() {
	are.mutex.Lock()
	defer are.mutex.Unlock()
	
	cutoff := time.Now().Add(-are.config.HistoryWindow)
	
	for key, data := range are.historicalData {
		if data.LastUpdate.Before(cutoff) {
			delete(are.historicalData, key)
		}
	}
}

// updateLoadMetrics updates load metrics based on current state
func (are *AdaptiveRetryEngine) updateLoadMetrics() {
	currentMetrics := are.loadMetrics.Load().(*SystemLoadMetrics)
	
	// Update queue depth
	queueSize := are.priorityQueue.Size()
	
	// Calculate error rate from observability data
	var totalRequests, failedRequests int64
	if are.observability != nil {
		opMetrics := are.observability.GetOperationMetrics()
		for _, metrics := range opMetrics {
			totalRequests += metrics.TotalAttempts
			failedRequests += metrics.FailedAttempts
		}
	}
	
	errorRate := 0.0
	if totalRequests > 0 {
		errorRate = float64(failedRequests) / float64(totalRequests)
	}
	
	// Update metrics (simplified - in reality would get from system)
	newMetrics := &SystemLoadMetrics{
		CPUUsage:     currentMetrics.CPUUsage,     // Would be updated from system
		MemoryUsage:  currentMetrics.MemoryUsage,  // Would be updated from system
		NetworkUsage: currentMetrics.NetworkUsage, // Would be updated from system
		QueueDepth:   queueSize,
		ErrorRate:    errorRate,
		Latency:      currentMetrics.Latency,      // Would be updated from system
		Timestamp:    time.Now(),
	}
	
	are.loadMetrics.Store(newMetrics)
}

// Stop stops the adaptive retry engine
func (are *AdaptiveRetryEngine) Stop() {
	close(are.stopChan)
	are.wg.Wait()
}

// GetHistoricalData returns historical performance data
func (are *AdaptiveRetryEngine) GetHistoricalData() map[string]*HistoricalData {
	are.mutex.RLock()
	defer are.mutex.RUnlock()
	
	result := make(map[string]*HistoricalData)
	for k, v := range are.historicalData {
		// Create copy to avoid mutations
		retryDist := make(map[int]float64)
		for attempt, rate := range v.RetryDistribution {
			retryDist[attempt] = rate
		}
		
		result[k] = &HistoricalData{
			OperationType:     v.OperationType,
			Endpoint:          v.Endpoint,
			ErrorType:         v.ErrorType,
			SuccessRate:       v.SuccessRate,
			AverageLatency:    v.AverageLatency,
			SampleCount:       v.SampleCount,
			LastUpdate:        v.LastUpdate,
			RetryDistribution: retryDist,
		}
	}
	
	return result
}

// GetSystemLoad returns current system load metrics
func (are *AdaptiveRetryEngine) GetSystemLoad() *SystemLoadMetrics {
	return are.loadMetrics.Load().(*SystemLoadMetrics)
}

// GetAdaptationStates returns current adaptation states
func (are *AdaptiveRetryEngine) GetAdaptationStates() map[string]*AdaptationState {
	are.mutex.RLock()
	defer are.mutex.RUnlock()
	
	result := make(map[string]*AdaptationState)
	for k, v := range are.adaptationState {
		result[k] = &AdaptationState{
			CurrentMultiplier: v.CurrentMultiplier,
			CurrentDelay:      v.CurrentDelay,
			LastAdaptation:    v.LastAdaptation,
			SuccessStreak:     v.SuccessStreak,
			FailureStreak:     v.FailureStreak,
		}
	}
	
	return result
}

// Utility functions

func generateOperationID() string {
	return fmt.Sprintf("op-%d", time.Now().UnixNano())
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}