package sync

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"harbor-replicator/pkg/client"
	"harbor-replicator/pkg/state"

	"go.uber.org/zap"
)

// GroupResolver handles group matching and resolution logic
type GroupResolver struct {
	localClient    client.ClientInterface
	remoteClients  map[string]client.ClientInterface
	stateManager   *state.StateManager
	logger         *zap.Logger
	config         GroupResolverConfig
}

// GroupResolverConfig contains configuration for the group resolver
type GroupResolverConfig struct {
	// MatchingStrategy defines how groups are matched between instances
	MatchingStrategy GroupMatchingStrategy
	
	// AllowCreateGroups enables automatic creation of missing groups
	AllowCreateGroups bool
	
	// StrictNameMatching requires exact name matches
	StrictNameMatching bool
	
	// NormalizeLDAPDN enables LDAP DN normalization for matching
	NormalizeLDAPDN bool
	
	// ConflictResolution defines how to handle conflicts
	ConflictResolution state.ConflictStrategy
	
	// MaxRetries for group resolution operations
	MaxRetries int
	
	// RetryDelay between resolution attempts
	RetryDelay time.Duration
}

// GroupMatchingStrategy defines the strategy for matching groups
type GroupMatchingStrategy string

const (
	MatchingStrategyName     GroupMatchingStrategy = "name"
	MatchingStrategyLDAPDN   GroupMatchingStrategy = "ldap_dn"
	MatchingStrategyOIDCClaim GroupMatchingStrategy = "oidc_claim"
	MatchingStrategyHybrid   GroupMatchingStrategy = "hybrid"
)

// GroupResolutionResult represents the result of group resolution
type GroupResolutionResult struct {
	SourceGroup      *client.OIDCGroup
	TargetGroup      *client.OIDCGroup
	Action           GroupResolutionAction
	ConflictDetected bool
	ConflictReason   string
	CreatedNewGroup  bool
	UpdateRequired   bool
	Changes          []string
	ResolutionTime   time.Time
}

// GroupResolutionAction defines the action taken during resolution
type GroupResolutionAction string

const (
	ActionMatched    GroupResolutionAction = "matched"
	ActionCreated    GroupResolutionAction = "created"
	ActionUpdated    GroupResolutionAction = "updated"
	ActionSkipped    GroupResolutionAction = "skipped"
	ActionConflict   GroupResolutionAction = "conflict"
	ActionError      GroupResolutionAction = "error"
)

// NewGroupResolver creates a new group resolver
func NewGroupResolver(
	localClient client.ClientInterface,
	remoteClients map[string]client.ClientInterface,
	stateManager *state.StateManager,
	logger *zap.Logger,
	config GroupResolverConfig,
) *GroupResolver {
	return &GroupResolver{
		localClient:   localClient,
		remoteClients: remoteClients,
		stateManager:  stateManager,
		logger:        logger,
		config:        config,
	}
}

