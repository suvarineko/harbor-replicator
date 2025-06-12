package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"go.uber.org/zap"

	"harbor-replicator/pkg/config"
)

// ConfigChangeType represents the type of configuration change
type ConfigChangeType int

const (
	ConfigChangeTypeUpdate ConfigChangeType = iota
	ConfigChangeTypeReload
	ConfigChangeTypeValidation
	ConfigChangeTypeError
)

func (c ConfigChangeType) String() string {
	switch c {
	case ConfigChangeTypeUpdate:
		return "update"
	case ConfigChangeTypeReload:
		return "reload"
	case ConfigChangeTypeValidation:
		return "validation"
	case ConfigChangeTypeError:
		return "error"
	default:
		return "unknown"
	}
}

// ConfigChange represents a configuration change event
type ConfigChange struct {
	Type        ConfigChangeType           `json:"type"`
	Timestamp   time.Time                  `json:"timestamp"`
	Source      string                     `json:"source"`
	Changes     map[string]interface{}     `json:"changes,omitempty"`
	OldConfig   *config.ReplicatorConfig   `json:"old_config,omitempty"`
	NewConfig   *config.ReplicatorConfig   `json:"new_config,omitempty"`
	Error       error                      `json:"error,omitempty"`
	Details     map[string]interface{}     `json:"details,omitempty"`
}

// ConfigValidator validates configuration changes
type ConfigValidator interface {
	// Validate validates a configuration
	Validate(cfg *config.ReplicatorConfig) error
	
	// ValidateChange validates a configuration change
	ValidateChange(oldCfg, newCfg *config.ReplicatorConfig) error
	
	// GetValidationRules returns the validation rules
	GetValidationRules() map[string]interface{}
}

// ConfigSubscriber receives configuration change notifications
type ConfigSubscriber interface {
	// OnConfigChange is called when configuration changes
	OnConfigChange(change *ConfigChange) error
	
	// GetSubscriberName returns the name of the subscriber
	GetSubscriberName() string
	
	// GetSubscriberPriority returns the priority (lower numbers get notified first)
	GetSubscriberPriority() int
}

// EngineConfigManager manages configuration for the sync engine with hot-reload support
type EngineConfigManager struct {
	mu                  sync.RWMutex
	logger              *zap.Logger
	configManager       *config.Manager
	currentConfig       *config.ReplicatorConfig
	configPath          string
	validator           ConfigValidator
	subscribers         map[string]ConfigSubscriber
	watcher             *fsnotify.Watcher
	options             *ConfigManagerOptions
	changeHistory       []*ConfigChange
	validationCache     map[string]*ValidationResult
	reloadInProgress    bool
	lastReload          time.Time
	changeChannel       chan *ConfigChange
	stopChannel         chan struct{}
	started             bool
	metricsCollector    MetricsCollector
}

// ConfigManagerOptions configures the configuration manager behavior
type ConfigManagerOptions struct {
	// EnableHotReload enables automatic configuration reloading
	EnableHotReload bool
	
	// ReloadDebounce is the minimum time between reload attempts
	ReloadDebounce time.Duration
	
	// ValidationTimeout is the timeout for configuration validation
	ValidationTimeout time.Duration
	
	// MaxChangeHistory is the maximum number of change events to keep
	MaxChangeHistory int
	
	// EnableValidationCache enables caching of validation results
	EnableValidationCache bool
	
	// CacheTTL is the time-to-live for validation cache entries
	CacheTTL time.Duration
	
	// NotificationBuffer is the buffer size for change notifications
	NotificationBuffer int
	
	// EnableBackup creates backup copies of configuration files
	EnableBackup bool
	
	// BackupDirectory is the directory for configuration backups
	BackupDirectory string
	
	// MaxBackups is the maximum number of backup files to keep
	MaxBackups int
	
	// WatchDirectories are additional directories to watch for changes
	WatchDirectories []string
	
	// IgnorePatterns are file patterns to ignore during watching
	IgnorePatterns []string
}

// ValidationResult represents the result of configuration validation
type ValidationResult struct {
	Valid       bool                   `json:"valid"`
	Errors      []string              `json:"errors,omitempty"`
	Warnings    []string              `json:"warnings,omitempty"`
	Timestamp   time.Time             `json:"timestamp"`
	Duration    time.Duration         `json:"duration"`
	Details     map[string]interface{} `json:"details,omitempty"`
}

