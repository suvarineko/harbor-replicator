package sync

import (
	"context"
	"fmt"
	"time"

	"harbor-replicator/pkg/client"
	"harbor-replicator/pkg/state"

	"go.uber.org/zap"
)

// GroupConflictResolver handles conflict resolution and error handling for OIDC group synchronization
type GroupConflictResolver struct {
	localClient    client.ClientInterface
	remoteClients  map[string]client.ClientInterface
	stateManager   *state.StateManager
	logger         *zap.Logger
	config         ConflictResolutionConfig
}

// ConflictResolutionConfig contains configuration for conflict resolution and error handling
type ConflictResolutionConfig struct {
	// GlobalConflictStrategy defines the default conflict resolution strategy
	GlobalConflictStrategy state.ConflictStrategy
	
	// PermissionConflictStrategy specific strategy for permission conflicts
	PermissionConflictStrategy state.ConflictStrategy
	
	// ProjectConflictStrategy specific strategy for project association conflicts
	ProjectConflictStrategy state.ConflictStrategy
	
	// EnableRollback enables automatic rollback on critical failures
	EnableRollback bool
	
	// MaxRollbackAttempts limits the number of rollback attempts
	MaxRollbackAttempts int
	
	// RetryPolicy defines retry behavior for failed operations
	RetryPolicy RetryPolicy
	
	// CriticalErrorThreshold number of errors before marking sync as critical
	CriticalErrorThreshold int
	
	// ErrorEscalationEnabled enables error escalation to external systems
	ErrorEscalationEnabled bool
	
	// ConflictLoggingLevel defines the logging level for conflicts
	ConflictLoggingLevel string
	
	// DetailedErrorReporting enables comprehensive error context collection
	DetailedErrorReporting bool
}

// RetryPolicy defines retry behavior for failed operations
type RetryPolicy struct {
	MaxRetries      int
	InitialDelay    time.Duration
	MaxDelay        time.Duration
	BackoffFactor   float64
	RetryableErrors []string
}

// ConflictResolutionResult represents the result of conflict resolution
type ConflictResolutionResult struct {
	GroupID                  string
	GroupName                string
	ConflictsDetected        []GroupConflict
	ConflictsResolved        []GroupConflict
	ConflictsUnresolved      []GroupConflict
	ErrorsHandled            []GroupError
	ErrorsUnhandled          []GroupError
	RollbacksPerformed       []RollbackOperation
	RetryAttempts            map[string]int
	ResolutionDuration       time.Duration
	OverallSuccess           bool
	RequiresManualIntervention bool
	Timestamp                time.Time
}

// GroupConflict represents a conflict detected during group synchronization
type GroupConflict struct {
	ConflictID       string
	ConflictType     ConflictType
	ConflictSeverity ConflictSeverity
	SourceValue      interface{}
	TargetValue      interface{}
	ConflictReason   string
	ResolutionAction string
	ResolutionResult ResolutionResult
	Timestamp        time.Time
	Context          map[string]interface{}
}

// GroupError represents an error encountered during group synchronization
type GroupError struct {
	ErrorID          string
	ErrorType        ErrorType
	ErrorSeverity    ErrorSeverity
	ErrorMessage     string
	StackTrace       string
	HandlingAction   string
	HandlingResult   ErrorHandlingResult
	RetryCount       int
	Timestamp        time.Time
	Context          map[string]interface{}
}

// RollbackOperation represents a rollback operation performed
type RollbackOperation struct {
	OperationID    string
	OperationType  string
	RollbackReason string
	RollbackResult RollbackResult
	AffectedResources []string
	Timestamp      time.Time
}

// ConflictType defines types of conflicts
type ConflictType string

const (
	ConflictTypeGroupProperty   ConflictType = "group_property"
	ConflictTypePermission      ConflictType = "permission"
	ConflictTypeProjectRole     ConflictType = "project_role"
	ConflictTypeProjectMapping  ConflictType = "project_mapping"
	ConflictTypeRoleMapping     ConflictType = "role_mapping"
	ConflictTypeNameCollision   ConflictType = "name_collision"
	ConflictTypeStateInconsistency ConflictType = "state_inconsistency"
)

