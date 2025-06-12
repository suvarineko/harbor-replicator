package monitoring

import (
	"context"
	"fmt"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"harbor-replicator/internal/engine"
	"harbor-replicator/pkg/config"
	"harbor-replicator/pkg/state"
)

// SimpleHealthServer provides simplified health and readiness endpoints
type SimpleHealthServer struct {
	engine       engine.SyncEngine
	stateManager *state.StateManager
	config       *config.HealthConfig
	logger       *zap.Logger
	startTime    time.Time
	version      string

	// Shutdown state
	shutdownMutex sync.RWMutex
	isShuttingDown bool
}

// SimpleHealthResponse represents a simplified health response
type SimpleHealthResponse struct {
	Status    string                 `json:"status"`
	Version   string                 `json:"version"`
	Uptime    float64                `json:"uptime_seconds"`
	Timestamp time.Time              `json:"timestamp"`
	Details   map[string]interface{} `json:"details,omitempty"`
}

// SimpleReadinessResponse represents a simplified readiness response
type SimpleReadinessResponse struct {
	Ready             bool                   `json:"ready"`
	Reason            string                 `json:"reason,omitempty"`
	InitializationAge float64                `json:"initialization_age_seconds"`
	Details           map[string]interface{} `json:"details,omitempty"`
}

// NewSimpleHealthServer creates a new simplified health server
func NewSimpleHealthServer(engine engine.SyncEngine, stateManager *state.StateManager, cfg *config.ReplicatorConfig, logger *zap.Logger) *SimpleHealthServer {
	healthConfig := &config.HealthConfig{
		Enabled:           true,
		Path:              "/health",
		Port:              8080,
		ReadyPath:         "/ready",
		LivePath:          "/live",
		CheckInterval:     30 * time.Second,
		CheckTimeout:      5 * time.Second,
		GracePeriod:       30 * time.Second,
		CacheExpiry:       10 * time.Second,
		EnableProfiling:   false,
		EnableDetailedMetrics: true,
	}
	
	// Override with config values if available
	if cfg != nil && cfg.Monitoring.Health.Enabled {
		if cfg.Monitoring.Health.CheckInterval > 0 {
			healthConfig.CheckInterval = cfg.Monitoring.Health.CheckInterval
		}
		if cfg.Monitoring.Health.CheckTimeout > 0 {
			healthConfig.CheckTimeout = cfg.Monitoring.Health.CheckTimeout
		}
		if cfg.Monitoring.Health.GracePeriod > 0 {
			healthConfig.GracePeriod = cfg.Monitoring.Health.GracePeriod
		}
	}

	return &SimpleHealthServer{
		engine:       engine,
		stateManager: stateManager,
		config:       healthConfig,
		logger:       logger.Named("health-server"),
		startTime:    time.Now(),
		version:      "1.0.0",
	}
}

// SetupRoutes configures the HTTP routes for health endpoints
func (hs *SimpleHealthServer) SetupRoutes() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	
	router := gin.New()
	router.Use(gin.Recovery())
	
	// Health endpoints
	router.GET("/health", hs.healthHandler)
	router.GET("/ready", hs.readyHandler)
	router.GET("/live", hs.livenessHandler)
	
	return router
}

// healthHandler implements the /health endpoint
func (hs *SimpleHealthServer) healthHandler(c *gin.Context) {
	if hs.IsShuttingDown() {
		c.JSON(http.StatusServiceUnavailable, SimpleHealthResponse{
			Status:    "shutting_down",
			Version:   hs.version,
			Uptime:    time.Since(hs.startTime).Seconds(),
			Timestamp: time.Now(),
		})
		return
	}

	details := make(map[string]interface{})
	overallStatus := "healthy"

	// Check sync engine
	if hs.engine != nil {
		engineStatus := hs.engine.GetStatus()
		if engineStatus != nil {
			details["sync_engine"] = map[string]interface{}{
				"state": engineStatus.State,
				"active_syncs": engineStatus.ActiveSyncs,
			}
			
			if engineStatus.State == "error" {
				overallStatus = "unhealthy"
			}
		} else {
			details["sync_engine"] = "unavailable"
			overallStatus = "degraded"
		}
	}

	// Check state manager
	if hs.stateManager != nil {
		stateDetails := map[string]interface{}{
			"available": true,
		}
		
		if err := hs.stateManager.Load(); err != nil {
			stateDetails["error"] = err.Error()
			overallStatus = "degraded"
		}
		
		details["state_manager"] = stateDetails
	}

	// Add runtime metrics
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	details["runtime"] = map[string]interface{}{
		"goroutines": runtime.NumGoroutine(),
		"memory_mb": m.Alloc / 1024 / 1024,
	}

	statusCode := http.StatusOK
	if overallStatus == "unhealthy" {
		statusCode = http.StatusServiceUnavailable
	}

	c.JSON(statusCode, SimpleHealthResponse{
		Status:    overallStatus,
		Version:   hs.version,
		Uptime:    time.Since(hs.startTime).Seconds(),
		Timestamp: time.Now(),
		Details:   details,
	})
}

