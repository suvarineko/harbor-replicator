package config

import (
	"context"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

type ConfigWatcher struct {
	mu           sync.RWMutex
	config       *ReplicatorConfig
	viper        *viper.Viper
	logger       *zap.Logger
	configPath   string
	changeHandlers []ConfigChangeHandler
	watchContext context.Context
	watchCancel  context.CancelFunc
	isWatching   bool
}

type ConfigChangeHandler func(oldConfig, newConfig *ReplicatorConfig) error

type ConfigChangeEvent struct {
	OldConfig *ReplicatorConfig
	NewConfig *ReplicatorConfig
	Timestamp time.Time
	Error     error
}

func NewConfigWatcher(configPath string, logger *zap.Logger) *ConfigWatcher {
	ctx, cancel := context.WithCancel(context.Background())
	
	return &ConfigWatcher{
		configPath:     configPath,
		logger:         logger,
		changeHandlers: make([]ConfigChangeHandler, 0),
		watchContext:   ctx,
		watchCancel:    cancel,
		isWatching:     false,
	}
}

func (cw *ConfigWatcher) LoadInitialConfig() (*ReplicatorConfig, error) {
	cw.mu.Lock()
	defer cw.mu.Unlock()

	config, err := LoadConfig(cw.configPath)
	if err != nil {
		return nil, err
	}

	cw.config = config
	return config, nil
}

func (cw *ConfigWatcher) GetCurrentConfig() *ReplicatorConfig {
	cw.mu.RLock()
	defer cw.mu.RUnlock()
	
	if cw.config == nil {
		return nil
	}

	configCopy := *cw.config
	return &configCopy
}

func (cw *ConfigWatcher) AddChangeHandler(handler ConfigChangeHandler) {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	
	cw.changeHandlers = append(cw.changeHandlers, handler)
}

func (cw *ConfigWatcher) RemoveAllHandlers() {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	
	cw.changeHandlers = make([]ConfigChangeHandler, 0)
}

func (cw *ConfigWatcher) StartWatching() error {
	cw.mu.Lock()
	defer cw.mu.Unlock()

	if cw.isWatching {
		return nil
	}

	v := viper.New()
	v.SetConfigFile(cw.configPath)

	if err := v.ReadInConfig(); err != nil {
		return err
	}

	cw.viper = v
	cw.isWatching = true

	v.WatchConfig()
	v.OnConfigChange(func(e fsnotify.Event) {
		cw.handleConfigChange()
	})

	cw.logger.Info("Configuration file watching started", 
		zap.String("config_path", cw.configPath))

	return nil
}

func (cw *ConfigWatcher) StopWatching() {
	cw.mu.Lock()
	defer cw.mu.Unlock()

	if !cw.isWatching {
		return
	}

	cw.watchCancel()
	cw.isWatching = false

	cw.logger.Info("Configuration file watching stopped")
}

func (cw *ConfigWatcher) handleConfigChange() {
	cw.logger.Info("Configuration file change detected")

	oldConfig := cw.GetCurrentConfig()

	newConfig, err := LoadConfig(cw.configPath)
	if err != nil {
		cw.logger.Error("Failed to reload configuration", 
			zap.Error(err),
			zap.String("config_path", cw.configPath))
		return
	}

	if err := ValidateConfig(newConfig); err != nil {
		cw.logger.Error("New configuration is invalid", 
			zap.Error(err),
			zap.String("config_path", cw.configPath))
		return
	}

	cw.mu.Lock()
	cw.config = newConfig
	handlers := make([]ConfigChangeHandler, len(cw.changeHandlers))
	copy(handlers, cw.changeHandlers)
	cw.mu.Unlock()

	cw.logger.Info("Configuration reloaded successfully")

	for _, handler := range handlers {
		if err := handler(oldConfig, newConfig); err != nil {
			cw.logger.Error("Configuration change handler failed", 
				zap.Error(err))
		}
	}
}

func (cw *ConfigWatcher) IsWatching() bool {
	cw.mu.RLock()
	defer cw.mu.RUnlock()
	
	return cw.isWatching
}

func (cw *ConfigWatcher) ReloadConfig() error {
	cw.logger.Info("Manual configuration reload triggered")

	oldConfig := cw.GetCurrentConfig()

	newConfig, err := LoadConfig(cw.configPath)
	if err != nil {
		cw.logger.Error("Failed to reload configuration", 
			zap.Error(err),
			zap.String("config_path", cw.configPath))
		return err
	}

	if err := ValidateConfig(newConfig); err != nil {
		cw.logger.Error("New configuration is invalid", 
			zap.Error(err),
			zap.String("config_path", cw.configPath))
		return err
	}

	cw.mu.Lock()
	cw.config = newConfig
	handlers := make([]ConfigChangeHandler, len(cw.changeHandlers))
	copy(handlers, cw.changeHandlers)
	cw.mu.Unlock()

	cw.logger.Info("Configuration reloaded successfully")

	for _, handler := range handlers {
		if err := handler(oldConfig, newConfig); err != nil {
			cw.logger.Error("Configuration change handler failed", 
				zap.Error(err))
		}
	}

	return nil
}