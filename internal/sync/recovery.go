package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// BatchOperationStatus represents the status of a batch operation
type BatchOperationStatus int

const (
	BatchStatusPending BatchOperationStatus = iota
	BatchStatusRunning
	BatchStatusPartiallyCompleted
	BatchStatusCompleted
	BatchStatusFailed
	BatchStatusRollingBack
	BatchStatusRolledBack
	BatchStatusCompensating
)

func (s BatchOperationStatus) String() string {
	switch s {
	case BatchStatusPending:
		return "pending"
	case BatchStatusRunning:
		return "running"
	case BatchStatusPartiallyCompleted:
		return "partially_completed"
	case BatchStatusCompleted:
		return "completed"
	case BatchStatusFailed:
		return "failed"
	case BatchStatusRollingBack:
		return "rolling_back"
	case BatchStatusRolledBack:
		return "rolled_back"
	case BatchStatusCompensating:
		return "compensating"
	default:
		return "unknown"
	}
}

// IdempotencyKey represents a unique identifier for ensuring operations are not duplicated
type IdempotencyKey string

// BatchOperation represents a single operation within a batch
type BatchOperation struct {
	ID             string                 `json:"id"`
	Type           string                 `json:"type"`
	Data           map[string]interface{} `json:"data"`
	Status         BatchOperationStatus   `json:"status"`
	StartTime      time.Time              `json:"start_time"`
	EndTime        time.Time              `json:"end_time"`
	Error          string                 `json:"error,omitempty"`
	Checkpoints    []Checkpoint           `json:"checkpoints"`
	IdempotencyKey IdempotencyKey         `json:"idempotency_key"`
	CompensationData map[string]interface{} `json:"compensation_data,omitempty"`
	Dependencies   []string               `json:"dependencies,omitempty"`
}

// Checkpoint represents a recoverable state in an operation
type Checkpoint struct {
	ID        string                 `json:"id"`
	Timestamp time.Time              `json:"timestamp"`
	State     map[string]interface{} `json:"state"`
	Message   string                 `json:"message"`
}

// BatchJobResult contains the result of a batch operation
type BatchJobResult struct {
	OperationID string                 `json:"operation_id"`
	Success     bool                   `json:"success"`
	Error       error                  `json:"error,omitempty"`
	Result      map[string]interface{} `json:"result,omitempty"`
	Checkpoints []Checkpoint           `json:"checkpoints,omitempty"`
}

// CompensationHandler defines how to compensate/rollback an operation
type CompensationHandler func(ctx context.Context, operation *BatchOperation) error

// OperationHandler defines how to execute a batch operation
type OperationHandler func(ctx context.Context, operation *BatchOperation, progress ProgressReporter) (*BatchJobResult, error)

// ProgressReporter allows operations to report progress and create checkpoints
type ProgressReporter interface {
	// ReportProgress reports the current progress (0.0 to 1.0)
	ReportProgress(operation string, progress float64, message string)
	// CreateCheckpoint creates a recoverable checkpoint
	CreateCheckpoint(operation string, checkpointID string, state map[string]interface{}, message string) error
	// GetLastCheckpoint retrieves the last checkpoint for resuming
	GetLastCheckpoint(operation string) (*Checkpoint, error)
}

// TransactionLog tracks batch operations for recovery purposes
type TransactionLog interface {
	// LogOperation logs a batch operation
	LogOperation(ctx context.Context, operation *BatchOperation) error
	// UpdateOperation updates an existing operation's status
	UpdateOperation(ctx context.Context, operationID string, status BatchOperationStatus, result *BatchJobResult) error
	// GetOperation retrieves an operation by ID
	GetOperation(ctx context.Context, operationID string) (*BatchOperation, error)
	// GetOperationsByBatch retrieves all operations for a batch
	GetOperationsByBatch(ctx context.Context, batchID string) ([]*BatchOperation, error)
	// GetIncompleteOperations retrieves operations that need recovery
	GetIncompleteOperations(ctx context.Context) ([]*BatchOperation, error)
	// MarkAsProcessed marks an idempotency key as processed
	MarkAsProcessed(ctx context.Context, key IdempotencyKey, result *BatchJobResult) error
	// IsProcessed checks if an idempotency key has been processed
	IsProcessed(ctx context.Context, key IdempotencyKey) (*BatchJobResult, bool, error)
}