// FindOrCreateGroup finds an existing group or creates a new one based on the source group
func (gr *GroupResolver) FindOrCreateGroup(ctx context.Context, sourceGroup *client.OIDCGroup, sourceInstance string) (*GroupResolutionResult, error) {
	if sourceGroup == nil {
		return nil, fmt.Errorf("source group cannot be nil")
	}

	result := &GroupResolutionResult{
		SourceGroup:    sourceGroup,
		ResolutionTime: time.Now(),
	}

	gr.logger.Debug("Starting group resolution",
		zap.String("group_name", sourceGroup.GroupName),
		zap.String("source_instance", sourceInstance),
		zap.String("matching_strategy", string(gr.config.MatchingStrategy)))

	// First, try to find an existing matching group
	existingGroup, err := gr.findMatchingGroup(ctx, sourceGroup)
	if err != nil {
		gr.logger.Error("Error finding matching group", zap.Error(err))
		result.Action = ActionError
		return result, err
	}

	if existingGroup != nil {
		// Group exists, check if update is needed
		result.TargetGroup = existingGroup
		result.Action = ActionMatched

		// Compare groups to detect changes
		changes, conflict := gr.compareGroups(sourceGroup, existingGroup)
		if len(changes) > 0 {
			result.UpdateRequired = true
			result.Changes = changes
			
			if conflict {
				result.ConflictDetected = true
				result.Action = ActionConflict
				result.ConflictReason = "Group properties conflict between source and target"
				
				// Handle conflict based on configuration
				if err := gr.handleGroupConflict(ctx, sourceGroup, existingGroup, result); err != nil {
					return result, err
				}
			} else {
				// No conflict, update the group
				if err := gr.updateGroup(ctx, sourceGroup, existingGroup, result); err != nil {
					return result, err
				}
			}
		}
	} else {
		// Group doesn't exist, create if allowed
		if gr.config.AllowCreateGroups {
			newGroup, err := gr.createGroup(ctx, sourceGroup, result)
			if err != nil {
				result.Action = ActionError
				return result, err
			}
			result.TargetGroup = newGroup
			result.Action = ActionCreated
			result.CreatedNewGroup = true
		} else {
			result.Action = ActionSkipped
			gr.logger.Info("Group creation disabled, skipping",
				zap.String("group_name", sourceGroup.GroupName))
		}
	}

	// Update state
	if err := gr.updateGroupState(sourceGroup, result, sourceInstance); err != nil {
		gr.logger.Error("Failed to update group state", zap.Error(err))
	}

	gr.logger.Info("Group resolution completed",
		zap.String("group_name", sourceGroup.GroupName),
		zap.String("action", string(result.Action)),
		zap.Bool("conflict_detected", result.ConflictDetected),
		zap.Bool("update_required", result.UpdateRequired))

	return result, nil
}

// findMatchingGroup finds a group in the local instance that matches the source group
func (gr *GroupResolver) findMatchingGroup(ctx context.Context, sourceGroup *client.OIDCGroup) (*client.OIDCGroup, error) {
	switch gr.config.MatchingStrategy {
	case MatchingStrategyName:
		return gr.findGroupByName(ctx, sourceGroup.GroupName)
	case MatchingStrategyLDAPDN:
		return gr.findGroupByLDAPDN(ctx, sourceGroup.LdapGroupDN)
	case MatchingStrategyOIDCClaim:
		return gr.findGroupByOIDCClaim(ctx, sourceGroup.GroupName)
	case MatchingStrategyHybrid:
		return gr.findGroupHybrid(ctx, sourceGroup)
	default:
		return gr.findGroupByName(ctx, sourceGroup.GroupName)
	}
}

// findGroupByName finds a group by exact name match
func (gr *GroupResolver) findGroupByName(ctx context.Context, groupName string) (*client.OIDCGroup, error) {
	groups, err := gr.localClient.ListOIDCGroups(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list local groups: %w", err)
	}

	for _, group := range groups {
		if gr.config.StrictNameMatching {
			if group.GroupName == groupName {
				return &group, nil
			}
		} else {
			// Case-insensitive match
			if strings.EqualFold(group.GroupName, groupName) {
				return &group, nil
			}
		}
	}

	return nil, nil
}

// findGroupByLDAPDN finds a group by LDAP distinguished name
func (gr *GroupResolver) findGroupByLDAPDN(ctx context.Context, ldapDN string) (*client.OIDCGroup, error) {
	if ldapDN == "" {
		return nil, nil
	}

	groups, err := gr.localClient.ListOIDCGroups(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list local groups: %w", err)
	}

	normalizedSourceDN := gr.normalizeLDAPDN(ldapDN)

	for _, group := range groups {
		if group.LdapGroupDN != "" {
			normalizedTargetDN := gr.normalizeLDAPDN(group.LdapGroupDN)
			if normalizedSourceDN == normalizedTargetDN {
				return &group, nil
			}
		}
	}

	return nil, nil
}

