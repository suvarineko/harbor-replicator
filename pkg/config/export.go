package config

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type ExportFormat string

const (
	FormatYAML ExportFormat = "yaml"
	FormatJSON ExportFormat = "json"
)

type ExportOptions struct {
	Format         ExportFormat
	MaskSecrets    bool
	IncludeDefaults bool
	PrettyPrint    bool
	SanitizeFields []string
}

type ImportOptions struct {
	ValidateOnly      bool
	MergeWithExisting bool
	BackupExisting    bool
	BackupPath        string
}

type ConfigExporter struct {
	secretFields []string
}

func NewConfigExporter() *ConfigExporter {
	return &ConfigExporter{
		secretFields: []string{
			"password",
			"token",
			"secret",
			"key",
			"credential",
			"auth",
		},
	}
}

func (ce *ConfigExporter) ExportConfig(config *ReplicatorConfig, options ExportOptions) ([]byte, error) {
	if config == nil {
		return nil, fmt.Errorf("config is nil")
	}

	exportConfig := *config

	if options.MaskSecrets {
		ce.maskSecrets(&exportConfig, options.SanitizeFields)
	}

	switch options.Format {
	case FormatYAML:
		return ce.exportYAML(&exportConfig, options)
	case FormatJSON:
		return ce.exportJSON(&exportConfig, options)
	default:
		return nil, fmt.Errorf("unsupported export format: %s", options.Format)
	}
}

func (ce *ConfigExporter) ExportToFile(config *ReplicatorConfig, filePath string, options ExportOptions) error {
	data, err := ce.ExportConfig(config, options)
	if err != nil {
		return fmt.Errorf("failed to export config: %w", err)
	}

	if err := os.WriteFile(filePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write config to file %s: %w", filePath, err)
	}

	return nil
}

func (ce *ConfigExporter) ImportConfig(data []byte, format ExportFormat, options ImportOptions) (*ReplicatorConfig, error) {
	var config ReplicatorConfig

	switch format {
	case FormatYAML:
		if err := yaml.Unmarshal(data, &config); err != nil {
			return nil, fmt.Errorf("failed to unmarshal YAML config: %w", err)
		}
	case FormatJSON:
		if err := json.Unmarshal(data, &config); err != nil {
			return nil, fmt.Errorf("failed to unmarshal JSON config: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported import format: %s", format)
	}

	if options.ValidateOnly {
		return &config, ValidateConfig(&config)
	}

	if err := ValidateConfig(&config); err != nil {
		return nil, fmt.Errorf("imported config validation failed: %w", err)
	}

	return &config, nil
}

func (ce *ConfigExporter) ImportFromFile(filePath string, format ExportFormat, options ImportOptions) (*ReplicatorConfig, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", filePath, err)
	}

	return ce.ImportConfig(data, format, options)
}

func (ce *ConfigExporter) exportYAML(config *ReplicatorConfig, options ExportOptions) ([]byte, error) {
	if options.PrettyPrint {
		return yaml.Marshal(config)
	}

	var buf strings.Builder
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	
	if err := encoder.Encode(config); err != nil {
		return nil, fmt.Errorf("failed to encode YAML: %w", err)
	}
	
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("failed to close YAML encoder: %w", err)
	}

	return []byte(buf.String()), nil
}

func (ce *ConfigExporter) exportJSON(config *ReplicatorConfig, options ExportOptions) ([]byte, error) {
	if options.PrettyPrint {
		return json.MarshalIndent(config, "", "  ")
	}

	return json.Marshal(config)
}

func (ce *ConfigExporter) maskSecrets(config *ReplicatorConfig, additionalFields []string) {
	fieldsToMask := append(ce.secretFields, additionalFields...)
	
	ce.maskSecretsInStruct(reflect.ValueOf(config).Elem(), fieldsToMask)
}

func (ce *ConfigExporter) maskSecretsInStruct(v reflect.Value, fieldsToMask []string) {
	if !v.IsValid() || !v.CanSet() {
		return
	}

	switch v.Kind() {
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			field := v.Field(i)
			fieldType := v.Type().Field(i)
			
			if !field.CanSet() {
				continue
			}

			fieldName := strings.ToLower(fieldType.Name)
			yamlTag := fieldType.Tag.Get("yaml")
			mapstructureTag := fieldType.Tag.Get("mapstructure")

			shouldMask := false
			for _, maskField := range fieldsToMask {
				if strings.Contains(fieldName, strings.ToLower(maskField)) ||
					strings.Contains(yamlTag, strings.ToLower(maskField)) ||
					strings.Contains(mapstructureTag, strings.ToLower(maskField)) {
					shouldMask = true
					break
				}
			}

			if shouldMask && field.Kind() == reflect.String && field.String() != "" {
				field.SetString("***MASKED***")
			} else {
				ce.maskSecretsInStruct(field, fieldsToMask)
			}
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			ce.maskSecretsInStruct(v.Index(i), fieldsToMask)
		}
	case reflect.Ptr:
		if !v.IsNil() {
			ce.maskSecretsInStruct(v.Elem(), fieldsToMask)
		}
	}
}

