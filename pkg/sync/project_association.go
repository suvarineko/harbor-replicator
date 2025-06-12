package sync

import (
	"context"
	"fmt"
	"strings"
	"time"

	"harbor-replicator/pkg/client"
	"harbor-replicator/pkg/state"

	"go.uber.org/zap"
)

// ProjectAssociationManager handles synchronization of group-project associations
type ProjectAssociationManager struct {
	localClient    client.ClientInterface
	remoteClients  map[string]client.ClientInterface
	stateManager   *state.StateManager
	logger         *zap.Logger
	config         ProjectAssociationConfig
}

// ProjectAssociationConfig contains configuration for project association synchronization
type ProjectAssociationConfig struct {
	// EnableProjectMapping enables mapping of project names between instances
	EnableProjectMapping bool
	
	// ProjectMappings defines how project names should be mapped between source and target
	ProjectMappings map[string]string
	
	// AutoCreateProjects enables automatic creation of missing projects
	AutoCreateProjects bool
	
	// StrictProjectMatching requires exact project name matches
	StrictProjectMatching bool
	
	// PreserveLocalAssociations keeps existing local associations not in source
	PreserveLocalAssociations bool
	
	// ValidateProjectAccess verifies project access before creating associations
	ValidateProjectAccess bool
	
	// DefaultProjectRole is assigned when no specific role is defined
	DefaultProjectRole string
	
	// MaxConcurrentAssociations limits concurrent association operations
	MaxConcurrentAssociations int
	
	// AssociationTimeout for individual association operations
	AssociationTimeout time.Duration
	
	// MaxRetries for association operations
	MaxRetries int
	
	// RetryDelay between association attempts
	RetryDelay time.Duration
}

// ProjectAssociationResult represents the result of project association synchronization
type ProjectAssociationResult struct {
	GroupID                string
	GroupName              string
	SourceAssociations     []client.OIDCGroupProjectRole
	TargetAssociations     []client.OIDCGroupProjectRole
	AssociationsAdded      []ProjectAssociationChange
	AssociationsRemoved    []ProjectAssociationChange
	AssociationsUpdated    []ProjectAssociationChange
	ProjectsCreated        []ProjectCreationResult
	MappingConflicts       []ProjectMappingConflict
	ValidationErrors       []ProjectValidationError
	SyncDuration           time.Duration
	Timestamp              time.Time
}

// ProjectAssociationChange represents a change in project association
type ProjectAssociationChange struct {
	ProjectID       int64
	ProjectName     string
	SourceProjectID int64
	RoleID          int64
	RoleName        string
	PreviousRoleID  int64
	PreviousRoleName string
	Operation       string // "add", "remove", "update"
	Reason          string
}

// ProjectCreationResult represents the result of creating a project
type ProjectCreationResult struct {
	ProjectName   string
	ProjectID     int64
	SourceProject string
	Created       bool
	Error         string
}

// ProjectMappingConflict represents a conflict in project mapping
type ProjectMappingConflict struct {
	SourceProjectName string
	TargetProjectName string
	ConflictReason    string
	Resolution        string
}

// ProjectValidationError represents an error in project validation
type ProjectValidationError struct {
	ProjectName string
	Error       string
	Severity    string // "warning", "error"
}

// NewProjectAssociationManager creates a new project association manager
func NewProjectAssociationManager(
	localClient client.ClientInterface,
	remoteClients map[string]client.ClientInterface,
	stateManager *state.StateManager,
	logger *zap.Logger,
	config ProjectAssociationConfig,
) *ProjectAssociationManager {
	return &ProjectAssociationManager{
		localClient:   localClient,
		remoteClients: remoteClients,
		stateManager:  stateManager,
		logger:        logger,
		config:        config,
	}
}

