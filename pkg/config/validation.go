package config

import (
	"fmt"
	"net/url"
	"strings"
)

type ValidationError struct {
	Field   string
	Message string
}

func (e ValidationError) Error() string {
	return fmt.Sprintf("validation error for field '%s': %s", e.Field, e.Message)
}

type ValidationErrors []ValidationError

func (e ValidationErrors) Error() string {
	if len(e) == 0 {
		return ""
	}
	if len(e) == 1 {
		return e[0].Error()
	}
	
	var messages []string
	for _, err := range e {
		messages = append(messages, err.Error())
	}
	return fmt.Sprintf("multiple validation errors: %s", strings.Join(messages, "; "))
}

func ValidateConfig(config *ReplicatorConfig) error {
	var errors ValidationErrors

	if err := validateHarborConfig(&config.Harbor); err != nil {
		if validationErrs, ok := err.(ValidationErrors); ok {
			errors = append(errors, validationErrs...)
		} else {
			errors = append(errors, ValidationError{Field: "harbor", Message: err.Error()})
		}
	}

	if err := validateSyncConfig(&config.Sync); err != nil {
		if validationErrs, ok := err.(ValidationErrors); ok {
			errors = append(errors, validationErrs...)
		} else {
			errors = append(errors, ValidationError{Field: "sync", Message: err.Error()})
		}
	}

	if err := validateLoggingConfig(&config.Logging); err != nil {
		if validationErrs, ok := err.(ValidationErrors); ok {
			errors = append(errors, validationErrs...)
		} else {
			errors = append(errors, ValidationError{Field: "logging", Message: err.Error()})
		}
	}

	if err := validateMonitoringConfig(&config.Monitoring); err != nil {
		if validationErrs, ok := err.(ValidationErrors); ok {
			errors = append(errors, validationErrs...)
		} else {
			errors = append(errors, ValidationError{Field: "monitoring", Message: err.Error()})
		}
	}

	if err := validateServerConfig(&config.Server); err != nil {
		if validationErrs, ok := err.(ValidationErrors); ok {
			errors = append(errors, validationErrs...)
		} else {
			errors = append(errors, ValidationError{Field: "server", Message: err.Error()})
		}
	}

	if err := validateStateManagerConfig(&config.StateManager); err != nil {
		if validationErrs, ok := err.(ValidationErrors); ok {
			errors = append(errors, validationErrs...)
		} else {
			errors = append(errors, ValidationError{Field: "state_manager", Message: err.Error()})
		}
	}

	if len(errors) > 0 {
		return errors
	}
	return nil
}

func validateHarborConfig(config *HarborConfig) error {
	var errors ValidationErrors

	if err := validateHarborInstanceConfig(&config.Source, "harbor.source"); err != nil {
		if validationErrs, ok := err.(ValidationErrors); ok {
			errors = append(errors, validationErrs...)
		} else {
			errors = append(errors, ValidationError{Field: "harbor.source", Message: err.Error()})
		}
	}

	if len(config.Targets) == 0 {
		errors = append(errors, ValidationError{
			Field:   "harbor.targets",
			Message: "at least one target Harbor instance must be configured",
		})
	}

	for i, target := range config.Targets {
		fieldPrefix := fmt.Sprintf("harbor.targets[%d]", i)
		if err := validateHarborInstanceConfig(&target, fieldPrefix); err != nil {
			if validationErrs, ok := err.(ValidationErrors); ok {
				errors = append(errors, validationErrs...)
			} else {
				errors = append(errors, ValidationError{Field: fieldPrefix, Message: err.Error()})
			}
		}

		if target.Name == "" {
			errors = append(errors, ValidationError{
				Field:   fmt.Sprintf("%s.name", fieldPrefix),
				Message: "target name is required",
			})
		}
	}

	targetNames := make(map[string]bool)
	for i, target := range config.Targets {
		if target.Name != "" {
			if targetNames[target.Name] {
				errors = append(errors, ValidationError{
					Field:   fmt.Sprintf("harbor.targets[%d].name", i),
					Message: fmt.Sprintf("duplicate target name: %s", target.Name),
				})
			}
			targetNames[target.Name] = true
		}
	}

	if len(errors) > 0 {
		return errors
	}
	return nil
}

