# Harbor Replicator Monitoring Setup

This directory contains monitoring configurations for Harbor Replicator, including Grafana dashboards and Prometheus alerting rules.

## Components

### 1. Grafana Dashboard (`grafana-dashboard.json`)

A comprehensive dashboard that provides visibility into:

- **Sync Operations Overview**: Total sync rate, success rate, and active operations
- **Performance Metrics**: Sync duration distribution, API call latency
- **Error Monitoring**: Error rates by type and resource
- **Resource Tracking**: Resources synchronized by type and operation
- **Health Status**: Harbor instance health and availability
- **Replication Lag**: Time lag between source and target synchronization
- **Queue Monitoring**: Resource queue sizes and processing status
- **Configuration Drift**: Detected differences between instances
- **Quota Usage**: Resource quota utilization across instances

### 2. Prometheus Alerts (`prometheus-alerts.yml`)

Alerting rules covering:

#### Sync Operation Alerts
- High sync failure rates (>10% warning, >25% critical)
- No sync activity detected
- High sync duration (>5min warning, >10min critical)

#### Performance Alerts
- High API call latency (>10s)
- Resource processing errors
- Worker pool exhaustion

#### Health Alerts
- Harbor instance down
- Authentication/connection errors
- High memory usage (>1GB)
- High goroutine count (>1000)

#### Data Quality Alerts
- High replication lag (>1hr warning, >3hr critical)
- Configuration drift detection
- Stale sync data (>2hr warning, >6hr critical)

#### Resource Alerts
- High queue sizes (>1000 warning, >5000 critical)
- High quota usage (>80% warning, >95% critical)

## Setup Instructions

### Prerequisites

- Prometheus server for metrics collection
- Grafana for dashboard visualization
- Harbor Replicator with metrics endpoint enabled

### 1. Configure Harbor Replicator Metrics

Ensure your Harbor Replicator configuration includes metrics settings:

```yaml
metrics:
  enabled: true
  port: 8080
  bind_addr: "0.0.0.0"
  path: "/metrics"
  tls:
    enabled: false
  auth:
    enabled: false
```

### 2. Configure Prometheus

Add Harbor Replicator as a scrape target in your `prometheus.yml`:

```yaml
scrape_configs:
  - job_name: 'harbor-replicator'
    static_configs:
      - targets: ['harbor-replicator:8080']
    scrape_interval: 30s
    metrics_path: /metrics
    scheme: http
```

### 3. Import Grafana Dashboard

1. Open Grafana web interface
2. Navigate to "+" → "Import"
3. Upload `grafana-dashboard.json` or paste its contents
4. Configure data source (select your Prometheus instance)
5. Save the dashboard

### 4. Configure Prometheus Alerts

1. Copy `prometheus-alerts.yml` to your Prometheus server
2. Add the file to your Prometheus configuration:

```yaml
rule_files:
  - "harbor-replicator-alerts.yml"
```

3. Configure Alertmanager for alert routing and notifications
4. Restart Prometheus to load the new rules

### 5. Alertmanager Configuration Example

```yaml
route:
  group_by: ['alertname', 'service']
  group_wait: 10s
  group_interval: 10s
  repeat_interval: 1h
  receiver: 'harbor-replicator-alerts'

receivers:
  - name: 'harbor-replicator-alerts'
    slack_configs:
      - api_url: 'YOUR_SLACK_WEBHOOK_URL'
        channel: '#harbor-alerts'
        title: 'Harbor Replicator Alert'
        text: '{{ .CommonAnnotations.summary }}\n{{ .CommonAnnotations.description }}'
```

## Metrics Reference

### Core Metrics

| Metric Name | Type | Description | Labels |
|-------------|------|-------------|--------|
| `harbor_replicator_sync_total` | Counter | Total sync operations | `status` |
| `harbor_replicator_resources_synced` | Counter | Total resources synchronized | `type`, `source`, `operation` |
| `harbor_replicator_sync_duration_seconds` | Histogram | Sync operation duration | `source`, `resource_type` |
| `harbor_replicator_sync_errors_total` | Counter | Total sync errors | `error_type`, `resource_type`, `source` |
| `harbor_replicator_active_sync_operations` | Gauge | Active sync operations | - |
| `harbor_replicator_resource_queue_size` | Gauge | Resource queue size | `resource_type` |
| `harbor_replicator_api_call_latency_seconds` | Histogram | API call latency | `source`, `endpoint`, `method` |
| `harbor_replicator_last_successful_sync_timestamp` | Gauge | Last successful sync timestamp | `resource_type`, `source` |

