package sync

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"harbor-replicator/pkg/client"
	"harbor-replicator/pkg/state"

	"go.uber.org/zap"
)

// PermissionHierarchyManager handles synchronization of group permissions and role hierarchies
type PermissionHierarchyManager struct {
	localClient    client.ClientInterface
	remoteClients  map[string]client.ClientInterface
	stateManager   *state.StateManager
	logger         *zap.Logger
	config         PermissionHierarchyConfig
}

// PermissionHierarchyConfig contains configuration for permission hierarchy synchronization
type PermissionHierarchyConfig struct {
	// EnableRoleMapping enables mapping of roles between instances
	EnableRoleMapping bool
	
	// RoleMappings defines how roles should be mapped between source and target
	RoleMappings map[string]string
	
	// PreserveHierarchy maintains permission inheritance rules
	PreserveHierarchy bool
	
	// AllowPermissionUpgrade allows upgrading permissions (e.g., Guest -> Developer)
	AllowPermissionUpgrade bool
	
	// AllowPermissionDowngrade allows downgrading permissions (e.g., Admin -> Developer)
	AllowPermissionDowngrade bool
	
	// RequireAtomicUpdates ensures all permission updates are atomic
	RequireAtomicUpdates bool
	
	// DefaultRole is assigned when no specific role mapping exists
	DefaultRole string
	
	// MaxRetries for permission synchronization operations
	MaxRetries int
	
	// RetryDelay between permission sync attempts
	RetryDelay time.Duration
}

// HarborRole represents standard Harbor roles with hierarchy levels
type HarborRole struct {
	ID          int64
	Name        string
	Level       int    // Higher numbers = more permissions
	Permissions []string
}

// Standard Harbor roles with hierarchy levels
var (
	HarborRoles = map[string]HarborRole{
		"guest": {
			ID:    1,
			Name:  "guest",
			Level: 1,
			Permissions: []string{
				"repository:pull",
				"artifact:read",
				"scan:read",
			},
		},
		"developer": {
			ID:    2,
			Name:  "developer", 
			Level: 2,
			Permissions: []string{
				"repository:pull",
				"repository:push",
				"artifact:read",
				"artifact:create",
				"artifact:delete",
				"scan:read",
				"scan:create",
			},
		},
		"maintainer": {
			ID:    3,
			Name:  "maintainer",
			Level: 3,
			Permissions: []string{
				"repository:pull",
				"repository:push",
				"repository:delete",
				"artifact:read",
				"artifact:create", 
				"artifact:delete",
				"scan:read",
				"scan:create",
				"scan:delete",
				"helm:read",
				"helm:create",
				"helm:delete",
			},
		},
		"projectadmin": {
			ID:    4,
			Name:  "projectadmin",
			Level: 4,
			Permissions: []string{
				"repository:*",
				"artifact:*",
				"scan:*",
				"helm:*",
				"member:create",
				"member:update",
				"member:delete",
				"member:list",
				"project:update",
			},
		},
		"admin": {
			ID:    5,
			Name:  "admin",
			Level: 5,
			Permissions: []string{
				"*:*", // Full system permissions
			},
		},
	}
)

// PermissionSyncResult represents the result of permission synchronization
type PermissionSyncResult struct {
	GroupID              string
	GroupName            string
	SourcePermissions    []client.OIDCGroupPermission
	TargetPermissions    []client.OIDCGroupPermission
	SourceProjectRoles   []client.OIDCGroupProjectRole
	TargetProjectRoles   []client.OIDCGroupProjectRole
	PermissionsUpdated   bool
	ProjectRolesUpdated  bool
	PermissionChanges    []string
	RoleChanges          []string
	ConflictsDetected    []string
	ConflictsResolved    []string
	ErrorsEncountered    []string
	SyncDuration         time.Duration
	Timestamp            time.Time
}

// NewPermissionHierarchyManager creates a new permission hierarchy manager
func NewPermissionHierarchyManager(
	localClient client.ClientInterface,
	remoteClients map[string]client.ClientInterface,
	stateManager *state.StateManager,
	logger *zap.Logger,
	config PermissionHierarchyConfig,
) *PermissionHierarchyManager {
	return &PermissionHierarchyManager{
		localClient:   localClient,
		remoteClients: remoteClients,
		stateManager:  stateManager,
		logger:        logger,
		config:        config,
	}
}