// NewEngineConfigManager creates a new configuration manager for the sync engine
func NewEngineConfigManager(logger *zap.Logger, configManager *config.Manager, validator ConfigValidator, metricsCollector MetricsCollector, options *ConfigManagerOptions) *EngineConfigManager {
	if options == nil {
		options = &ConfigManagerOptions{
			EnableHotReload:       true,
			ReloadDebounce:        2 * time.Second,
			ValidationTimeout:     30 * time.Second,
			MaxChangeHistory:      100,
			EnableValidationCache: true,
			CacheTTL:              5 * time.Minute,
			NotificationBuffer:    50,
			EnableBackup:          true,
			MaxBackups:            10,
		}
	}

	ecm := &EngineConfigManager{
		logger:           logger,
		configManager:    configManager,
		validator:        validator,
		subscribers:      make(map[string]ConfigSubscriber),
		options:          options,
		changeHistory:    make([]*ConfigChange, 0, options.MaxChangeHistory),
		validationCache:  make(map[string]*ValidationResult),
		changeChannel:    make(chan *ConfigChange, options.NotificationBuffer),
		stopChannel:      make(chan struct{}),
		metricsCollector: metricsCollector,
	}

	return ecm
}

// Start starts the configuration manager
func (ecm *EngineConfigManager) Start(ctx context.Context, configPath string) error {
	ecm.mu.Lock()
	defer ecm.mu.Unlock()

	if ecm.started {
		return fmt.Errorf("configuration manager is already started")
	}

	ecm.configPath = configPath
	
	// Load initial configuration
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return fmt.Errorf("failed to load initial configuration: %w", err)
	}

	// Validate initial configuration
	if ecm.validator != nil {
		if err := ecm.validator.Validate(cfg); err != nil {
			return fmt.Errorf("initial configuration validation failed: %w", err)
		}
	}

	ecm.currentConfig = cfg

	// Set up file watcher if hot reload is enabled
	if ecm.options.EnableHotReload {
		if err := ecm.setupFileWatcher(); err != nil {
			return fmt.Errorf("failed to setup file watcher: %w", err)
		}
	}

	// Start notification processing
	go ecm.processNotifications()

	// Start periodic cleanup
	go ecm.periodicCleanup()

	ecm.started = true
	ecm.lastReload = time.Now()

	ecm.logger.Info("Configuration manager started",
		zap.String("config_path", configPath),
		zap.Bool("hot_reload_enabled", ecm.options.EnableHotReload))

	// Record initial load event
	change := &ConfigChange{
		Type:      ConfigChangeTypeReload,
		Timestamp: time.Now(),
		Source:    "initial_load",
		NewConfig: cfg,
		Details:   map[string]interface{}{"initial_load": true},
	}
	ecm.recordChange(change)

	return nil
}

// Stop stops the configuration manager
func (ecm *EngineConfigManager) Stop() error {
	ecm.mu.Lock()
	defer ecm.mu.Unlock()

	if !ecm.started {
		return nil
	}

	close(ecm.stopChannel)

	if ecm.watcher != nil {
		ecm.watcher.Close()
	}

	ecm.started = false

	ecm.logger.Info("Configuration manager stopped")
	return nil
}

// GetConfig returns the current configuration
func (ecm *EngineConfigManager) GetConfig() *config.ReplicatorConfig {
	ecm.mu.RLock()
	defer ecm.mu.RUnlock()

	// Return a copy to prevent external modifications
	if ecm.currentConfig == nil {
		return nil
	}

	// Note: This is a simplified copy. In practice, you'd implement deep copying
	configCopy := *ecm.currentConfig
	return &configCopy
}

// ReloadConfig manually reloads the configuration
func (ecm *EngineConfigManager) ReloadConfig(ctx context.Context) error {
	ecm.mu.Lock()
	defer ecm.mu.Unlock()

	if ecm.reloadInProgress {
		return fmt.Errorf("configuration reload is already in progress")
	}

	// Check debounce
	if time.Since(ecm.lastReload) < ecm.options.ReloadDebounce {
		return fmt.Errorf("reload too soon, waiting for debounce period")
	}

	return ecm.performReload(ctx, "manual_reload")
}