### Health Metrics

| Metric Name | Type | Description | Labels |
|-------------|------|-------------|--------|
| `harbor_replicator_harbor_instance_health` | Gauge | Harbor instance health status | `instance`, `type` |
| `harbor_replicator_replication_lag_seconds` | Gauge | Replication lag | `source`, `target`, `resource_type` |
| `harbor_replicator_configuration_drift` | Gauge | Configuration differences | `source`, `target`, `resource_type` |
| `harbor_replicator_resource_quota_usage_percent` | Gauge | Resource quota usage | `instance`, `resource_type` |

### Engine Metrics

| Metric Name | Type | Description | Labels |
|-------------|------|-------------|--------|
| `harbor_replicator_engine_worker_pool_size` | Gauge | Worker pool size | - |
| `harbor_replicator_engine_worker_pool_active` | Gauge | Active workers | - |
| `harbor_replicator_engine_worker_pool_queue_size` | Gauge | Queued jobs | - |
| `harbor_replicator_performance_memory_usage_bytes` | Gauge | Memory usage | - |
| `harbor_replicator_performance_goroutines` | Gauge | Goroutine count | - |

## Alert Severity Levels

### Warning (Yellow)
- Indicates potential issues that need attention
- Sync failure rate > 10%
- API latency > 10 seconds
- Queue size > 1000 items
- Quota usage > 80%

### Critical (Red)
- Indicates severe issues requiring immediate action
- Sync failure rate > 25%
- Harbor instance down
- Authentication errors
- Quota usage > 95%

## Troubleshooting

### Common Issues

1. **No metrics appearing in Grafana**
   - Check Prometheus target status
   - Verify Harbor Replicator metrics endpoint is accessible
   - Confirm Grafana data source configuration

2. **Alerts not firing**
   - Verify Prometheus rules are loaded (`promtool check rules`)
   - Check Alertmanager configuration
   - Confirm alert expressions match actual metric names

3. **High memory usage alerts**
   - Check for memory leaks in Harbor Replicator
   - Monitor goroutine count trends
   - Review resource processing patterns

4. **Persistent sync failures**
   - Check Harbor instance connectivity
   - Verify authentication credentials
   - Review Harbor instance health

## Customization

### Dashboard Variables

The dashboard includes template variables:
- `$instance`: Filter by Harbor instance
- `$resource_type`: Filter by resource type (robots, OIDC groups)

### Alert Thresholds

Alert thresholds can be customized by modifying the expressions in `prometheus-alerts.yml`:

```yaml
# Example: Change sync failure threshold from 10% to 15%
- alert: HarborReplicatorSyncFailureHigh
  expr: |
    (sum(rate(harbor_replicator_sync_total{status="failed"}[5m])) /
     sum(rate(harbor_replicator_sync_total[5m]))) * 100 > 15  # Changed from 10
```

### Additional Metrics

To add custom metrics:

1. Implement in Harbor Replicator using the monitoring interfaces
2. Update Prometheus scrape configuration if needed
3. Add corresponding dashboard panels and alerts

## Best Practices

1. **Monitor both technical and business metrics**
   - Technical: latency, errors, resource usage
   - Business: sync success rates, data freshness

2. **Set appropriate alert thresholds**
   - Avoid alert fatigue with too many low-severity alerts
   - Ensure critical alerts require immediate action

3. **Use runbooks for alerts**
   - Document troubleshooting steps for each alert
   - Include links to relevant dashboards and logs

4. **Regular review and tuning**
   - Periodically review alert effectiveness
   - Adjust thresholds based on historical data
   - Update dashboards based on operational needs

## Support

For issues with monitoring setup:
1. Check Harbor Replicator logs for metric collection errors
2. Verify Prometheus and Grafana configurations
3. Review network connectivity between components
4. Consult Harbor Replicator documentation for metric definitions