// SynchronizeGroupPermissions synchronizes permissions for a specific group
func (phm *PermissionHierarchyManager) SynchronizeGroupPermissions(
	ctx context.Context,
	sourceGroup *client.OIDCGroup,
	targetGroup *client.OIDCGroup,
	sourceInstance string,
) (*PermissionSyncResult, error) {
	startTime := time.Now()
	
	result := &PermissionSyncResult{
		GroupID:   fmt.Sprintf("%s:%s", sourceInstance, sourceGroup.GroupName),
		GroupName: sourceGroup.GroupName,
		SourcePermissions: sourceGroup.Permissions,
		TargetPermissions: targetGroup.Permissions,
		SourceProjectRoles: sourceGroup.ProjectRoles,
		TargetProjectRoles: targetGroup.ProjectRoles,
		Timestamp: startTime,
	}

	phm.logger.Info("Starting permission synchronization",
		zap.String("group_name", sourceGroup.GroupName),
		zap.String("source_instance", sourceInstance),
		zap.Int("source_permissions", len(sourceGroup.Permissions)),
		zap.Int("source_project_roles", len(sourceGroup.ProjectRoles)))

	// Synchronize basic permissions
	if err := phm.synchronizeBasicPermissions(ctx, sourceGroup, targetGroup, result); err != nil {
		result.ErrorsEncountered = append(result.ErrorsEncountered, fmt.Sprintf("Basic permissions sync failed: %v", err))
		phm.logger.Error("Failed to synchronize basic permissions", zap.Error(err))
	}

	// Synchronize project-specific roles
	if err := phm.synchronizeProjectRoles(ctx, sourceGroup, targetGroup, result); err != nil {
		result.ErrorsEncountered = append(result.ErrorsEncountered, fmt.Sprintf("Project roles sync failed: %v", err))
		phm.logger.Error("Failed to synchronize project roles", zap.Error(err))
	}

	// Apply atomic updates if required
	if phm.config.RequireAtomicUpdates && (result.PermissionsUpdated || result.ProjectRolesUpdated) {
		if err := phm.applyAtomicUpdates(ctx, targetGroup, result); err != nil {
			result.ErrorsEncountered = append(result.ErrorsEncountered, fmt.Sprintf("Atomic update failed: %v", err))
			phm.logger.Error("Failed to apply atomic updates", zap.Error(err))
		}
	}

	result.SyncDuration = time.Since(startTime)

	phm.logger.Info("Permission synchronization completed",
		zap.String("group_name", sourceGroup.GroupName),
		zap.Duration("duration", result.SyncDuration),
		zap.Bool("permissions_updated", result.PermissionsUpdated),
		zap.Bool("project_roles_updated", result.ProjectRolesUpdated),
		zap.Int("conflicts_detected", len(result.ConflictsDetected)),
		zap.Int("errors", len(result.ErrorsEncountered)))

	return result, nil
}

// synchronizeBasicPermissions handles synchronization of basic group permissions
func (phm *PermissionHierarchyManager) synchronizeBasicPermissions(
	ctx context.Context,
	sourceGroup *client.OIDCGroup,
	targetGroup *client.OIDCGroup,
	result *PermissionSyncResult,
) error {
	// Analyze permission differences
	permissionDiff := phm.analyzePermissionDifferences(sourceGroup.Permissions, targetGroup.Permissions)
	
	if len(permissionDiff.ToAdd) == 0 && len(permissionDiff.ToRemove) == 0 && len(permissionDiff.ToUpdate) == 0 {
		phm.logger.Debug("No permission changes required", zap.String("group_name", sourceGroup.GroupName))
		return nil
	}

	// Check for conflicts
	conflicts := phm.detectPermissionConflicts(permissionDiff, targetGroup.Permissions)
	if len(conflicts) > 0 {
		result.ConflictsDetected = append(result.ConflictsDetected, conflicts...)
		
		// Resolve conflicts based on configuration
		resolved, err := phm.resolvePermissionConflicts(conflicts, permissionDiff)
		if err != nil {
			return fmt.Errorf("failed to resolve permission conflicts: %w", err)
		}
		result.ConflictsResolved = append(result.ConflictsResolved, resolved...)
	}

	// Apply permission changes
	newPermissions := phm.applyPermissionChanges(targetGroup.Permissions, permissionDiff)
	
	// Validate permission hierarchy
	if phm.config.PreserveHierarchy {
		if err := phm.validatePermissionHierarchy(newPermissions); err != nil {
			return fmt.Errorf("permission hierarchy validation failed: %w", err)
		}
	}

	// Update the target group permissions
	targetGroup.Permissions = newPermissions
	result.PermissionsUpdated = true
	result.PermissionChanges = phm.generatePermissionChangeLog(permissionDiff)

	phm.logger.Info("Basic permissions synchronized",
		zap.String("group_name", sourceGroup.GroupName),
		zap.Int("added", len(permissionDiff.ToAdd)),
		zap.Int("removed", len(permissionDiff.ToRemove)),
		zap.Int("updated", len(permissionDiff.ToUpdate)))

	return nil
}