// findGroupByOIDCClaim finds a group by OIDC claim matching
func (gr *GroupResolver) findGroupByOIDCClaim(ctx context.Context, groupName string) (*client.OIDCGroup, error) {
	// For OIDC groups, the group name typically comes from the OIDC claim
	// This is similar to name matching but with OIDC-specific logic
	return gr.findGroupByName(ctx, groupName)
}

// findGroupHybrid uses multiple strategies to find a matching group
func (gr *GroupResolver) findGroupHybrid(ctx context.Context, sourceGroup *client.OIDCGroup) (*client.OIDCGroup, error) {
	// Try LDAP DN first if available
	if sourceGroup.LdapGroupDN != "" {
		if group, err := gr.findGroupByLDAPDN(ctx, sourceGroup.LdapGroupDN); err == nil && group != nil {
			return group, nil
		}
	}

	// Fall back to name matching
	return gr.findGroupByName(ctx, sourceGroup.GroupName)
}

// normalizeLDAPDN normalizes an LDAP distinguished name for comparison
func (gr *GroupResolver) normalizeLDAPDN(dn string) string {
	if !gr.config.NormalizeLDAPDN {
		return dn
	}

	// Basic normalization: remove extra spaces, convert to lowercase
	normalized := strings.TrimSpace(dn)
	normalized = strings.ToLower(normalized)
	
	// Remove extra spaces around commas
	normalized = strings.ReplaceAll(normalized, " ,", ",")
	normalized = strings.ReplaceAll(normalized, ", ", ",")
	
	return normalized
}

// compareGroups compares two groups and returns a list of differences
func (gr *GroupResolver) compareGroups(sourceGroup, targetGroup *client.OIDCGroup) ([]string, bool) {
	var changes []string
	hasConflict := false

	// Compare basic properties
	if sourceGroup.GroupName != targetGroup.GroupName {
		changes = append(changes, "group_name")
		// Name differences are typically conflicts unless we're doing normalization
		if gr.config.StrictNameMatching {
			hasConflict = true
		}
	}

	if sourceGroup.GroupType != targetGroup.GroupType {
		changes = append(changes, "group_type")
		hasConflict = true // Type mismatches are always conflicts
	}

	if sourceGroup.LdapGroupDN != targetGroup.LdapGroupDN {
		changes = append(changes, "ldap_group_dn")
		// LDAP DN differences might be conflicts depending on strategy
		if gr.config.MatchingStrategy == MatchingStrategyLDAPDN {
			hasConflict = true
		}
	}

	// Compare permissions
	if !gr.comparePermissions(sourceGroup.Permissions, targetGroup.Permissions) {
		changes = append(changes, "permissions")
	}

	// Compare project roles
	if !gr.compareProjectRoles(sourceGroup.ProjectRoles, targetGroup.ProjectRoles) {
		changes = append(changes, "project_roles")
	}

	return changes, hasConflict
}

// comparePermissions compares two permission slices
func (gr *GroupResolver) comparePermissions(perms1, perms2 []client.OIDCGroupPermission) bool {
	if len(perms1) != len(perms2) {
		return false
	}

	permsMap1 := make(map[string]client.OIDCGroupPermission)
	for _, perm := range perms1 {
		key := fmt.Sprintf("%s:%s", perm.Resource, perm.Action)
		permsMap1[key] = perm
	}

	for _, perm := range perms2 {
		key := fmt.Sprintf("%s:%s", perm.Resource, perm.Action)
		if _, exists := permsMap1[key]; !exists {
			return false
		}
	}

	return true
}

// compareProjectRoles compares two project role slices
func (gr *GroupResolver) compareProjectRoles(roles1, roles2 []client.OIDCGroupProjectRole) bool {
	if len(roles1) != len(roles2) {
		return false
	}

	rolesMap1 := make(map[int64]client.OIDCGroupProjectRole)
	for _, role := range roles1 {
		rolesMap1[role.ProjectID] = role
	}

	for _, role := range roles2 {
		if existingRole, exists := rolesMap1[role.ProjectID]; !exists || existingRole.RoleID != role.RoleID {
			return false
		}
	}

	return true
}

