package config

import (
	"time"
)

type ReplicatorConfig struct {
	Harbor       HarborConfig       `mapstructure:"harbor" yaml:"harbor"`
	Sync         SyncConfig         `mapstructure:"sync" yaml:"sync"`
	Logging      LoggingConfig      `mapstructure:"logging" yaml:"logging"`
	Monitoring   MonitoringConfig   `mapstructure:"monitoring" yaml:"monitoring"`
	Server       ServerConfig       `mapstructure:"server" yaml:"server"`
	StateManager StateManagerConfig `mapstructure:"state_manager" yaml:"state_manager"`
}

type HarborConfig struct {
	Source  HarborInstanceConfig   `mapstructure:"source" yaml:"source"`
	Targets []HarborInstanceConfig `mapstructure:"targets" yaml:"targets"`
}

type HarborInstanceConfig struct {
	Name                 string          `mapstructure:"name" yaml:"name"`
	URL                  string          `mapstructure:"url" yaml:"url"`
	Username             string          `mapstructure:"username" yaml:"username"`
	Password             string          `mapstructure:"password" yaml:"password"`
	Insecure             bool            `mapstructure:"insecure" yaml:"insecure"`
	InsecureSkipVerify   bool            `mapstructure:"insecure_skip_verify" yaml:"insecure_skip_verify"`
	Timeout              time.Duration   `mapstructure:"timeout" yaml:"timeout"`
	MaxIdleConns         int             `mapstructure:"max_idle_conns" yaml:"max_idle_conns"`
	MaxIdleConnsPerHost  int             `mapstructure:"max_idle_conns_per_host" yaml:"max_idle_conns_per_host"`
	RateLimit            RateLimitConfig `mapstructure:"rate_limit" yaml:"rate_limit"`
	CircuitBreaker       CircuitBreakerConfig `mapstructure:"circuit_breaker" yaml:"circuit_breaker"`
	Retry                RetryConfig     `mapstructure:"retry" yaml:"retry"`
}

type RateLimitConfig struct {
	RequestsPerSecond float64       `mapstructure:"requests_per_second" yaml:"requests_per_second"`
	BurstSize         int           `mapstructure:"burst_size" yaml:"burst_size"`
	Timeout           time.Duration `mapstructure:"timeout" yaml:"timeout"`
}

type CircuitBreakerConfig struct {
	Name              string        `mapstructure:"name" yaml:"name"`
	Enabled           bool          `mapstructure:"enabled" yaml:"enabled"`
	MaxRequests       uint32        `mapstructure:"max_requests" yaml:"max_requests"`
	Interval          time.Duration `mapstructure:"interval" yaml:"interval"`
	Timeout           time.Duration `mapstructure:"timeout" yaml:"timeout"`
	ReadyToTrip       func(counts CircuitBreakerCounts) bool `mapstructure:"-" yaml:"-"`
	OnStateChange     func(name string, from CircuitBreakerState, to CircuitBreakerState) `mapstructure:"-" yaml:"-"`
	FailureThreshold  uint32        `mapstructure:"failure_threshold" yaml:"failure_threshold"`
	SuccessThreshold  uint32        `mapstructure:"success_threshold" yaml:"success_threshold"`
}

type CircuitBreakerCounts struct {
	Requests             uint32
	TotalSuccesses       uint32
	TotalFailures        uint32
	ConsecutiveSuccesses uint32
	ConsecutiveFailures  uint32
}

func (c *CircuitBreakerCounts) OnRequest() {
	c.Requests++
}

func (c *CircuitBreakerCounts) OnSuccess() {
	c.TotalSuccesses++
	c.ConsecutiveSuccesses++
	c.ConsecutiveFailures = 0
}

func (c *CircuitBreakerCounts) OnFailure() {
	c.TotalFailures++
	c.ConsecutiveFailures++
	c.ConsecutiveSuccesses = 0
}

func (c *CircuitBreakerCounts) Clear() {
	c.Requests = 0
	c.TotalSuccesses = 0
	c.TotalFailures = 0
	c.ConsecutiveSuccesses = 0
	c.ConsecutiveFailures = 0
}

type CircuitBreakerState int

const (
	CircuitBreakerClosed CircuitBreakerState = iota
	CircuitBreakerHalfOpen
	CircuitBreakerOpen
)

func (s CircuitBreakerState) String() string {
	switch s {
	case CircuitBreakerClosed:
		return "closed"
	case CircuitBreakerHalfOpen:
		return "half-open"
	case CircuitBreakerOpen:
		return "open"
	default:
		return "unknown"
	}
}

