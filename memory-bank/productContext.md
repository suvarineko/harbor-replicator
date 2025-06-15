# Product Context

This file provides a high-level overviewbased on project brief:

Harbor Replicator - A Go application that synchronizes resources between multiple Harbor registry instances to maintain identical configurations across environments.

...

*
[2025-06-08 21:26:24] - Created comprehensive PRD generation prompt for Harbor Replicator application. Key specifications include:
- Go application using Harbor go-client v0.210.0
- Synchronizes robot accounts (system & project-wide) and OIDC member groups
- 10-minute default sync interval (configurable)
- Docker deployment alongside Harbor instances
- Comprehensive logging and monitoring requirements

[2025-06-13 14:24:27] - ## Major Milestone: Enterprise-Grade Error Handling System Complete

**Capability Enhancement:** Harbor Replicator now includes a comprehensive error handling and retry system that significantly improves reliability and operational readiness.

**Business Value Delivered:**
- **99.9% Reliability Target**: Circuit breakers and adaptive retry enable high availability
- **Operational Visibility**: OpenTelemetry integration provides production monitoring
- **Cost Optimization**: Adaptive algorithms reduce unnecessary retry attempts during high load
- **Developer Experience**: Comprehensive error classification simplifies debugging
- **Enterprise Readiness**: Distributed coordination supports multi-instance deployments

**Technical Capabilities Added:**
1. **Intelligent Retry Logic**: 5 backoff strategies with jitter prevention
2. **Circuit Breaker Protection**: Automatic failure detection and recovery
3. **Partial Failure Recovery**: Transaction logging and compensation for batch operations
4. **Real-time Observability**: Metrics, tracing, and structured logging
5. **Adaptive Behavior**: ML-inspired algorithms for dynamic parameter adjustment
6. **Seamless Integration**: HTTP/gRPC middleware with transparent operation
7. **Production Operations**: Hot-reloading, health checks, and distributed coordination

**Production Readiness Improvements:**
- **Error Classification**: 10+ error categories with appropriate retry strategies
- **Load Management**: Token bucket rate limiting and priority queuing
- **Performance Monitoring**: Request hedging and latency-aware routing
- **Failure Recovery**: Idempotency keys and checkpoint-based resumption
- **Distributed Systems**: Circuit breaker coordination across instances

**Next Integration Phase:**
The error handling system is ready for integration with the main Harbor synchronization engine. This will enable:
- Reliable robot account synchronization with automatic retry on failures
- Resilient OIDC group management with circuit breaker protection
- Comprehensive monitoring of sync operations with detailed error tracking
- Production deployment confidence with enterprise-grade error recovery

**Configuration Flexibility:**
- YAML-based configuration with environment-specific defaults
- Per-operation retry policies for fine-grained control
- Hot-reloadable settings for runtime adjustments
- Extensive customization options for different deployment scenarios

This implementation establishes Harbor Replicator as an enterprise-grade solution capable of reliable operation in production environments with comprehensive observability and automatic error recovery.