// SynchronizeProjectAssociations synchronizes project associations for a group
func (pam *ProjectAssociationManager) SynchronizeProjectAssociations(
	ctx context.Context,
	sourceGroup *client.OIDCGroup,
	targetGroup *client.OIDCGroup,
	sourceInstance string,
) (*ProjectAssociationResult, error) {
	startTime := time.Now()
	
	result := &ProjectAssociationResult{
		GroupID:            fmt.Sprintf("%s:%s", sourceInstance, sourceGroup.GroupName),
		GroupName:          sourceGroup.GroupName,
		SourceAssociations: sourceGroup.ProjectRoles,
		TargetAssociations: targetGroup.ProjectRoles,
		Timestamp:          startTime,
	}

	pam.logger.Info("Starting project association synchronization",
		zap.String("group_name", sourceGroup.GroupName),
		zap.String("source_instance", sourceInstance),
		zap.Int("source_associations", len(sourceGroup.ProjectRoles)),
		zap.Int("target_associations", len(targetGroup.ProjectRoles)))

	// Get all projects from both instances for mapping
	sourceProjects, err := pam.getSourceProjects(ctx, sourceInstance)
	if err != nil {
		return result, fmt.Errorf("failed to get source projects: %w", err)
	}

	targetProjects, err := pam.getTargetProjects(ctx)
	if err != nil {
		return result, fmt.Errorf("failed to get target projects: %w", err)
	}

	// Create project mapping
	projectMapping, err := pam.createProjectMapping(sourceProjects, targetProjects, result)
	if err != nil {
		return result, fmt.Errorf("failed to create project mapping: %w", err)
	}

	// Validate source associations
	if err := pam.validateSourceAssociations(ctx, sourceGroup.ProjectRoles, sourceProjects, result); err != nil {
		pam.logger.Warn("Source association validation issues detected", zap.Error(err))
	}

	// Create missing projects if enabled
	if pam.config.AutoCreateProjects {
		if err := pam.createMissingProjects(ctx, sourceGroup.ProjectRoles, projectMapping, sourceProjects, result); err != nil {
			pam.logger.Error("Failed to create missing projects", zap.Error(err))
		}
	}

	// Analyze association differences
	associationDiff := pam.analyzeAssociationDifferences(sourceGroup.ProjectRoles, targetGroup.ProjectRoles, projectMapping)

	// Apply association changes
	if err := pam.applyAssociationChanges(ctx, targetGroup, associationDiff, projectMapping, result); err != nil {
		return result, fmt.Errorf("failed to apply association changes: %w", err)
	}

	result.SyncDuration = time.Since(startTime)

	pam.logger.Info("Project association synchronization completed",
		zap.String("group_name", sourceGroup.GroupName),
		zap.Duration("duration", result.SyncDuration),
		zap.Int("added", len(result.AssociationsAdded)),
		zap.Int("removed", len(result.AssociationsRemoved)),
		zap.Int("updated", len(result.AssociationsUpdated)),
		zap.Int("projects_created", len(result.ProjectsCreated)),
		zap.Int("conflicts", len(result.MappingConflicts)))

	return result, nil
}

// getSourceProjects retrieves projects from the source instance
func (pam *ProjectAssociationManager) getSourceProjects(ctx context.Context, sourceInstance string) ([]client.Project, error) {
	sourceClient, exists := pam.remoteClients[sourceInstance]
	if !exists {
		return nil, fmt.Errorf("source instance %s not found", sourceInstance)
	}

	projects, err := sourceClient.ListProjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list projects from source %s: %w", sourceInstance, err)
	}

	pam.logger.Debug("Retrieved source projects",
		zap.String("source_instance", sourceInstance),
		zap.Int("project_count", len(projects)))

	return projects, nil
}

// getTargetProjects retrieves projects from the target instance
func (pam *ProjectAssociationManager) getTargetProjects(ctx context.Context) ([]client.Project, error) {
	projects, err := pam.localClient.ListProjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list local projects: %w", err)
	}

	pam.logger.Debug("Retrieved target projects", zap.Int("project_count", len(projects)))
	return projects, nil
}