func (ce *ConfigExporter) CreateBackup(config *ReplicatorConfig, backupPath string) error {
	if backupPath == "" {
		backupPath = fmt.Sprintf("config-backup-%d.yaml", time.Now().Unix())
	}

	options := ExportOptions{
		Format:      FormatYAML,
		MaskSecrets: false,
		PrettyPrint: true,
	}

	return ce.ExportToFile(config, backupPath, options)
}

func MergeConfigs(base, override *ReplicatorConfig) *ReplicatorConfig {
	if base == nil && override == nil {
		return nil
	}
	if base == nil {
		result := *override
		return &result
	}
	if override == nil {
		result := *base
		return &result
	}

	merged := *base

	mergeHarborConfig(&merged.Harbor, &override.Harbor)
	mergeSyncConfig(&merged.Sync, &override.Sync)
	mergeLoggingConfig(&merged.Logging, &override.Logging)
	mergeMonitoringConfig(&merged.Monitoring, &override.Monitoring)
	mergeServerConfig(&merged.Server, &override.Server)
	mergeStateManagerConfig(&merged.StateManager, &override.StateManager)

	return &merged
}

func mergeHarborConfig(base, override *HarborConfig) {
	if override.Source.URL != "" {
		base.Source = override.Source
	}
	if len(override.Targets) > 0 {
		base.Targets = override.Targets
	}
}

func mergeSyncConfig(base, override *SyncConfig) {
	if override.Interval != 0 {
		base.Interval = override.Interval
	}
	if override.Concurrency != 0 {
		base.Concurrency = override.Concurrency
	}
	if override.ConflictResolution.Strategy != "" {
		base.ConflictResolution = override.ConflictResolution
	}
}

func mergeLoggingConfig(base, override *LoggingConfig) {
	if override.Level != "" {
		base.Level = override.Level
	}
	if override.Format != "" {
		base.Format = override.Format
	}
	if len(override.Output) > 0 {
		base.Output = override.Output
	}
	if override.FilePath != "" {
		base.FilePath = override.FilePath
	}
}

func mergeMonitoringConfig(base, override *MonitoringConfig) {
	if override.Prometheus.Port != 0 {
		base.Prometheus = override.Prometheus
	}
	if override.Health.Port != 0 {
		base.Health = override.Health
	}
}

func mergeServerConfig(base, override *ServerConfig) {
	if override.Host != "" {
		base.Host = override.Host
	}
	if override.Port != 0 {
		base.Port = override.Port
	}
	if override.ReadTimeout != 0 {
		base.ReadTimeout = override.ReadTimeout
	}
	if override.WriteTimeout != 0 {
		base.WriteTimeout = override.WriteTimeout
	}
}

func mergeStateManagerConfig(base, override *StateManagerConfig) {
	if override.StorageType != "" {
		base.StorageType = override.StorageType
	}
	if override.FilePath != "" {
		base.FilePath = override.FilePath
	}
	if override.BackupInterval != 0 {
		base.BackupInterval = override.BackupInterval
	}
}

func ValidateImportedConfig(config *ReplicatorConfig, requiredFields []string) error {
	if config == nil {
		return fmt.Errorf("config is nil")
	}

	for _, field := range requiredFields {
		if !hasRequiredField(config, field) {
			return fmt.Errorf("required field '%s' is missing or empty", field)
		}
	}

	return ValidateConfig(config)
}

func hasRequiredField(config *ReplicatorConfig, fieldPath string) bool {
	parts := strings.Split(fieldPath, ".")
	v := reflect.ValueOf(config).Elem()

	for _, part := range parts {
		if v.Kind() == reflect.Ptr {
			if v.IsNil() {
				return false
			}
			v = v.Elem()
		}

		if v.Kind() != reflect.Struct {
			return false
		}

		found := false
		for i := 0; i < v.NumField(); i++ {
			fieldType := v.Type().Field(i)
			if strings.EqualFold(fieldType.Name, part) {
				v = v.Field(i)
				found = true
				break
			}
		}

		if !found {
			return false
		}
	}

	if v.Kind() == reflect.String {
		return v.String() != ""
	}

	return !v.IsZero()
}