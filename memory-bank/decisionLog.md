# Decision Log

This file records architectural and implementation decisions...

*
[2025-06-09 15:26:29] - Key architectural decisions for Harbor Registry Replicator:
1. Use resource names as primary identifiers instead of Harbor's unique IDs to ensure consistency across instances
2. Implement unidirectional synchronization (remote to local) for simplicity and data integrity
3. Support both Docker and Kubernetes deployment models for flexibility
4. Use YAML configuration with runtime reload capability for operational efficiency
5. Default 10-minute sync interval balancing timeliness with resource usage
6. Structured logging and Prometheus metrics for comprehensive observability

[2025-06-10 20:12:46] - ## OIDC Group Synchronization Architecture Decisions

### Decision: Extend Existing Types vs Create New Package
**Date**: Current Session  
**Context**: Tasks 6.1-6.2 found existing comprehensive OIDC implementations  
**Decision**: Leverage existing `pkg/client/types.go` and `pkg/client/oidc.go` implementations  
**Rationale**: 
- High-quality existing code with complete OIDC group API coverage
- Consistent with project architecture patterns
- Avoids duplication and maintains single source of truth
- Includes advanced features like pagination and validation

### Decision: Group State Management Integration Strategy
**Date**: Current Session  
**Context**: Need to track group synchronization state  
**Decision**: Extend existing `SyncState` struct with `GroupStates` field and add methods to `StateManager`  
**Rationale**:
- Maintains unified state management approach
- Leverages existing state persistence and recovery mechanisms
- Allows for cross-resource state queries and reporting
- Consistent with existing resource tracking patterns

### Decision: Group Matching Strategy Design
**Date**: Current Session  
**Context**: Need flexible group identity resolution across Harbor instances  
**Decision**: Implement multiple configurable matching strategies (name, LDAP DN, OIDC claim, hybrid)  
**Rationale**:
- Different organizations use different group identity schemes
- Hybrid approach provides fallback mechanisms
- Configurable strategy allows deployment-specific optimization
- Supports both strict and fuzzy matching based on requirements

### Decision: Conflict Resolution Framework
**Date**: Current Session  
**Context**: Groups may exist with same identity but different properties  
**Decision**: Implement comprehensive conflict detection with configurable resolution strategies  
**Rationale**:
- Different environments may require different conflict handling
- Manual resolution option preserves human oversight when needed
- Merge strategies handle complex permission scenarios
- Source/target wins strategies provide simple resolution for most cases

### Decision: Checksum-Based Change Detection
**Date**: Current Session  
**Context**: Need efficient way to detect group changes without full comparison  
**Decision**: Implement SHA256 checksums for group content comparison  
**Rationale**:
- Efficient change detection for large group sets
- Avoids unnecessary API calls for unchanged groups
- Provides reliable change detection across complex group structures
- Supports incremental synchronization strategies

### Decision: State Integration with TaskMaster
**Date**: Current Session  
**Context**: Need to track completed work in project management system  
**Decision**: Update TaskMaster task status for all completed subtasks  
**Rationale**:
- Maintains accurate project tracking
- Enables proper dependency management for remaining tasks
- Provides visibility into development progress
- Ensures consistent task management across development sessions

## Architecture Decisions

[2025-06-11 18:59:32] - **Synchronization Engine Architecture Decisions**

### 1. Interface-Based Design Pattern
**Decision**: Implemented comprehensive interface system for all major components
**Rationale**: Enables modularity, testability, and future extensibility. Allows different implementations without changing core logic.
**Components**: ResourceSynchronizer, WorkerPool, Scheduler, SyncOrchestrator, ProgressTracker, MetricsCollector, HealthChecker, CircuitBreaker

### 2. Worker Pool with Rate Limiting
**Decision**: Built configurable worker pool with rate limiting and circuit breaker integration
**Rationale**: Prevents overwhelming Harbor instances, provides back-pressure, and enables graceful degradation
**Implementation**: Uses golang.org/x/time/rate for rate limiting, channels for job queuing, context for cancellation

### 3. Scheduler with Jitter and Immediate Triggers
**Decision**: Implemented scheduler with configurable jitter and support for immediate sync triggers
**Rationale**: Prevents thundering herd problems, allows flexible scheduling, supports both periodic and on-demand syncs
**Features**: Random jitter, concurrent sync limits, skip-if-running option

### 4. Orchestrator with Parallel Resource Sync
**Decision**: Designed orchestrator to handle multiple resource types in parallel with individual circuit breakers
**Rationale**: Maximizes throughput, isolates failures between resource types, maintains sync independence
**Pattern**: Per-resource-type circuit breakers, parallel execution with sync.WaitGroup, comprehensive error collection

### 5. Comprehensive Error Handling Strategy
**Decision**: Multi-layered error handling with circuit breakers, retries, and detailed error tracking
**Rationale**: Ensures resilience against Harbor API failures, provides debugging information, enables automated recovery
**Layers**: Circuit breakers per remote, retry logic in worker pool, error collection and reporting

### 6. Configuration Hot-Reload Design
**Decision**: Built configuration system to support hot-reload without restart
**Rationale**: Enables operational flexibility, reduces downtime, allows dynamic tuning of sync behavior
**Implementation**: Separate config validation, dynamic interval updates, component reconfiguration

### 7. Progress Tracking Architecture
**Decision**: Designed progress tracking system with subscription model and persistence
**Rationale**: Enables real-time monitoring, supports long-running sync operations, provides user feedback
**Features**: Channel-based subscriptions, ETA calculations, progress persistence for recovery