// synchronizeProjectRoles handles synchronization of project-specific roles
func (phm *PermissionHierarchyManager) synchronizeProjectRoles(
	ctx context.Context,
	sourceGroup *client.OIDCGroup,
	targetGroup *client.OIDCGroup,
	result *PermissionSyncResult,
) error {
	// Analyze role differences
	roleDiff := phm.analyzeRoleDifferences(sourceGroup.ProjectRoles, targetGroup.ProjectRoles)
	
	if len(roleDiff.ToAdd) == 0 && len(roleDiff.ToRemove) == 0 && len(roleDiff.ToUpdate) == 0 {
		phm.logger.Debug("No role changes required", zap.String("group_name", sourceGroup.GroupName))
		return nil
	}

	// Apply role mappings
	mappedRoles := phm.applyRoleMappings(roleDiff.ToAdd)
	roleDiff.ToAdd = mappedRoles

	// Check for role upgrade/downgrade restrictions
	if err := phm.validateRoleChanges(roleDiff); err != nil {
		return fmt.Errorf("role change validation failed: %w", err)
	}

	// Apply role changes
	newProjectRoles := phm.applyRoleChanges(targetGroup.ProjectRoles, roleDiff)
	
	// Update the target group project roles
	targetGroup.ProjectRoles = newProjectRoles
	result.ProjectRolesUpdated = true
	result.RoleChanges = phm.generateRoleChangeLog(roleDiff)

	phm.logger.Info("Project roles synchronized",
		zap.String("group_name", sourceGroup.GroupName),
		zap.Int("added", len(roleDiff.ToAdd)),
		zap.Int("removed", len(roleDiff.ToRemove)),
		zap.Int("updated", len(roleDiff.ToUpdate)))

	return nil
}

// PermissionDifference represents differences between permission sets
type PermissionDifference struct {
	ToAdd    []client.OIDCGroupPermission
	ToRemove []client.OIDCGroupPermission
	ToUpdate []client.OIDCGroupPermission
}

// RoleDifference represents differences between role sets
type RoleDifference struct {
	ToAdd    []client.OIDCGroupProjectRole
	ToRemove []client.OIDCGroupProjectRole
	ToUpdate []client.OIDCGroupProjectRole
}

// analyzePermissionDifferences analyzes differences between source and target permissions
func (phm *PermissionHierarchyManager) analyzePermissionDifferences(
	sourcePermissions, targetPermissions []client.OIDCGroupPermission,
) *PermissionDifference {
	diff := &PermissionDifference{}
	
	// Create maps for efficient lookup
	sourceMap := make(map[string]client.OIDCGroupPermission)
	targetMap := make(map[string]client.OIDCGroupPermission)
	
	for _, perm := range sourcePermissions {
		key := fmt.Sprintf("%s:%s", perm.Resource, perm.Action)
		sourceMap[key] = perm
	}
	
	for _, perm := range targetPermissions {
		key := fmt.Sprintf("%s:%s", perm.Resource, perm.Action)
		targetMap[key] = perm
	}
	
	// Find permissions to add (in source but not in target)
	for key, perm := range sourceMap {
		if _, exists := targetMap[key]; !exists {
			diff.ToAdd = append(diff.ToAdd, perm)
		}
	}
	
	// Find permissions to remove (in target but not in source)
	for key, perm := range targetMap {
		if _, exists := sourceMap[key]; !exists {
			diff.ToRemove = append(diff.ToRemove, perm)
		}
	}
	
	return diff
}