type RetryConfig struct {
	MaxAttempts     int           `mapstructure:"max_attempts" yaml:"max_attempts"`
	InitialDelay    time.Duration `mapstructure:"initial_delay" yaml:"initial_delay"`
	MaxDelay        time.Duration `mapstructure:"max_delay" yaml:"max_delay"`
	Multiplier      float64       `mapstructure:"multiplier" yaml:"multiplier"`
	Jitter          bool          `mapstructure:"jitter" yaml:"jitter"`
	RetryableErrors []string      `mapstructure:"retryable_errors" yaml:"retryable_errors"`
}

type SyncConfig struct {
	Interval        time.Duration       `mapstructure:"interval" yaml:"interval"`
	Resources       SyncResourcesConfig `mapstructure:"resources" yaml:"resources"`
	Concurrency     int                 `mapstructure:"concurrency" yaml:"concurrency"`
	DryRun          bool                `mapstructure:"dry_run" yaml:"dry_run"`
	ConflictResolution ConflictResolutionConfig `mapstructure:"conflict_resolution" yaml:"conflict_resolution"`
	Filters         FilterConfig        `mapstructure:"filters" yaml:"filters"`
}

type SyncResourcesConfig struct {
	RobotAccounts RobotAccountSyncConfig `mapstructure:"robot_accounts" yaml:"robot_accounts"`
	OIDCGroups    OIDCGroupSyncConfig    `mapstructure:"oidc_groups" yaml:"oidc_groups"`
}

type RobotAccountSyncConfig struct {
	Enabled     bool                  `mapstructure:"enabled" yaml:"enabled"`
	SystemLevel SystemLevelRobotConfig `mapstructure:"system_level" yaml:"system_level"`
	ProjectLevel ProjectLevelRobotConfig `mapstructure:"project_level" yaml:"project_level"`
}

type SystemLevelRobotConfig struct {
	Enabled    bool     `mapstructure:"enabled" yaml:"enabled"`
	NameFilter []string `mapstructure:"name_filter" yaml:"name_filter"`
	Exclude    []string `mapstructure:"exclude" yaml:"exclude"`
}

type ProjectLevelRobotConfig struct {
	Enabled      bool     `mapstructure:"enabled" yaml:"enabled"`
	ProjectFilter []string `mapstructure:"project_filter" yaml:"project_filter"`
	NameFilter   []string `mapstructure:"name_filter" yaml:"name_filter"`
	Exclude      []string `mapstructure:"exclude" yaml:"exclude"`
}

type OIDCGroupSyncConfig struct {
	Enabled       bool     `mapstructure:"enabled" yaml:"enabled"`
	GroupFilter   []string `mapstructure:"group_filter" yaml:"group_filter"`
	Exclude       []string `mapstructure:"exclude" yaml:"exclude"`
	SyncPermissions bool   `mapstructure:"sync_permissions" yaml:"sync_permissions"`
	SyncProjectAssociations bool `mapstructure:"sync_project_associations" yaml:"sync_project_associations"`
}

type ConflictResolutionConfig struct {
	Strategy     string `mapstructure:"strategy" yaml:"strategy"` // "source_wins", "target_wins", "manual", "skip"
	BackupBefore bool   `mapstructure:"backup_before" yaml:"backup_before"`
}

type FilterConfig struct {
	IncludePatterns []string `mapstructure:"include_patterns" yaml:"include_patterns"`
	ExcludePatterns []string `mapstructure:"exclude_patterns" yaml:"exclude_patterns"`
}

type LoggingConfig struct {
	Level          string            `mapstructure:"level" yaml:"level"`
	Format         string            `mapstructure:"format" yaml:"format"` // "json", "console"
	Output         []string          `mapstructure:"output" yaml:"output"` // "stdout", "stderr", "file"
	FilePath       string            `mapstructure:"file_path" yaml:"file_path"`
	MaxSize        int               `mapstructure:"max_size" yaml:"max_size"`       // MB
	MaxBackups     int               `mapstructure:"max_backups" yaml:"max_backups"`
	MaxAge         int               `mapstructure:"max_age" yaml:"max_age"`         // days
	Compress       bool              `mapstructure:"compress" yaml:"compress"`
	SanitizeFields []string          `mapstructure:"sanitize_fields" yaml:"sanitize_fields"`
	CorrelationID  CorrelationIDConfig `mapstructure:"correlation_id" yaml:"correlation_id"`
}

type CorrelationIDConfig struct {
	Enabled    bool   `mapstructure:"enabled" yaml:"enabled"`
	Header     string `mapstructure:"header" yaml:"header"`
	FieldName  string `mapstructure:"field_name" yaml:"field_name"`
	Generator  string `mapstructure:"generator" yaml:"generator"` // "uuid", "random", "sequential"
}