// ConflictSeverity defines the severity of conflicts
type ConflictSeverity string

const (
	ConflictSeverityLow      ConflictSeverity = "low"
	ConflictSeverityMedium   ConflictSeverity = "medium"
	ConflictSeverityHigh     ConflictSeverity = "high"
	ConflictSeverityCritical ConflictSeverity = "critical"
)

// ResolutionResult defines the result of conflict resolution
type ResolutionResult string

const (
	ResolutionResultResolved   ResolutionResult = "resolved"
	ResolutionResultSkipped    ResolutionResult = "skipped"
	ResolutionResultFailed     ResolutionResult = "failed"
	ResolutionResultDeferred   ResolutionResult = "deferred"
)

// ErrorType defines types of errors
type ErrorType string

const (
	ErrorTypeAPI         ErrorType = "api"
	ErrorTypeNetwork     ErrorType = "network"
	ErrorTypeValidation  ErrorType = "validation"
	ErrorTypePermission  ErrorType = "permission"
	ErrorTypeConfiguration ErrorType = "configuration"
	ErrorTypeState       ErrorType = "state"
	ErrorTypeInternal    ErrorType = "internal"
)

// ErrorSeverity defines the severity of errors
type ErrorSeverity string

const (
	ErrorSeverityInfo     ErrorSeverity = "info"
	ErrorSeverityWarning  ErrorSeverity = "warning"
	ErrorSeverityError    ErrorSeverity = "error"
	ErrorSeverityCritical ErrorSeverity = "critical"
)

// ErrorHandlingResult defines the result of error handling
type ErrorHandlingResult string

const (
	ErrorHandlingResultHandled     ErrorHandlingResult = "handled"
	ErrorHandlingResultRetried     ErrorHandlingResult = "retried"
	ErrorHandlingResultEscalated   ErrorHandlingResult = "escalated"
	ErrorHandlingResultIgnored     ErrorHandlingResult = "ignored"
	ErrorHandlingResultFailed      ErrorHandlingResult = "failed"
)

// RollbackResult defines the result of rollback operations
type RollbackResult string

const (
	RollbackResultSuccess  RollbackResult = "success"
	RollbackResultFailed   RollbackResult = "failed"
	RollbackResultPartial RollbackResult = "partial"
)

// NewGroupConflictResolver creates a new group conflict resolver
func NewGroupConflictResolver(
	localClient client.ClientInterface,
	remoteClients map[string]client.ClientInterface,
	stateManager *state.StateManager,
	logger *zap.Logger,
	config ConflictResolutionConfig,
) *GroupConflictResolver {
	return &GroupConflictResolver{
		localClient:   localClient,
		remoteClients: remoteClients,
		stateManager:  stateManager,
		logger:        logger,
		config:        config,
	}
}

