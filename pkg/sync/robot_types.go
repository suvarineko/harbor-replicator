package sync

import (
	"context"
	"time"

	"harbor-replicator/pkg/client"
	"harbor-replicator/pkg/state"
)

// RobotSyncType defines the type of robot synchronization
type RobotSyncType string

const (
	RobotSyncTypeSystem  RobotSyncType = "system"
	RobotSyncTypeProject RobotSyncType = "project"
)

// RobotSyncOperation defines the type of operation performed on a robot
type RobotSyncOperation string

const (
	RobotSyncOperationCreate   RobotSyncOperation = "create"
	RobotSyncOperationUpdate   RobotSyncOperation = "update"
	RobotSyncOperationDelete   RobotSyncOperation = "delete"
	RobotSyncOperationSkip     RobotSyncOperation = "skip"
	RobotSyncOperationConflict RobotSyncOperation = "conflict"
)

// RobotConflictType defines the type of conflict encountered
type RobotConflictType string

const (
	RobotConflictTypeNameExists       RobotConflictType = "name_exists"
	RobotConflictTypePermissionsDiffer RobotConflictType = "permissions_differ"
	RobotConflictTypeProjectMismatch  RobotConflictType = "project_mismatch"
	RobotConflictTypeSecretMismatch   RobotConflictType = "secret_mismatch"
	RobotConflictTypeLevelMismatch    RobotConflictType = "level_mismatch"
)

// Robot extends the client.RobotAccount with sync-specific metadata
type Robot struct {
	*client.RobotAccount
	
	// Sync metadata
	SourceInstance   string                 `json:"source_instance"`
	SyncID           string                 `json:"sync_id"`
	LastSynced       time.Time              `json:"last_synced"`
	SyncStatus       state.MappingStatus    `json:"sync_status"`
	ConflictInfo     *RobotConflict         `json:"conflict_info,omitempty"`
	
	// Version tracking
	SourceVersion    string                 `json:"source_version"`
	TargetVersion    string                 `json:"target_version"`
	VersionChecksum  string                 `json:"version_checksum"`
	
	// Additional metadata
	Tags             map[string]string      `json:"tags,omitempty"`
	LastError        string                 `json:"last_error,omitempty"`
	RetryCount       int                    `json:"retry_count"`
	NextRetry        time.Time              `json:"next_retry,omitempty"`
}

// RobotConflict represents a conflict between source and target robots
type RobotConflict struct {
	Type            RobotConflictType      `json:"type"`
	SourceRobot     *client.RobotAccount   `json:"source_robot"`
	TargetRobot     *client.RobotAccount   `json:"target_robot"`
	ConflictFields  []string               `json:"conflict_fields"`
	Severity        ConflictSeverity       `json:"severity"`
	ResolutionStrategy string              `json:"resolution_strategy,omitempty"`
	Details         map[string]interface{} `json:"details,omitempty"`
	Timestamp       time.Time              `json:"timestamp"`
}


// RobotFilter defines filtering criteria for robot synchronization
type RobotFilter struct {
	// Name filtering
	NamePatterns     []string              `json:"name_patterns,omitempty"`
	NameRegex        string                `json:"name_regex,omitempty"`
	ExcludePatterns  []string              `json:"exclude_patterns,omitempty"`
	
	// Type filtering
	Types            []RobotSyncType       `json:"types,omitempty"`
	Levels           []string              `json:"levels,omitempty"`
	
	// Project filtering
	ProjectIDs       []int64               `json:"project_ids,omitempty"`
	ProjectPatterns  []string              `json:"project_patterns,omitempty"`
	
	// Permission filtering
	RequiredPermissions []PermissionFilter `json:"required_permissions,omitempty"`
	
	// Status filtering
	SyncStatuses     []state.MappingStatus `json:"sync_statuses,omitempty"`
	
	// Time filtering
	CreatedAfter     *time.Time            `json:"created_after,omitempty"`
	CreatedBefore    *time.Time            `json:"created_before,omitempty"`
	SyncedAfter      *time.Time            `json:"synced_after,omitempty"`
	SyncedBefore     *time.Time            `json:"synced_before,omitempty"`
	
	// Advanced filtering
	Tags             map[string]string     `json:"tags,omitempty"`
	HasConflicts     *bool                 `json:"has_conflicts,omitempty"`
	ErrorsOnly       bool                  `json:"errors_only,omitempty"`
	
	// Pagination
	Limit            int                   `json:"limit,omitempty"`
	Offset           int                   `json:"offset,omitempty"`
}