// PartialFailureRecoveryManager manages partial failures in batch operations
type PartialFailureRecoveryManager struct {
	transactionLog      TransactionLog
	compensationHandlers map[string]CompensationHandler
	operationHandlers   map[string]OperationHandler
	idempotencyStore    map[IdempotencyKey]*BatchJobResult
	progressReporters   map[string]ProgressReporter
	mutex               sync.RWMutex
	
	// Configuration
	maxRetries          int
	checkpointInterval  time.Duration
	compensationTimeout time.Duration
	parallelism         int
}

// RecoveryConfig defines configuration for the recovery manager
type RecoveryConfig struct {
	MaxRetries          int           `yaml:"max_retries" json:"max_retries"`
	CheckpointInterval  time.Duration `yaml:"checkpoint_interval" json:"checkpoint_interval"`
	CompensationTimeout time.Duration `yaml:"compensation_timeout" json:"compensation_timeout"`
	Parallelism         int           `yaml:"parallelism" json:"parallelism"`
}

// DefaultRecoveryConfig returns a sensible default configuration
func DefaultRecoveryConfig() *RecoveryConfig {
	return &RecoveryConfig{
		MaxRetries:          3,
		CheckpointInterval:  30 * time.Second,
		CompensationTimeout: 5 * time.Minute,
		Parallelism:         5,
	}
}

// NewPartialFailureRecoveryManager creates a new recovery manager
func NewPartialFailureRecoveryManager(
	config *RecoveryConfig,
	transactionLog TransactionLog,
) *PartialFailureRecoveryManager {
	if config == nil {
		config = DefaultRecoveryConfig()
	}
	
	return &PartialFailureRecoveryManager{
		transactionLog:       transactionLog,
		compensationHandlers: make(map[string]CompensationHandler),
		operationHandlers:    make(map[string]OperationHandler),
		idempotencyStore:     make(map[IdempotencyKey]*BatchJobResult),
		progressReporters:    make(map[string]ProgressReporter),
		maxRetries:           config.MaxRetries,
		checkpointInterval:   config.CheckpointInterval,
		compensationTimeout:  config.CompensationTimeout,
		parallelism:          config.Parallelism,
	}
}

// RegisterOperationHandler registers a handler for a specific operation type
func (pfrm *PartialFailureRecoveryManager) RegisterOperationHandler(operationType string, handler OperationHandler) {
	pfrm.mutex.Lock()
	defer pfrm.mutex.Unlock()
	pfrm.operationHandlers[operationType] = handler
}

// RegisterCompensationHandler registers a compensation handler for a specific operation type
func (pfrm *PartialFailureRecoveryManager) RegisterCompensationHandler(operationType string, handler CompensationHandler) {
	pfrm.mutex.Lock()
	defer pfrm.mutex.Unlock()
	pfrm.compensationHandlers[operationType] = handler
}

// RegisterProgressReporter registers a progress reporter for an operation
func (pfrm *PartialFailureRecoveryManager) RegisterProgressReporter(operationID string, reporter ProgressReporter) {
	pfrm.mutex.Lock()
	defer pfrm.mutex.Unlock()
	pfrm.progressReporters[operationID] = reporter
}