// createProjectMapping creates a mapping between source and target projects
func (pam *ProjectAssociationManager) createProjectMapping(
	sourceProjects, targetProjects []client.Project,
	result *ProjectAssociationResult,
) (map[int64]int64, error) {
	mapping := make(map[int64]int64)
	
	// Create lookup maps
	sourceProjectsMap := make(map[string]client.Project)
	targetProjectsMap := make(map[string]client.Project)
	
	for _, project := range sourceProjects {
		sourceProjectsMap[project.Name] = project
	}
	
	for _, project := range targetProjects {
		targetProjectsMap[project.Name] = project
	}

	// Create mappings based on configuration
	for _, sourceProject := range sourceProjects {
		targetProjectName := sourceProject.Name
		
		// Apply project name mapping if configured
		if pam.config.EnableProjectMapping {
			if mappedName, exists := pam.config.ProjectMappings[sourceProject.Name]; exists {
				targetProjectName = mappedName
			}
		}
		
		// Find target project
		if targetProject, exists := targetProjectsMap[targetProjectName]; exists {
			mapping[sourceProject.ProjectID] = targetProject.ProjectID
			pam.logger.Debug("Mapped project",
				zap.String("source_project", sourceProject.Name),
				zap.Int64("source_id", sourceProject.ProjectID),
				zap.String("target_project", targetProject.Name),
				zap.Int64("target_id", targetProject.ProjectID))
		} else {
			// Project not found, record as conflict
			conflict := ProjectMappingConflict{
				SourceProjectName: sourceProject.Name,
				TargetProjectName: targetProjectName,
				ConflictReason:    "Target project not found",
				Resolution:        "Project will be created if auto-creation is enabled",
			}
			
			if !pam.config.AutoCreateProjects {
				conflict.Resolution = "Association will be skipped"
			}
			
			result.MappingConflicts = append(result.MappingConflicts, conflict)
			
			pam.logger.Warn("Project mapping conflict",
				zap.String("source_project", sourceProject.Name),
				zap.String("target_project", targetProjectName),
				zap.String("reason", conflict.ConflictReason))
		}
	}

	return mapping, nil
}

// validateSourceAssociations validates that source associations reference valid projects
func (pam *ProjectAssociationManager) validateSourceAssociations(
	ctx context.Context,
	associations []client.OIDCGroupProjectRole,
	sourceProjects []client.Project,
	result *ProjectAssociationResult,
) error {
	sourceProjectMap := make(map[int64]client.Project)
	for _, project := range sourceProjects {
		sourceProjectMap[project.ProjectID] = project
	}

	for _, association := range associations {
		if project, exists := sourceProjectMap[association.ProjectID]; exists {
			if project.Deleted {
				error := ProjectValidationError{
					ProjectName: project.Name,
					Error:       "Associated project is marked as deleted",
					Severity:    "warning",
				}
				result.ValidationErrors = append(result.ValidationErrors, error)
			}
		} else {
			error := ProjectValidationError{
				ProjectName: fmt.Sprintf("ID:%d", association.ProjectID),
				Error:       "Associated project not found in source instance",
				Severity:    "error",
			}
			result.ValidationErrors = append(result.ValidationErrors, error)
		}
	}

	return nil
}

