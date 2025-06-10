package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

var (
	envVarPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:-([^}]*))?\}`)
	simpleEnvVarPattern = regexp.MustCompile(`\$([A-Za-z_][A-Za-z0-9_]*)`)
)

type EnvSubstituter struct {
	enableSimpleVars bool
	enableDefaults   bool
	prefix           string
	cache            map[string]string
	cacheEnabled     bool
}

type EnvSubstituterOption func(*EnvSubstituter)

func WithSimpleVars(enable bool) EnvSubstituterOption {
	return func(es *EnvSubstituter) {
		es.enableSimpleVars = enable
	}
}

func WithDefaults(enable bool) EnvSubstituterOption {
	return func(es *EnvSubstituter) {
		es.enableDefaults = enable
	}
}

func WithPrefix(prefix string) EnvSubstituterOption {
	return func(es *EnvSubstituter) {
		es.prefix = prefix
	}
}

func WithCache(enable bool) EnvSubstituterOption {
	return func(es *EnvSubstituter) {
		es.cacheEnabled = enable
		if enable && es.cache == nil {
			es.cache = make(map[string]string)
		}
	}
}

func NewEnvSubstituter(options ...EnvSubstituterOption) *EnvSubstituter {
	es := &EnvSubstituter{
		enableSimpleVars: true,
		enableDefaults:   true,
		cacheEnabled:     false,
	}

	for _, option := range options {
		option(es)
	}

	return es
}

func (es *EnvSubstituter) Substitute(value string) string {
	if value == "" {
		return value
	}

	result := value

	if es.enableDefaults {
		result = es.substituteWithDefaults(result)
	}

	if es.enableSimpleVars {
		result = es.substituteSimpleVars(result)
	}

	return result
}

func (es *EnvSubstituter) substituteWithDefaults(value string) string {
	return envVarPattern.ReplaceAllStringFunc(value, func(match string) string {
		submatch := envVarPattern.FindStringSubmatch(match)
		if len(submatch) < 2 {
			return match
		}

		envVarName := submatch[1]
		defaultValue := ""
		if len(submatch) >= 4 {
			defaultValue = submatch[3]
		}

		envValue := es.getEnvValue(envVarName)
		if envValue == "" {
			return defaultValue
		}

		return envValue
	})
}

func (es *EnvSubstituter) substituteSimpleVars(value string) string {
	return simpleEnvVarPattern.ReplaceAllStringFunc(value, func(match string) string {
		submatch := simpleEnvVarPattern.FindStringSubmatch(match)
		if len(submatch) < 2 {
			return match
		}

		envVarName := submatch[1]
		envValue := es.getEnvValue(envVarName)
		if envValue == "" {
			return match
		}

		return envValue
	})
}

func (es *EnvSubstituter) getEnvValue(name string) string {
	fullName := name
	if es.prefix != "" {
		fullName = es.prefix + "_" + name
	}

	if es.cacheEnabled {
		if cached, exists := es.cache[fullName]; exists {
			return cached
		}
	}

	value := os.Getenv(fullName)
	if value == "" && es.prefix != "" {
		value = os.Getenv(name)
	}

	if es.cacheEnabled {
		es.cache[fullName] = value
	}

	return value
}

func (es *EnvSubstituter) ClearCache() {
	if es.cacheEnabled && es.cache != nil {
		es.cache = make(map[string]string)
	}
}

func (es *EnvSubstituter) SubstituteConfig(config *ReplicatorConfig) error {
	if config == nil {
		return fmt.Errorf("config is nil")
	}

	es.substituteHarborConfig(&config.Harbor)
	es.substituteLoggingConfig(&config.Logging)
	es.substituteStateManagerConfig(&config.StateManager)
	es.substituteServerConfig(&config.Server)

	return nil
}

func (es *EnvSubstituter) substituteHarborConfig(config *HarborConfig) {
	es.substituteHarborInstanceConfig(&config.Source)
	
	for i := range config.Targets {
		es.substituteHarborInstanceConfig(&config.Targets[i])
	}
}

func (es *EnvSubstituter) substituteHarborInstanceConfig(config *HarborInstanceConfig) {
	config.URL = es.Substitute(config.URL)
	config.Username = es.Substitute(config.Username)
	config.Password = es.Substitute(config.Password)
}

func (es *EnvSubstituter) substituteLoggingConfig(config *LoggingConfig) {
	config.Level = es.Substitute(config.Level)
	config.Format = es.Substitute(config.Format)
	config.FilePath = es.Substitute(config.FilePath)
}

func (es *EnvSubstituter) substituteStateManagerConfig(config *StateManagerConfig) {
	config.StorageType = es.Substitute(config.StorageType)
	config.FilePath = es.Substitute(config.FilePath)
	
	config.Redis.Address = es.Substitute(config.Redis.Address)
	config.Redis.Password = es.Substitute(config.Redis.Password)
	
	for i := range config.Etcd.Endpoints {
		config.Etcd.Endpoints[i] = es.Substitute(config.Etcd.Endpoints[i])
	}
	config.Etcd.Username = es.Substitute(config.Etcd.Username)
	config.Etcd.Password = es.Substitute(config.Etcd.Password)
}

func (es *EnvSubstituter) substituteServerConfig(config *ServerConfig) {
	config.Host = es.Substitute(config.Host)
	config.TLS.CertFile = es.Substitute(config.TLS.CertFile)
	config.TLS.KeyFile = es.Substitute(config.TLS.KeyFile)
}

func SubstituteEnvVars(value string) string {
	substituter := NewEnvSubstituter()
	return substituter.Substitute(value)
}

func SubstituteEnvVarsWithDefaults(value string) string {
	substituter := NewEnvSubstituter(
		WithDefaults(true),
		WithSimpleVars(true),
	)
	return substituter.Substitute(value)
}

func SubstituteEnvVarsWithPrefix(value string, prefix string) string {
	substituter := NewEnvSubstituter(
		WithPrefix(prefix),
		WithDefaults(true),
		WithSimpleVars(true),
	)
	return substituter.Substitute(value)
}

func ValidateEnvVars(value string) error {
	matches := envVarPattern.FindAllStringSubmatch(value, -1)
	
	var missingVars []string
	for _, match := range matches {
		if len(match) >= 2 {
			envVarName := match[1]
			hasDefault := len(match) >= 4 && match[3] != ""
			
			if os.Getenv(envVarName) == "" && !hasDefault {
				missingVars = append(missingVars, envVarName)
			}
		}
	}

	if es := simpleEnvVarPattern.FindAllStringSubmatch(value, -1); len(es) > 0 {
		for _, match := range es {
			if len(match) >= 2 {
				envVarName := match[1]
				if os.Getenv(envVarName) == "" {
					missingVars = append(missingVars, envVarName)
				}
			}
		}
	}

	if len(missingVars) > 0 {
		return fmt.Errorf("missing required environment variables: %s", strings.Join(missingVars, ", "))
	}

	return nil
}

func GetRequiredEnvVars(value string) []string {
	var envVars []string
	
	matches := envVarPattern.FindAllStringSubmatch(value, -1)
	for _, match := range matches {
		if len(match) >= 2 {
			envVars = append(envVars, match[1])
		}
	}

	simpleMatches := simpleEnvVarPattern.FindAllStringSubmatch(value, -1)
	for _, match := range simpleMatches {
		if len(match) >= 2 {
			envVars = append(envVars, match[1])
		}
	}

	return envVars
}