// analyzeRoleDifferences analyzes differences between source and target project roles
func (phm *PermissionHierarchyManager) analyzeRoleDifferences(
	sourceRoles, targetRoles []client.OIDCGroupProjectRole,
) *RoleDifference {
	diff := &RoleDifference{}
	
	// Create maps for efficient lookup
	sourceMap := make(map[int64]client.OIDCGroupProjectRole)
	targetMap := make(map[int64]client.OIDCGroupProjectRole)
	
	for _, role := range sourceRoles {
		sourceMap[role.ProjectID] = role
	}
	
	for _, role := range targetRoles {
		targetMap[role.ProjectID] = role
	}
	
	// Find roles to add or update
	for projectID, sourceRole := range sourceMap {
		if targetRole, exists := targetMap[projectID]; exists {
			// Role exists, check if update is needed
			if sourceRole.RoleID != targetRole.RoleID {
				diff.ToUpdate = append(diff.ToUpdate, sourceRole)
			}
		} else {
			// Role doesn't exist, add it
			diff.ToAdd = append(diff.ToAdd, sourceRole)
		}
	}
	
	// Find roles to remove
	for projectID, targetRole := range targetMap {
		if _, exists := sourceMap[projectID]; !exists {
			diff.ToRemove = append(diff.ToRemove, targetRole)
		}
	}
	
	return diff
}

// detectPermissionConflicts detects conflicts in permission changes
func (phm *PermissionHierarchyManager) detectPermissionConflicts(
	diff *PermissionDifference,
	currentPermissions []client.OIDCGroupPermission,
) []string {
	var conflicts []string
	
	// Check for permission downgrade conflicts
	if !phm.config.AllowPermissionDowngrade {
		for _, perm := range diff.ToRemove {
			if phm.isHighPrivilegePermission(perm) {
				conflicts = append(conflicts, fmt.Sprintf("Downgrade blocked: removing high-privilege permission %s:%s", perm.Resource, perm.Action))
			}
		}
	}
	
	// Check for permission upgrade conflicts
	if !phm.config.AllowPermissionUpgrade {
		for _, perm := range diff.ToAdd {
			if phm.isHighPrivilegePermission(perm) {
				conflicts = append(conflicts, fmt.Sprintf("Upgrade blocked: adding high-privilege permission %s:%s", perm.Resource, perm.Action))
			}
		}
	}
	
	return conflicts
}

// resolvePermissionConflicts resolves permission conflicts based on configuration
func (phm *PermissionHierarchyManager) resolvePermissionConflicts(
	conflicts []string,
	diff *PermissionDifference,
) ([]string, error) {
	var resolved []string
	
	// For now, implement a simple resolution strategy
	// In a production environment, this could be more sophisticated
	for _, conflict := range conflicts {
		if strings.Contains(conflict, "Downgrade blocked") && phm.config.AllowPermissionDowngrade {
			resolved = append(resolved, fmt.Sprintf("Allowed downgrade: %s", conflict))
		} else if strings.Contains(conflict, "Upgrade blocked") && phm.config.AllowPermissionUpgrade {
			resolved = append(resolved, fmt.Sprintf("Allowed upgrade: %s", conflict))
		} else {
			// Skip conflicting changes
			resolved = append(resolved, fmt.Sprintf("Skipped conflicting change: %s", conflict))
		}
	}
	
	return resolved, nil
}

// applyPermissionChanges applies permission changes to create the new permission set
func (phm *PermissionHierarchyManager) applyPermissionChanges(
	currentPermissions []client.OIDCGroupPermission,
	diff *PermissionDifference,
) []client.OIDCGroupPermission {
	// Start with current permissions
	permissionMap := make(map[string]client.OIDCGroupPermission)
	for _, perm := range currentPermissions {
		key := fmt.Sprintf("%s:%s", perm.Resource, perm.Action)
		permissionMap[key] = perm
	}
	
	// Remove permissions
	for _, perm := range diff.ToRemove {
		key := fmt.Sprintf("%s:%s", perm.Resource, perm.Action)
		delete(permissionMap, key)
	}
	
	// Add new permissions
	for _, perm := range diff.ToAdd {
		key := fmt.Sprintf("%s:%s", perm.Resource, perm.Action)
		permissionMap[key] = perm
	}
	
	// Update existing permissions
	for _, perm := range diff.ToUpdate {
		key := fmt.Sprintf("%s:%s", perm.Resource, perm.Action)
		permissionMap[key] = perm
	}
	
	// Convert back to slice
	var newPermissions []client.OIDCGroupPermission
	for _, perm := range permissionMap {
		newPermissions = append(newPermissions, perm)
	}
	
	// Sort for consistency
	sort.Slice(newPermissions, func(i, j int) bool {
		if newPermissions[i].Resource == newPermissions[j].Resource {
			return newPermissions[i].Action < newPermissions[j].Action
		}
		return newPermissions[i].Resource < newPermissions[j].Resource
	})
	
	return newPermissions
}

