package engine

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Additional engine states for lifecycle management
const (
	EngineStateMaintenance EngineState = "maintenance"
)

// LifecycleManager manages the lifecycle of the sync engine
type LifecycleManager struct {
	mu                sync.RWMutex
	logger            *zap.Logger
	state             EngineState
	lastStateChange   time.Time
	startTime         time.Time
	dependencies      map[string]Dependency
	hooks             map[LifecyclePhase][]LifecycleHook
	healthChecker     HealthChecker
	metricsCollector  MetricsCollector
	options           *LifecycleOptions
	shutdownTimeout   time.Duration
	stateChangeCh     chan EngineState
	errorCh           chan error
	shutdownCh        chan struct{}
	restartCh         chan struct{}
	maintenanceCh     chan struct{}
	done              chan struct{}
}

// Dependency represents a component that the engine depends on
type Dependency interface {
	// Name returns the name of the dependency
	Name() string
	
	// Start initializes and starts the dependency
	Start(ctx context.Context) error
	
	// Stop gracefully stops the dependency
	Stop(ctx context.Context) error
	
	// HealthCheck performs a health check on the dependency
	HealthCheck(ctx context.Context) error
	
	// IsRunning returns whether the dependency is currently running
	IsRunning() bool
	
	// Priority returns the start/stop priority (lower numbers start first, stop last)
	Priority() int
}

// LifecyclePhase represents a phase in the engine lifecycle
type LifecyclePhase int

const (
	PhasePreStart LifecyclePhase = iota
	PhasePostStart
	PhasePreStop
	PhasePostStop
	PhasePreRestart
	PhasePostRestart
	PhaseError
	PhaseRecovery
)

func (p LifecyclePhase) String() string {
	switch p {
	case PhasePreStart:
		return "pre_start"
	case PhasePostStart:
		return "post_start"
	case PhasePreStop:
		return "pre_stop"
	case PhasePostStop:
		return "post_stop"
	case PhasePreRestart:
		return "pre_restart"
	case PhasePostRestart:
		return "post_restart"
	case PhaseError:
		return "error"
	case PhaseRecovery:
		return "recovery"
	default:
		return "unknown"
	}
}

// LifecycleHook is a function that can be called during lifecycle phases
type LifecycleHook func(ctx context.Context, state EngineState) error

// LifecycleOptions configures the lifecycle manager behavior
type LifecycleOptions struct {
	// StartupTimeout is the maximum time to wait for startup
	StartupTimeout time.Duration
	
	// ShutdownTimeout is the maximum time to wait for shutdown
	ShutdownTimeout time.Duration
	
	// HealthCheckInterval is how often to check component health
	HealthCheckInterval time.Duration
	
	// EnableAutoRecovery enables automatic recovery from errors
	EnableAutoRecovery bool
	
	// MaxRecoveryAttempts is the maximum number of recovery attempts
	MaxRecoveryAttempts int
	
	// RecoveryBackoff is the backoff strategy for recovery attempts
	RecoveryBackoff time.Duration
	
	// EnableGracefulRestart enables graceful restart capabilities
	EnableGracefulRestart bool
	
	// MaintenanceMode enables maintenance mode capabilities
	EnableMaintenanceMode bool
	
	// StateChangeBuffer is the buffer size for state change notifications
	StateChangeBuffer int
}