type MonitoringConfig struct {
	Enabled    bool          `mapstructure:"enabled" yaml:"enabled"`
	Prometheus PrometheusConfig `mapstructure:"prometheus" yaml:"prometheus"`
	Health     HealthConfig  `mapstructure:"health" yaml:"health"`
}

type PrometheusConfig struct {
	Enabled    bool   `mapstructure:"enabled" yaml:"enabled"`
	Path       string `mapstructure:"path" yaml:"path"`
	Port       int    `mapstructure:"port" yaml:"port"`
	Namespace  string `mapstructure:"namespace" yaml:"namespace"`
	Subsystem  string `mapstructure:"subsystem" yaml:"subsystem"`
}

type HealthConfig struct {
	Enabled           bool          `mapstructure:"enabled" yaml:"enabled"`
	Path              string        `mapstructure:"path" yaml:"path"`
	Port              int           `mapstructure:"port" yaml:"port"`
	ReadyPath         string        `mapstructure:"ready_path" yaml:"ready_path"`
	LivePath          string        `mapstructure:"live_path" yaml:"live_path"`
	CheckInterval     time.Duration `mapstructure:"check_interval" yaml:"check_interval"`
	CheckTimeout      time.Duration `mapstructure:"check_timeout" yaml:"check_timeout"`
	GracePeriod       time.Duration `mapstructure:"grace_period" yaml:"grace_period"`
	CacheExpiry       time.Duration `mapstructure:"cache_expiry" yaml:"cache_expiry"`
	EnableProfiling   bool          `mapstructure:"enable_profiling" yaml:"enable_profiling"`
	EnableDetailedMetrics bool      `mapstructure:"enable_detailed_metrics" yaml:"enable_detailed_metrics"`
}

type ServerConfig struct {
	Host            string        `mapstructure:"host" yaml:"host"`
	Port            int           `mapstructure:"port" yaml:"port"`
	ReadTimeout     time.Duration `mapstructure:"read_timeout" yaml:"read_timeout"`
	WriteTimeout    time.Duration `mapstructure:"write_timeout" yaml:"write_timeout"`
	IdleTimeout     time.Duration `mapstructure:"idle_timeout" yaml:"idle_timeout"`
	ShutdownTimeout time.Duration `mapstructure:"shutdown_timeout" yaml:"shutdown_timeout"`
	TLS             TLSConfig     `mapstructure:"tls" yaml:"tls"`
}

type TLSConfig struct {
	Enabled  bool   `mapstructure:"enabled" yaml:"enabled"`
	CertFile string `mapstructure:"cert_file" yaml:"cert_file"`
	KeyFile  string `mapstructure:"key_file" yaml:"key_file"`
}

type StateManagerConfig struct {
	StorageType    string        `mapstructure:"storage_type" yaml:"storage_type"` // "file", "redis", "etcd"
	FilePath       string        `mapstructure:"file_path" yaml:"file_path"`
	BackupInterval time.Duration `mapstructure:"backup_interval" yaml:"backup_interval"`
	BackupRetention int          `mapstructure:"backup_retention" yaml:"backup_retention"`
	Redis          RedisConfig   `mapstructure:"redis" yaml:"redis"`
	Etcd           EtcdConfig    `mapstructure:"etcd" yaml:"etcd"`
}

type RedisConfig struct {
	Address     string        `mapstructure:"address" yaml:"address"`
	Password    string        `mapstructure:"password" yaml:"password"`
	DB          int           `mapstructure:"db" yaml:"db"`
	MaxRetries  int           `mapstructure:"max_retries" yaml:"max_retries"`
	DialTimeout time.Duration `mapstructure:"dial_timeout" yaml:"dial_timeout"`
	ReadTimeout time.Duration `mapstructure:"read_timeout" yaml:"read_timeout"`
	WriteTimeout time.Duration `mapstructure:"write_timeout" yaml:"write_timeout"`
	PoolSize    int           `mapstructure:"pool_size" yaml:"pool_size"`
	MinIdleConns int          `mapstructure:"min_idle_conns" yaml:"min_idle_conns"`
}

type EtcdConfig struct {
	Endpoints   []string      `mapstructure:"endpoints" yaml:"endpoints"`
	Username    string        `mapstructure:"username" yaml:"username"`
	Password    string        `mapstructure:"password" yaml:"password"`
	DialTimeout time.Duration `mapstructure:"dial_timeout" yaml:"dial_timeout"`
	TLS         TLSConfig     `mapstructure:"tls" yaml:"tls"`
}