package state

import (
	"time"
)

// ResourceType represents the type of resource being managed
type ResourceType string

const (
	ResourceTypeRobotAccount ResourceType = "robot_account"
	ResourceTypeOIDCGroup    ResourceType = "oidc_group"
	ResourceTypeProject      ResourceType = "project"
)

// String returns the string representation of the resource type
func (rt ResourceType) String() string {
	return string(rt)
}

// SyncState represents the overall state of synchronization operations
type SyncState struct {
	// LastSync tracks the last synchronization time for each resource type
	LastSync map[string]time.Time `json:"last_sync"`
	
	// ResourceMappings maps source resources to target resources
	ResourceMappings map[string]ResourceMapping `json:"resource_mappings"`
	
	// SyncErrors tracks synchronization errors
	SyncErrors []SyncError `json:"sync_errors"`
	
	// SyncInProgress indicates if a sync operation is currently running
	SyncInProgress bool `json:"sync_in_progress"`
	
	// CurrentSyncID is the unique identifier for the current sync operation
	CurrentSyncID string `json:"current_sync_id,omitempty"`
	
	// ResourceVersions tracks version information for each resource
	ResourceVersions map[string]ResourceVersion `json:"resource_versions"`
	
	// SyncHistory maintains a history of sync operations
	SyncHistory []SyncOperation `json:"sync_history"`
	
	// Statistics tracks aggregated sync statistics
	Statistics SyncStatistics `json:"statistics"`
	
	// GroupStates tracks the state of OIDC groups
	GroupStates map[string]GroupState `json:"group_states,omitempty"`
	
	// CreatedAt tracks when the state was first created
	CreatedAt time.Time `json:"created_at"`
	
	// UpdatedAt tracks the last time the state was modified
	UpdatedAt time.Time `json:"updated_at"`
	
	// Version tracks the state schema version for compatibility
	Version string `json:"version"`
}

// ResourceMapping represents the mapping between source and target resources
type ResourceMapping struct {
	// ID is a unique identifier for this mapping
	ID string `json:"id"`
	
	// SourceID is the identifier of the resource in the source Harbor instance
	SourceID string `json:"source_id"`
	
	// TargetID is the identifier of the resource in the target Harbor instance
	TargetID string `json:"target_id"`
	
	// ResourceType indicates the type of resource being mapped
	ResourceType ResourceType `json:"resource_type"`
	
	// SourceInstance is the name/identifier of the source Harbor instance
	SourceInstance string `json:"source_instance"`
	
	// TargetInstance is the name/identifier of the target Harbor instance
	TargetInstance string `json:"target_instance"`
	
	// LastModified tracks when this mapping was last updated
	LastModified time.Time `json:"last_modified"`
	
	// CreatedAt tracks when this mapping was created
	CreatedAt time.Time `json:"created_at"`
	
	// LastSynced tracks when this resource was last successfully synchronized
	LastSynced time.Time `json:"last_synced,omitempty"`
	
	// SyncStatus indicates the current synchronization status
	SyncStatus MappingStatus `json:"sync_status"`
	
	// ConflictResolution strategy for handling conflicts
	ConflictResolution ConflictStrategy `json:"conflict_resolution"`
	
	// Metadata stores additional resource-specific information
	Metadata map[string]interface{} `json:"metadata,omitempty"`
	
	// Dependencies lists other mappings this one depends on
	Dependencies []string `json:"dependencies,omitempty"`
}

// MappingStatus represents the synchronization status of a resource mapping
type MappingStatus string

const (
	MappingStatusPending    MappingStatus = "pending"
	MappingStatusSyncing    MappingStatus = "syncing"
	MappingStatusSynced     MappingStatus = "synced"
	MappingStatusFailed     MappingStatus = "failed"
	MappingStatusConflict   MappingStatus = "conflict"
	MappingStatusOrphaned   MappingStatus = "orphaned"
	MappingStatusSkipped    MappingStatus = "skipped"
)

// String returns the string representation of the mapping status
func (ms MappingStatus) String() string {
	return string(ms)
}

// ConflictStrategy defines how to handle synchronization conflicts
type ConflictStrategy string

const (
	ConflictStrategySourceWins ConflictStrategy = "source_wins"
	ConflictStrategyTargetWins ConflictStrategy = "target_wins"
	ConflictStrategyManual     ConflictStrategy = "manual"
	ConflictStrategySkip       ConflictStrategy = "skip"
	ConflictStrategyMerge      ConflictStrategy = "merge"
)