// NewLifecycleManager creates a new lifecycle manager
func NewLifecycleManager(logger *zap.Logger, healthChecker HealthChecker, metricsCollector MetricsCollector, options *LifecycleOptions) *LifecycleManager {
	if options == nil {
		options = &LifecycleOptions{
			StartupTimeout:        2 * time.Minute,
			ShutdownTimeout:       30 * time.Second,
			HealthCheckInterval:   30 * time.Second,
			EnableAutoRecovery:    true,
			MaxRecoveryAttempts:   3,
			RecoveryBackoff:       10 * time.Second,
			EnableGracefulRestart: true,
			EnableMaintenanceMode: true,
			StateChangeBuffer:     10,
		}
	}

	lm := &LifecycleManager{
		logger:           logger,
		state:            EngineStateStopped,
		lastStateChange:  time.Now(),
		dependencies:     make(map[string]Dependency),
		hooks:            make(map[LifecyclePhase][]LifecycleHook),
		healthChecker:    healthChecker,
		metricsCollector: metricsCollector,
		options:          options,
		shutdownTimeout:  options.ShutdownTimeout,
		stateChangeCh:    make(chan EngineState, options.StateChangeBuffer),
		errorCh:          make(chan error, 10),
		shutdownCh:       make(chan struct{}),
		restartCh:        make(chan struct{}),
		maintenanceCh:    make(chan struct{}),
		done:             make(chan struct{}),
	}

	// Start background monitoring
	go lm.monitoringLoop()

	return lm
}

// AddDependency adds a dependency to the engine
func (lm *LifecycleManager) AddDependency(dep Dependency) {
	lm.mu.Lock()
	defer lm.mu.Unlock()

	lm.dependencies[dep.Name()] = dep
	lm.logger.Info("Added dependency",
		zap.String("name", dep.Name()),
		zap.Int("priority", dep.Priority()))
}

// RemoveDependency removes a dependency from the engine
func (lm *LifecycleManager) RemoveDependency(name string) {
	lm.mu.Lock()
	defer lm.mu.Unlock()

	delete(lm.dependencies, name)
	lm.logger.Info("Removed dependency", zap.String("name", name))
}

// AddHook adds a lifecycle hook for a specific phase
func (lm *LifecycleManager) AddHook(phase LifecyclePhase, hook LifecycleHook) {
	lm.mu.Lock()
	defer lm.mu.Unlock()

	if lm.hooks[phase] == nil {
		lm.hooks[phase] = make([]LifecycleHook, 0)
	}
	lm.hooks[phase] = append(lm.hooks[phase], hook)
	lm.logger.Info("Added lifecycle hook", zap.String("phase", phase.String()))
}

// Start starts the sync engine and all its dependencies
func (lm *LifecycleManager) Start(ctx context.Context) error {
	lm.mu.Lock()
	defer lm.mu.Unlock()

	if lm.state == EngineStateRunning {
		return fmt.Errorf("engine is already running")
	}

	if lm.state == EngineStateStarting {
		return fmt.Errorf("engine is already starting")
	}

	lm.logger.Info("Starting sync engine")
	lm.changeState(EngineStateStarting)

	// Create a timeout context
	startCtx, cancel := context.WithTimeout(ctx, lm.options.StartupTimeout)
	defer cancel()

	// Execute pre-start hooks
	if err := lm.executeHooks(startCtx, PhasePreStart); err != nil {
		lm.changeState(EngineStateError)
		return fmt.Errorf("pre-start hooks failed: %w", err)
	}

	// Validate dependencies before starting
	if err := lm.validateDependencies(startCtx); err != nil {
		lm.changeState(EngineStateError)
		return fmt.Errorf("dependency validation failed: %w", err)
	}

	// Start dependencies in priority order
	if err := lm.startDependencies(startCtx); err != nil {
		lm.changeState(EngineStateError)
		return fmt.Errorf("failed to start dependencies: %w", err)
	}

	// Record start time
	lm.startTime = time.Now()

	// Change state to running
	lm.changeState(EngineStateRunning)

	// Execute post-start hooks
	if err := lm.executeHooks(startCtx, PhasePostStart); err != nil {
		lm.logger.Error("Post-start hooks failed", zap.Error(err))
		// Don't fail startup for post-start hook errors, just log them
	}

	// Update metrics
	if lm.metricsCollector != nil {
		lm.metricsCollector.SetGauge("health_status", 1.0, map[string]string{"component": "engine"})
		lm.metricsCollector.SetGauge("engine_state", 1.0, map[string]string{"state": string(lm.state)})
	}

	lm.logger.Info("Sync engine started successfully",
		zap.Duration("startup_duration", time.Since(lm.lastStateChange)))

	return nil
}