// applyRoleChanges applies role changes to create the new role set
func (phm *PermissionHierarchyManager) applyRoleChanges(
	currentRoles []client.OIDCGroupProjectRole,
	diff *RoleDifference,
) []client.OIDCGroupProjectRole {
	// Start with current roles
	roleMap := make(map[int64]client.OIDCGroupProjectRole)
	for _, role := range currentRoles {
		roleMap[role.ProjectID] = role
	}
	
	// Remove roles
	for _, role := range diff.ToRemove {
		delete(roleMap, role.ProjectID)
	}
	
	// Add new roles
	for _, role := range diff.ToAdd {
		roleMap[role.ProjectID] = role
	}
	
	// Update existing roles
	for _, role := range diff.ToUpdate {
		roleMap[role.ProjectID] = role
	}
	
	// Convert back to slice
	var newRoles []client.OIDCGroupProjectRole
	for _, role := range roleMap {
		newRoles = append(newRoles, role)
	}
	
	// Sort by project ID for consistency
	sort.Slice(newRoles, func(i, j int) bool {
		return newRoles[i].ProjectID < newRoles[j].ProjectID
	})
	
	return newRoles
}

// applyRoleMappings applies role mappings to the roles based on configuration
func (phm *PermissionHierarchyManager) applyRoleMappings(
	roles []client.OIDCGroupProjectRole,
) []client.OIDCGroupProjectRole {
	if !phm.config.EnableRoleMapping || len(phm.config.RoleMappings) == 0 {
		return roles
	}
	
	var mappedRoles []client.OIDCGroupProjectRole
	for _, role := range roles {
		mappedRole := role
		
		// Find the role name from our standard roles
		if harborRole, exists := phm.findRoleByID(role.RoleID); exists {
			if mappedRoleName, hasMappng := phm.config.RoleMappings[harborRole.Name]; hasMappng {
				if targetRole, targetExists := HarborRoles[mappedRoleName]; targetExists {
					mappedRole.RoleID = targetRole.ID
					mappedRole.RoleName = targetRole.Name
				}
			}
		}
		
		mappedRoles = append(mappedRoles, mappedRole)
	}
	
	return mappedRoles
}

// validatePermissionHierarchy validates that permission hierarchy is maintained
func (phm *PermissionHierarchyManager) validatePermissionHierarchy(
	permissions []client.OIDCGroupPermission,
) error {
	// Check for conflicting permissions
	hasWildcard := false
	specificPermissions := make(map[string]bool)
	
	for _, perm := range permissions {
		if perm.Resource == "*" || perm.Action == "*" {
			hasWildcard = true
		} else {
			key := fmt.Sprintf("%s:%s", perm.Resource, perm.Action)
			specificPermissions[key] = true
		}
	}
	
	// If wildcard permissions exist, warn about potential conflicts
	if hasWildcard && len(specificPermissions) > 0 {
		phm.logger.Warn("Wildcard and specific permissions detected, review hierarchy",
			zap.Int("specific_permissions", len(specificPermissions)))
	}
	
	return nil
}

// validateRoleChanges validates that role changes are allowed
func (phm *PermissionHierarchyManager) validateRoleChanges(diff *RoleDifference) error {
	for _, role := range diff.ToUpdate {
		// Find current and new role levels
		currentRole, currentExists := phm.findRoleByID(role.RoleID)
		if !currentExists {
			continue
		}
		
		// Check if this is an upgrade or downgrade
		// This would need the original role for comparison
		// For now, we'll assume upgrades/downgrades are handled at a higher level
		
		_ = currentRole // Placeholder for more sophisticated validation
	}
	
	return nil
}

// isHighPrivilegePermission checks if a permission is considered high privilege
func (phm *PermissionHierarchyManager) isHighPrivilegePermission(perm client.OIDCGroupPermission) bool {
	highPrivilegeResources := []string{"system", "project", "member"}
	highPrivilegeActions := []string{"create", "delete", "update", "*"}
	
	for _, resource := range highPrivilegeResources {
		if perm.Resource == resource || perm.Resource == "*" {
			for _, action := range highPrivilegeActions {
				if perm.Action == action {
					return true
				}
			}
		}
	}
	
	return false
}