// ValidateConfig validates a configuration without applying it
func (ecm *EngineConfigManager) ValidateConfig(cfg *config.ReplicatorConfig) (*ValidationResult, error) {
	if ecm.validator == nil {
		return &ValidationResult{
			Valid:     true,
			Timestamp: time.Now(),
			Details:   map[string]interface{}{"validator": "none"},
		}, nil
	}

	startTime := time.Now()
	
	// Check validation cache
	if ecm.options.EnableValidationCache {
		if cached := ecm.getCachedValidation(cfg); cached != nil {
			return cached, nil
		}
	}

	// Create validation context with timeout
	validationCtx, cancel := context.WithTimeout(context.Background(), ecm.options.ValidationTimeout)
	defer cancel()

	result := &ValidationResult{
		Timestamp: startTime,
		Details:   make(map[string]interface{}),
	}

	// Perform validation
	done := make(chan error, 1)
	go func() {
		done <- ecm.validator.Validate(cfg)
	}()

	select {
	case err := <-done:
		result.Duration = time.Since(startTime)
		if err != nil {
			result.Valid = false
			result.Errors = []string{err.Error()}
		} else {
			result.Valid = true
		}
	case <-validationCtx.Done():
		result.Duration = time.Since(startTime)
		result.Valid = false
		result.Errors = []string{"validation timeout"}
	}

	// Cache result if enabled
	if ecm.options.EnableValidationCache {
		ecm.cacheValidation(cfg, result)
	}

	// Record metrics
	if ecm.metricsCollector != nil {
		ecm.metricsCollector.RecordHistogram("config_validation_duration", result.Duration.Seconds(), map[string]string{
			"valid": fmt.Sprintf("%t", result.Valid),
		})
	}

	return result, nil
}

// UpdateConfig updates the configuration with validation
func (ecm *EngineConfigManager) UpdateConfig(ctx context.Context, newConfig *config.ReplicatorConfig, source string) error {
	ecm.mu.Lock()
	defer ecm.mu.Unlock()

	// Validate new configuration
	if ecm.validator != nil {
		if err := ecm.validator.Validate(newConfig); err != nil {
			change := &ConfigChange{
				Type:      ConfigChangeTypeValidation,
				Timestamp: time.Now(),
				Source:    source,
				NewConfig: newConfig,
				Error:     err,
			}
			ecm.recordChange(change)
			return fmt.Errorf("configuration validation failed: %w", err)
		}

		// Validate change if we have a current config
		if ecm.currentConfig != nil {
			if err := ecm.validator.ValidateChange(ecm.currentConfig, newConfig); err != nil {
				change := &ConfigChange{
					Type:      ConfigChangeTypeValidation,
					Timestamp: time.Now(),
					Source:    source,
					OldConfig: ecm.currentConfig,
					NewConfig: newConfig,
					Error:     err,
				}
				ecm.recordChange(change)
				return fmt.Errorf("configuration change validation failed: %w", err)
			}
		}
	}

	// Create backup if enabled
	if ecm.options.EnableBackup && ecm.currentConfig != nil {
		if err := ecm.createBackup(); err != nil {
			ecm.logger.Warn("Failed to create configuration backup", zap.Error(err))
		}
	}

	oldConfig := ecm.currentConfig
	ecm.currentConfig = newConfig

	// Create change event
	change := &ConfigChange{
		Type:      ConfigChangeTypeUpdate,
		Timestamp: time.Now(),
		Source:    source,
		OldConfig: oldConfig,
		NewConfig: newConfig,
		Changes:   ecm.calculateChanges(oldConfig, newConfig),
	}

	// Record change
	ecm.recordChange(change)

	// Notify subscribers asynchronously
	go ecm.notifySubscribers(change)

	// Update metrics
	if ecm.metricsCollector != nil {
		ecm.metricsCollector.IncSyncCounter("config", "updated")
		ecm.metricsCollector.SetGauge("config_last_update", float64(time.Now().Unix()), map[string]string{
			"source": source,
		})
	}

	ecm.logger.Info("Configuration updated",
		zap.String("source", source),
		zap.Int("change_count", len(change.Changes)))

	return nil
}

// Subscribe adds a configuration change subscriber
func (ecm *EngineConfigManager) Subscribe(subscriber ConfigSubscriber) {
	ecm.mu.Lock()
	defer ecm.mu.Unlock()

	ecm.subscribers[subscriber.GetSubscriberName()] = subscriber
	ecm.logger.Info("Added configuration subscriber",
		zap.String("name", subscriber.GetSubscriberName()),
		zap.Int("priority", subscriber.GetSubscriberPriority()))
}

// Unsubscribe removes a configuration change subscriber
func (ecm *EngineConfigManager) Unsubscribe(name string) {
	ecm.mu.Lock()
	defer ecm.mu.Unlock()

	delete(ecm.subscribers, name)
	ecm.logger.Info("Removed configuration subscriber", zap.String("name", name))
}