// handleGroupConflict handles conflicts based on the configured strategy
func (gr *GroupResolver) handleGroupConflict(ctx context.Context, sourceGroup, targetGroup *client.OIDCGroup, result *GroupResolutionResult) error {
	switch gr.config.ConflictResolution {
	case state.ConflictStrategySourceWins:
		return gr.updateGroup(ctx, sourceGroup, targetGroup, result)
	case state.ConflictStrategyTargetWins:
		result.Action = ActionSkipped
		gr.logger.Info("Conflict resolved by keeping target group unchanged",
			zap.String("group_name", sourceGroup.GroupName))
	case state.ConflictStrategySkip:
		result.Action = ActionSkipped
		gr.logger.Info("Conflict resolved by skipping synchronization",
			zap.String("group_name", sourceGroup.GroupName))
	case state.ConflictStrategyMerge:
		return gr.mergeGroups(ctx, sourceGroup, targetGroup, result)
	case state.ConflictStrategyManual:
		result.Action = ActionConflict
		gr.logger.Warn("Manual conflict resolution required",
			zap.String("group_name", sourceGroup.GroupName))
	}
	return nil
}

// createGroup creates a new group in the local Harbor instance
func (gr *GroupResolver) createGroup(ctx context.Context, sourceGroup *client.OIDCGroup, result *GroupResolutionResult) (*client.OIDCGroup, error) {
	// Create a copy of the source group for local creation
	newGroup := &client.OIDCGroup{
		GroupName:   sourceGroup.GroupName,
		GroupType:   sourceGroup.GroupType,
		LdapGroupDN: sourceGroup.LdapGroupDN,
		// Note: Permissions and ProjectRoles will be synchronized separately
	}

	createdGroup, err := gr.localClient.CreateOIDCGroup(ctx, newGroup)
	if err != nil {
		return nil, fmt.Errorf("failed to create group: %w", err)
	}

	gr.logger.Info("Created new group",
		zap.String("group_name", sourceGroup.GroupName),
		zap.Int64("group_id", createdGroup.ID))

	return createdGroup, nil
}

// updateGroup updates an existing group with source group properties
func (gr *GroupResolver) updateGroup(ctx context.Context, sourceGroup, targetGroup *client.OIDCGroup, result *GroupResolutionResult) error {
	// Create updated group with source properties
	updatedGroup := &client.OIDCGroup{
		ID:          targetGroup.ID, // Keep the existing ID
		GroupName:   sourceGroup.GroupName,
		GroupType:   sourceGroup.GroupType,
		LdapGroupDN: sourceGroup.LdapGroupDN,
		// Preserve creation time
		CreationTime: targetGroup.CreationTime,
	}

	_, err := gr.localClient.UpdateOIDCGroup(ctx, targetGroup.ID, updatedGroup)
	if err != nil {
		return fmt.Errorf("failed to update group: %w", err)
	}

	result.Action = ActionUpdated
	gr.logger.Info("Updated group",
		zap.String("group_name", sourceGroup.GroupName),
		zap.Int64("group_id", targetGroup.ID),
		zap.Strings("changes", result.Changes))

	return nil
}

// mergeGroups merges properties from source and target groups
func (gr *GroupResolver) mergeGroups(ctx context.Context, sourceGroup, targetGroup *client.OIDCGroup, result *GroupResolutionResult) error {
	// For now, implement a simple merge strategy that preserves target name but takes source permissions
	mergedGroup := &client.OIDCGroup{
		ID:           targetGroup.ID,
		GroupName:    targetGroup.GroupName, // Keep target name
		GroupType:    sourceGroup.GroupType, // Take source type
		LdapGroupDN:  sourceGroup.LdapGroupDN, // Take source LDAP DN
		CreationTime: targetGroup.CreationTime,
	}

	_, err := gr.localClient.UpdateOIDCGroup(ctx, targetGroup.ID, mergedGroup)
	if err != nil {
		return fmt.Errorf("failed to merge group: %w", err)
	}

	result.Action = ActionUpdated
	gr.logger.Info("Merged group",
		zap.String("group_name", sourceGroup.GroupName),
		zap.Int64("group_id", targetGroup.ID))

	return nil
}

