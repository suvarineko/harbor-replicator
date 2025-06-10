package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/viper"
)

func LoadConfig(configPath string) (*ReplicatorConfig, error) {
	v := viper.New()

	v.SetDefault("harbor.source.timeout", 30*time.Second)
	v.SetDefault("harbor.source.rate_limit.requests_per_second", 10.0)
	v.SetDefault("harbor.source.rate_limit.burst_size", 20)
	v.SetDefault("harbor.source.rate_limit.timeout", 5*time.Second)
	v.SetDefault("harbor.source.circuit_breaker.enabled", true)
	v.SetDefault("harbor.source.circuit_breaker.max_requests", 10)
	v.SetDefault("harbor.source.circuit_breaker.interval", 60*time.Second)
	v.SetDefault("harbor.source.circuit_breaker.timeout", 60*time.Second)
	v.SetDefault("harbor.source.circuit_breaker.failure_threshold", 5)
	v.SetDefault("harbor.source.circuit_breaker.success_threshold", 2)
	v.SetDefault("harbor.source.retry.max_attempts", 3)
	v.SetDefault("harbor.source.retry.initial_delay", 1*time.Second)
	v.SetDefault("harbor.source.retry.max_delay", 30*time.Second)
	v.SetDefault("harbor.source.retry.multiplier", 2.0)
	v.SetDefault("harbor.source.retry.jitter", true)

	v.SetDefault("sync.interval", 10*time.Minute)
	v.SetDefault("sync.concurrency", 5)
	v.SetDefault("sync.dry_run", false)
	v.SetDefault("sync.resources.robot_accounts.enabled", true)
	v.SetDefault("sync.resources.robot_accounts.system_level.enabled", true)
	v.SetDefault("sync.resources.robot_accounts.project_level.enabled", true)
	v.SetDefault("sync.resources.oidc_groups.enabled", true)
	v.SetDefault("sync.resources.oidc_groups.sync_permissions", true)
	v.SetDefault("sync.resources.oidc_groups.sync_project_associations", true)
	v.SetDefault("sync.conflict_resolution.strategy", "source_wins")
	v.SetDefault("sync.conflict_resolution.backup_before", true)

	v.SetDefault("logging.level", "info")
	v.SetDefault("logging.format", "json")
	v.SetDefault("logging.output", []string{"stdout"})
	v.SetDefault("logging.max_size", 100)
	v.SetDefault("logging.max_backups", 5)
	v.SetDefault("logging.max_age", 30)
	v.SetDefault("logging.compress", true)
	v.SetDefault("logging.sanitize_fields", []string{"password", "token", "secret", "key"})
	v.SetDefault("logging.correlation_id.enabled", true)
	v.SetDefault("logging.correlation_id.header", "X-Correlation-ID")
	v.SetDefault("logging.correlation_id.field_name", "correlation_id")
	v.SetDefault("logging.correlation_id.generator", "uuid")

	v.SetDefault("monitoring.enabled", true)
	v.SetDefault("monitoring.prometheus.enabled", true)
	v.SetDefault("monitoring.prometheus.path", "/metrics")
	v.SetDefault("monitoring.prometheus.port", 9090)
	v.SetDefault("monitoring.prometheus.namespace", "harbor_replicator")
	v.SetDefault("monitoring.prometheus.subsystem", "sync")
	v.SetDefault("monitoring.health.enabled", true)
	v.SetDefault("monitoring.health.path", "/health")
	v.SetDefault("monitoring.health.port", 8080)
	v.SetDefault("monitoring.health.ready_path", "/ready")
	v.SetDefault("monitoring.health.live_path", "/live")

	v.SetDefault("server.host", "0.0.0.0")
	v.SetDefault("server.port", 8080)
	v.SetDefault("server.read_timeout", 30*time.Second)
	v.SetDefault("server.write_timeout", 30*time.Second)
	v.SetDefault("server.idle_timeout", 60*time.Second)
	v.SetDefault("server.shutdown_timeout", 10*time.Second)
	v.SetDefault("server.tls.enabled", false)

	v.SetDefault("state_manager.storage_type", "file")
	v.SetDefault("state_manager.file_path", "./state/replicator-state.json")
	v.SetDefault("state_manager.backup_interval", 1*time.Hour)
	v.SetDefault("state_manager.backup_retention", 24)

	v.AutomaticEnv()
	v.SetEnvPrefix("HARBOR_REPLICATOR")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	if configPath != "" {
		if !filepath.IsAbs(configPath) {
			wd, err := os.Getwd()
			if err != nil {
				return nil, fmt.Errorf("failed to get working directory: %w", err)
			}
			configPath = filepath.Join(wd, configPath)
		}

		if _, err := os.Stat(configPath); os.IsNotExist(err) {
			return nil, fmt.Errorf("configuration file does not exist: %s", configPath)
		}

		v.SetConfigFile(configPath)
		if err := v.ReadInConfig(); err != nil {
			return nil, fmt.Errorf("failed to read configuration file %s: %w", configPath, err)
		}
	}

	var config ReplicatorConfig
	if err := v.Unmarshal(&config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal configuration: %w", err)
	}

	applyTargetDefaults(&config)

	return &config, nil
}

func applyTargetDefaults(config *ReplicatorConfig) {
	for i := range config.Harbor.Targets {
		target := &config.Harbor.Targets[i]
		
		if target.Timeout == 0 {
			target.Timeout = config.Harbor.Source.Timeout
		}
		if target.RateLimit.RequestsPerSecond == 0 {
			target.RateLimit = config.Harbor.Source.RateLimit
		}
		if !target.CircuitBreaker.Enabled {
			target.CircuitBreaker = config.Harbor.Source.CircuitBreaker
		}
		if target.Retry.MaxAttempts == 0 {
			target.Retry = config.Harbor.Source.Retry
		}
	}
}

func ExpandEnvVars(value string) string {
	return os.ExpandEnv(value)
}

func SetDefaults(v *viper.Viper) {
	defaults := map[string]interface{}{
		"harbor.source.timeout":    30 * time.Second,
		"sync.interval":           10 * time.Minute,
		"logging.level":           "info",
		"monitoring.enabled":      true,
		"server.port":            8080,
		"state_manager.storage_type": "file",
	}

	for key, value := range defaults {
		v.SetDefault(key, value)
	}
}