// PermissionFilter defines filtering criteria for robot permissions
type PermissionFilter struct {
	Kind      string   `json:"kind,omitempty"`
	Namespace string   `json:"namespace,omitempty"`
	Resources []string `json:"resources,omitempty"`
	Actions   []string `json:"actions,omitempty"`
}

// RobotFilterResult contains the results of applying filters
type RobotFilterResult struct {
	Robots       []*Robot              `json:"robots"`
	TotalCount   int                   `json:"total_count"`
	FilteredCount int                  `json:"filtered_count"`
	Filters      *RobotFilter          `json:"filters"`
	ExecutionTime time.Duration        `json:"execution_time"`
	Details      map[string]interface{} `json:"details,omitempty"`
}

// RobotSyncOptions defines options for robot synchronization
type RobotSyncOptions struct {
	// Source configuration
	SourceInstance   string                `json:"source_instance"`
	TargetInstance   string                `json:"target_instance"`
	
	// Sync behavior
	DryRun           bool                  `json:"dry_run"`
	ConflictStrategy ConflictStrategy      `json:"conflict_strategy"`
	CreateMissing    bool                  `json:"create_missing"`
	UpdateExisting   bool                  `json:"update_existing"`
	DeleteOrphaned   bool                  `json:"delete_orphaned"`
	
	// Filtering
	Filter           *RobotFilter          `json:"filter,omitempty"`
	
	// Batching and performance
	BatchSize        int                   `json:"batch_size"`
	MaxConcurrency   int                   `json:"max_concurrency"`
	Timeout          time.Duration         `json:"timeout"`
	
	// Error handling
	StopOnError      bool                  `json:"stop_on_error"`
	MaxRetries       int                   `json:"max_retries"`
	RetryInterval    time.Duration         `json:"retry_interval"`
	
	// Secret management
	RotateSecrets    bool                  `json:"rotate_secrets"`
	PreserveSecrets  bool                  `json:"preserve_secrets"`
	
	// Reporting
	GenerateReport   bool                  `json:"generate_report"`
	ReportFormat     string                `json:"report_format"`
	
	// Advanced options
	ValidateOnly     bool                  `json:"validate_only"`
	SkipValidation   bool                  `json:"skip_validation"`
	Tags             map[string]string     `json:"tags,omitempty"`
	
	// Project-specific options
	CreateMissingProjects  bool            `json:"create_missing_projects"`
	SkipMissingProjects    bool            `json:"skip_missing_projects"`
	ProjectMappings        map[int64]int64 `json:"project_mappings,omitempty"` // source project ID -> target project ID
}

// ConflictStrategy defines how to handle conflicts during synchronization
type ConflictStrategy string

const (
	ConflictStrategySourceWins     ConflictStrategy = "source_wins"
	ConflictStrategyTargetWins     ConflictStrategy = "target_wins"
	ConflictStrategyMergePermissions ConflictStrategy = "merge_permissions"
	ConflictStrategyRenameDuplicate ConflictStrategy = "rename_duplicate"
	ConflictStrategyInteractive    ConflictStrategy = "interactive"
	ConflictStrategySkip           ConflictStrategy = "skip"
	ConflictStrategyFail           ConflictStrategy = "fail"
)

// RobotSyncResult represents the result of a robot synchronization operation
type RobotSyncResult struct {
	Operation        RobotSyncOperation    `json:"operation"`
	Robot            *Robot                `json:"robot"`
	SourceRobot      *client.RobotAccount  `json:"source_robot,omitempty"`
	TargetRobot      *client.RobotAccount  `json:"target_robot,omitempty"`
	Success          bool                  `json:"success"`
	Error            string                `json:"error,omitempty"`
	Warnings         []string              `json:"warnings,omitempty"`
	ConflictResolved bool                  `json:"conflict_resolved"`
	ConflictInfo     *RobotConflict        `json:"conflict_info,omitempty"`
	Duration         time.Duration         `json:"duration"`
	Timestamp        time.Time             `json:"timestamp"`
	Details          map[string]interface{} `json:"details,omitempty"`
}