func validateHarborInstanceConfig(config *HarborInstanceConfig, fieldPrefix string) error {
	var errors ValidationErrors

	if config.URL == "" {
		errors = append(errors, ValidationError{
			Field:   fmt.Sprintf("%s.url", fieldPrefix),
			Message: "URL is required",
		})
	} else {
		if _, err := url.Parse(config.URL); err != nil {
			errors = append(errors, ValidationError{
				Field:   fmt.Sprintf("%s.url", fieldPrefix),
				Message: fmt.Sprintf("invalid URL format: %v", err),
			})
		}
	}

	if config.Username == "" {
		errors = append(errors, ValidationError{
			Field:   fmt.Sprintf("%s.username", fieldPrefix),
			Message: "username is required",
		})
	}

	if config.Password == "" {
		errors = append(errors, ValidationError{
			Field:   fmt.Sprintf("%s.password", fieldPrefix),
			Message: "password is required",
		})
	}

	if config.Timeout <= 0 {
		errors = append(errors, ValidationError{
			Field:   fmt.Sprintf("%s.timeout", fieldPrefix),
			Message: "timeout must be greater than 0",
		})
	}

	if err := validateRateLimitConfig(&config.RateLimit, fmt.Sprintf("%s.rate_limit", fieldPrefix)); err != nil {
		if validationErrs, ok := err.(ValidationErrors); ok {
			errors = append(errors, validationErrs...)
		} else {
			errors = append(errors, ValidationError{Field: fmt.Sprintf("%s.rate_limit", fieldPrefix), Message: err.Error()})
		}
	}

	if err := validateRetryConfig(&config.Retry, fmt.Sprintf("%s.retry", fieldPrefix)); err != nil {
		if validationErrs, ok := err.(ValidationErrors); ok {
			errors = append(errors, validationErrs...)
		} else {
			errors = append(errors, ValidationError{Field: fmt.Sprintf("%s.retry", fieldPrefix), Message: err.Error()})
		}
	}

	if len(errors) > 0 {
		return errors
	}
	return nil
}

func validateRateLimitConfig(config *RateLimitConfig, fieldPrefix string) error {
	var errors ValidationErrors

	if config.RequestsPerSecond <= 0 {
		errors = append(errors, ValidationError{
			Field:   fmt.Sprintf("%s.requests_per_second", fieldPrefix),
			Message: "requests per second must be greater than 0",
		})
	}

	if config.BurstSize <= 0 {
		errors = append(errors, ValidationError{
			Field:   fmt.Sprintf("%s.burst_size", fieldPrefix),
			Message: "burst size must be greater than 0",
		})
	}

	if config.Timeout <= 0 {
		errors = append(errors, ValidationError{
			Field:   fmt.Sprintf("%s.timeout", fieldPrefix),
			Message: "timeout must be greater than 0",
		})
	}

	if len(errors) > 0 {
		return errors
	}
	return nil
}

func validateRetryConfig(config *RetryConfig, fieldPrefix string) error {
	var errors ValidationErrors

	if config.MaxAttempts < 0 {
		errors = append(errors, ValidationError{
			Field:   fmt.Sprintf("%s.max_attempts", fieldPrefix),
			Message: "max attempts must be 0 or greater",
		})
	}

	if config.InitialDelay <= 0 {
		errors = append(errors, ValidationError{
			Field:   fmt.Sprintf("%s.initial_delay", fieldPrefix),
			Message: "initial delay must be greater than 0",
		})
	}

	if config.MaxDelay <= 0 {
		errors = append(errors, ValidationError{
			Field:   fmt.Sprintf("%s.max_delay", fieldPrefix),
			Message: "max delay must be greater than 0",
		})
	}

	if config.InitialDelay > config.MaxDelay {
		errors = append(errors, ValidationError{
			Field:   fmt.Sprintf("%s.initial_delay", fieldPrefix),
			Message: "initial delay must be less than or equal to max delay",
		})
	}

	if config.Multiplier <= 1 {
		errors = append(errors, ValidationError{
			Field:   fmt.Sprintf("%s.multiplier", fieldPrefix),
			Message: "multiplier must be greater than 1",
		})
	}

	if len(errors) > 0 {
		return errors
	}
	return nil
}