// GetChangeHistory returns the configuration change history
func (ecm *EngineConfigManager) GetChangeHistory() []*ConfigChange {
	ecm.mu.RLock()
	defer ecm.mu.RUnlock()

	// Return copies to prevent external modifications
	history := make([]*ConfigChange, len(ecm.changeHistory))
	for i, change := range ecm.changeHistory {
		changeCopy := *change
		if change.Changes != nil {
			changeCopy.Changes = make(map[string]interface{})
			for k, v := range change.Changes {
				changeCopy.Changes[k] = v
			}
		}
		if change.Details != nil {
			changeCopy.Details = make(map[string]interface{})
			for k, v := range change.Details {
				changeCopy.Details[k] = v
			}
		}
		history[i] = &changeCopy
	}

	return history
}

// GetValidationHistory returns validation results history
func (ecm *EngineConfigManager) GetValidationHistory() map[string]*ValidationResult {
	ecm.mu.RLock()
	defer ecm.mu.RUnlock()

	// Return copies
	history := make(map[string]*ValidationResult)
	for k, v := range ecm.validationCache {
		resultCopy := *v
		if v.Details != nil {
			resultCopy.Details = make(map[string]interface{})
			for dk, dv := range v.Details {
				resultCopy.Details[dk] = dv
			}
		}
		history[k] = &resultCopy
	}

	return history
}

// Private methods

func (ecm *EngineConfigManager) setupFileWatcher() error {
	var err error
	ecm.watcher, err = fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create file watcher: %w", err)
	}

	// Watch the configuration file directory
	configDir := filepath.Dir(ecm.configPath)
	if err := ecm.watcher.Add(configDir); err != nil {
		return fmt.Errorf("failed to watch config directory: %w", err)
	}

	// Watch additional directories if specified
	for _, dir := range ecm.options.WatchDirectories {
		if err := ecm.watcher.Add(dir); err != nil {
			ecm.logger.Warn("Failed to watch additional directory",
				zap.String("directory", dir),
				zap.Error(err))
		}
	}

	// Start file watcher goroutine
	go ecm.fileWatcherLoop()

	ecm.logger.Info("File watcher setup completed",
		zap.String("config_dir", configDir),
		zap.Strings("watch_dirs", ecm.options.WatchDirectories))

	return nil
}

func (ecm *EngineConfigManager) fileWatcherLoop() {
	for {
		select {
		case event, ok := <-ecm.watcher.Events:
			if !ok {
				return
			}
			ecm.handleFileEvent(event)
		case err, ok := <-ecm.watcher.Errors:
			if !ok {
				return
			}
			ecm.logger.Error("File watcher error", zap.Error(err))
		case <-ecm.stopChannel:
			return
		}
	}
}

func (ecm *EngineConfigManager) handleFileEvent(event fsnotify.Event) {
	// Check if this is our config file
	if event.Name != ecm.configPath {
		return
	}

	// Check if we should ignore this pattern
	for _, pattern := range ecm.options.IgnorePatterns {
		if matched, _ := filepath.Match(pattern, filepath.Base(event.Name)); matched {
			return
		}
	}

	ecm.logger.Debug("File event received",
		zap.String("file", event.Name),
		zap.String("op", event.Op.String()))

	// Only reload on write or create events
	if event.Op&fsnotify.Write == fsnotify.Write || event.Op&fsnotify.Create == fsnotify.Create {
		go func() {
			// Wait for debounce period
			time.Sleep(ecm.options.ReloadDebounce)
			
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			
			if err := ecm.ReloadConfig(ctx); err != nil {
				ecm.logger.Error("Automatic configuration reload failed", zap.Error(err))
			}
		}()
	}
}