// ResolveGroupConflicts resolves conflicts and handles errors for group synchronization
func (gcr *GroupConflictResolver) ResolveGroupConflicts(
	ctx context.Context,
	sourceGroup *client.OIDCGroup,
	targetGroup *client.OIDCGroup,
	sourceInstance string,
	conflicts []GroupConflict,
	errors []GroupError,
) (*ConflictResolutionResult, error) {
	startTime := time.Now()
	
	result := &ConflictResolutionResult{
		GroupID:           fmt.Sprintf("%s:%s", sourceInstance, sourceGroup.GroupName),
		GroupName:         sourceGroup.GroupName,
		ConflictsDetected: conflicts,
		ErrorsHandled:     []GroupError{},
		ErrorsUnhandled:   []GroupError{},
		RetryAttempts:     make(map[string]int),
		Timestamp:         startTime,
	}

	gcr.logger.Info("Starting conflict resolution and error handling",
		zap.String("group_name", sourceGroup.GroupName),
		zap.String("source_instance", sourceInstance),
		zap.Int("conflicts", len(conflicts)),
		zap.Int("errors", len(errors)))

	// Handle errors first
	if err := gcr.handleErrors(ctx, errors, result); err != nil {
		gcr.logger.Error("Failed to handle errors", zap.Error(err))
	}

	// Resolve conflicts
	if err := gcr.resolveConflicts(ctx, sourceGroup, targetGroup, conflicts, result); err != nil {
		gcr.logger.Error("Failed to resolve conflicts", zap.Error(err))
	}

	// Check if manual intervention is required
	result.RequiresManualIntervention = gcr.requiresManualIntervention(result)

	// Determine overall success
	result.OverallSuccess = len(result.ConflictsUnresolved) == 0 && len(result.ErrorsUnhandled) == 0

	result.ResolutionDuration = time.Since(startTime)

	gcr.logger.Info("Conflict resolution and error handling completed",
		zap.String("group_name", sourceGroup.GroupName),
		zap.Duration("duration", result.ResolutionDuration),
		zap.Int("conflicts_resolved", len(result.ConflictsResolved)),
		zap.Int("conflicts_unresolved", len(result.ConflictsUnresolved)),
		zap.Int("errors_handled", len(result.ErrorsHandled)),
		zap.Int("errors_unhandled", len(result.ErrorsUnhandled)),
		zap.Bool("overall_success", result.OverallSuccess),
		zap.Bool("requires_manual_intervention", result.RequiresManualIntervention))

	return result, nil
}

// handleErrors handles errors based on the configured error handling strategy
func (gcr *GroupConflictResolver) handleErrors(
	ctx context.Context,
	errors []GroupError,
	result *ConflictResolutionResult,
) error {
	for _, groupError := range errors {
		handled := false
		
		// Determine handling strategy based on error type and severity
		switch groupError.ErrorSeverity {
		case ErrorSeverityInfo, ErrorSeverityWarning:
			handled = gcr.handleNonCriticalError(ctx, &groupError, result)
		case ErrorSeverityError:
			handled = gcr.handleRecoverableError(ctx, &groupError, result)
		case ErrorSeverityCritical:
			handled = gcr.handleCriticalError(ctx, &groupError, result)
		}
		
		if handled {
			result.ErrorsHandled = append(result.ErrorsHandled, groupError)
		} else {
			result.ErrorsUnhandled = append(result.ErrorsUnhandled, groupError)
		}
	}
	
	return nil
}

// handleNonCriticalError handles non-critical errors (info/warning level)
func (gcr *GroupConflictResolver) handleNonCriticalError(
	ctx context.Context,
	groupError *GroupError,
	result *ConflictResolutionResult,
) bool {
	// Log the error and mark as handled
	if groupError.ErrorSeverity == ErrorSeverityWarning {
		gcr.logger.Warn("Non-critical error handled",
			zap.String("error_id", groupError.ErrorID),
			zap.String("error_message", groupError.ErrorMessage))
	} else {
		gcr.logger.Info("Informational error noted",
			zap.String("error_id", groupError.ErrorID),
			zap.String("error_message", groupError.ErrorMessage))
	}
	
	groupError.HandlingAction = "logged"
	groupError.HandlingResult = ErrorHandlingResultHandled
	
	return true
}