// Stop gracefully stops the sync engine and all its dependencies
func (lm *LifecycleManager) Stop(ctx context.Context) error {
	lm.mu.Lock()
	defer lm.mu.Unlock()

	if lm.state == EngineStateStopped {
		return nil
	}

	if lm.state == EngineStateStopping {
		return fmt.Errorf("engine is already stopping")
	}

	lm.logger.Info("Stopping sync engine")
	lm.changeState(EngineStateStopping)

	// Create a timeout context
	stopCtx, cancel := context.WithTimeout(ctx, lm.shutdownTimeout)
	defer cancel()

	// Execute pre-stop hooks
	if err := lm.executeHooks(stopCtx, PhasePreStop); err != nil {
		lm.logger.Error("Pre-stop hooks failed", zap.Error(err))
		// Continue with shutdown even if hooks fail
	}

	// Stop dependencies in reverse priority order
	if err := lm.stopDependencies(stopCtx); err != nil {
		lm.logger.Error("Failed to stop some dependencies", zap.Error(err))
		// Continue with shutdown even if some dependencies fail to stop
	}

	// Change state to stopped
	lm.changeState(EngineStateStopped)

	// Execute post-stop hooks
	if err := lm.executeHooks(stopCtx, PhasePostStop); err != nil {
		lm.logger.Error("Post-stop hooks failed", zap.Error(err))
	}

	// Update metrics
	if lm.metricsCollector != nil {
		lm.metricsCollector.SetGauge("health_status", 0.0, map[string]string{"component": "engine"})
		lm.metricsCollector.SetGauge("engine_state", 0.0, map[string]string{"state": string(lm.state)})
	}

	lm.logger.Info("Sync engine stopped",
		zap.Duration("shutdown_duration", time.Since(lm.lastStateChange)))

	return nil
}

// Restart performs a graceful restart of the sync engine
func (lm *LifecycleManager) Restart(ctx context.Context) error {
	if !lm.options.EnableGracefulRestart {
		return fmt.Errorf("graceful restart is not enabled")
	}

	lm.logger.Info("Restarting sync engine")

	// Execute pre-restart hooks
	if err := lm.executeHooks(ctx, PhasePreRestart); err != nil {
		return fmt.Errorf("pre-restart hooks failed: %w", err)
	}

	// Stop the engine
	if err := lm.Stop(ctx); err != nil {
		return fmt.Errorf("failed to stop engine for restart: %w", err)
	}

	// Start the engine
	if err := lm.Start(ctx); err != nil {
		return fmt.Errorf("failed to start engine after restart: %w", err)
	}

	// Execute post-restart hooks
	if err := lm.executeHooks(ctx, PhasePostRestart); err != nil {
		lm.logger.Error("Post-restart hooks failed", zap.Error(err))
	}

	lm.logger.Info("Sync engine restarted successfully")
	return nil
}

// EnterMaintenanceMode puts the engine into maintenance mode
func (lm *LifecycleManager) EnterMaintenanceMode(ctx context.Context) error {
	if !lm.options.EnableMaintenanceMode {
		return fmt.Errorf("maintenance mode is not enabled")
	}

	lm.mu.Lock()
	defer lm.mu.Unlock()

	if lm.state == EngineStateMaintenance {
		return fmt.Errorf("engine is already in maintenance mode")
	}

	lm.logger.Info("Entering maintenance mode")
	lm.changeState(EngineStateMaintenance)

	// Update metrics
	if lm.metricsCollector != nil {
		lm.metricsCollector.SetGauge("engine_state", 1.0, map[string]string{"state": string(lm.state)})
	}

	return nil
}

// ExitMaintenanceMode exits maintenance mode
func (lm *LifecycleManager) ExitMaintenanceMode(ctx context.Context) error {
	lm.mu.Lock()
	defer lm.mu.Unlock()

	if lm.state != EngineStateMaintenance {
		return fmt.Errorf("engine is not in maintenance mode")
	}

	lm.logger.Info("Exiting maintenance mode")
	lm.changeState(EngineStateRunning)

	// Update metrics
	if lm.metricsCollector != nil {
		lm.metricsCollector.SetGauge("engine_state", 1.0, map[string]string{"state": string(lm.state)})
	}

	return nil
}