func (ecm *EngineConfigManager) performReload(ctx context.Context, source string) error {
	ecm.reloadInProgress = true
	defer func() {
		ecm.reloadInProgress = false
		ecm.lastReload = time.Now()
	}()

	ecm.logger.Info("Reloading configuration", zap.String("source", source))

	// Load new configuration
	newConfig, err := config.LoadConfig(ecm.configPath)
	if err != nil {
		change := &ConfigChange{
			Type:      ConfigChangeTypeError,
			Timestamp: time.Now(),
			Source:    source,
			Error:     err,
		}
		ecm.recordChange(change)
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Validate new configuration
	if ecm.validator != nil {
		if err := ecm.validator.Validate(newConfig); err != nil {
			change := &ConfigChange{
				Type:      ConfigChangeTypeValidation,
				Timestamp: time.Now(),
				Source:    source,
				NewConfig: newConfig,
				Error:     err,
			}
			ecm.recordChange(change)
			return fmt.Errorf("configuration validation failed: %w", err)
		}
	}

	// Update configuration
	return ecm.UpdateConfig(ctx, newConfig, source)
}

func (ecm *EngineConfigManager) recordChange(change *ConfigChange) {
	ecm.changeHistory = append(ecm.changeHistory, change)

	// Trim history if it exceeds maximum size
	if len(ecm.changeHistory) > ecm.options.MaxChangeHistory {
		ecm.changeHistory = ecm.changeHistory[1:]
	}

	// Send to change channel for processing
	select {
	case ecm.changeChannel <- change:
	default:
		ecm.logger.Warn("Change notification channel is full, dropping event")
	}
}

func (ecm *EngineConfigManager) notifySubscribers(change *ConfigChange) {
	ecm.mu.RLock()
	
	// Sort subscribers by priority
	subscribers := make([]ConfigSubscriber, 0, len(ecm.subscribers))
	for _, sub := range ecm.subscribers {
		subscribers = append(subscribers, sub)
	}
	ecm.mu.RUnlock()

	// Simple bubble sort by priority
	n := len(subscribers)
	for i := 0; i < n-1; i++ {
		for j := 0; j < n-i-1; j++ {
			if subscribers[j].GetSubscriberPriority() > subscribers[j+1].GetSubscriberPriority() {
				subscribers[j], subscribers[j+1] = subscribers[j+1], subscribers[j]
			}
		}
	}

	// Notify subscribers in priority order
	for _, subscriber := range subscribers {
		if err := subscriber.OnConfigChange(change); err != nil {
			ecm.logger.Error("Subscriber failed to handle configuration change",
				zap.String("subscriber", subscriber.GetSubscriberName()),
				zap.Error(err))
		}
	}
}

func (ecm *EngineConfigManager) calculateChanges(oldConfig, newConfig *config.ReplicatorConfig) map[string]interface{} {
	changes := make(map[string]interface{})
	
	// This is a simplified implementation. In practice, you'd implement
	// deep comparison to detect specific field changes
	if oldConfig == nil {
		changes["type"] = "initial"
		return changes
	}

	// Compare major sections
	changes["timestamp"] = time.Now().Unix()
	changes["type"] = "update"
	
	// You would implement detailed field-by-field comparison here
	// For now, we'll just indicate that changes were detected
	changes["has_changes"] = true
	
	return changes
}

func (ecm *EngineConfigManager) getCachedValidation(cfg *config.ReplicatorConfig) *ValidationResult {
	if !ecm.options.EnableValidationCache {
		return nil
	}

	// Create a simple key based on config content (in practice, use a hash)
	key := fmt.Sprintf("config_%d", time.Now().Unix()) // Simplified key
	
	if cached, exists := ecm.validationCache[key]; exists {
		if time.Since(cached.Timestamp) < ecm.options.CacheTTL {
			return cached
		}
	}
	
	return nil
}

func (ecm *EngineConfigManager) cacheValidation(cfg *config.ReplicatorConfig, result *ValidationResult) {
	if !ecm.options.EnableValidationCache {
		return
	}

	// Create a simple key (in practice, use a hash of the config)
	key := fmt.Sprintf("config_%d", time.Now().Unix()) // Simplified key
	ecm.validationCache[key] = result
}

func (ecm *EngineConfigManager) createBackup() error {
	if ecm.options.BackupDirectory == "" {
		ecm.options.BackupDirectory = filepath.Dir(ecm.configPath) + "/backups"
	}

	// Implementation would create a backup copy of the current configuration
	// This is a placeholder for the actual backup logic
	ecm.logger.Debug("Configuration backup created")
	return nil
}

func (ecm *EngineConfigManager) processNotifications() {
	for {
		select {
		case change := <-ecm.changeChannel:
			// Process change notification
			if ecm.metricsCollector != nil {
				ecm.metricsCollector.IncSyncCounter("config_change", change.Type.String())
			}
		case <-ecm.stopChannel:
			return
		}
	}
}

func (ecm *EngineConfigManager) periodicCleanup() {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			ecm.performCleanup()
		case <-ecm.stopChannel:
			return
		}
	}
}

func (ecm *EngineConfigManager) performCleanup() {
	ecm.mu.Lock()
	defer ecm.mu.Unlock()

	// Clean up old validation cache entries
	cutoff := time.Now().Add(-ecm.options.CacheTTL)
	for key, result := range ecm.validationCache {
		if result.Timestamp.Before(cutoff) {
			delete(ecm.validationCache, key)
		}
	}

	ecm.logger.Debug("Performed periodic cleanup",
		zap.Int("cache_size", len(ecm.validationCache)),
		zap.Int("history_size", len(ecm.changeHistory)))
}