// ExecuteBatchOperations executes a batch of operations with partial failure recovery
func (pfrm *PartialFailureRecoveryManager) ExecuteBatchOperations(
	ctx context.Context,
	batchID string,
	operations []*BatchOperation,
) ([]*BatchJobResult, error) {
	
	// Check for duplicate operations using idempotency keys
	for _, op := range operations {
		if result, processed, err := pfrm.transactionLog.IsProcessed(ctx, op.IdempotencyKey); err != nil {
			return nil, fmt.Errorf("failed to check idempotency for operation %s: %w", op.ID, err)
		} else if processed {
			// Return cached result for idempotent operation
			continue
		}
	}
	
	// Log all operations
	for _, op := range operations {
		op.Status = BatchStatusPending
		if err := pfrm.transactionLog.LogOperation(ctx, op); err != nil {
			return nil, fmt.Errorf("failed to log operation %s: %w", op.ID, err)
		}
	}
	
	// Create dependency graph and execute in proper order
	dependencyGraph := pfrm.buildDependencyGraph(operations)
	executionOrder := pfrm.topologicalSort(dependencyGraph)
	
	results := make([]*BatchJobResult, len(operations))
	var wg sync.WaitGroup
	resultsChan := make(chan *BatchJobResult, len(operations))
	errorsChan := make(chan error, len(operations))
	
	// Execute operations in dependency order with limited parallelism
	semaphore := make(chan struct{}, pfrm.parallelism)
	
	for _, opIndex := range executionOrder {
		wg.Add(1)
		go func(operation *BatchOperation, index int) {
			defer wg.Done()
			
			// Acquire semaphore
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			
			result, err := pfrm.executeOperationWithRecovery(ctx, operation)
			if err != nil {
				errorsChan <- fmt.Errorf("operation %s failed: %w", operation.ID, err)
				return
			}
			
			results[index] = result
			resultsChan <- result
		}(operations[opIndex], opIndex)
	}
	
	// Wait for completion
	wg.Wait()
	close(resultsChan)
	close(errorsChan)
	
	// Check for errors
	var errors []error
	for err := range errorsChan {
		errors = append(errors, err)
	}
	
	if len(errors) > 0 {
		// Partial failure detected - attempt compensation
		if err := pfrm.compensateFailedOperations(ctx, batchID); err != nil {
			return results, fmt.Errorf("compensation failed: %w", err)
		}
		return results, fmt.Errorf("batch operation partially failed with %d errors", len(errors))
	}
	
	return results, nil
}

// executeOperationWithRecovery executes a single operation with recovery support
func (pfrm *PartialFailureRecoveryManager) executeOperationWithRecovery(
	ctx context.Context,
	operation *BatchOperation,
) (*BatchJobResult, error) {
	
	// Check for existing progress reporter
	pfrm.mutex.RLock()
	reporter, hasReporter := pfrm.progressReporters[operation.ID]
	handler, hasHandler := pfrm.operationHandlers[operation.Type]
	pfrm.mutex.RUnlock()
	
	if !hasHandler {
		return nil, fmt.Errorf("no handler registered for operation type: %s", operation.Type)
	}
	
	if !hasReporter {
		reporter = &DefaultProgressReporter{
			transactionLog: pfrm.transactionLog,
		}
	}
	
	// Check for existing checkpoint to resume from
	checkpoint, err := reporter.GetLastCheckpoint(operation.ID)
	if err == nil && checkpoint != nil {
		// Resume from checkpoint
		operation.Status = BatchStatusRunning
		if err := pfrm.transactionLog.UpdateOperation(ctx, operation.ID, BatchStatusRunning, nil); err != nil {
			return nil, fmt.Errorf("failed to update operation status: %w", err)
		}
	}
	
	// Execute with retries
	var lastResult *BatchJobResult
	var lastErr error
	
	for attempt := 1; attempt <= pfrm.maxRetries; attempt++ {
		operation.StartTime = time.Now()
		operation.Status = BatchStatusRunning
		
		// Execute the operation
		result, err := handler(ctx, operation, reporter)
		operation.EndTime = time.Now()
		
		if err == nil {
			// Success
			operation.Status = BatchStatusCompleted
			lastResult = result
			lastErr = nil
			
			// Mark idempotency key as processed
			if err := pfrm.transactionLog.MarkAsProcessed(ctx, operation.IdempotencyKey, result); err != nil {
				return nil, fmt.Errorf("failed to mark idempotency key as processed: %w", err)
			}
			
			break
		}
		
		// Handle failure
		lastErr = err
		operation.Error = err.Error()
		
		// Check if error is retryable
		classifier := NewDefaultErrorClassifier()
		classified := classifier.Classify(err, operation.Type)
		
		if !classified.IsRetryable() || attempt == pfrm.maxRetries {
			operation.Status = BatchStatusFailed
			break
		}
		
		// Wait before retry
		delay := time.Duration(attempt*attempt) * time.Second // Quadratic backoff
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
			// Continue to next attempt
		}
	}
	
	// Update final operation status
	if err := pfrm.transactionLog.UpdateOperation(ctx, operation.ID, operation.Status, lastResult); err != nil {
		return nil, fmt.Errorf("failed to update final operation status: %w", err)
	}
	
	if lastErr != nil {
		return lastResult, lastErr
	}
	
	return lastResult, nil
}