// String returns the string representation of the conflict strategy
func (cs ConflictStrategy) String() string {
	return string(cs)
}

// SyncError represents an error that occurred during synchronization
type SyncError struct {
	// ID is a unique identifier for this error
	ID string `json:"id"`
	
	// Timestamp when the error occurred
	Timestamp time.Time `json:"timestamp"`
	
	// ResourceType indicates what type of resource caused the error
	ResourceType ResourceType `json:"resource_type"`
	
	// ResourceID is the identifier of the specific resource
	ResourceID string `json:"resource_id"`
	
	// MappingID references the resource mapping if applicable
	MappingID string `json:"mapping_id,omitempty"`
	
	// SyncID references the sync operation that caused this error
	SyncID string `json:"sync_id,omitempty"`
	
	// ErrorMessage contains the detailed error description
	ErrorMessage string `json:"error_message"`
	
	// ErrorType categorizes the error for handling
	ErrorType ErrorType `json:"error_type"`
	
	// RetryCount tracks how many times this operation has been retried
	RetryCount int `json:"retry_count"`
	
	// MaxRetries is the maximum number of retries allowed
	MaxRetries int `json:"max_retries"`
	
	// NextRetryAt indicates when the next retry should be attempted
	NextRetryAt time.Time `json:"next_retry_at,omitempty"`
	
	// Resolved indicates if this error has been resolved
	Resolved bool `json:"resolved"`
	
	// ResolvedAt tracks when this error was resolved
	ResolvedAt time.Time `json:"resolved_at,omitempty"`
	
	// Context provides additional error context
	Context map[string]interface{} `json:"context,omitempty"`
}

// ErrorType categorizes synchronization errors
type ErrorType string

const (
	ErrorTypeTransient   ErrorType = "transient"
	ErrorTypePermanent   ErrorType = "permanent"
	ErrorTypeConflict    ErrorType = "conflict"
	ErrorTypePermission  ErrorType = "permission"
	ErrorTypeValidation  ErrorType = "validation"
	ErrorTypeNetworkError ErrorType = "network"
	ErrorTypeTimeout     ErrorType = "timeout"
	ErrorTypeRateLimit   ErrorType = "rate_limit"
	ErrorTypeUnknown     ErrorType = "unknown"
)

// String returns the string representation of the error type
func (et ErrorType) String() string {
	return string(et)
}

// IsRetryable returns true if the error type is retryable
func (et ErrorType) IsRetryable() bool {
	switch et {
	case ErrorTypeTransient, ErrorTypeNetworkError, ErrorTypeTimeout, ErrorTypeRateLimit:
		return true
	default:
		return false
	}
}

// ResourceVersion tracks version information for resources
type ResourceVersion struct {
	// ResourceID is the identifier of the resource
	ResourceID string `json:"resource_id"`
	
	// ResourceType indicates the type of resource
	ResourceType ResourceType `json:"resource_type"`
	
	// SourceVersion is the version in the source Harbor instance
	SourceVersion string `json:"source_version"`
	
	// TargetVersion is the version in the target Harbor instance
	TargetVersion string `json:"target_version"`
	
	// LastCompared tracks when versions were last compared
	LastCompared time.Time `json:"last_compared"`
	
	// Checksum provides content-based versioning
	Checksum string `json:"checksum,omitempty"`
	
	// ConflictDetected indicates if a version conflict was detected
	ConflictDetected bool `json:"conflict_detected"`
	
	// ConflictDetails provides details about the conflict
	ConflictDetails string `json:"conflict_details,omitempty"`
}