// updateGroupState updates the group state in the state manager
func (gr *GroupResolver) updateGroupState(sourceGroup *client.OIDCGroup, result *GroupResolutionResult, sourceInstance string) error {
	groupID := fmt.Sprintf("%s:%s", sourceInstance, sourceGroup.GroupName)
	
	// Calculate group checksum for change detection
	checksum := gr.calculateGroupChecksum(sourceGroup)
	
	// Determine sync status based on result
	var syncStatus state.MappingStatus
	switch result.Action {
	case ActionMatched, ActionCreated, ActionUpdated:
		syncStatus = state.MappingStatusSynced
	case ActionConflict:
		syncStatus = state.MappingStatusConflict
	case ActionSkipped:
		syncStatus = state.MappingStatusSkipped
	case ActionError:
		syncStatus = state.MappingStatusFailed
	default:
		syncStatus = state.MappingStatusPending
	}

	groupState := state.GroupState{
		GroupID:            groupID,
		GroupName:          sourceGroup.GroupName,
		GroupType:          sourceGroup.GroupType,
		LdapGroupDN:        sourceGroup.LdapGroupDN,
		SyncStatus:         syncStatus,
		LastSyncTime:       time.Now(),
		GroupChecksum:      checksum,
		SourceInstance:     sourceInstance,
		TargetInstance:     "local",
		ConflictResolution: gr.config.ConflictResolution,
		ErrorCount:         0,
		RetryCount:         0,
	}

	// Set error information if action was error
	if result.Action == ActionError {
		groupState.ErrorCount = 1
		groupState.LastError = "Group resolution failed"
	}

	return gr.stateManager.SaveGroupState(groupID, groupState)
}

// calculateGroupChecksum calculates a checksum for the group for change detection
func (gr *GroupResolver) calculateGroupChecksum(group *client.OIDCGroup) string {
	data := fmt.Sprintf("%s:%d:%s:%d:%d",
		group.GroupName,
		group.GroupType,
		group.LdapGroupDN,
		len(group.Permissions),
		len(group.ProjectRoles))
	
	hash := sha256.Sum256([]byte(data))
	return fmt.Sprintf("%x", hash)
}

// GetMatchingStatistics returns statistics about group matching operations
func (gr *GroupResolver) GetMatchingStatistics() (GroupMatchingStatistics, error) {
	stats := GroupMatchingStatistics{
		MatchingStrategy:   gr.config.MatchingStrategy,
		TotalResolutions:   0,
		SuccessfulMatches:  0,
		NewGroupsCreated:   0,
		GroupsUpdated:      0,
		ConflictsDetected:  0,
		ResolutionFailures: 0,
		LastCalculated:     time.Now(),
	}

	// Get statistics from state manager
	groupStats, err := gr.stateManager.GetGroupSyncStatistics()
	if err != nil {
		return stats, err
	}

	stats.TotalResolutions = groupStats.TotalGroups
	stats.SuccessfulMatches = groupStats.SyncedGroups
	stats.ConflictsDetected = groupStats.ConflictGroups
	stats.ResolutionFailures = groupStats.FailedGroups

	return stats, nil
}

// GroupMatchingStatistics provides statistics about group matching operations
type GroupMatchingStatistics struct {
	MatchingStrategy   GroupMatchingStrategy `json:"matching_strategy"`
	TotalResolutions   int                   `json:"total_resolutions"`
	SuccessfulMatches  int                   `json:"successful_matches"`
	NewGroupsCreated   int                   `json:"new_groups_created"`
	GroupsUpdated      int                   `json:"groups_updated"`
	ConflictsDetected  int                   `json:"conflicts_detected"`
	ResolutionFailures int                   `json:"resolution_failures"`
	LastCalculated     time.Time             `json:"last_calculated"`
}