// readyHandler implements the /ready endpoint
func (hs *SimpleHealthServer) readyHandler(c *gin.Context) {
	if hs.IsShuttingDown() {
		c.JSON(http.StatusServiceUnavailable, SimpleReadinessResponse{
			Ready:             false,
			Reason:            "server is shutting down",
			InitializationAge: time.Since(hs.startTime).Seconds(),
		})
		return
	}

	// Check grace period
	if time.Since(hs.startTime) < hs.config.GracePeriod {
		c.JSON(http.StatusServiceUnavailable, SimpleReadinessResponse{
			Ready:  false,
			Reason: fmt.Sprintf("still in grace period (%.0fs remaining)", 
				(hs.config.GracePeriod - time.Since(hs.startTime)).Seconds()),
			InitializationAge: time.Since(hs.startTime).Seconds(),
		})
		return
	}

	details := make(map[string]interface{})
	ready := true
	reason := "all systems ready"

	// Check sync engine readiness
	if hs.engine != nil {
		engineStatus := hs.engine.GetStatus()
		if engineStatus != nil {
			details["sync_engine"] = engineStatus.State
			
			if engineStatus.State != "running" && engineStatus.State != "idle" {
				ready = false
				reason = fmt.Sprintf("sync engine not ready (state: %s)", engineStatus.State)
			}
		} else {
			ready = false
			reason = "sync engine status unavailable"
		}
	} else {
		ready = false
		reason = "sync engine not initialized"
	}

	// Check state manager readiness
	if hs.stateManager != nil && ready {
		if err := hs.stateManager.Load(); err != nil {
			ready = false
			reason = "state manager not ready"
			details["state_manager_error"] = err.Error()
		} else {
			details["state_manager"] = "ready"
		}
	}

	statusCode := http.StatusOK
	if !ready {
		statusCode = http.StatusServiceUnavailable
	}

	c.JSON(statusCode, SimpleReadinessResponse{
		Ready:             ready,
		Reason:            reason,
		InitializationAge: time.Since(hs.startTime).Seconds(),
		Details:           details,
	})
}

// livenessHandler implements the /live endpoint
func (hs *SimpleHealthServer) livenessHandler(c *gin.Context) {
	if hs.IsShuttingDown() {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"alive": false,
			"status": "shutting_down",
			"timestamp": time.Now(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"alive": true,
		"status": "running",
		"uptime": time.Since(hs.startTime).Seconds(),
		"timestamp": time.Now(),
	})
}

// MarkShuttingDown marks the server as shutting down
func (hs *SimpleHealthServer) MarkShuttingDown() {
	hs.shutdownMutex.Lock()
	defer hs.shutdownMutex.Unlock()
	hs.isShuttingDown = true
	hs.logger.Info("Health server marked as shutting down")
}

// IsShuttingDown returns whether the server is in shutdown state
func (hs *SimpleHealthServer) IsShuttingDown() bool {
	hs.shutdownMutex.RLock()
	defer hs.shutdownMutex.RUnlock()
	return hs.isShuttingDown
}

// StartHealthServer starts the health check HTTP server
func (hs *SimpleHealthServer) StartHealthServer(ctx context.Context, addr string) error {
	router := hs.SetupRoutes()
	
	server := &http.Server{
		Addr:         addr,
		Handler:      router,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Start server in goroutine
	serverErr := make(chan error, 1)
	go func() {
		hs.logger.Info("Starting health server", zap.String("addr", addr))
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
	}()

	// Wait for context cancellation or server error
	select {
	case err := <-serverErr:
		return fmt.Errorf("health server failed to start: %w", err)
	case <-ctx.Done():
		// Graceful shutdown
		hs.logger.Info("Shutting down health server")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		
		return server.Shutdown(shutdownCtx)
	case <-time.After(100 * time.Millisecond):
		// Server started successfully
		hs.logger.Info("Health server started successfully", zap.String("addr", addr))
		return nil
	}
}