// RobotSyncReport contains comprehensive synchronization results
type RobotSyncReport struct {
	SyncID           string                 `json:"sync_id"`
	SourceInstance   string                 `json:"source_instance"`
	TargetInstance   string                 `json:"target_instance"`
	StartTime        time.Time              `json:"start_time"`
	EndTime          time.Time              `json:"end_time"`
	Duration         time.Duration          `json:"duration"`
	
	// Summary statistics
	TotalRobots      int                    `json:"total_robots"`
	ProcessedRobots  int                    `json:"processed_robots"`
	SuccessfulRobots int                    `json:"successful_robots"`
	FailedRobots     int                    `json:"failed_robots"`
	SkippedRobots    int                    `json:"skipped_robots"`
	ConflictRobots   int                    `json:"conflict_robots"`
	
	// Operation breakdown
	CreatedRobots    int                    `json:"created_robots"`
	UpdatedRobots    int                    `json:"updated_robots"`
	DeletedRobots    int                    `json:"deleted_robots"`
	
	// Detailed results
	Results          []RobotSyncResult      `json:"results"`
	Conflicts        []RobotConflict        `json:"conflicts"`
	Errors           []RobotSyncError       `json:"errors"`
	
	// Configuration
	Options          *RobotSyncOptions      `json:"options"`
	Filter           *RobotFilter           `json:"filter,omitempty"`
	
	// Performance metrics
	AverageLatency   time.Duration          `json:"average_latency"`
	Throughput       float64                `json:"throughput"` // robots per second
	
	// Additional metadata
	Tags             map[string]string      `json:"tags,omitempty"`
}

// RobotSyncError represents an error that occurred during synchronization
type RobotSyncError struct {
	RobotName        string                 `json:"robot_name"`
	RobotID          int64                  `json:"robot_id"`
	Operation        RobotSyncOperation     `json:"operation"`
	ErrorType        string                 `json:"error_type"`
	ErrorMessage     string                 `json:"error_message"`
	ErrorDetails     map[string]interface{} `json:"error_details,omitempty"`
	Timestamp        time.Time              `json:"timestamp"`
	Retryable        bool                   `json:"retryable"`
	RetryAttempts    int                    `json:"retry_attempts"`
}

// RobotSynchronizer defines the interface for robot account synchronization
type RobotSynchronizer interface {
	// System-level operations
	SyncSystemRobots(ctx context.Context, options *RobotSyncOptions) (*RobotSyncReport, error)
	ListSystemRobots(ctx context.Context, instance string, filter *RobotFilter) (*RobotFilterResult, error)
	
	// Project-level operations
	SyncProjectRobots(ctx context.Context, projectID int64, options *RobotSyncOptions) (*RobotSyncReport, error)
	ListProjectRobots(ctx context.Context, instance string, projectID int64, filter *RobotFilter) (*RobotFilterResult, error)
	
	// Individual robot operations
	SyncRobot(ctx context.Context, robotName string, options *RobotSyncOptions) (*RobotSyncResult, error)
	CompareRobots(source, target *client.RobotAccount) (*RobotConflict, error)
	
	// Conflict resolution
	ResolveConflict(ctx context.Context, conflict *RobotConflict, strategy ConflictStrategy) (*RobotSyncResult, error)
	ListConflicts(ctx context.Context, filter *RobotFilter) ([]RobotConflict, error)
	
	// Filtering and search
	ApplyFilter(robots []*Robot, filter *RobotFilter) (*RobotFilterResult, error)
	SearchRobots(ctx context.Context, instance string, query string) ([]*Robot, error)
	
	// Validation and health
	ValidateRobotSync(ctx context.Context, options *RobotSyncOptions) ([]string, error)
	GetSyncStatus(ctx context.Context, syncID string) (*RobotSyncReport, error)
	
	// Reporting and monitoring
	GenerateReport(ctx context.Context, syncID string, format string) ([]byte, error)
	GetSyncHistory(ctx context.Context, filter *RobotFilter) ([]*RobotSyncReport, error)
	
	// Configuration
	UpdateSyncConfig(options *RobotSyncOptions) error
	GetSyncConfig() *RobotSyncOptions
}