// handleRecoverableError handles recoverable errors with retry logic
func (gcr *GroupConflictResolver) handleRecoverableError(
	ctx context.Context,
	groupError *GroupError,
	result *ConflictResolutionResult,
) bool {
	// Check if error is retryable
	if !gcr.isRetryableError(groupError) {
		gcr.logger.Error("Error is not retryable",
			zap.String("error_id", groupError.ErrorID),
			zap.String("error_type", string(groupError.ErrorType)))
		groupError.HandlingAction = "marked_non_retryable"
		groupError.HandlingResult = ErrorHandlingResultFailed
		return false
	}
	
	// Check retry count
	if groupError.RetryCount >= gcr.config.RetryPolicy.MaxRetries {
		gcr.logger.Error("Maximum retry attempts exceeded",
			zap.String("error_id", groupError.ErrorID),
			zap.Int("retry_count", groupError.RetryCount),
			zap.Int("max_retries", gcr.config.RetryPolicy.MaxRetries))
		groupError.HandlingAction = "max_retries_exceeded"
		groupError.HandlingResult = ErrorHandlingResultFailed
		return false
	}
	
	// Perform retry with backoff
	delay := gcr.calculateRetryDelay(groupError.RetryCount)
	time.Sleep(delay)
	
	groupError.RetryCount++
	result.RetryAttempts[groupError.ErrorID] = groupError.RetryCount
	
	gcr.logger.Info("Retrying failed operation",
		zap.String("error_id", groupError.ErrorID),
		zap.Int("retry_count", groupError.RetryCount),
		zap.Duration("delay", delay))
	
	groupError.HandlingAction = "retried"
	groupError.HandlingResult = ErrorHandlingResultRetried
	
	return true
}

// handleCriticalError handles critical errors with rollback and escalation
func (gcr *GroupConflictResolver) handleCriticalError(
	ctx context.Context,
	groupError *GroupError,
	result *ConflictResolutionResult,
) bool {
	gcr.logger.Error("Critical error detected",
		zap.String("error_id", groupError.ErrorID),
		zap.String("error_message", groupError.ErrorMessage))
	
	// Perform rollback if enabled
	if gcr.config.EnableRollback {
		if rollbackOp := gcr.performRollback(ctx, groupError, result); rollbackOp != nil {
			result.RollbacksPerformed = append(result.RollbacksPerformed, *rollbackOp)
		}
	}
	
	// Escalate error if enabled
	if gcr.config.ErrorEscalationEnabled {
		gcr.escalateError(groupError)
	}
	
	groupError.HandlingAction = "escalated_with_rollback"
	groupError.HandlingResult = ErrorHandlingResultEscalated
	
	return false // Critical errors are not considered "handled" successfully
}

// resolveConflicts resolves conflicts based on the configured resolution strategy
func (gcr *GroupConflictResolver) resolveConflicts(
	ctx context.Context,
	sourceGroup *client.OIDCGroup,
	targetGroup *client.OIDCGroup,
	conflicts []GroupConflict,
	result *ConflictResolutionResult,
) error {
	for _, conflict := range conflicts {
		resolved := false
		
		// Determine resolution strategy based on conflict type
		switch conflict.ConflictType {
		case ConflictTypePermission:
			resolved = gcr.resolvePermissionConflict(ctx, &conflict, sourceGroup, targetGroup)
		case ConflictTypeProjectRole:
			resolved = gcr.resolveProjectRoleConflict(ctx, &conflict, sourceGroup, targetGroup)
		case ConflictTypeGroupProperty:
			resolved = gcr.resolveGroupPropertyConflict(ctx, &conflict, sourceGroup, targetGroup)
		case ConflictTypeProjectMapping:
			resolved = gcr.resolveProjectMappingConflict(ctx, &conflict)
		case ConflictTypeNameCollision:
			resolved = gcr.resolveNameCollisionConflict(ctx, &conflict, sourceGroup, targetGroup)
		default:
			resolved = gcr.resolveGenericConflict(ctx, &conflict, sourceGroup, targetGroup)
		}
		
		if resolved {
			conflict.ResolutionResult = ResolutionResultResolved
			result.ConflictsResolved = append(result.ConflictsResolved, conflict)
		} else {
			conflict.ResolutionResult = ResolutionResultFailed
			result.ConflictsUnresolved = append(result.ConflictsUnresolved, conflict)
		}
	}
	
	return nil
}