// SyncOperation represents a synchronization operation
type SyncOperation struct {
	// ID is the unique identifier for this sync operation
	ID string `json:"id"`
	
	// StartTime when the sync operation started
	StartTime time.Time `json:"start_time"`
	
	// EndTime when the sync operation completed (if completed)
	EndTime time.Time `json:"end_time,omitempty"`
	
	// Status of the sync operation
	Status OperationStatus `json:"status"`
	
	// ResourceTypes being synchronized in this operation
	ResourceTypes []ResourceType `json:"resource_types"`
	
	// SourceInstance identifier
	SourceInstance string `json:"source_instance"`
	
	// TargetInstance identifier  
	TargetInstance string `json:"target_instance"`
	
	// Progress tracks the completion percentage
	Progress float64 `json:"progress"`
	
	// TotalResources is the total number of resources to sync
	TotalResources int `json:"total_resources"`
	
	// ProcessedResources is the number of resources processed so far
	ProcessedResources int `json:"processed_resources"`
	
	// SuccessfulResources is the number of successfully synced resources
	SuccessfulResources int `json:"successful_resources"`
	
	// FailedResources is the number of failed resources
	FailedResources int `json:"failed_resources"`
	
	// SkippedResources is the number of skipped resources
	SkippedResources int `json:"skipped_resources"`
	
	// ErrorSummary provides a summary of errors encountered
	ErrorSummary map[ErrorType]int `json:"error_summary,omitempty"`
	
	// Trigger indicates what triggered this sync operation
	Trigger string `json:"trigger,omitempty"`
	
	// Configuration used for this sync operation
	Configuration map[string]interface{} `json:"configuration,omitempty"`
}

// OperationStatus represents the status of a sync operation
type OperationStatus string

const (
	OperationStatusPending    OperationStatus = "pending"
	OperationStatusRunning    OperationStatus = "running"
	OperationStatusCompleted  OperationStatus = "completed"
	OperationStatusFailed     OperationStatus = "failed"
	OperationStatusCancelled  OperationStatus = "cancelled"
	OperationStatusPaused     OperationStatus = "paused"
)

// String returns the string representation of the operation status
func (os OperationStatus) String() string {
	return string(os)
}

// IsCompleted returns true if the operation has finished (successfully or not)
func (os OperationStatus) IsCompleted() bool {
	switch os {
	case OperationStatusCompleted, OperationStatusFailed, OperationStatusCancelled:
		return true
	default:
		return false
	}
}

// SyncStatistics tracks aggregated synchronization statistics
type SyncStatistics struct {
	// TotalSyncOperations is the total number of sync operations performed
	TotalSyncOperations int64 `json:"total_sync_operations"`
	
	// SuccessfulSyncOperations is the number of successful sync operations
	SuccessfulSyncOperations int64 `json:"successful_sync_operations"`
	
	// FailedSyncOperations is the number of failed sync operations
	FailedSyncOperations int64 `json:"failed_sync_operations"`
	
	// TotalResourcesSynced is the total number of resources synchronized
	TotalResourcesSynced int64 `json:"total_resources_synced"`
	
	// TotalErrors is the total number of errors encountered
	TotalErrors int64 `json:"total_errors"`
	
	// AverageSyncDuration is the average duration of sync operations
	AverageSyncDuration time.Duration `json:"average_sync_duration"`
	
	// LastSuccessfulSync is the timestamp of the last successful sync
	LastSuccessfulSync time.Time `json:"last_successful_sync,omitempty"`
	
	// ResourceTypeStats provides statistics per resource type
	ResourceTypeStats map[ResourceType]ResourceTypeStatistics `json:"resource_type_stats"`
	
	// ErrorRatePercent is the percentage of operations that result in errors
	ErrorRatePercent float64 `json:"error_rate_percent"`
	
	// UptimePercent tracks the percentage of time sync operations are successful
	UptimePercent float64 `json:"uptime_percent"`
}

// ResourceTypeStatistics tracks statistics for a specific resource type
type ResourceTypeStatistics struct {
	// TotalResources is the total number of resources of this type
	TotalResources int64 `json:"total_resources"`
	
	// SyncedResources is the number of successfully synced resources
	SyncedResources int64 `json:"synced_resources"`
	
	// FailedResources is the number of failed resources
	FailedResources int64 `json:"failed_resources"`
	
	// LastSyncTime is when resources of this type were last synced
	LastSyncTime time.Time `json:"last_sync_time,omitempty"`
	
	// AverageSyncTime is the average time to sync a resource of this type
	AverageSyncTime time.Duration `json:"average_sync_time"`
	
	// ErrorCount is the number of errors for this resource type
	ErrorCount int64 `json:"error_count"`
}

