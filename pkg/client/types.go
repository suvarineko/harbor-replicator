package client

import (
	"context"
	"net/http"
	"time"

	"harbor-replicator/pkg/config"
)

type HarborClient struct {
	config       *config.HarborInstanceConfig
	httpClient   *http.Client
	transport    *Transport
	rateLimiter  *RateLimiter
	circuitBreaker *CircuitBreaker
	metrics      MetricsCollector
	logger       Logger
	userAgent    string
	baseURL      string
	authHeader   string
}

type ClientOption func(*HarborClient)

type Logger interface {
	Debug(msg string, fields ...interface{})
	Info(msg string, fields ...interface{})
	Warn(msg string, fields ...interface{})
	Error(msg string, fields ...interface{})
}

type MetricsCollector interface {
	RecordRequest(method, endpoint string, duration time.Duration, statusCode int)
	RecordError(method, endpoint string, errorType string)
	RecordRateLimit(limited bool)
	RecordCircuitBreakerState(state string)
}

type Transport struct {
	baseTransport http.RoundTripper
	timeout       time.Duration
	maxIdleConns  int
	maxConnsPerHost int
	tlsConfig     *TLSConfig
}

type TLSConfig struct {
	InsecureSkipVerify bool
	CertFile           string
	KeyFile            string
	CAFile             string
}

type RateLimiter struct {
	requestsPerSecond float64
	burstSize         int
	timeout           time.Duration
	limiter           TokenBucket
}

type TokenBucket interface {
	Allow() bool
	Wait(ctx context.Context) error
	AllowN(now time.Time, n int) bool
}

type CircuitBreaker struct {
	enabled           bool
	maxRequests       uint32
	interval          time.Duration
	timeout           time.Duration
	failureThreshold  int
	successThreshold  int
	state             CircuitBreakerState
	counts            CircuitBreakerCounts
	expiry            time.Time
}

type CircuitBreakerState int

const (
	CircuitBreakerClosed CircuitBreakerState = iota
	CircuitBreakerHalfOpen
	CircuitBreakerOpen
)

type CircuitBreakerCounts struct {
	Requests             uint32
	TotalSuccesses       uint32
	TotalFailures        uint32
	ConsecutiveSuccesses uint32
	ConsecutiveFailures  uint32
}

type RequestOptions struct {
	Method      string
	Path        string
	Body        interface{}
	Headers     map[string]string
	QueryParams map[string]string
	Timeout     time.Duration
	Retryable   bool
}

type ResponseInfo struct {
	StatusCode   int
	Headers      http.Header
	Duration     time.Duration
	Retries      int
	FromCache    bool
}

type APIError struct {
	StatusCode int
	Message    string
	Details    interface{}
	Retryable  bool
}

func (e *APIError) Error() string {
	return e.Message
}


type ClientStats struct {
	TotalRequests      int64
	SuccessfulRequests int64
	FailedRequests     int64
	AverageLatency     time.Duration
	RateLimitHits      int64
	CircuitBreakerTrips int64
	LastActivity       time.Time
}

type RobotAccount struct {
	ID          int64             `json:"id,omitempty"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Secret      string            `json:"secret,omitempty"`
	Level       string            `json:"level"` // "system" or "project"
	Duration    int64             `json:"duration,omitempty"`
	Disabled    bool              `json:"disabled,omitempty"`
	ExpiresAt   int64             `json:"expires_at,omitempty"`
	Permissions []RobotPermission `json:"permissions,omitempty"`
	CreationTime time.Time        `json:"creation_time,omitempty"`
	UpdateTime   time.Time        `json:"update_time,omitempty"`
	// Project-specific fields
	ProjectID   int64             `json:"project_id,omitempty"`
}

type RobotPermission struct {
	Kind      string             `json:"kind"`
	Namespace string             `json:"namespace"`
	Access    []RobotAccess      `json:"access"`
}

type RobotAccess struct {
	Resource string `json:"resource"`
	Action   string `json:"action"`
}

type Project struct {
	ProjectID    int64             `json:"project_id,omitempty"`
	Name         string            `json:"name"`
	OwnerID      int64             `json:"owner_id,omitempty"`
	OwnerName    string            `json:"owner_name,omitempty"`
	CreationTime time.Time         `json:"creation_time,omitempty"`
	UpdateTime   time.Time         `json:"update_time,omitempty"`
	Deleted      bool              `json:"deleted,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	CVEAllowlist *CVEAllowlist     `json:"cve_allowlist,omitempty"`
}