// GetState returns the current engine state
func (lm *LifecycleManager) GetState() EngineState {
	lm.mu.RLock()
	defer lm.mu.RUnlock()
	return lm.state
}

// GetUptime returns how long the engine has been running
func (lm *LifecycleManager) GetUptime() time.Duration {
	lm.mu.RLock()
	defer lm.mu.RUnlock()

	if lm.state == EngineStateRunning && !lm.startTime.IsZero() {
		return time.Since(lm.startTime)
	}
	return 0
}

// GetStateChanges returns a channel for state change notifications
func (lm *LifecycleManager) GetStateChanges() <-chan EngineState {
	return lm.stateChangeCh
}

// GetErrors returns a channel for error notifications
func (lm *LifecycleManager) GetErrors() <-chan error {
	return lm.errorCh
}

// Shutdown performs a complete shutdown of the lifecycle manager
func (lm *LifecycleManager) Shutdown(ctx context.Context) error {
	// Stop the engine if it's running
	if lm.state != EngineStateStopped {
		if err := lm.Stop(ctx); err != nil {
			lm.logger.Error("Failed to stop engine during shutdown", zap.Error(err))
		}
	}

	// Stop monitoring
	close(lm.done)

	lm.logger.Info("Lifecycle manager shutdown completed")
	return nil
}

// Private methods

func (lm *LifecycleManager) changeState(newState EngineState) {
	oldState := lm.state
	lm.state = newState
	lm.lastStateChange = time.Now()

	lm.logger.Info("Engine state changed",
		zap.String("from", string(oldState)),
		zap.String("to", string(newState)))

	// Send state change notification (non-blocking)
	select {
	case lm.stateChangeCh <- newState:
	default:
		// Channel is full, log a warning
		lm.logger.Warn("State change channel is full, dropping notification")
	}
}

func (lm *LifecycleManager) validateDependencies(ctx context.Context) error {
	lm.logger.Debug("Validating dependencies")

	for name, dep := range lm.dependencies {
		if err := dep.HealthCheck(ctx); err != nil {
			return fmt.Errorf("dependency '%s' failed validation: %w", name, err)
		}
	}

	lm.logger.Debug("All dependencies validated successfully")
	return nil
}

func (lm *LifecycleManager) startDependencies(ctx context.Context) error {
	// Sort dependencies by priority
	deps := lm.sortDependenciesByPriority(true)

	for _, dep := range deps {
		lm.logger.Info("Starting dependency",
			zap.String("name", dep.Name()),
			zap.Int("priority", dep.Priority()))

		if err := dep.Start(ctx); err != nil {
			return fmt.Errorf("failed to start dependency '%s': %w", dep.Name(), err)
		}

		// Verify the dependency started successfully
		if !dep.IsRunning() {
			return fmt.Errorf("dependency '%s' is not running after start", dep.Name())
		}
	}

	lm.logger.Info("All dependencies started successfully")
	return nil
}

func (lm *LifecycleManager) stopDependencies(ctx context.Context) error {
	// Sort dependencies by priority (reverse order for stopping)
	deps := lm.sortDependenciesByPriority(false)
	var lastError error

	for _, dep := range deps {
		if !dep.IsRunning() {
			continue
		}

		lm.logger.Info("Stopping dependency",
			zap.String("name", dep.Name()),
			zap.Int("priority", dep.Priority()))

		if err := dep.Stop(ctx); err != nil {
			lm.logger.Error("Failed to stop dependency",
				zap.String("name", dep.Name()),
				zap.Error(err))
			lastError = err
		}
	}

	if lastError != nil {
		return fmt.Errorf("some dependencies failed to stop: %w", lastError)
	}

	lm.logger.Info("All dependencies stopped successfully")
	return nil
}

