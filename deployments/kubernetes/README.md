# Harbor Replicator Kubernetes Deployment

This directory contains Kubernetes manifests for deploying Harbor Replicator with comprehensive health checks and monitoring.

## Prerequisites

- Kubernetes cluster (v1.19+)
- kubectl configured to access your cluster
- Kustomize (optional, but recommended)
- Storage class for persistent volumes

## Health Check Configuration

The deployment includes comprehensive health checks compatible with Kubernetes:

### Probe Configuration

- **Startup Probe**: `/ready` endpoint, 30 attempts with 5s intervals
- **Liveness Probe**: `/live` endpoint, every 10s with 5s timeout
- **Readiness Probe**: `/ready` endpoint, every 5s with 3s timeout

### Health Endpoints

- `/health` - Overall health status with component details
- `/ready` - Readiness check for load balancer registration
- `/live` - Basic liveness check
- `/metrics/health` - Detailed health metrics (if enabled)

### Recommended Probe Settings

Based on application characteristics:

```yaml
startupProbe:
  initialDelaySeconds: 10  # Allow time for initialization
  periodSeconds: 5         # Check every 5 seconds
  timeoutSeconds: 3        # 3 second timeout
  failureThreshold: 30     # 150 seconds total startup time
  successThreshold: 1      # One success marks as started

livenessProbe:
  initialDelaySeconds: 30  # Start after startup probe succeeds
  periodSeconds: 10        # Check every 10 seconds
  timeoutSeconds: 5        # 5 second timeout
  failureThreshold: 3      # 3 failures trigger restart
  successThreshold: 1      # One success marks as healthy

readinessProbe:
  initialDelaySeconds: 5   # Check soon after startup
  periodSeconds: 5         # Check every 5 seconds
  timeoutSeconds: 3        # 3 second timeout
  failureThreshold: 3      # 3 failures remove from load balancer
  successThreshold: 1      # One success adds to load balancer
```

## Quick Deployment

### Option 1: Using kubectl directly

```bash
# Create namespace
kubectl apply -f namespace.yaml

# Create secrets (update with your actual credentials first)
kubectl apply -f secrets.yaml

# Create config map
kubectl apply -f configmap.yaml

# Deploy application
kubectl apply -f deployment.yaml

# Create services
kubectl apply -f service.yaml
```

### Option 2: Using Kustomize (Recommended)

```bash
# Deploy everything with kustomize
kubectl apply -k .

# Or with specific overlay
kubectl apply -k overlays/production
```

## Configuration

### 1. Update Secrets

Edit `secrets.yaml` and update the base64 encoded passwords:

```bash
# Encode your passwords
echo -n "your_harbor_password" | base64

# Update the secrets.yaml file with the encoded values
```

### 2. Update Harbor Configuration

Edit `configmap.yaml` to configure your Harbor instances:

```yaml
harbor:
  source:
    url: "https://your-source-harbor.com"
    username: "admin"
    password: "your_password"  # Will be replaced by secret
  targets:
    - url: "https://your-target-harbor.com"
      username: "admin"
      password: "your_password"  # Will be replaced by secret
```

### 3. Adjust Resource Limits

Modify `deployment.yaml` based on your requirements:

```yaml
resources:
  requests:
    memory: "256Mi"  # Minimum required
    cpu: "250m"      # Minimum required
  limits:
    memory: "512Mi"  # Maximum allowed
    cpu: "500m"      # Maximum allowed
```

## Health Check Verification

### Check Health Status

```bash
# Port forward to health endpoint
kubectl port-forward svc/harbor-replicator 8080:8080

# Check overall health
curl http://localhost:8080/health

# Check readiness
curl http://localhost:8080/ready

# Check liveness
curl http://localhost:8080/live

# Detailed metrics (if enabled)
curl http://localhost:8080/metrics/health
```

### Monitor Probe Status

```bash
# Check probe status
kubectl describe pod -l app=harbor-replicator

# View probe failures in events
kubectl get events --field-selector reason=Unhealthy

# Monitor real-time status
kubectl get pods -l app=harbor-replicator -w
```

## Troubleshooting

### Common Issues

1. **Startup Probe Failing**
   - Check if the grace period is sufficient
   - Verify Harbor connectivity
   - Check resource constraints

2. **Readiness Probe Failing**
   - Verify external Harbor instances are accessible
   - Check state manager initialization
   - Review worker pool configuration

3. **Liveness Probe Failing**
   - Check for application deadlocks
   - Review memory/CPU limits
   - Check for panic conditions

### Debug Commands

```bash
# Check pod logs
kubectl logs -l app=harbor-replicator -f

# Exec into pod for debugging
kubectl exec -it deployment/harbor-replicator -- /bin/sh

# Check endpoints
kubectl get endpoints harbor-replicator

# Check service status
kubectl get svc harbor-replicator
```

## Monitoring Integration

The deployment includes Prometheus annotations for monitoring:

```yaml
annotations:
  prometheus.io/scrape: "true"
  prometheus.io/port: "9090"
  prometheus.io/path: "/metrics"
```

### ServiceMonitor for Prometheus Operator

```yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: harbor-replicator
spec:
  selector:
    matchLabels:
      app: harbor-replicator
  endpoints:
  - port: metrics
    path: /metrics
    interval: 30s
```

## Security Considerations

The deployment follows security best practices:

- Non-root user (UID 1000)
- Read-only root filesystem
- Dropped capabilities
- Security context constraints
- Separate service account
- RBAC permissions

## Scaling Considerations

Currently configured for single replica deployment. For scaling:

1. **State Management**: Ensure state manager supports concurrent access
2. **Leader Election**: Implement leader election for sync operations
3. **Resource Sharing**: Consider shared storage for state persistence

## Production Checklist

- [ ] Update all default passwords and secrets
- [ ] Configure appropriate resource limits
- [ ] Set up persistent storage with appropriate storage class
- [ ] Configure monitoring and alerting
- [ ] Set up backup strategy for state data
- [ ] Review and adjust probe timeouts for your environment
- [ ] Configure ingress if external access is needed
- [ ] Set up log aggregation
- [ ] Configure network policies if required
- [ ] Test failover and recovery procedures