// SyncProgressInfo represents the current progress of an ongoing sync operation
type SyncProgressInfo struct {
	// OperationID is the ID of the current sync operation
	OperationID string `json:"operation_id"`
	
	// StartTime when the current operation started
	StartTime time.Time `json:"start_time"`
	
	// ElapsedTime since the operation started
	ElapsedTime time.Duration `json:"elapsed_time"`
	
	// EstimatedTimeRemaining based on current progress
	EstimatedTimeRemaining time.Duration `json:"estimated_time_remaining,omitempty"`
	
	// OverallProgress percentage (0-100)
	OverallProgress float64 `json:"overall_progress"`
	
	// CurrentResourceType being processed
	CurrentResourceType ResourceType `json:"current_resource_type,omitempty"`
	
	// CurrentResourceID being processed
	CurrentResourceID string `json:"current_resource_id,omitempty"`
	
	// Phase describes the current phase of the sync operation
	Phase string `json:"phase,omitempty"`
	
	// ResourceProgress tracks progress per resource type
	ResourceProgress map[ResourceType]ResourceProgress `json:"resource_progress"`
	
	// RecentErrors lists recent errors during this operation
	RecentErrors []string `json:"recent_errors,omitempty"`
	
	// ThroughputPerSecond indicates how many resources are being processed per second
	ThroughputPerSecond float64 `json:"throughput_per_second"`
}

// ResourceProgress tracks progress for a specific resource type
type ResourceProgress struct {
	// Total number of resources of this type to process
	Total int `json:"total"`
	
	// Processed number of resources processed so far
	Processed int `json:"processed"`
	
	// Successful number of successfully processed resources
	Successful int `json:"successful"`
	
	// Failed number of failed resources
	Failed int `json:"failed"`
	
	// Skipped number of skipped resources
	Skipped int `json:"skipped"`
	
	// Progress percentage for this resource type
	Progress float64 `json:"progress"`
}

// StateValidationResult represents the result of state validation
type StateValidationResult struct {
	// Valid indicates if the state is valid
	Valid bool `json:"valid"`
	
	// Errors contains validation errors found
	Errors []string `json:"errors,omitempty"`
	
	// Warnings contains non-critical issues
	Warnings []string `json:"warnings,omitempty"`
	
	// OrphanedMappings lists mappings that reference non-existent resources
	OrphanedMappings []string `json:"orphaned_mappings,omitempty"`
	
	// DuplicateMappings lists duplicate mappings found
	DuplicateMappings []string `json:"duplicate_mappings,omitempty"`
	
	// InconsistentVersions lists resources with version inconsistencies
	InconsistentVersions []string `json:"inconsistent_versions,omitempty"`
	
	// CheckedAt timestamp when validation was performed
	CheckedAt time.Time `json:"checked_at"`
}

// Default values and constants
const (
	// DefaultStateVersion is the default state schema version
	DefaultStateVersion = "1.0.0"
	
	// DefaultMaxRetries is the default maximum number of retries for failed operations
	DefaultMaxRetries = 3
	
	// DefaultRetryInterval is the default interval between retries
	DefaultRetryInterval = 5 * time.Minute
	
	// DefaultSyncHistoryRetention is how long to keep sync history
	DefaultSyncHistoryRetention = 30 * 24 * time.Hour // 30 days
	
	// DefaultErrorRetention is how long to keep resolved errors
	DefaultErrorRetention = 7 * 24 * time.Hour // 7 days
)

// Group State Management Types

// GroupState represents the state of an OIDC group in the synchronization system
type GroupState struct {
	// GroupID is the unique identifier for the group
	GroupID string `json:"group_id"`
	
	// GroupName is the name of the group
	GroupName string `json:"group_name"`
	
	// GroupType indicates the type of group (1 = OIDC, 2 = LDAP)
	GroupType int `json:"group_type"`
	
	// LdapGroupDN is the LDAP distinguished name (for LDAP groups)
	LdapGroupDN string `json:"ldap_group_dn,omitempty"`
	
	// SyncStatus indicates the current synchronization status
	SyncStatus MappingStatus `json:"sync_status"`
	
	// LastSyncTime tracks when this group was last synchronized
	LastSyncTime time.Time `json:"last_sync_time,omitempty"`
	
	// LastUpdated tracks when this state was last updated
	LastUpdated time.Time `json:"last_updated"`
	
	// GroupChecksum provides content-based versioning for change detection
	GroupChecksum string `json:"group_checksum"`
	
	// Permissions contains the group's permissions
	Permissions []GroupPermission `json:"permissions,omitempty"`
	
	// ProjectMappings tracks project associations and their mappings
	ProjectMappings []GroupProjectMapping `json:"project_mappings,omitempty"`
	
	// SourceInstance is the source Harbor instance
	SourceInstance string `json:"source_instance"`
	
	// TargetInstance is the target Harbor instance
	TargetInstance string `json:"target_instance"`
	
	// ConflictResolution strategy for handling conflicts
	ConflictResolution ConflictStrategy `json:"conflict_resolution"`
	
	// ErrorCount tracks the number of errors for this group
	ErrorCount int `json:"error_count"`
	
	// LastError contains the most recent error message
	LastError string `json:"last_error,omitempty"`
	
	// RetryCount tracks how many times sync has been retried
	RetryCount int `json:"retry_count"`
	
	// NextRetryAt indicates when the next retry should be attempted
	NextRetryAt time.Time `json:"next_retry_at,omitempty"`
}