// createMissingProjects creates projects that exist in source but not in target
func (pam *ProjectAssociationManager) createMissingProjects(
	ctx context.Context,
	associations []client.OIDCGroupProjectRole,
	projectMapping map[int64]int64,
	sourceProjects []client.Project,
	result *ProjectAssociationResult,
) error {
	sourceProjectMap := make(map[int64]client.Project)
	for _, project := range sourceProjects {
		sourceProjectMap[project.ProjectID] = project
	}

	for _, association := range associations {
		// Check if mapping exists
		if _, exists := projectMapping[association.ProjectID]; !exists {
			// Project needs to be created
			sourceProject, sourceExists := sourceProjectMap[association.ProjectID]
			if !sourceExists {
				continue
			}

			// Apply project name mapping
			targetProjectName := sourceProject.Name
			if pam.config.EnableProjectMapping {
				if mappedName, exists := pam.config.ProjectMappings[sourceProject.Name]; exists {
					targetProjectName = mappedName
				}
			}

			// Create the project
			createResult := ProjectCreationResult{
				ProjectName:   targetProjectName,
				SourceProject: sourceProject.Name,
			}

			projectRequest := &client.ProjectRequest{
				ProjectName: targetProjectName,
				Metadata:    sourceProject.Metadata,
			}

			createdProject, err := pam.localClient.CreateProject(ctx, projectRequest)
			if err != nil {
				createResult.Error = err.Error()
				pam.logger.Error("Failed to create project",
					zap.String("project_name", targetProjectName),
					zap.Error(err))
			} else {
				createResult.Created = true
				createResult.ProjectID = createdProject.ProjectID
				
				// Add to mapping
				projectMapping[association.ProjectID] = createdProject.ProjectID
				
				pam.logger.Info("Created project",
					zap.String("project_name", targetProjectName),
					zap.Int64("project_id", createdProject.ProjectID),
					zap.String("source_project", sourceProject.Name))
			}

			result.ProjectsCreated = append(result.ProjectsCreated, createResult)
		}
	}

	return nil
}

// AssociationDifference represents differences between association sets
type AssociationDifference struct {
	ToAdd    []client.OIDCGroupProjectRole
	ToRemove []client.OIDCGroupProjectRole
	ToUpdate []AssociationUpdate
}

// AssociationUpdate represents an association that needs to be updated
type AssociationUpdate struct {
	ProjectID       int64
	CurrentRoleID   int64
	NewRoleID       int64
	CurrentRoleName string
	NewRoleName     string
}

// analyzeAssociationDifferences analyzes differences between source and target associations
func (pam *ProjectAssociationManager) analyzeAssociationDifferences(
	sourceAssociations, targetAssociations []client.OIDCGroupProjectRole,
	projectMapping map[int64]int64,
) *AssociationDifference {
	diff := &AssociationDifference{}
	
	// Map source associations to target project IDs
	mappedSourceAssociations := make(map[int64]client.OIDCGroupProjectRole)
	for _, association := range sourceAssociations {
		if targetProjectID, exists := projectMapping[association.ProjectID]; exists {
			mappedAssociation := association
			mappedAssociation.ProjectID = targetProjectID
			mappedSourceAssociations[targetProjectID] = mappedAssociation
		}
	}
	
	// Create target associations map
	targetAssociationsMap := make(map[int64]client.OIDCGroupProjectRole)
	for _, association := range targetAssociations {
		targetAssociationsMap[association.ProjectID] = association
	}
	
	// Find associations to add (in mapped source but not in target)
	for projectID, association := range mappedSourceAssociations {
		if _, exists := targetAssociationsMap[projectID]; !exists {
			diff.ToAdd = append(diff.ToAdd, association)
		}
	}
	
	// Find associations to remove or update
	for projectID, targetAssociation := range targetAssociationsMap {
		if sourceAssociation, exists := mappedSourceAssociations[projectID]; exists {
			// Association exists in both, check if update is needed
			if sourceAssociation.RoleID != targetAssociation.RoleID {
				update := AssociationUpdate{
					ProjectID:       projectID,
					CurrentRoleID:   targetAssociation.RoleID,
					NewRoleID:       sourceAssociation.RoleID,
					CurrentRoleName: targetAssociation.RoleName,
					NewRoleName:     sourceAssociation.RoleName,
				}
				diff.ToUpdate = append(diff.ToUpdate, update)
			}
		} else {
			// Association exists in target but not in mapped source
			if !pam.config.PreserveLocalAssociations {
				diff.ToRemove = append(diff.ToRemove, targetAssociation)
			}
		}
	}
	
	return diff
}