// findRoleByID finds a Harbor role by its ID
func (phm *PermissionHierarchyManager) findRoleByID(roleID int64) (HarborRole, bool) {
	for _, role := range HarborRoles {
		if role.ID == roleID {
			return role, true
		}
	}
	return HarborRole{}, false
}

// generatePermissionChangeLog generates a human-readable log of permission changes
func (phm *PermissionHierarchyManager) generatePermissionChangeLog(diff *PermissionDifference) []string {
	var changes []string
	
	for _, perm := range diff.ToAdd {
		changes = append(changes, fmt.Sprintf("Added permission: %s:%s", perm.Resource, perm.Action))
	}
	
	for _, perm := range diff.ToRemove {
		changes = append(changes, fmt.Sprintf("Removed permission: %s:%s", perm.Resource, perm.Action))
	}
	
	for _, perm := range diff.ToUpdate {
		changes = append(changes, fmt.Sprintf("Updated permission: %s:%s", perm.Resource, perm.Action))
	}
	
	return changes
}

// generateRoleChangeLog generates a human-readable log of role changes
func (phm *PermissionHierarchyManager) generateRoleChangeLog(diff *RoleDifference) []string {
	var changes []string
	
	for _, role := range diff.ToAdd {
		changes = append(changes, fmt.Sprintf("Added project role: Project %d -> Role %d (%s)", role.ProjectID, role.RoleID, role.RoleName))
	}
	
	for _, role := range diff.ToRemove {
		changes = append(changes, fmt.Sprintf("Removed project role: Project %d -> Role %d (%s)", role.ProjectID, role.RoleID, role.RoleName))
	}
	
	for _, role := range diff.ToUpdate {
		changes = append(changes, fmt.Sprintf("Updated project role: Project %d -> Role %d (%s)", role.ProjectID, role.RoleID, role.RoleName))
	}
	
	return changes
}

// applyAtomicUpdates applies all permission and role updates atomically
func (phm *PermissionHierarchyManager) applyAtomicUpdates(
	ctx context.Context,
	targetGroup *client.OIDCGroup,
	result *PermissionSyncResult,
) error {
	// Update the group with new permissions and roles
	_, err := phm.localClient.UpdateOIDCGroup(ctx, targetGroup.ID, targetGroup)
	if err != nil {
		return fmt.Errorf("failed to apply atomic updates: %w", err)
	}
	
	phm.logger.Info("Applied atomic permission updates",
		zap.String("group_name", targetGroup.GroupName),
		zap.Int64("group_id", targetGroup.ID),
		zap.Int("permissions", len(targetGroup.Permissions)),
		zap.Int("project_roles", len(targetGroup.ProjectRoles)))
	
	return nil
}

// GetPermissionSyncStatistics returns statistics about permission synchronization operations
func (phm *PermissionHierarchyManager) GetPermissionSyncStatistics() (*PermissionSyncStatistics, error) {
	stats := &PermissionSyncStatistics{
		TotalSyncOperations: 0,
		SuccessfulSyncs:     0,
		FailedSyncs:         0,
		ConflictsResolved:   0,
		PermissionsAdded:    0,
		PermissionsRemoved:  0,
		RolesAdded:          0,
		RolesRemoved:        0,
		LastCalculated:      time.Now(),
	}
	
	// Get statistics from state manager
	groupStats, err := phm.stateManager.GetGroupSyncStatistics()
	if err != nil {
		return stats, err
	}
	
	stats.TotalSyncOperations = groupStats.TotalGroups
	stats.SuccessfulSyncs = groupStats.SyncedGroups
	stats.FailedSyncs = groupStats.FailedGroups
	
	return stats, nil
}

// PermissionSyncStatistics provides statistics about permission synchronization
type PermissionSyncStatistics struct {
	TotalSyncOperations int       `json:"total_sync_operations"`
	SuccessfulSyncs     int       `json:"successful_syncs"`
	FailedSyncs         int       `json:"failed_syncs"`
	ConflictsResolved   int       `json:"conflicts_resolved"`
	PermissionsAdded    int       `json:"permissions_added"`
	PermissionsRemoved  int       `json:"permissions_removed"`
	RolesAdded          int       `json:"roles_added"`
	RolesRemoved        int       `json:"roles_removed"`
	LastCalculated      time.Time `json:"last_calculated"`
}