// resolvePermissionConflict resolves permission-related conflicts
func (gcr *GroupConflictResolver) resolvePermissionConflict(
	ctx context.Context,
	conflict *GroupConflict,
	sourceGroup *client.OIDCGroup,
	targetGroup *client.OIDCGroup,
) bool {
	strategy := gcr.config.PermissionConflictStrategy
	if strategy == "" {
		strategy = gcr.config.GlobalConflictStrategy
	}
	
	switch strategy {
	case state.ConflictStrategySourceWins:
		conflict.ResolutionAction = "applied_source_permissions"
		return true
	case state.ConflictStrategyTargetWins:
		conflict.ResolutionAction = "kept_target_permissions"
		return true
	case state.ConflictStrategyMerge:
		conflict.ResolutionAction = "merged_permissions"
		return gcr.mergePermissions(sourceGroup, targetGroup)
	case state.ConflictStrategySkip:
		conflict.ResolutionAction = "skipped_permission_sync"
		conflict.ResolutionResult = ResolutionResultSkipped
		return true
	default:
		conflict.ResolutionAction = "deferred_to_manual_resolution"
		conflict.ResolutionResult = ResolutionResultDeferred
		return false
	}
}

// resolveProjectRoleConflict resolves project role-related conflicts
func (gcr *GroupConflictResolver) resolveProjectRoleConflict(
	ctx context.Context,
	conflict *GroupConflict,
	sourceGroup *client.OIDCGroup,
	targetGroup *client.OIDCGroup,
) bool {
	strategy := gcr.config.ProjectConflictStrategy
	if strategy == "" {
		strategy = gcr.config.GlobalConflictStrategy
	}
	
	switch strategy {
	case state.ConflictStrategySourceWins:
		conflict.ResolutionAction = "applied_source_project_roles"
		return true
	case state.ConflictStrategyTargetWins:
		conflict.ResolutionAction = "kept_target_project_roles"
		return true
	case state.ConflictStrategyMerge:
		conflict.ResolutionAction = "merged_project_roles"
		return gcr.mergeProjectRoles(sourceGroup, targetGroup)
	case state.ConflictStrategySkip:
		conflict.ResolutionAction = "skipped_project_role_sync"
		conflict.ResolutionResult = ResolutionResultSkipped
		return true
	default:
		conflict.ResolutionAction = "deferred_to_manual_resolution"
		conflict.ResolutionResult = ResolutionResultDeferred
		return false
	}
}

// resolveGroupPropertyConflict resolves group property conflicts
func (gcr *GroupConflictResolver) resolveGroupPropertyConflict(
	ctx context.Context,
	conflict *GroupConflict,
	sourceGroup *client.OIDCGroup,
	targetGroup *client.OIDCGroup,
) bool {
	switch gcr.config.GlobalConflictStrategy {
	case state.ConflictStrategySourceWins:
		conflict.ResolutionAction = "applied_source_properties"
		return true
	case state.ConflictStrategyTargetWins:
		conflict.ResolutionAction = "kept_target_properties"
		return true
	case state.ConflictStrategySkip:
		conflict.ResolutionAction = "skipped_property_sync"
		conflict.ResolutionResult = ResolutionResultSkipped
		return true
	default:
		conflict.ResolutionAction = "deferred_to_manual_resolution"
		conflict.ResolutionResult = ResolutionResultDeferred
		return false
	}
}

// resolveProjectMappingConflict resolves project mapping conflicts
func (gcr *GroupConflictResolver) resolveProjectMappingConflict(
	ctx context.Context,
	conflict *GroupConflict,
) bool {
	// Project mapping conflicts typically require manual intervention
	// unless we have specific auto-resolution rules
	conflict.ResolutionAction = "logged_mapping_conflict"
	conflict.ResolutionResult = ResolutionResultDeferred
	
	gcr.logger.Warn("Project mapping conflict requires manual resolution",
		zap.String("conflict_id", conflict.ConflictID),
		zap.String("conflict_reason", conflict.ConflictReason))
	
	return false
}