// compensateFailedOperations performs compensation for failed operations in a batch
func (pfrm *PartialFailureRecoveryManager) compensateFailedOperations(ctx context.Context, batchID string) error {
	// Get all operations for the batch
	operations, err := pfrm.transactionLog.GetOperationsByBatch(ctx, batchID)
	if err != nil {
		return fmt.Errorf("failed to get operations for batch %s: %w", batchID, err)
	}
	
	// Find completed operations that need compensation (reverse order)
	var opsToCompensate []*BatchOperation
	for i := len(operations) - 1; i >= 0; i-- {
		op := operations[i]
		if op.Status == BatchStatusCompleted {
			opsToCompensate = append(opsToCompensate, op)
		}
	}
	
	// Perform compensation
	for _, op := range opsToCompensate {
		if err := pfrm.compensateOperation(ctx, op); err != nil {
			return fmt.Errorf("failed to compensate operation %s: %w", op.ID, err)
		}
	}
	
	return nil
}

// compensateOperation performs compensation for a single operation
func (pfrm *PartialFailureRecoveryManager) compensateOperation(ctx context.Context, operation *BatchOperation) error {
	pfrm.mutex.RLock()
	compensationHandler, exists := pfrm.compensationHandlers[operation.Type]
	pfrm.mutex.RUnlock()
	
	if !exists {
		// No compensation handler - log warning and continue
		return nil
	}
	
	// Create timeout context for compensation
	compensationCtx, cancel := context.WithTimeout(ctx, pfrm.compensationTimeout)
	defer cancel()
	
	// Mark as compensating
	operation.Status = BatchStatusCompensating
	if err := pfrm.transactionLog.UpdateOperation(ctx, operation.ID, BatchStatusCompensating, nil); err != nil {
		return fmt.Errorf("failed to mark operation as compensating: %w", err)
	}
	
	// Execute compensation
	if err := compensationHandler(compensationCtx, operation); err != nil {
		operation.Status = BatchStatusFailed
		operation.Error = fmt.Sprintf("compensation failed: %v", err)
		pfrm.transactionLog.UpdateOperation(ctx, operation.ID, BatchStatusFailed, nil)
		return err
	}
	
	// Mark as rolled back
	operation.Status = BatchStatusRolledBack
	return pfrm.transactionLog.UpdateOperation(ctx, operation.ID, BatchStatusRolledBack, nil)
}

// RecoverIncompleteOperations recovers operations that were interrupted
func (pfrm *PartialFailureRecoveryManager) RecoverIncompleteOperations(ctx context.Context) error {
	incompleteOps, err := pfrm.transactionLog.GetIncompleteOperations(ctx)
	if err != nil {
		return fmt.Errorf("failed to get incomplete operations: %w", err)
	}
	
	for _, op := range incompleteOps {
		// Attempt to recover the operation
		if _, err := pfrm.executeOperationWithRecovery(ctx, op); err != nil {
			// If recovery fails, attempt compensation
			if compensationErr := pfrm.compensateOperation(ctx, op); compensationErr != nil {
				return fmt.Errorf("failed to recover or compensate operation %s: %w", op.ID, compensationErr)
			}
		}
	}
	
	return nil
}

// buildDependencyGraph builds a dependency graph from operations
func (pfrm *PartialFailureRecoveryManager) buildDependencyGraph(operations []*BatchOperation) map[string][]string {
	graph := make(map[string][]string)
	
	for _, op := range operations {
		graph[op.ID] = op.Dependencies
	}
	
	return graph
}

