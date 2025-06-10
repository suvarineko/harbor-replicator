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