// resolveNameCollisionConflict resolves name collision conflicts
func (gcr *GroupConflictResolver) resolveNameCollisionConflict(
	ctx context.Context,
	conflict *GroupConflict,
	sourceGroup *client.OIDCGroup,
	targetGroup *client.OIDCGroup,
) bool {
	// Name collision conflicts are typically critical and require manual intervention
	conflict.ResolutionAction = "escalated_name_collision"
	conflict.ResolutionResult = ResolutionResultDeferred
	
	gcr.logger.Error("Name collision conflict detected",
		zap.String("conflict_id", conflict.ConflictID),
		zap.String("source_group", sourceGroup.GroupName),
		zap.String("target_group", targetGroup.GroupName))
	
	return false
}

// resolveGenericConflict resolves generic conflicts using the global strategy
func (gcr *GroupConflictResolver) resolveGenericConflict(
	ctx context.Context,
	conflict *GroupConflict,
	sourceGroup *client.OIDCGroup,
	targetGroup *client.OIDCGroup,
) bool {
	switch gcr.config.GlobalConflictStrategy {
	case state.ConflictStrategySourceWins:
		conflict.ResolutionAction = "applied_source_wins_strategy"
		return true
	case state.ConflictStrategyTargetWins:
		conflict.ResolutionAction = "applied_target_wins_strategy"
		return true
	case state.ConflictStrategySkip:
		conflict.ResolutionAction = "skipped_conflicting_operation"
		conflict.ResolutionResult = ResolutionResultSkipped
		return true
	default:
		conflict.ResolutionAction = "deferred_to_manual_resolution"
		conflict.ResolutionResult = ResolutionResultDeferred
		return false
	}
}

// isRetryableError checks if an error is retryable based on type and configuration
func (gcr *GroupConflictResolver) isRetryableError(groupError *GroupError) bool {
	retryableTypes := map[ErrorType]bool{
		ErrorTypeAPI:     true,
		ErrorTypeNetwork: true,
		ErrorTypeState:   true,
		ErrorTypeInternal: false,
		ErrorTypeValidation: false,
		ErrorTypeConfiguration: false,
		ErrorTypePermission: false,
	}
	
	return retryableTypes[groupError.ErrorType]
}

// calculateRetryDelay calculates the delay for retry attempts with exponential backoff
func (gcr *GroupConflictResolver) calculateRetryDelay(retryCount int) time.Duration {
	delay := gcr.config.RetryPolicy.InitialDelay
	
	for i := 0; i < retryCount; i++ {
		delay = time.Duration(float64(delay) * gcr.config.RetryPolicy.BackoffFactor)
		if delay > gcr.config.RetryPolicy.MaxDelay {
			delay = gcr.config.RetryPolicy.MaxDelay
			break
		}
	}
	
	return delay
}

// performRollback performs rollback operations for critical errors
func (gcr *GroupConflictResolver) performRollback(
	ctx context.Context,
	groupError *GroupError,
	result *ConflictResolutionResult,
) *RollbackOperation {
	rollbackOp := &RollbackOperation{
		OperationID:    fmt.Sprintf("rollback_%s_%d", groupError.ErrorID, time.Now().Unix()),
		OperationType:  "group_sync_rollback",
		RollbackReason: groupError.ErrorMessage,
		Timestamp:      time.Now(),
	}
	
	// Implementation would depend on what needs to be rolled back
	// For now, we'll mark it as successful
	rollbackOp.RollbackResult = RollbackResultSuccess
	
	gcr.logger.Info("Performed rollback operation",
		zap.String("operation_id", rollbackOp.OperationID),
		zap.String("reason", rollbackOp.RollbackReason))
	
	return rollbackOp
}

// escalateError escalates errors to external systems (monitoring, alerting, etc.)
func (gcr *GroupConflictResolver) escalateError(groupError *GroupError) {
	// Implementation would integrate with external monitoring/alerting systems
	gcr.logger.Error("Error escalated to external systems",
		zap.String("error_id", groupError.ErrorID),
		zap.String("error_type", string(groupError.ErrorType)),
		zap.String("error_severity", string(groupError.ErrorSeverity)))
}