// topologicalSort performs topological sorting for dependency resolution
func (pfrm *PartialFailureRecoveryManager) topologicalSort(graph map[string][]string) []int {
	// Simplified topological sort implementation
	// In a real implementation, you would use a proper topological sort algorithm
	
	visited := make(map[string]bool)
	var result []int
	operationIndex := make(map[string]int)
	
	// Create operation index mapping
	i := 0
	for opID := range graph {
		operationIndex[opID] = i
		i++
	}
	
	var visit func(string)
	visit = func(opID string) {
		if visited[opID] {
			return
		}
		visited[opID] = true
		
		// Visit dependencies first
		for _, dep := range graph[opID] {
			visit(dep)
		}
		
		if index, exists := operationIndex[opID]; exists {
			result = append(result, index)
		}
	}
	
	for opID := range graph {
		visit(opID)
	}
	
	return result
}

// InMemoryTransactionLog provides an in-memory implementation of TransactionLog
type InMemoryTransactionLog struct {
	operations       map[string]*BatchOperation
	batchOperations  map[string][]*BatchOperation
	idempotencyStore map[IdempotencyKey]*BatchJobResult
	mutex            sync.RWMutex
}

// NewInMemoryTransactionLog creates a new in-memory transaction log
func NewInMemoryTransactionLog() *InMemoryTransactionLog {
	return &InMemoryTransactionLog{
		operations:       make(map[string]*BatchOperation),
		batchOperations:  make(map[string][]*BatchOperation),
		idempotencyStore: make(map[IdempotencyKey]*BatchJobResult),
	}
}

// LogOperation logs a batch operation
func (imtl *InMemoryTransactionLog) LogOperation(ctx context.Context, operation *BatchOperation) error {
	imtl.mutex.Lock()
	defer imtl.mutex.Unlock()
	
	// Deep copy the operation to avoid mutations
	opCopy := *operation
	opCopy.Checkpoints = make([]Checkpoint, len(operation.Checkpoints))
	copy(opCopy.Checkpoints, operation.Checkpoints)
	
	imtl.operations[operation.ID] = &opCopy
	
	// Group by batch ID (extracted from operation data)
	if batchID, exists := operation.Data["batch_id"].(string); exists {
		imtl.batchOperations[batchID] = append(imtl.batchOperations[batchID], &opCopy)
	}
	
	return nil
}

// UpdateOperation updates an existing operation's status
func (imtl *InMemoryTransactionLog) UpdateOperation(ctx context.Context, operationID string, status BatchOperationStatus, result *BatchJobResult) error {
	imtl.mutex.Lock()
	defer imtl.mutex.Unlock()
	
	if op, exists := imtl.operations[operationID]; exists {
		op.Status = status
		op.EndTime = time.Now()
		
		if result != nil && result.Error != nil {
			op.Error = result.Error.Error()
		}
		
		return nil
	}
	
	return fmt.Errorf("operation %s not found", operationID)
}

// GetOperation retrieves an operation by ID
func (imtl *InMemoryTransactionLog) GetOperation(ctx context.Context, operationID string) (*BatchOperation, error) {
	imtl.mutex.RLock()
	defer imtl.mutex.RUnlock()
	
	if op, exists := imtl.operations[operationID]; exists {
		// Return a copy to avoid mutations
		opCopy := *op
		return &opCopy, nil
	}
	
	return nil, fmt.Errorf("operation %s not found", operationID)
}

// GetOperationsByBatch retrieves all operations for a batch
func (imtl *InMemoryTransactionLog) GetOperationsByBatch(ctx context.Context, batchID string) ([]*BatchOperation, error) {
	imtl.mutex.RLock()
	defer imtl.mutex.RUnlock()
	
	if ops, exists := imtl.batchOperations[batchID]; exists {
		// Return copies to avoid mutations
		result := make([]*BatchOperation, len(ops))
		for i, op := range ops {
			opCopy := *op
			result[i] = &opCopy
		}
		return result, nil
	}
	
	return []*BatchOperation{}, nil
}