func validateSyncConfig(config *SyncConfig, ) error {
	var errors ValidationErrors

	if config.Interval <= 0 {
		errors = append(errors, ValidationError{
			Field:   "sync.interval",
			Message: "sync interval must be greater than 0",
		})
	}

	if config.Concurrency <= 0 {
		errors = append(errors, ValidationError{
			Field:   "sync.concurrency",
			Message: "concurrency must be greater than 0",
		})
	}

	validStrategies := map[string]bool{
		"source_wins": true,
		"target_wins": true,
		"manual":      true,
		"skip":        true,
	}

	if !validStrategies[config.ConflictResolution.Strategy] {
		errors = append(errors, ValidationError{
			Field:   "sync.conflict_resolution.strategy",
			Message: "strategy must be one of: source_wins, target_wins, manual, skip",
		})
	}

	if len(errors) > 0 {
		return errors
	}
	return nil
}

func validateLoggingConfig(config *LoggingConfig) error {
	var errors ValidationErrors

	validLevels := map[string]bool{
		"debug": true,
		"info":  true,
		"warn":  true,
		"error": true,
		"fatal": true,
	}

	if !validLevels[strings.ToLower(config.Level)] {
		errors = append(errors, ValidationError{
			Field:   "logging.level",
			Message: "level must be one of: debug, info, warn, error, fatal",
		})
	}

	validFormats := map[string]bool{
		"json":    true,
		"console": true,
	}

	if !validFormats[config.Format] {
		errors = append(errors, ValidationError{
			Field:   "logging.format",
			Message: "format must be one of: json, console",
		})
	}

	if len(config.Output) == 0 {
		errors = append(errors, ValidationError{
			Field:   "logging.output",
			Message: "at least one output must be specified",
		})
	}

	validOutputs := map[string]bool{
		"stdout": true,
		"stderr": true,
		"file":   true,
	}

	for _, output := range config.Output {
		if !validOutputs[output] {
			errors = append(errors, ValidationError{
				Field:   "logging.output",
				Message: fmt.Sprintf("invalid output '%s': must be one of stdout, stderr, file", output),
			})
		}
	}

	hasFileOutput := false
	for _, output := range config.Output {
		if output == "file" {
			hasFileOutput = true
			break
		}
	}

	if hasFileOutput && config.FilePath == "" {
		errors = append(errors, ValidationError{
			Field:   "logging.file_path",
			Message: "file_path is required when file output is specified",
		})
	}

	if config.MaxSize <= 0 {
		errors = append(errors, ValidationError{
			Field:   "logging.max_size",
			Message: "max_size must be greater than 0",
		})
	}

	if config.MaxBackups < 0 {
		errors = append(errors, ValidationError{
			Field:   "logging.max_backups",
			Message: "max_backups must be 0 or greater",
		})
	}

	if config.MaxAge < 0 {
		errors = append(errors, ValidationError{
			Field:   "logging.max_age",
			Message: "max_age must be 0 or greater",
		})
	}

	if len(errors) > 0 {
		return errors
	}
	return nil
}