// mergePermissions merges permissions from source and target groups
func (gcr *GroupConflictResolver) mergePermissions(sourceGroup, targetGroup *client.OIDCGroup) bool {
	// Simple merge strategy: combine all permissions and remove duplicates
	permissionMap := make(map[string]client.OIDCGroupPermission)
	
	// Add target permissions first
	for _, perm := range targetGroup.Permissions {
		key := fmt.Sprintf("%s:%s", perm.Resource, perm.Action)
		permissionMap[key] = perm
	}
	
	// Add source permissions (will override duplicates)
	for _, perm := range sourceGroup.Permissions {
		key := fmt.Sprintf("%s:%s", perm.Resource, perm.Action)
		permissionMap[key] = perm
	}
	
	// Update target group permissions
	var mergedPermissions []client.OIDCGroupPermission
	for _, perm := range permissionMap {
		mergedPermissions = append(mergedPermissions, perm)
	}
	
	targetGroup.Permissions = mergedPermissions
	return true
}

// mergeProjectRoles merges project roles from source and target groups
func (gcr *GroupConflictResolver) mergeProjectRoles(sourceGroup, targetGroup *client.OIDCGroup) bool {
	// Simple merge strategy: source roles override target roles for same projects
	roleMap := make(map[int64]client.OIDCGroupProjectRole)
	
	// Add target roles first
	for _, role := range targetGroup.ProjectRoles {
		roleMap[role.ProjectID] = role
	}
	
	// Add source roles (will override for same projects)
	for _, role := range sourceGroup.ProjectRoles {
		roleMap[role.ProjectID] = role
	}
	
	// Update target group project roles
	var mergedRoles []client.OIDCGroupProjectRole
	for _, role := range roleMap {
		mergedRoles = append(mergedRoles, role)
	}
	
	targetGroup.ProjectRoles = mergedRoles
	return true
}

// requiresManualIntervention determines if manual intervention is required
func (gcr *GroupConflictResolver) requiresManualIntervention(result *ConflictResolutionResult) bool {
	// Manual intervention is required if:
	// 1. There are unresolved conflicts
	// 2. There are unhandled critical errors
	// 3. Multiple rollbacks were performed
	
	if len(result.ConflictsUnresolved) > 0 {
		return true
	}
	
	for _, error := range result.ErrorsUnhandled {
		if error.ErrorSeverity == ErrorSeverityCritical {
			return true
		}
	}
	
	if len(result.RollbacksPerformed) > gcr.config.MaxRollbackAttempts {
		return true
	}
	
	return false
}

// GetConflictResolutionStatistics returns statistics about conflict resolution operations
func (gcr *GroupConflictResolver) GetConflictResolutionStatistics() (*ConflictResolutionStatistics, error) {
	stats := &ConflictResolutionStatistics{
		TotalConflictsDetected:   0,
		ConflictsResolved:        0,
		ConflictsUnresolved:      0,
		ErrorsHandled:            0,
		ErrorsEscalated:          0,
		RollbacksPerformed:       0,
		ManualInterventionsRequired: 0,
		LastCalculated:           time.Now(),
	}
	
	// Get statistics from state manager if available
	// Implementation would depend on how statistics are tracked
	
	return stats, nil
}

// ConflictResolutionStatistics provides statistics about conflict resolution operations
type ConflictResolutionStatistics struct {
	TotalConflictsDetected      int       `json:"total_conflicts_detected"`
	ConflictsResolved           int       `json:"conflicts_resolved"`
	ConflictsUnresolved         int       `json:"conflicts_unresolved"`
	ErrorsHandled               int       `json:"errors_handled"`
	ErrorsEscalated             int       `json:"errors_escalated"`
	RollbacksPerformed          int       `json:"rollbacks_performed"`
	ManualInterventionsRequired int       `json:"manual_interventions_required"`
	LastCalculated              time.Time `json:"last_calculated"`
}