type ProjectRequest struct {
	ProjectName  string            `json:"project_name"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	CVEAllowlist *CVEAllowlist     `json:"cve_allowlist,omitempty"`
}

type CVEAllowlist struct {
	ID           int64     `json:"id,omitempty"`
	ProjectID    int64     `json:"project_id,omitempty"`
	ExpiresAt    int64     `json:"expires_at,omitempty"`
	Items        []CVEItem `json:"items,omitempty"`
	CreationTime time.Time `json:"creation_time,omitempty"`
	UpdateTime   time.Time `json:"update_time,omitempty"`
}

type CVEItem struct {
	CVEID string `json:"cve_id"`
}

type OIDCGroup struct {
	ID             int64                    `json:"id,omitempty"`
	GroupName      string                   `json:"group_name"`
	GroupType      int                      `json:"group_type,omitempty"`
	LdapGroupDN    string                   `json:"ldap_group_dn,omitempty"`
	CreationTime   time.Time                `json:"creation_time,omitempty"`
	UpdateTime     time.Time                `json:"update_time,omitempty"`
	Permissions    []OIDCGroupPermission    `json:"permissions,omitempty"`
	ProjectRoles   []OIDCGroupProjectRole   `json:"project_roles,omitempty"`
}

type OIDCGroupPermission struct {
	Resource string `json:"resource"`
	Action   string `json:"action"`
}

type OIDCGroupProjectRole struct {
	ProjectID int64  `json:"project_id"`
	RoleID    int64  `json:"role_id"`
	RoleName  string `json:"role_name,omitempty"`
}

type SyncResult struct {
	ResourceType    string                 `json:"resource_type"`
	Operation       string                 `json:"operation"` // "create", "update", "delete", "skip"
	ResourceID      string                 `json:"resource_id"`
	ResourceName    string                 `json:"resource_name"`
	SourceInstance  string                 `json:"source_instance"`
	TargetInstance  string                 `json:"target_instance"`
	Success         bool                   `json:"success"`
	Error           string                 `json:"error,omitempty"`
	Details         map[string]interface{} `json:"details,omitempty"`
	Duration        time.Duration          `json:"duration"`
	Timestamp       time.Time              `json:"timestamp"`
}

type BatchSyncResult struct {
	TotalItems      int          `json:"total_items"`
	SuccessfulItems int          `json:"successful_items"`
	FailedItems     int          `json:"failed_items"`
	SkippedItems    int          `json:"skipped_items"`
	Results         []SyncResult `json:"results"`
	Duration        time.Duration `json:"duration"`
	Timestamp       time.Time    `json:"timestamp"`
}

type ClientInterface interface {
	// Health and diagnostics
	HealthCheck(ctx context.Context) (*HealthCheckResult, error)
	GetStats() ClientStats
	
	// Robot account operations
	ListSystemRobotAccounts(ctx context.Context) ([]RobotAccount, error)
	ListProjectRobotAccounts(ctx context.Context, projectID int64) ([]RobotAccount, error)
	GetRobotAccount(ctx context.Context, robotID int64) (*RobotAccount, error)
	CreateRobotAccount(ctx context.Context, robot *RobotAccount) (*RobotAccount, error)
	UpdateRobotAccount(ctx context.Context, robotID int64, robot *RobotAccount) (*RobotAccount, error)
	DeleteRobotAccount(ctx context.Context, robotID int64) error
	RegenerateRobotSecret(ctx context.Context, robotID int64) (*RobotAccount, error)
	
	// OIDC group operations
	ListOIDCGroups(ctx context.Context) ([]OIDCGroup, error)
	GetOIDCGroup(ctx context.Context, groupID int64) (*OIDCGroup, error)
	CreateOIDCGroup(ctx context.Context, group *OIDCGroup) (*OIDCGroup, error)
	UpdateOIDCGroup(ctx context.Context, groupID int64, group *OIDCGroup) (*OIDCGroup, error)
	DeleteOIDCGroup(ctx context.Context, groupID int64) error
	
	// Project operations
	GetProject(ctx context.Context, projectID int64) (*Project, error)
	CreateProject(ctx context.Context, project *ProjectRequest) (*Project, error)
	UpdateProject(ctx context.Context, projectID int64, project *ProjectRequest) (*Project, error)
	DeleteProject(ctx context.Context, projectID int64) error
	ListProjects(ctx context.Context) ([]Project, error)
	
	// Project associations
	AddGroupToProject(ctx context.Context, groupID, projectID int64, roleID int64) error
	RemoveGroupFromProject(ctx context.Context, groupID, projectID int64) error
	ListGroupProjectRoles(ctx context.Context, groupID int64) ([]OIDCGroupProjectRole, error)
	
	// Generic request methods
	Get(ctx context.Context, path string, options ...RequestOption) (*http.Response, error)
	Post(ctx context.Context, path string, body interface{}, options ...RequestOption) (*http.Response, error)
	Put(ctx context.Context, path string, body interface{}, options ...RequestOption) (*http.Response, error)
	Delete(ctx context.Context, path string, options ...RequestOption) (*http.Response, error)
	
	// Configuration and lifecycle
	UpdateConfig(config *config.HarborInstanceConfig) error
	Close() error
}

type RequestOption func(*RequestOptions)

func WithTimeout(timeout time.Duration) RequestOption {
	return func(options *RequestOptions) {
		options.Timeout = timeout
	}
}

func WithHeaders(headers map[string]string) RequestOption {
	return func(options *RequestOptions) {
		if options.Headers == nil {
			options.Headers = make(map[string]string)
		}
		for k, v := range headers {
			options.Headers[k] = v
		}
	}
}

func WithQueryParams(params map[string]string) RequestOption {
	return func(options *RequestOptions) {
		if options.QueryParams == nil {
			options.QueryParams = make(map[string]string)
		}
		for k, v := range params {
			options.QueryParams[k] = v
		}
	}
}

func WithRetryable(retryable bool) RequestOption {
	return func(options *RequestOptions) {
		options.Retryable = retryable
	}
}