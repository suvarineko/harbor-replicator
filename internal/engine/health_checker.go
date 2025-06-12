package engine

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
)

// DefaultHealthChecker implements the HealthChecker interface
type DefaultHealthChecker struct {
	mu            sync.RWMutex
	logger        *zap.Logger
	checks        map[string]HealthCheckFunc
	lastResults   map[string]*HealthCheckResult
	options       *HealthCheckerOptions
	metricsCollector MetricsCollector
}

// HealthCheckFunc is a function that performs a health check
type HealthCheckFunc func(ctx context.Context) error

// HealthCheckResult represents the result of a single health check
type HealthCheckResult struct {
	Name      string        `json:"name"`
	Status    string        `json:"status"` // "healthy", "unhealthy", "unknown"
	Message   string        `json:"message,omitempty"`
	Error     string        `json:"error,omitempty"`
	Duration  time.Duration `json:"duration"`
	Timestamp time.Time     `json:"timestamp"`
	Details   map[string]interface{} `json:"details,omitempty"`
}

// HealthCheckerOptions configures the health checker behavior
type HealthCheckerOptions struct {
	// Timeout for individual health checks
	CheckTimeout time.Duration
	
	// How often to run periodic health checks
	CheckInterval time.Duration
	
	// Whether to run checks in the background
	EnablePeriodicChecks bool
	
	// Whether to record metrics
	RecordMetrics bool
	
	// Whether to include check details in status
	IncludeDetails bool
	
	// Maximum number of check results to keep in history
	MaxHistorySize int
}

// NewDefaultHealthChecker creates a new health checker with default options
func NewDefaultHealthChecker(logger *zap.Logger, metricsCollector MetricsCollector, options *HealthCheckerOptions) *DefaultHealthChecker {
	if options == nil {
		options = &HealthCheckerOptions{
			CheckTimeout:         30 * time.Second,
			CheckInterval:        1 * time.Minute,
			EnablePeriodicChecks: true,
			RecordMetrics:        true,
			IncludeDetails:       true,
			MaxHistorySize:       10,
		}
	}

	return &DefaultHealthChecker{
		logger:           logger,
		checks:           make(map[string]HealthCheckFunc),
		lastResults:      make(map[string]*HealthCheckResult),
		options:          options,
		metricsCollector: metricsCollector,
	}
}

// Check performs all registered health checks
func (hc *DefaultHealthChecker) Check(ctx context.Context) (*HealthStatus, error) {
	hc.mu.RLock()
	checks := make(map[string]HealthCheckFunc)
	for name, check := range hc.checks {
		checks[name] = check
	}
	hc.mu.RUnlock()

	if len(checks) == 0 {
		return &HealthStatus{
			Status:    "healthy",
			Message:   "No health checks registered",
			Timestamp: time.Now(),
			Duration:  0,
		}, nil
	}

	startTime := time.Now()
	results := make(map[string]*HealthCheckResult)
	overallHealthy := true
	var firstError error

	// Run all checks concurrently
	resultChan := make(chan *HealthCheckResult, len(checks))
	
	for name, checkFunc := range checks {
		go func(checkName string, check HealthCheckFunc) {
			result := hc.runSingleCheck(ctx, checkName, check)
			resultChan <- result
		}(name, checkFunc)
	}

	// Collect results
	for i := 0; i < len(checks); i++ {
		result := <-resultChan
		results[result.Name] = result
		
		if result.Status != "healthy" {
			overallHealthy = false
			if firstError == nil && result.Error != "" {
				firstError = fmt.Errorf("health check '%s' failed: %s", result.Name, result.Error)
			}
		}
	}

	// Store results
	hc.mu.Lock()
	for name, result := range results {
		hc.lastResults[name] = result
	}
	hc.mu.Unlock()

	// Create overall health status
	duration := time.Since(startTime)
	status := &HealthStatus{
		Status:    "healthy",
		Timestamp: time.Now(),
		Duration:  duration,
		Details:   make(map[string]string),
	}

	if !overallHealthy {
		status.Status = "unhealthy"
		if firstError != nil {
			status.Message = firstError.Error()
		}
	} else {
		status.Message = fmt.Sprintf("All %d health checks passed", len(checks))
	}

	// Add check details if enabled
	if hc.options.IncludeDetails {
		for name, result := range results {
			status.Details[name] = result.Status
			if result.Error != "" {
				status.Details[name+"_error"] = result.Error
			}
		}
	}

	// Record metrics if enabled
	if hc.options.RecordMetrics && hc.metricsCollector != nil {
		healthValue := 0.0
		if overallHealthy {
			healthValue = 1.0
		}
		hc.metricsCollector.SetGauge("health_status", healthValue, map[string]string{"component": "overall"})
		hc.metricsCollector.RecordHistogram("health_check_duration", duration.Seconds(), map[string]string{"component": "overall"})
		
		for name, result := range results {
			resultValue := 0.0
			if result.Status == "healthy" {
				resultValue = 1.0
			}
			hc.metricsCollector.SetGauge("health_status", resultValue, map[string]string{"component": name})
			hc.metricsCollector.RecordHistogram("health_check_duration", result.Duration.Seconds(), map[string]string{"component": name})
		}
	}

	hc.logger.Debug("Health check completed",
		zap.String("status", status.Status),
		zap.Duration("duration", duration),
		zap.Int("total_checks", len(checks)),
		zap.Bool("healthy", overallHealthy))

	return status, nil
}