func (lm *LifecycleManager) sortDependenciesByPriority(ascending bool) []Dependency {
	deps := make([]Dependency, 0, len(lm.dependencies))
	for _, dep := range lm.dependencies {
		deps = append(deps, dep)
	}

	// Simple bubble sort by priority
	n := len(deps)
	for i := 0; i < n-1; i++ {
		for j := 0; j < n-i-1; j++ {
			var shouldSwap bool
			if ascending {
				shouldSwap = deps[j].Priority() > deps[j+1].Priority()
			} else {
				shouldSwap = deps[j].Priority() < deps[j+1].Priority()
			}

			if shouldSwap {
				deps[j], deps[j+1] = deps[j+1], deps[j]
			}
		}
	}

	return deps
}

func (lm *LifecycleManager) executeHooks(ctx context.Context, phase LifecyclePhase) error {
	hooks := lm.hooks[phase]
	if len(hooks) == 0 {
		return nil
	}

	lm.logger.Debug("Executing lifecycle hooks",
		zap.String("phase", phase.String()),
		zap.Int("count", len(hooks)))

	for i, hook := range hooks {
		if err := hook(ctx, lm.state); err != nil {
			return fmt.Errorf("hook %d for phase %s failed: %w", i, phase.String(), err)
		}
	}

	return nil
}

func (lm *LifecycleManager) monitoringLoop() {
	ticker := time.NewTicker(lm.options.HealthCheckInterval)
	defer ticker.Stop()

	recoveryAttempts := 0

	for {
		select {
		case <-ticker.C:
			lm.performHealthChecks()
		case err := <-lm.errorCh:
			lm.handleError(err, &recoveryAttempts)
		case <-lm.shutdownCh:
			lm.logger.Info("Shutting down lifecycle manager")
			return
		case <-lm.done:
			return
		}
	}
}

func (lm *LifecycleManager) performHealthChecks() {
	if lm.state != EngineStateRunning {
		return
	}

	lm.mu.RLock()
	deps := make(map[string]Dependency)
	for name, dep := range lm.dependencies {
		deps[name] = dep
	}
	lm.mu.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for name, dep := range deps {
		if err := dep.HealthCheck(ctx); err != nil {
			lm.logger.Error("Dependency health check failed",
				zap.String("dependency", name),
				zap.Error(err))

			// Send error to error channel for potential recovery
			select {
			case lm.errorCh <- fmt.Errorf("dependency %s health check failed: %w", name, err):
			default:
				// Error channel is full
			}
		}
	}
}

func (lm *LifecycleManager) handleError(err error, recoveryAttempts *int) {
	lm.logger.Error("Engine error detected", zap.Error(err))

	if !lm.options.EnableAutoRecovery {
		return
	}

	if *recoveryAttempts >= lm.options.MaxRecoveryAttempts {
		lm.logger.Error("Maximum recovery attempts reached, giving up",
			zap.Int("attempts", *recoveryAttempts))
		lm.mu.Lock()
		lm.changeState(EngineStateError)
		lm.mu.Unlock()
		return
	}

	*recoveryAttempts++
	lm.logger.Info("Attempting recovery",
		zap.Int("attempt", *recoveryAttempts),
		zap.Int("max_attempts", lm.options.MaxRecoveryAttempts))

	// Execute error hooks
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := lm.executeHooks(ctx, PhaseError); err != nil {
		lm.logger.Error("Error hooks failed", zap.Error(err))
	}

	// Wait before attempting recovery
	time.Sleep(lm.options.RecoveryBackoff)

	// Attempt recovery by restarting
	if err := lm.Restart(ctx); err != nil {
		lm.logger.Error("Recovery restart failed", zap.Error(err))
		return
	}

	// Execute recovery hooks
	if err := lm.executeHooks(ctx, PhaseRecovery); err != nil {
		lm.logger.Error("Recovery hooks failed", zap.Error(err))
	}

	// Reset recovery attempts on successful recovery
	*recoveryAttempts = 0
	lm.logger.Info("Recovery successful")
}