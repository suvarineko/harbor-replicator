# System Patterns *Optional*

This file documents recurring patterns...

*
## Implementation Patterns

[2025-06-11 18:59:48] - **Synchronization Engine Implementation Patterns**

### 1. Interface Segregation Pattern
**Pattern**: Separate interfaces for each major concern (WorkerPool, Scheduler, Orchestrator, etc.)
**Benefits**: Clean dependencies, easier testing, flexible implementations
**Usage**: Each component depends only on interfaces it needs, enabling mock implementations for testing

### 2. Worker Pool Pattern
**Pattern**: Configurable worker pool with job queue and result collection
**Implementation**: 
- Bounded job queue with configurable size
- Worker goroutines with lifecycle management
- Rate limiting with golang.org/x/time/rate
- Graceful shutdown with context cancellation
**Benefits**: Controlled concurrency, back-pressure handling, resource management

### 3. Circuit Breaker Pattern
**Pattern**: Per-resource circuit breakers to handle remote service failures
**States**: Closed (normal), Open (failing), Half-Open (testing recovery)
**Implementation**: Configurable failure thresholds, timeout periods, and success criteria
**Benefits**: Prevents cascading failures, automatic recovery, fail-fast behavior

### 4. Observer Pattern for Progress Tracking
**Pattern**: Channel-based subscriptions for real-time progress updates
**Implementation**: Progress channels per sync operation, subscription management, cleanup on completion
**Benefits**: Real-time monitoring, decoupled progress reporting, efficient resource usage

### 5. Strategy Pattern for Conflict Resolution
**Pattern**: Pluggable conflict resolution strategies (source wins, target wins, merge, etc.)
**Implementation**: Strategy interface with multiple implementations, configurable per resource type
**Benefits**: Flexible conflict handling, policy-driven resolution, extensible strategies

### 6. Lifecycle Management Pattern
**Pattern**: Consistent Start/Stop/Restart pattern across all components
**Implementation**: 
- Context-based cancellation
- WaitGroup for goroutine coordination
- Atomic state management
- Graceful shutdown timeouts
**Benefits**: Predictable lifecycle, clean resource cleanup, coordinated shutdown

### 7. Configuration Pattern
**Pattern**: Hierarchical configuration with defaults and validation
**Structure**: Engine config → Component configs → Feature configs
**Features**: Hot-reload support, validation functions, sensible defaults
**Benefits**: Flexible configuration, operational safety, easy deployment

[2025-06-13 14:24:11] - ## Error Handling and Resilience Patterns Implementation

### Established Patterns for Harbor Replicator

**1. Comprehensive Error Classification Pattern**
```go
// Standard error classification approach
type ErrorCategory int
const (
    ErrorCategoryTransient ErrorCategory = iota
    ErrorCategoryPermanent
    // ... additional categories
)

type ClassifiedError struct {
    Category ErrorCategory
    Eligibility RetryEligibility
    // ... metadata fields
}
```

**2. Adaptive Configuration Pattern**
- Base configuration with operation-specific overrides
- Per-operation retry policies stored in map[string]*OperationRetryConfig
- Hot-reloading capability with configuration watchers
- Environment-aware defaults (development vs production)

**3. Circuit Breaker State Management Pattern**
```go
// Three-state pattern with atomic operations
type CircuitBreakerState int32
const (
    StateClosed CircuitBreakerState = iota
    StateOpen
    StateHalfOpen
)
// Use atomic.LoadInt32/SwapInt32 for thread-safe state transitions
```

**4. Sliding Window Metrics Pattern**
- Count-based vs time-based window implementations
- Interface-driven design for different window strategies
- Automatic cleanup of expired data
- Thread-safe bucket rotation for time-based windows

**5. Observability Integration Pattern**
```go
// Correlation context for request tracing
type CorrelationContext struct {
    CorrelationID string
    OperationName string
    StartTime     time.Time
    Metadata      map[string]string
}
```

**6. Middleware Integration Pattern**
- HTTP RoundTripper wrapper for transparent retry
- gRPC interceptor chain for unary and streaming calls
- Priority-based request queuing under load
- Request hedging for latency-sensitive operations

**7. Transaction Recovery Pattern**
```go
// Idempotency and compensation pattern
type BatchOperation struct {
    IdempotencyKey     IdempotencyKey
    CompensationData   map[string]interface{}
    Checkpoints        []Checkpoint
    Dependencies       []string
}
```

**8. Adaptive Algorithm Pattern**
```go
// ML-inspired adaptation with historical data
type AdaptationState struct {
    CurrentMultiplier float64
    SuccessStreak     int
    FailureStreak     int
    LastAdaptation    time.Time
}
```

**9. Resource Management Pattern**
- Proper cleanup with Stop() methods and WaitGroups
- Context cancellation propagation
- Graceful shutdown sequences
- Resource leak prevention

**10. Configuration Builder Pattern**
```go
// Predefined configuration builders for common scenarios
func NewWebServiceRetryConfig() *RetryConfig
func NewDatabaseRetryConfig() *RetryConfig 
func NewCriticalOperationRetryConfig() *RetryConfig
```

### Integration Guidelines
- All error handling components implement proper interfaces
- Configuration hot-reloading supported throughout
- Metrics collection enabled by default with opt-out capability
- Thread-safety guaranteed across all components
- OpenTelemetry integration for distributed tracing