// GetIncompleteOperations retrieves operations that need recovery
func (imtl *InMemoryTransactionLog) GetIncompleteOperations(ctx context.Context) ([]*BatchOperation, error) {
	imtl.mutex.RLock()
	defer imtl.mutex.RUnlock()
	
	var incompleteOps []*BatchOperation
	for _, op := range imtl.operations {
		if op.Status == BatchStatusRunning || op.Status == BatchStatusPartiallyCompleted {
			opCopy := *op
			incompleteOps = append(incompleteOps, &opCopy)
		}
	}
	
	return incompleteOps, nil
}

// MarkAsProcessed marks an idempotency key as processed
func (imtl *InMemoryTransactionLog) MarkAsProcessed(ctx context.Context, key IdempotencyKey, result *BatchJobResult) error {
	imtl.mutex.Lock()
	defer imtl.mutex.Unlock()
	
	imtl.idempotencyStore[key] = result
	return nil
}

// IsProcessed checks if an idempotency key has been processed
func (imtl *InMemoryTransactionLog) IsProcessed(ctx context.Context, key IdempotencyKey) (*BatchJobResult, bool, error) {
	imtl.mutex.RLock()
	defer imtl.mutex.RUnlock()
	
	if result, exists := imtl.idempotencyStore[key]; exists {
		return result, true, nil
	}
	
	return nil, false, nil
}

// DefaultProgressReporter provides a default implementation of ProgressReporter
type DefaultProgressReporter struct {
	transactionLog TransactionLog
	mutex          sync.RWMutex
}

// ReportProgress reports the current progress
func (dpr *DefaultProgressReporter) ReportProgress(operation string, progress float64, message string) {
	// In a real implementation, this would report to a monitoring system
	// For now, we just log the progress
}

// CreateCheckpoint creates a recoverable checkpoint
func (dpr *DefaultProgressReporter) CreateCheckpoint(operation string, checkpointID string, state map[string]interface{}, message string) error {
	dpr.mutex.Lock()
	defer dpr.mutex.Unlock()
	
	// In a real implementation, this would persist the checkpoint
	// For now, we just create the checkpoint structure
	checkpoint := Checkpoint{
		ID:        checkpointID,
		Timestamp: time.Now(),
		State:     state,
		Message:   message,
	}
	
	// Store checkpoint (simplified implementation)
	_ = checkpoint
	return nil
}

// GetLastCheckpoint retrieves the last checkpoint for resuming
func (dpr *DefaultProgressReporter) GetLastCheckpoint(operation string) (*Checkpoint, error) {
	dpr.mutex.RLock()
	defer dpr.mutex.RUnlock()
	
	// In a real implementation, this would retrieve from persistent storage
	// For now, return nil (no checkpoint found)
	return nil, nil
}

// Utility functions for creating operations

// NewBatchOperation creates a new batch operation
func NewBatchOperation(id, operationType string, data map[string]interface{}) *BatchOperation {
	return &BatchOperation{
		ID:             id,
		Type:           operationType,
		Data:           data,
		Status:         BatchStatusPending,
		Checkpoints:    make([]Checkpoint, 0),
		IdempotencyKey: IdempotencyKey(fmt.Sprintf("%s-%s-%d", operationType, id, time.Now().UnixNano())),
	}
}

// WithDependencies adds dependencies to a batch operation
func (bo *BatchOperation) WithDependencies(dependencies ...string) *BatchOperation {
	bo.Dependencies = append(bo.Dependencies, dependencies...)
	return bo
}

// WithIdempotencyKey sets a custom idempotency key
func (bo *BatchOperation) WithIdempotencyKey(key string) *BatchOperation {
	bo.IdempotencyKey = IdempotencyKey(key)
	return bo
}

// WithCompensationData adds compensation data to the operation
func (bo *BatchOperation) WithCompensationData(data map[string]interface{}) *BatchOperation {
	if bo.CompensationData == nil {
		bo.CompensationData = make(map[string]interface{})
	}
	for k, v := range data {
		bo.CompensationData[k] = v
	}
	return bo
}