// applyAssociationChanges applies the association changes to the target group
func (pam *ProjectAssociationManager) applyAssociationChanges(
	ctx context.Context,
	targetGroup *client.OIDCGroup,
	diff *AssociationDifference,
	projectMapping map[int64]int64,
	result *ProjectAssociationResult,
) error {
	// Apply additions
	for _, association := range diff.ToAdd {
		if err := pam.addGroupToProject(ctx, targetGroup.ID, association.ProjectID, association.RoleID); err != nil {
			pam.logger.Error("Failed to add group to project",
				zap.Int64("group_id", targetGroup.ID),
				zap.Int64("project_id", association.ProjectID),
				zap.Int64("role_id", association.RoleID),
				zap.Error(err))
			continue
		}

		change := ProjectAssociationChange{
			ProjectID:   association.ProjectID,
			RoleID:      association.RoleID,
			RoleName:    association.RoleName,
			Operation:   "add",
			Reason:      "Association exists in source but not in target",
		}
		result.AssociationsAdded = append(result.AssociationsAdded, change)
		
		pam.logger.Info("Added group to project",
			zap.String("group_name", targetGroup.GroupName),
			zap.Int64("project_id", association.ProjectID),
			zap.Int64("role_id", association.RoleID))
	}

	// Apply removals
	for _, association := range diff.ToRemove {
		if err := pam.removeGroupFromProject(ctx, targetGroup.ID, association.ProjectID); err != nil {
			pam.logger.Error("Failed to remove group from project",
				zap.Int64("group_id", targetGroup.ID),
				zap.Int64("project_id", association.ProjectID),
				zap.Error(err))
			continue
		}

		change := ProjectAssociationChange{
			ProjectID:        association.ProjectID,
			PreviousRoleID:   association.RoleID,
			PreviousRoleName: association.RoleName,
			Operation:        "remove",
			Reason:           "Association exists in target but not in source",
		}
		result.AssociationsRemoved = append(result.AssociationsRemoved, change)
		
		pam.logger.Info("Removed group from project",
			zap.String("group_name", targetGroup.GroupName),
			zap.Int64("project_id", association.ProjectID))
	}

	// Apply updates
	for _, update := range diff.ToUpdate {
		// Remove old association and add new one
		if err := pam.removeGroupFromProject(ctx, targetGroup.ID, update.ProjectID); err != nil {
			pam.logger.Error("Failed to remove group from project during update",
				zap.Int64("group_id", targetGroup.ID),
				zap.Int64("project_id", update.ProjectID),
				zap.Error(err))
			continue
		}

		if err := pam.addGroupToProject(ctx, targetGroup.ID, update.ProjectID, update.NewRoleID); err != nil {
			pam.logger.Error("Failed to add group to project during update",
				zap.Int64("group_id", targetGroup.ID),
				zap.Int64("project_id", update.ProjectID),
				zap.Int64("role_id", update.NewRoleID),
				zap.Error(err))
			continue
		}

		change := ProjectAssociationChange{
			ProjectID:        update.ProjectID,
			RoleID:           update.NewRoleID,
			RoleName:         update.NewRoleName,
			PreviousRoleID:   update.CurrentRoleID,
			PreviousRoleName: update.CurrentRoleName,
			Operation:        "update",
			Reason:           "Role changed in source",
		}
		result.AssociationsUpdated = append(result.AssociationsUpdated, change)
		
		pam.logger.Info("Updated group project role",
			zap.String("group_name", targetGroup.GroupName),
			zap.Int64("project_id", update.ProjectID),
			zap.Int64("old_role_id", update.CurrentRoleID),
			zap.Int64("new_role_id", update.NewRoleID))
	}

	// Update the target group's project roles
	updatedRoles, err := pam.localClient.ListGroupProjectRoles(ctx, targetGroup.ID)
	if err != nil {
		pam.logger.Warn("Failed to refresh group project roles", zap.Error(err))
	} else {
		targetGroup.ProjectRoles = updatedRoles
	}

	return nil
}

// addGroupToProject adds a group to a project with the specified role
func (pam *ProjectAssociationManager) addGroupToProject(ctx context.Context, groupID, projectID, roleID int64) error {
	// Use timeout context if configured
	if pam.config.AssociationTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, pam.config.AssociationTimeout)
		defer cancel()
	}

	return pam.localClient.AddGroupToProject(ctx, groupID, projectID, roleID)
}