func validateMonitoringConfig(config *MonitoringConfig) error {
	var errors ValidationErrors

	if config.Prometheus.Enabled {
		if config.Prometheus.Port <= 0 || config.Prometheus.Port > 65535 {
			errors = append(errors, ValidationError{
				Field:   "monitoring.prometheus.port",
				Message: "port must be between 1 and 65535",
			})
		}

		if config.Prometheus.Path == "" {
			errors = append(errors, ValidationError{
				Field:   "monitoring.prometheus.path",
				Message: "prometheus path is required when prometheus is enabled",
			})
		}
	}

	if config.Health.Enabled {
		if config.Health.Port <= 0 || config.Health.Port > 65535 {
			errors = append(errors, ValidationError{
				Field:   "monitoring.health.port",
				Message: "port must be between 1 and 65535",
			})
		}

		if config.Health.Path == "" {
			errors = append(errors, ValidationError{
				Field:   "monitoring.health.path",
				Message: "health path is required when health checks are enabled",
			})
		}

		if config.Health.ReadyPath == "" {
			errors = append(errors, ValidationError{
				Field:   "monitoring.health.ready_path",
				Message: "ready path is required when health checks are enabled",
			})
		}

		if config.Health.LivePath == "" {
			errors = append(errors, ValidationError{
				Field:   "monitoring.health.live_path",
				Message: "live path is required when health checks are enabled",
			})
		}
	}

	if len(errors) > 0 {
		return errors
	}
	return nil
}

func validateServerConfig(config *ServerConfig) error {
	var errors ValidationErrors

	if config.Port <= 0 || config.Port > 65535 {
		errors = append(errors, ValidationError{
			Field:   "server.port",
			Message: "port must be between 1 and 65535",
		})
	}

	if config.ReadTimeout <= 0 {
		errors = append(errors, ValidationError{
			Field:   "server.read_timeout",
			Message: "read timeout must be greater than 0",
		})
	}

	if config.WriteTimeout <= 0 {
		errors = append(errors, ValidationError{
			Field:   "server.write_timeout",
			Message: "write timeout must be greater than 0",
		})
	}

	if config.IdleTimeout <= 0 {
		errors = append(errors, ValidationError{
			Field:   "server.idle_timeout",
			Message: "idle timeout must be greater than 0",
		})
	}

	if config.ShutdownTimeout <= 0 {
		errors = append(errors, ValidationError{
			Field:   "server.shutdown_timeout",
			Message: "shutdown timeout must be greater than 0",
		})
	}

	if config.TLS.Enabled {
		if config.TLS.CertFile == "" {
			errors = append(errors, ValidationError{
				Field:   "server.tls.cert_file",
				Message: "cert file is required when TLS is enabled",
			})
		}

		if config.TLS.KeyFile == "" {
			errors = append(errors, ValidationError{
				Field:   "server.tls.key_file",
				Message: "key file is required when TLS is enabled",
			})
		}
	}

	if len(errors) > 0 {
		return errors
	}
	return nil
}

func validateStateManagerConfig(config *StateManagerConfig) error {
	var errors ValidationErrors

	validStorageTypes := map[string]bool{
		"file":  true,
		"redis": true,
		"etcd":  true,
	}

	if !validStorageTypes[config.StorageType] {
		errors = append(errors, ValidationError{
			Field:   "state_manager.storage_type",
			Message: "storage type must be one of: file, redis, etcd",
		})
	}

	if config.StorageType == "file" && config.FilePath == "" {
		errors = append(errors, ValidationError{
			Field:   "state_manager.file_path",
			Message: "file path is required when storage type is file",
		})
	}

	if config.BackupInterval <= 0 {
		errors = append(errors, ValidationError{
			Field:   "state_manager.backup_interval",
			Message: "backup interval must be greater than 0",
		})
	}

	if config.BackupRetention <= 0 {
		errors = append(errors, ValidationError{
			Field:   "state_manager.backup_retention",
			Message: "backup retention must be greater than 0",
		})
	}

	if config.StorageType == "redis" {
		if config.Redis.Address == "" {
			errors = append(errors, ValidationError{
				Field:   "state_manager.redis.address",
				Message: "redis address is required when storage type is redis",
			})
		}
	}

	if config.StorageType == "etcd" {
		if len(config.Etcd.Endpoints) == 0 {
			errors = append(errors, ValidationError{
				Field:   "state_manager.etcd.endpoints",
				Message: "etcd endpoints are required when storage type is etcd",
			})
		}
	}

	if len(errors) > 0 {
		return errors
	}
	return nil
}