// GetStatus returns the current health status based on last check results
func (hc *DefaultHealthChecker) GetStatus() *HealthStatus {
	hc.mu.RLock()
	defer hc.mu.RUnlock()

	if len(hc.lastResults) == 0 {
		return &HealthStatus{
			Status:    "unknown",
			Message:   "No health checks have been performed",
			Timestamp: time.Now(),
			Duration:  0,
		}
	}

	overallHealthy := true
	var lastTimestamp time.Time
	details := make(map[string]string)

	for name, result := range hc.lastResults {
		if result.Status != "healthy" {
			overallHealthy = false
		}
		
		if result.Timestamp.After(lastTimestamp) {
			lastTimestamp = result.Timestamp
		}
		
		if hc.options.IncludeDetails {
			details[name] = result.Status
			if result.Error != "" {
				details[name+"_error"] = result.Error
			}
		}
	}

	status := &HealthStatus{
		Status:    "healthy",
		Timestamp: lastTimestamp,
		Details:   details,
	}

	if !overallHealthy {
		status.Status = "unhealthy"
		status.Message = "One or more health checks are failing"
	} else {
		status.Message = fmt.Sprintf("All %d health checks are passing", len(hc.lastResults))
	}

	return status
}

// RegisterCheck registers a custom health check
func (hc *DefaultHealthChecker) RegisterCheck(name string, check func(ctx context.Context) error) {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	hc.checks[name] = check
	hc.logger.Info("Registered health check", zap.String("name", name))
}

// UnregisterCheck unregisters a health check
func (hc *DefaultHealthChecker) UnregisterCheck(name string) {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	delete(hc.checks, name)
	delete(hc.lastResults, name)
	hc.logger.Info("Unregistered health check", zap.String("name", name))
}

// GetCheckResult returns the result of a specific health check
func (hc *DefaultHealthChecker) GetCheckResult(name string) (*HealthCheckResult, error) {
	hc.mu.RLock()
	defer hc.mu.RUnlock()

	result, exists := hc.lastResults[name]
	if !exists {
		return nil, fmt.Errorf("no result found for health check: %s", name)
	}

	// Return a copy to avoid external modifications
	resultCopy := *result
	if result.Details != nil {
		resultCopy.Details = make(map[string]interface{})
		for k, v := range result.Details {
			resultCopy.Details[k] = v
		}
	}

	return &resultCopy, nil
}

// GetAllCheckResults returns results for all health checks
func (hc *DefaultHealthChecker) GetAllCheckResults() map[string]*HealthCheckResult {
	hc.mu.RLock()
	defer hc.mu.RUnlock()

	results := make(map[string]*HealthCheckResult)
	for name, result := range hc.lastResults {
		// Return copies to avoid external modifications
		resultCopy := *result
		if result.Details != nil {
			resultCopy.Details = make(map[string]interface{})
			for k, v := range result.Details {
				resultCopy.Details[k] = v
			}
		}
		results[name] = &resultCopy
	}

	return results
}