// removeGroupFromProject removes a group from a project
func (pam *ProjectAssociationManager) removeGroupFromProject(ctx context.Context, groupID, projectID int64) error {
	// Use timeout context if configured
	if pam.config.AssociationTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, pam.config.AssociationTimeout)
		defer cancel()
	}

	return pam.localClient.RemoveGroupFromProject(ctx, groupID, projectID)
}

// GetProjectAssociationStatistics returns statistics about project association operations
func (pam *ProjectAssociationManager) GetProjectAssociationStatistics() (*ProjectAssociationStatistics, error) {
	stats := &ProjectAssociationStatistics{
		TotalAssociationOperations: 0,
		AssociationsAdded:          0,
		AssociationsRemoved:        0,
		AssociationsUpdated:        0,
		ProjectsCreated:            0,
		MappingConflicts:           0,
		ValidationErrors:           0,
		LastCalculated:             time.Now(),
	}

	// Get statistics from state manager
	groupStats, err := pam.stateManager.GetGroupSyncStatistics()
	if err != nil {
		return stats, err
	}

	// For now, use basic statistics
	// In a production system, you might track more detailed association statistics
	stats.TotalAssociationOperations = groupStats.TotalGroups

	return stats, nil
}

// ValidateProjectMapping validates project mappings between instances
func (pam *ProjectAssociationManager) ValidateProjectMapping(
	ctx context.Context,
	sourceInstance string,
) (*ProjectMappingValidationResult, error) {
	result := &ProjectMappingValidationResult{
		SourceInstance: sourceInstance,
		Timestamp:      time.Now(),
	}

	// Get projects from both instances
	sourceProjects, err := pam.getSourceProjects(ctx, sourceInstance)
	if err != nil {
		return result, fmt.Errorf("failed to get source projects: %w", err)
	}

	targetProjects, err := pam.getTargetProjects(ctx)
	if err != nil {
		return result, fmt.Errorf("failed to get target projects: %w", err)
	}

	// Validate mappings
	result.SourceProjectCount = len(sourceProjects)
	result.TargetProjectCount = len(targetProjects)

	for _, sourceProject := range sourceProjects {
		targetProjectName := sourceProject.Name
		
		// Apply project name mapping if configured
		if pam.config.EnableProjectMapping {
			if mappedName, exists := pam.config.ProjectMappings[sourceProject.Name]; exists {
				targetProjectName = mappedName
			}
		}
		
		// Check if target project exists
		found := false
		for _, targetProject := range targetProjects {
			if (pam.config.StrictProjectMatching && targetProject.Name == targetProjectName) ||
			   (!pam.config.StrictProjectMatching && strings.EqualFold(targetProject.Name, targetProjectName)) {
				found = true
				result.MappedProjects++
				break
			}
		}
		
		if !found {
			result.UnmappedProjects++
			result.UnmappedProjectNames = append(result.UnmappedProjectNames, sourceProject.Name)
		}
	}

	return result, nil
}

// ProjectAssociationStatistics provides statistics about project association operations
type ProjectAssociationStatistics struct {
	TotalAssociationOperations int       `json:"total_association_operations"`
	AssociationsAdded          int       `json:"associations_added"`
	AssociationsRemoved        int       `json:"associations_removed"`
	AssociationsUpdated        int       `json:"associations_updated"`
	ProjectsCreated            int       `json:"projects_created"`
	MappingConflicts           int       `json:"mapping_conflicts"`
	ValidationErrors           int       `json:"validation_errors"`
	LastCalculated             time.Time `json:"last_calculated"`
}

// ProjectMappingValidationResult provides results of project mapping validation
type ProjectMappingValidationResult struct {
	SourceInstance       string    `json:"source_instance"`
	SourceProjectCount   int       `json:"source_project_count"`
	TargetProjectCount   int       `json:"target_project_count"`
	MappedProjects       int       `json:"mapped_projects"`
	UnmappedProjects     int       `json:"unmapped_projects"`
	UnmappedProjectNames []string  `json:"unmapped_project_names"`
	Timestamp            time.Time `json:"timestamp"`
}