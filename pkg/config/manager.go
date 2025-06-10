package config

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
)

type Manager struct {
	mu             sync.RWMutex
	config         *ReplicatorConfig
	watcher        *ConfigWatcher
	logger         *zap.Logger
	configPath     string
	subscribers    []ConfigSubscriber
	ctx            context.Context
	cancel         context.CancelFunc
	started        bool
	lastReload     time.Time
	reloadCount    int64
	validationFunc ValidationFunc
}

type ConfigSubscriber interface {
	OnConfigChange(oldConfig, newConfig *ReplicatorConfig) error
	GetSubscriberName() string
}

type ValidationFunc func(*ReplicatorConfig) error

type ManagerStats struct {
	LastReload     time.Time
	ReloadCount    int64
	SubscriberCount int
	IsWatching     bool
	ConfigPath     string
}

type ManagerOption func(*Manager)

func WithValidationFunc(fn ValidationFunc) ManagerOption {
	return func(m *Manager) {
		m.validationFunc = fn
	}
}

func WithLogger(logger *zap.Logger) ManagerOption {
	return func(m *Manager) {
		m.logger = logger
	}
}

func NewManager(configPath string, options ...ManagerOption) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	
	manager := &Manager{
		configPath:     configPath,
		subscribers:    make([]ConfigSubscriber, 0),
		ctx:            ctx,
		cancel:         cancel,
		started:        false,
		validationFunc: ValidateConfig,
		logger:         zap.NewNop(),
	}

	for _, option := range options {
		option(manager)
	}

	manager.watcher = NewConfigWatcher(configPath, manager.logger)

	return manager
}

func (m *Manager) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.started {
		return fmt.Errorf("configuration manager is already started")
	}

	config, err := m.watcher.LoadInitialConfig()
	if err != nil {
		return fmt.Errorf("failed to load initial configuration: %w", err)
	}

	if m.validationFunc != nil {
		if err := m.validationFunc(config); err != nil {
			return fmt.Errorf("initial configuration validation failed: %w", err)
		}
	}

	m.config = config
	m.lastReload = time.Now()

	m.watcher.AddChangeHandler(m.handleConfigChange)

	if err := m.watcher.StartWatching(); err != nil {
		return fmt.Errorf("failed to start configuration watching: %w", err)
	}

	m.started = true
	m.logger.Info("Configuration manager started successfully",
		zap.String("config_path", m.configPath),
		zap.Int("subscribers", len(m.subscribers)))

	return nil
}

func (m *Manager) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.started {
		return nil
	}

	m.watcher.StopWatching()
	m.cancel()
	m.started = false

	m.logger.Info("Configuration manager stopped")
	return nil
}

func (m *Manager) GetConfig() *ReplicatorConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.config == nil {
		return nil
	}

	configCopy := *m.config
	return &configCopy
}

func (m *Manager) Subscribe(subscriber ConfigSubscriber) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, existing := range m.subscribers {
		if existing.GetSubscriberName() == subscriber.GetSubscriberName() {
			return fmt.Errorf("subscriber with name '%s' already exists", subscriber.GetSubscriberName())
		}
	}

	m.subscribers = append(m.subscribers, subscriber)
	
	m.logger.Info("Configuration subscriber added",
		zap.String("subscriber", subscriber.GetSubscriberName()),
		zap.Int("total_subscribers", len(m.subscribers)))

	return nil
}

func (m *Manager) Unsubscribe(subscriberName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i, subscriber := range m.subscribers {
		if subscriber.GetSubscriberName() == subscriberName {
			m.subscribers = append(m.subscribers[:i], m.subscribers[i+1:]...)
			
			m.logger.Info("Configuration subscriber removed",
				zap.String("subscriber", subscriberName),
				zap.Int("total_subscribers", len(m.subscribers)))
			
			return nil
		}
	}

	return fmt.Errorf("subscriber with name '%s' not found", subscriberName)
}

func (m *Manager) ReloadConfig() error {
	if !m.started {
		return fmt.Errorf("configuration manager is not started")
	}

	return m.watcher.ReloadConfig()
}

func (m *Manager) GetStats() ManagerStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return ManagerStats{
		LastReload:      m.lastReload,
		ReloadCount:     m.reloadCount,
		SubscriberCount: len(m.subscribers),
		IsWatching:      m.watcher.IsWatching(),
		ConfigPath:      m.configPath,
	}
}

func (m *Manager) IsStarted() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	return m.started
}

func (m *Manager) handleConfigChange(oldConfig, newConfig *ReplicatorConfig) error {
	m.mu.Lock()
	m.config = newConfig
	m.lastReload = time.Now()
	m.reloadCount++
	
	subscribers := make([]ConfigSubscriber, len(m.subscribers))
	copy(subscribers, m.subscribers)
	m.mu.Unlock()

	m.logger.Info("Configuration changed, notifying subscribers",
		zap.Int("subscriber_count", len(subscribers)),
		zap.Int64("reload_count", m.reloadCount))

	var errors []error
	for _, subscriber := range subscribers {
		if err := subscriber.OnConfigChange(oldConfig, newConfig); err != nil {
			m.logger.Error("Subscriber failed to handle configuration change",
				zap.String("subscriber", subscriber.GetSubscriberName()),
				zap.Error(err))
			errors = append(errors, fmt.Errorf("subscriber %s: %w", subscriber.GetSubscriberName(), err))
		} else {
			m.logger.Debug("Subscriber handled configuration change successfully",
				zap.String("subscriber", subscriber.GetSubscriberName()))
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("failed to notify %d subscribers: %v", len(errors), errors)
	}

	return nil
}

func (m *Manager) ValidateCurrentConfig() error {
	config := m.GetConfig()
	if config == nil {
		return fmt.Errorf("no configuration loaded")
	}

	if m.validationFunc != nil {
		return m.validationFunc(config)
	}

	return nil
}

func (m *Manager) SetValidationFunc(fn ValidationFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	m.validationFunc = fn
}

type SimpleSubscriber struct {
	Name    string
	Handler func(oldConfig, newConfig *ReplicatorConfig) error
}

func (s *SimpleSubscriber) OnConfigChange(oldConfig, newConfig *ReplicatorConfig) error {
	if s.Handler != nil {
		return s.Handler(oldConfig, newConfig)
	}
	return nil
}

func (s *SimpleSubscriber) GetSubscriberName() string {
	return s.Name
}