// SecretManager defines the interface for managing robot account secrets
type SecretManager interface {
	// Secret storage and retrieval
	StoreSecret(robotID int64, secret string, metadata map[string]string) error
	RetrieveSecret(robotID int64) (string, error)
	DeleteSecret(robotID int64) error
	
	// Secret validation and comparison
	ValidateSecret(robotID int64, secret string) bool
	CompareSecrets(robotID int64, secret string) bool
	
	// Secret rotation
	RotateSecret(ctx context.Context, robotID int64) (string, error)
	ScheduleRotation(robotID int64, rotateAt time.Time) error
	GetRotationSchedule(robotID int64) (time.Time, error)
	
	// Secret metadata
	GetSecretMetadata(robotID int64) (map[string]string, error)
	UpdateSecretMetadata(robotID int64, metadata map[string]string) error
	
	// Cleanup and maintenance
	CleanupExpiredSecrets(ctx context.Context) error
	GetSecretStats() SecretStats
}

// SecretStats provides statistics about secret management
type SecretStats struct {
	TotalSecrets     int       `json:"total_secrets"`
	EncryptedSecrets int       `json:"encrypted_secrets"`
	ExpiredSecrets   int       `json:"expired_secrets"`
	RotationsPending int       `json:"rotations_pending"`
	LastRotation     time.Time `json:"last_rotation"`
	LastCleanup      time.Time `json:"last_cleanup"`
}

// RobotComparator defines the interface for comparing robot accounts
type RobotComparator interface {
	// Basic comparison
	Equal(source, target *client.RobotAccount) bool
	Compare(source, target *client.RobotAccount) (*RobotConflict, error)
	
	// Field-specific comparison
	ComparePermissions(source, target []client.RobotPermission) (bool, []string)
	CompareMetadata(source, target *client.RobotAccount) (bool, []string)
	
	// Conflict detection
	DetectConflicts(source, target *client.RobotAccount) ([]RobotConflict, error)
	AnalyzeConflict(conflict *RobotConflict) ConflictSeverity
	
	// Normalization
	NormalizeRobot(robot *client.RobotAccount) *client.RobotAccount
	GenerateChecksum(robot *client.RobotAccount) string
}

// ConflictResolver defines the interface for resolving robot conflicts
type ConflictResolver interface {
	// Strategy-based resolution
	ResolveConflict(ctx context.Context, conflict *RobotConflict, strategy ConflictStrategy) (*client.RobotAccount, error)
	
	// Strategy implementations
	ResolveSourceWins(conflict *RobotConflict) (*client.RobotAccount, error)
	ResolveTargetWins(conflict *RobotConflict) (*client.RobotAccount, error)
	ResolveMergePermissions(conflict *RobotConflict) (*client.RobotAccount, error)
	ResolveRenameDuplicate(conflict *RobotConflict) (*client.RobotAccount, error)
	
	// Interactive resolution
	ResolveInteractive(ctx context.Context, conflict *RobotConflict) (*client.RobotAccount, error)
	
	// Conflict analysis
	AnalyzeResolutionImpact(conflict *RobotConflict, strategy ConflictStrategy) (*ResolutionImpact, error)
	SuggestResolution(conflict *RobotConflict) (ConflictStrategy, string)
}

// ResolutionImpact describes the impact of applying a conflict resolution strategy
type ResolutionImpact struct {
	Strategy         ConflictStrategy       `json:"strategy"`
	DataLoss         bool                   `json:"data_loss"`
	PermissionChanges bool                  `json:"permission_changes"`
	NameChanges      bool                   `json:"name_changes"`
	Risks           []string               `json:"risks"`
	Benefits        []string               `json:"benefits"`
	Recommendations []string               `json:"recommendations"`
}

// ResolvedConflict represents the result of resolving a robot conflict
type ResolvedConflict struct {
	OriginalConflict RobotConflict          `json:"original_conflict"`
	ResolvedRobot    *client.RobotAccount   `json:"resolved_robot"`
	Strategy         ConflictStrategy       `json:"strategy"`
	ResolutionTime   time.Duration          `json:"resolution_time"`
	Success          bool                   `json:"success"`
	Error            string                 `json:"error,omitempty"`
	Impact           *ResolutionImpact      `json:"impact,omitempty"`
}