// GroupPermission represents a permission held by a group
type GroupPermission struct {
	// Resource is the resource type (e.g., "repository", "project")
	Resource string `json:"resource"`
	
	// Action is the action allowed (e.g., "read", "write", "delete")
	Action string `json:"action"`
	
	// Scope defines the scope of the permission
	Scope string `json:"scope,omitempty"`
}

// GroupProjectMapping represents the mapping of a group to projects
type GroupProjectMapping struct {
	// SourceProjectID is the project ID in the source Harbor instance
	SourceProjectID int64 `json:"source_project_id"`
	
	// SourceProjectName is the project name in the source Harbor instance
	SourceProjectName string `json:"source_project_name"`
	
	// TargetProjectID is the project ID in the target Harbor instance
	TargetProjectID int64 `json:"target_project_id"`
	
	// TargetProjectName is the project name in the target Harbor instance
	TargetProjectName string `json:"target_project_name"`
	
	// RoleID is the role assigned to the group in the project
	RoleID int64 `json:"role_id"`
	
	// RoleName is the human-readable name of the role
	RoleName string `json:"role_name"`
	
	// LastSynced tracks when this mapping was last synchronized
	LastSynced time.Time `json:"last_synced,omitempty"`
	
	// MappingStatus indicates the status of this specific mapping
	MappingStatus MappingStatus `json:"mapping_status"`
}

// GroupStateComparison represents the result of comparing two group states
type GroupStateComparison struct {
	// GroupsEqual indicates if the groups are identical
	GroupsEqual bool `json:"groups_equal"`
	
	// PermissionsChanged indicates if permissions have changed
	PermissionsChanged bool `json:"permissions_changed"`
	
	// ProjectsChanged indicates if project associations have changed
	ProjectsChanged bool `json:"projects_changed"`
	
	// ChangedFields lists the fields that have changed
	ChangedFields []string `json:"changed_fields"`
	
	// ComparedAt is when the comparison was performed
	ComparedAt time.Time `json:"compared_at"`
}

// GroupStateFilter provides filtering options for group state queries
type GroupStateFilter struct {
	// SyncStatus filters by synchronization status
	SyncStatus MappingStatus `json:"sync_status,omitempty"`
	
	// GroupType filters by group type (1 = OIDC, 2 = LDAP)
	GroupType int `json:"group_type,omitempty"`
	
	// SourceInstance filters by source Harbor instance
	SourceInstance string `json:"source_instance,omitempty"`
	
	// TargetInstance filters by target Harbor instance
	TargetInstance string `json:"target_instance,omitempty"`
	
	// LastSyncBefore filters groups last synced before this time
	LastSyncBefore *time.Time `json:"last_sync_before,omitempty"`
	
	// LastSyncAfter filters groups last synced after this time
	LastSyncAfter *time.Time `json:"last_sync_after,omitempty"`
	
	// HasErrors filters groups that have errors
	HasErrors bool `json:"has_errors,omitempty"`
}

// GroupSyncStatistics provides statistics about group synchronization
type GroupSyncStatistics struct {
	// TotalGroups is the total number of groups being tracked
	TotalGroups int `json:"total_groups"`
	
	// SyncedGroups is the number of successfully synced groups
	SyncedGroups int `json:"synced_groups"`
	
	// FailedGroups is the number of groups that failed to sync
	FailedGroups int `json:"failed_groups"`
	
	// PendingGroups is the number of groups pending synchronization
	PendingGroups int `json:"pending_groups"`
	
	// ConflictGroups is the number of groups with conflicts
	ConflictGroups int `json:"conflict_groups"`
	
	// StatusCounts provides counts for each status type
	StatusCounts map[MappingStatus]int `json:"status_counts"`
	
	// SuccessRate is the percentage of groups successfully synced
	SuccessRate float64 `json:"success_rate"`
	
	// LastCalculated is when these statistics were calculated
	LastCalculated time.Time `json:"last_calculated"`
}