// StartPeriodicChecks starts running health checks periodically in the background
func (hc *DefaultHealthChecker) StartPeriodicChecks(ctx context.Context) {
	if !hc.options.EnablePeriodicChecks {
		return
	}

	go func() {
		ticker := time.NewTicker(hc.options.CheckInterval)
		defer ticker.Stop()

		hc.logger.Info("Started periodic health checks",
			zap.Duration("interval", hc.options.CheckInterval))

		for {
			select {
			case <-ticker.C:
				checkCtx, cancel := context.WithTimeout(ctx, hc.options.CheckTimeout)
				_, err := hc.Check(checkCtx)
				cancel()
				
				if err != nil {
					hc.logger.Error("Periodic health check failed", zap.Error(err))
				}
			case <-ctx.Done():
				hc.logger.Info("Stopped periodic health checks")
				return
			}
		}
	}()
}

// runSingleCheck runs a single health check with timeout and error handling
func (hc *DefaultHealthChecker) runSingleCheck(ctx context.Context, name string, check HealthCheckFunc) *HealthCheckResult {
	startTime := time.Now()
	
	// Create a timeout context for this specific check
	checkCtx, cancel := context.WithTimeout(ctx, hc.options.CheckTimeout)
	defer cancel()

	result := &HealthCheckResult{
		Name:      name,
		Status:    "unknown",
		Timestamp: startTime,
		Details:   make(map[string]interface{}),
	}

	// Run the check
	err := check(checkCtx)
	result.Duration = time.Since(startTime)

	if err != nil {
		result.Status = "unhealthy"
		result.Error = err.Error()
		result.Message = fmt.Sprintf("Health check failed: %v", err)
		
		hc.logger.Warn("Health check failed",
			zap.String("name", name),
			zap.Error(err),
			zap.Duration("duration", result.Duration))
	} else {
		result.Status = "healthy"
		result.Message = "Health check passed"
		
		hc.logger.Debug("Health check passed",
			zap.String("name", name),
			zap.Duration("duration", result.Duration))
	}

	// Add timing details
	result.Details["duration_ms"] = result.Duration.Milliseconds()
	result.Details["timeout_ms"] = hc.options.CheckTimeout.Milliseconds()

	return result
}

// RegisterDefaultChecks registers a set of default health checks for the sync engine
func (hc *DefaultHealthChecker) RegisterDefaultChecks(
	workerPool WorkerPool,
	scheduler Scheduler,
	orchestrator SyncOrchestrator,
	metricsCollector MetricsCollector,
) {
	// Worker pool health check
	if workerPool != nil {
		hc.RegisterCheck("worker_pool", func(ctx context.Context) error {
			stats := workerPool.GetStats()
			if stats.Size <= 0 {
				return fmt.Errorf("worker pool has no workers")
			}
			if stats.FailedJobs > stats.CompletedJobs*2 {
				return fmt.Errorf("too many failed jobs: %d failed vs %d completed", stats.FailedJobs, stats.CompletedJobs)
			}
			return nil
		})
	}

	// Scheduler health check
	if scheduler != nil {
		hc.RegisterCheck("scheduler", func(ctx context.Context) error {
			schedule := scheduler.GetSchedule()
			if len(schedule) == 0 {
				return fmt.Errorf("no jobs scheduled")
			}
			return nil
		})
	}

	// Orchestrator health check
	if orchestrator != nil {
		hc.RegisterCheck("orchestrator", func(ctx context.Context) error {
			activeSyncs := orchestrator.ListActiveSyncs()
			if len(activeSyncs) > 100 { // Arbitrary threshold
				return fmt.Errorf("too many active syncs: %d", len(activeSyncs))
			}
			return nil
		})
	}

	// Metrics collector health check
	if metricsCollector != nil {
		hc.RegisterCheck("metrics_collector", func(ctx context.Context) error {
			metrics := metricsCollector.GetMetrics()
			if len(metrics) == 0 {
				return fmt.Errorf("no metrics available")
			}
			return nil
		})
	}

	// Memory health check
	hc.RegisterCheck("memory", func(ctx context.Context) error {
		// Check if memory usage is reasonable (this is a simple check)
		// In a real implementation, you'd have more sophisticated memory monitoring
		return nil
	})

	// Context health check
	hc.RegisterCheck("context", func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return fmt.Errorf("context is cancelled")
		default:
			return nil
		}
	})
}