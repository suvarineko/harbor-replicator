package sync

import (
	"context"
	"fmt"
	"sort"
	"time"

	"go.uber.org/zap"

	"harbor-replicator/pkg/client"
)

// ConflictResolverImpl implements the ConflictResolver interface
type ConflictResolverImpl struct {
	logger *zap.Logger
	config *ResolverConfig
}

// ResolverConfig holds configuration for conflict resolution
type ResolverConfig struct {
	// Default strategies
	DefaultStrategy        ConflictStrategy `json:"default_strategy"`
	NameConflictStrategy   ConflictStrategy `json:"name_conflict_strategy"`
	PermissionStrategy     ConflictStrategy `json:"permission_strategy"`
	
	// Naming conventions
	DuplicateSuffix        string           `json:"duplicate_suffix"`
	MaxNameLength          int              `json:"max_name_length"`
	
	// Interactive settings
	InteractiveTimeout     time.Duration    `json:"interactive_timeout"`
	
	// Merge settings
	MergePermissionPolicy  string           `json:"merge_permission_policy"` // "union", "intersection", "source_priority"
	PreserveTargetMetadata bool             `json:"preserve_target_metadata"`
	
	// Validation settings
	ValidateResolution     bool             `json:"validate_resolution"`
	AllowDataLoss          bool             `json:"allow_data_loss"`
}

// NewConflictResolver creates a new ConflictResolver instance
func NewConflictResolver(logger *zap.Logger, config *ResolverConfig) *ConflictResolverImpl {
	if logger == nil {
		logger = zap.NewNop()
	}
	
	if config == nil {
		config = DefaultResolverConfig()
	}
	
	return &ConflictResolverImpl{
		logger: logger,
		config: config,
	}
}

// DefaultResolverConfig returns a default resolver configuration
func DefaultResolverConfig() *ResolverConfig {
	return &ResolverConfig{
		DefaultStrategy:        ConflictStrategySourceWins,
		NameConflictStrategy:   ConflictStrategyRenameDuplicate,
		PermissionStrategy:     ConflictStrategyMergePermissions,
		DuplicateSuffix:        "_sync",
		MaxNameLength:          64,
		InteractiveTimeout:     30 * time.Second,
		MergePermissionPolicy:  "union",
		PreserveTargetMetadata: true,
		ValidateResolution:     true,
		AllowDataLoss:          false,
	}
}

// ResolveConflict resolves a conflict using the specified strategy
func (cr *ConflictResolverImpl) ResolveConflict(ctx context.Context, conflict *RobotConflict, strategy ConflictStrategy) (*client.RobotAccount, error) {
	if conflict == nil {
		return nil, fmt.Errorf("conflict cannot be nil")
	}
	
	if conflict.SourceRobot == nil || conflict.TargetRobot == nil {
		return nil, fmt.Errorf("conflict must have both source and target robots")
	}
	
	cr.logger.Info("Resolving robot conflict", 
		zap.String("strategy", string(strategy)),
		zap.String("conflict_type", string(conflict.Type)),
		zap.String("source_robot", conflict.SourceRobot.Name),
		zap.String("target_robot", conflict.TargetRobot.Name))
	
	var resolvedRobot *client.RobotAccount
	var err error
	
	switch strategy {
	case ConflictStrategySourceWins:
		resolvedRobot, err = cr.ResolveSourceWins(conflict)
	case ConflictStrategyTargetWins:
		resolvedRobot, err = cr.ResolveTargetWins(conflict)
	case ConflictStrategyMergePermissions:
		resolvedRobot, err = cr.ResolveMergePermissions(conflict)
	case ConflictStrategyRenameDuplicate:
		resolvedRobot, err = cr.ResolveRenameDuplicate(conflict)
	case ConflictStrategyInteractive:
		resolvedRobot, err = cr.ResolveInteractive(ctx, conflict)
	case ConflictStrategySkip:
		return nil, fmt.Errorf("conflict resolution skipped")
	case ConflictStrategyFail:
		return nil, fmt.Errorf("conflict resolution failed by strategy")
	default:
		return nil, fmt.Errorf("unknown conflict resolution strategy: %s", strategy)
	}
	
	if err != nil {
		return nil, fmt.Errorf("failed to resolve conflict with strategy %s: %w", strategy, err)
	}
	
	// Validate resolution if configured
	if cr.config.ValidateResolution {
		if err := cr.validateResolvedRobot(resolvedRobot, conflict); err != nil {
			return nil, fmt.Errorf("resolution validation failed: %w", err)
		}
	}
	
	cr.logger.Info("Conflict resolved successfully", 
		zap.String("strategy", string(strategy)),
		zap.String("resolved_robot", resolvedRobot.Name))
	
	return resolvedRobot, nil
}

// ResolveSourceWins implements source-wins strategy
func (cr *ConflictResolverImpl) ResolveSourceWins(conflict *RobotConflict) (*client.RobotAccount, error) {
	if conflict.SourceRobot == nil {
		return nil, fmt.Errorf("source robot is nil")
	}
	
	// Create a copy of the source robot
	resolved := cr.copyRobot(conflict.SourceRobot)
	
	// Preserve target ID if it exists (for updates)
	if conflict.TargetRobot != nil && conflict.TargetRobot.ID != 0 {
		resolved.ID = conflict.TargetRobot.ID
	}
	
	// Optionally preserve some target metadata
	if cr.config.PreserveTargetMetadata && conflict.TargetRobot != nil {
		resolved.CreationTime = conflict.TargetRobot.CreationTime
		// Keep target timestamps if they're more recent
		if conflict.TargetRobot.UpdateTime.After(resolved.UpdateTime) {
			resolved.UpdateTime = conflict.TargetRobot.UpdateTime
		}
	}
	
	return resolved, nil
}

// ResolveTargetWins implements target-wins strategy
func (cr *ConflictResolverImpl) ResolveTargetWins(conflict *RobotConflict) (*client.RobotAccount, error) {
	if conflict.TargetRobot == nil {
		return nil, fmt.Errorf("target robot is nil")
	}
	
	// Return a copy of the target robot
	return cr.copyRobot(conflict.TargetRobot), nil
}

// ResolveMergePermissions implements permission merging strategy
func (cr *ConflictResolverImpl) ResolveMergePermissions(conflict *RobotConflict) (*client.RobotAccount, error) {
	if conflict.SourceRobot == nil || conflict.TargetRobot == nil {
		return nil, fmt.Errorf("both source and target robots are required for merging")
	}
	
	// Start with source robot as base
	resolved := cr.copyRobot(conflict.SourceRobot)
	
	// Preserve target ID for updates
	if conflict.TargetRobot.ID != 0 {
		resolved.ID = conflict.TargetRobot.ID
	}
	
	// Merge permissions based on policy
	mergedPermissions, err := cr.mergePermissions(
		conflict.SourceRobot.Permissions,
		conflict.TargetRobot.Permissions,
		cr.config.MergePermissionPolicy,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to merge permissions: %w", err)
	}
	
	resolved.Permissions = mergedPermissions
	
	// Merge other fields intelligently
	resolved = cr.mergeRobotFields(resolved, conflict.TargetRobot)
	
	return resolved, nil
}

// ResolveRenameDuplicate implements rename duplicate strategy
func (cr *ConflictResolverImpl) ResolveRenameDuplicate(conflict *RobotConflict) (*client.RobotAccount, error) {
	if conflict.SourceRobot == nil {
		return nil, fmt.Errorf("source robot is nil")
	}
	
	// Create a copy of the source robot
	resolved := cr.copyRobot(conflict.SourceRobot)
	
	// Generate a unique name
	newName := cr.generateUniqueName(conflict.SourceRobot.Name, conflict.TargetRobot)
	if len(newName) > cr.config.MaxNameLength {
		newName = newName[:cr.config.MaxNameLength]
	}
	
	resolved.Name = newName
	resolved.ID = 0 // Clear ID to indicate this is a new robot
	
	// Update description to indicate this is a renamed duplicate
	if resolved.Description == "" {
		resolved.Description = fmt.Sprintf("Renamed from %s to resolve naming conflict", conflict.SourceRobot.Name)
	} else {
		resolved.Description = fmt.Sprintf("%s (renamed from %s)", resolved.Description, conflict.SourceRobot.Name)
	}
	
	return resolved, nil
}

// ResolveInteractive implements interactive resolution (placeholder)
func (cr *ConflictResolverImpl) ResolveInteractive(ctx context.Context, conflict *RobotConflict) (*client.RobotAccount, error) {
	// In a real implementation, this would prompt the user for input
	// For now, we'll fall back to the default strategy
	
	cr.logger.Warn("Interactive resolution not implemented, falling back to default strategy",
		zap.String("default_strategy", string(cr.config.DefaultStrategy)))
	
	return cr.ResolveConflict(ctx, conflict, cr.config.DefaultStrategy)
}

// AnalyzeResolutionImpact analyzes the impact of applying a resolution strategy
func (cr *ConflictResolverImpl) AnalyzeResolutionImpact(conflict *RobotConflict, strategy ConflictStrategy) (*ResolutionImpact, error) {
	if conflict == nil {
		return nil, fmt.Errorf("conflict cannot be nil")
	}
	
	impact := &ResolutionImpact{
		Strategy:          strategy,
		DataLoss:          false,
		PermissionChanges: false,
		NameChanges:       false,
		Risks:             make([]string, 0),
		Benefits:          make([]string, 0),
		Recommendations:   make([]string, 0),
	}
	
	switch strategy {
	case ConflictStrategySourceWins:
		impact = cr.analyzeSourceWinsImpact(conflict, impact)
	case ConflictStrategyTargetWins:
		impact = cr.analyzeTargetWinsImpact(conflict, impact)
	case ConflictStrategyMergePermissions:
		impact = cr.analyzeMergeImpact(conflict, impact)
	case ConflictStrategyRenameDuplicate:
		impact = cr.analyzeRenameImpact(conflict, impact)
	case ConflictStrategySkip:
		impact.Risks = append(impact.Risks, "Conflict remains unresolved")
		impact.Recommendations = append(impact.Recommendations, "Address conflict manually")
	case ConflictStrategyFail:
		impact.Risks = append(impact.Risks, "Synchronization will fail")
		impact.Recommendations = append(impact.Recommendations, "Choose a different strategy")
	}
	
	return impact, nil
}

// SuggestResolution suggests the best resolution strategy for a conflict
func (cr *ConflictResolverImpl) SuggestResolution(conflict *RobotConflict) (ConflictStrategy, string) {
	if conflict == nil {
		return cr.config.DefaultStrategy, "Default strategy due to nil conflict"
	}
	
	switch conflict.Type {
	case RobotConflictTypeNameExists:
		return ConflictStrategyRenameDuplicate, "Rename duplicate to avoid naming conflicts"
	
	case RobotConflictTypePermissionsDiffer:
		return ConflictStrategyMergePermissions, "Merge permissions to preserve access rights"
	
	case RobotConflictTypeLevelMismatch:
		return ConflictStrategySourceWins, "Level conflicts require source precedence"
	
	case RobotConflictTypeProjectMismatch:
		return ConflictStrategyRenameDuplicate, "Project mismatch requires separate robots"
	
	case RobotConflictTypeSecretMismatch:
		return ConflictStrategySourceWins, "Secret conflicts should prefer source"
	
	default:
		// Analyze conflict severity
		switch conflict.Severity {
		case ConflictSeverityCritical:
			return ConflictStrategyFail, "Critical conflicts require manual intervention"
		case ConflictSeverityHigh:
			return ConflictStrategyInteractive, "High severity conflicts need user decision"
		case ConflictSeverityMedium:
			return ConflictStrategyMergePermissions, "Medium conflicts can be merged safely"
		case ConflictSeverityLow:
			return ConflictStrategySourceWins, "Low severity conflicts default to source"
		default:
			return cr.config.DefaultStrategy, "Unknown severity, using default strategy"
		}
	}
}

// Private helper methods

func (cr *ConflictResolverImpl) copyRobot(robot *client.RobotAccount) *client.RobotAccount {
	if robot == nil {
		return nil
	}
	
	copy := &client.RobotAccount{
		ID:           robot.ID,
		Name:         robot.Name,
		Description:  robot.Description,
		Secret:       robot.Secret,
		Level:        robot.Level,
		Duration:     robot.Duration,
		Disabled:     robot.Disabled,
		ExpiresAt:    robot.ExpiresAt,
		CreationTime: robot.CreationTime,
		UpdateTime:   robot.UpdateTime,
	}
	
	// Deep copy permissions
	if robot.Permissions != nil {
		copy.Permissions = make([]client.RobotPermission, len(robot.Permissions))
		for i, perm := range robot.Permissions {
			copy.Permissions[i] = client.RobotPermission{
				Kind:      perm.Kind,
				Namespace: perm.Namespace,
			}
			
			// Deep copy access
			if perm.Access != nil {
				copy.Permissions[i].Access = make([]client.RobotAccess, len(perm.Access))
				for j, access := range perm.Access {
					copy.Permissions[i].Access[j] = client.RobotAccess{
						Resource: access.Resource,
						Action:   access.Action,
					}
				}
			}
		}
	}
	
	return copy
}

func (cr *ConflictResolverImpl) mergePermissions(sourcePerms, targetPerms []client.RobotPermission, policy string) ([]client.RobotPermission, error) {
	switch policy {
	case "union":
		return cr.mergePermissionsUnion(sourcePerms, targetPerms), nil
	case "intersection":
		return cr.mergePermissionsIntersection(sourcePerms, targetPerms), nil
	case "source_priority":
		return cr.mergePermissionsSourcePriority(sourcePerms, targetPerms), nil
	default:
		return nil, fmt.Errorf("unknown merge policy: %s", policy)
	}
}

func (cr *ConflictResolverImpl) mergePermissionsUnion(sourcePerms, targetPerms []client.RobotPermission) []client.RobotPermission {
	// Create a map to track unique permissions
	permMap := make(map[string]client.RobotPermission)
	
	// Add source permissions
	for _, perm := range sourcePerms {
		key := fmt.Sprintf("%s:%s", perm.Kind, perm.Namespace)
		permMap[key] = perm
	}
	
	// Add target permissions, merging access if permission already exists
	for _, perm := range targetPerms {
		key := fmt.Sprintf("%s:%s", perm.Kind, perm.Namespace)
		if existing, exists := permMap[key]; exists {
			// Merge access
			merged := cr.mergeAccess(existing.Access, perm.Access)
			existing.Access = merged
			permMap[key] = existing
		} else {
			permMap[key] = perm
		}
	}
	
	// Convert back to slice
	result := make([]client.RobotPermission, 0, len(permMap))
	for _, perm := range permMap {
		result = append(result, perm)
	}
	
	// Sort for consistency
	sort.Slice(result, func(i, j int) bool {
		if result[i].Kind != result[j].Kind {
			return result[i].Kind < result[j].Kind
		}
		return result[i].Namespace < result[j].Namespace
	})
	
	return result
}

func (cr *ConflictResolverImpl) mergePermissionsIntersection(sourcePerms, targetPerms []client.RobotPermission) []client.RobotPermission {
	sourceMap := make(map[string]client.RobotPermission)
	targetMap := make(map[string]client.RobotPermission)
	
	// Build maps
	for _, perm := range sourcePerms {
		key := fmt.Sprintf("%s:%s", perm.Kind, perm.Namespace)
		sourceMap[key] = perm
	}
	
	for _, perm := range targetPerms {
		key := fmt.Sprintf("%s:%s", perm.Kind, perm.Namespace)
		targetMap[key] = perm
	}
	
	// Find intersection
	result := make([]client.RobotPermission, 0)
	for key, sourcePerm := range sourceMap {
		if targetPerm, exists := targetMap[key]; exists {
			// Merge access (intersection)
			merged := sourcePerm
			merged.Access = cr.intersectAccess(sourcePerm.Access, targetPerm.Access)
			if len(merged.Access) > 0 {
				result = append(result, merged)
			}
		}
	}
	
	return result
}

func (cr *ConflictResolverImpl) mergePermissionsSourcePriority(sourcePerms, targetPerms []client.RobotPermission) []client.RobotPermission {
	// Start with source permissions
	result := make([]client.RobotPermission, len(sourcePerms))
	copy(result, sourcePerms)
	
	// Add target permissions that don't conflict
	sourceMap := make(map[string]bool)
	for _, perm := range sourcePerms {
		key := fmt.Sprintf("%s:%s", perm.Kind, perm.Namespace)
		sourceMap[key] = true
	}
	
	for _, perm := range targetPerms {
		key := fmt.Sprintf("%s:%s", perm.Kind, perm.Namespace)
		if !sourceMap[key] {
			result = append(result, perm)
		}
	}
	
	return result
}

func (cr *ConflictResolverImpl) mergeAccess(sourceAccess, targetAccess []client.RobotAccess) []client.RobotAccess {
	accessMap := make(map[string]bool)
	result := make([]client.RobotAccess, 0)
	
	// Add source access
	for _, access := range sourceAccess {
		key := fmt.Sprintf("%s:%s", access.Resource, access.Action)
		if !accessMap[key] {
			result = append(result, access)
			accessMap[key] = true
		}
	}
	
	// Add target access
	for _, access := range targetAccess {
		key := fmt.Sprintf("%s:%s", access.Resource, access.Action)
		if !accessMap[key] {
			result = append(result, access)
			accessMap[key] = true
		}
	}
	
	return result
}

func (cr *ConflictResolverImpl) intersectAccess(sourceAccess, targetAccess []client.RobotAccess) []client.RobotAccess {
	sourceMap := make(map[string]bool)
	result := make([]client.RobotAccess, 0)
	
	// Build source map
	for _, access := range sourceAccess {
		key := fmt.Sprintf("%s:%s", access.Resource, access.Action)
		sourceMap[key] = true
	}
	
	// Find intersection
	for _, access := range targetAccess {
		key := fmt.Sprintf("%s:%s", access.Resource, access.Action)
		if sourceMap[key] {
			result = append(result, access)
		}
	}
	
	return result
}

func (cr *ConflictResolverImpl) mergeRobotFields(base, target *client.RobotAccount) *client.RobotAccount {
	// Use target metadata if preserving it is configured
	if cr.config.PreserveTargetMetadata {
		base.CreationTime = target.CreationTime
		if target.UpdateTime.After(base.UpdateTime) {
			base.UpdateTime = target.UpdateTime
		}
	}
	
	// Merge description intelligently
	if base.Description == "" && target.Description != "" {
		base.Description = target.Description
	}
	
	// Use the longer duration
	if target.Duration > base.Duration {
		base.Duration = target.Duration
	}
	
	return base
}

func (cr *ConflictResolverImpl) generateUniqueName(baseName string, existingRobot *client.RobotAccount) string {
	suffix := cr.config.DuplicateSuffix
	counter := 1
	
	for {
		newName := fmt.Sprintf("%s%s", baseName, suffix)
		if counter > 1 {
			newName = fmt.Sprintf("%s%s_%d", baseName, suffix, counter)
		}
		
		// In a real implementation, this would check against the target instance
		// For now, we'll just return the first attempt
		if existingRobot == nil || existingRobot.Name != newName {
			return newName
		}
		
		counter++
		if counter > 100 { // Safety limit
			return fmt.Sprintf("%s%s_%d_%d", baseName, suffix, counter, time.Now().Unix())
		}
	}
}

func (cr *ConflictResolverImpl) validateResolvedRobot(robot *client.RobotAccount, conflict *RobotConflict) error {
	if robot == nil {
		return fmt.Errorf("resolved robot cannot be nil")
	}
	
	if robot.Name == "" {
		return fmt.Errorf("resolved robot must have a name")
	}
	
	if robot.Level != "system" && robot.Level != "project" {
		return fmt.Errorf("resolved robot must have valid level")
	}
	
	if len(robot.Permissions) == 0 {
		return fmt.Errorf("resolved robot must have at least one permission")
	}
	
	// Check for data loss if not allowed
	if !cr.config.AllowDataLoss {
		if err := cr.checkForDataLoss(robot, conflict); err != nil {
			return fmt.Errorf("resolution would cause data loss: %w", err)
		}
	}
	
	return nil
}

func (cr *ConflictResolverImpl) checkForDataLoss(resolved *client.RobotAccount, conflict *RobotConflict) error {
	// Check if we're losing important information from either robot
	
	// Check permissions
	sourcePermCount := len(conflict.SourceRobot.Permissions)
	targetPermCount := len(conflict.TargetRobot.Permissions)
	resolvedPermCount := len(resolved.Permissions)
	
	if resolvedPermCount < sourcePermCount && resolvedPermCount < targetPermCount {
		return fmt.Errorf("permission count reduced from %d/%d to %d", 
			sourcePermCount, targetPermCount, resolvedPermCount)
	}
	
	return nil
}

// Impact analysis methods

func (cr *ConflictResolverImpl) analyzeSourceWinsImpact(conflict *RobotConflict, impact *ResolutionImpact) *ResolutionImpact {
	impact.Benefits = append(impact.Benefits, "Preserves source robot configuration")
	impact.Benefits = append(impact.Benefits, "Consistent with source instance")
	
	if conflict.TargetRobot != nil {
		impact.DataLoss = true
		impact.Risks = append(impact.Risks, "Target robot configuration will be lost")
		impact.Recommendations = append(impact.Recommendations, "Consider merging strategies if target has valuable configuration")
	}
	
	return impact
}

func (cr *ConflictResolverImpl) analyzeTargetWinsImpact(conflict *RobotConflict, impact *ResolutionImpact) *ResolutionImpact {
	impact.Benefits = append(impact.Benefits, "Preserves existing target configuration")
	impact.Benefits = append(impact.Benefits, "No disruption to target instance")
	
	if conflict.SourceRobot != nil {
		impact.DataLoss = true
		impact.Risks = append(impact.Risks, "Source robot configuration will be ignored")
		impact.Recommendations = append(impact.Recommendations, "Ensure target configuration is adequate")
	}
	
	return impact
}

func (cr *ConflictResolverImpl) analyzeMergeImpact(conflict *RobotConflict, impact *ResolutionImpact) *ResolutionImpact {
	impact.PermissionChanges = true
	impact.Benefits = append(impact.Benefits, "Preserves permissions from both robots")
	impact.Benefits = append(impact.Benefits, "Minimizes data loss")
	
	impact.Risks = append(impact.Risks, "May create overly permissive robot")
	impact.Risks = append(impact.Risks, "Complex permission sets may be hard to manage")
	impact.Recommendations = append(impact.Recommendations, "Review merged permissions for security")
	
	return impact
}

func (cr *ConflictResolverImpl) analyzeRenameImpact(conflict *RobotConflict, impact *ResolutionImpact) *ResolutionImpact {
	impact.NameChanges = true
	impact.Benefits = append(impact.Benefits, "Avoids overwriting existing robot")
	impact.Benefits = append(impact.Benefits, "Preserves both robot configurations")
	
	impact.Risks = append(impact.Risks, "Creates additional robot to manage")
	impact.Risks = append(impact.Risks, "May cause confusion with similar names")
	impact.Recommendations = append(impact.Recommendations, "Update documentation to reflect new robot name")
	impact.Recommendations = append(impact.Recommendations, "Consider consolidating robots in the future")
	
	return impact
}

// AutoResolveConflict automatically resolves a conflict using the best strategy
func (cr *ConflictResolverImpl) AutoResolveConflict(ctx context.Context, conflict *RobotConflict) (*client.RobotAccount, error) {
	// First, suggest the best resolution strategy
	strategy, reason := cr.SuggestResolution(conflict)
	
	cr.logger.Info("Auto-resolving conflict", 
		zap.String("suggested_strategy", string(strategy)),
		zap.String("reason", reason),
		zap.String("conflict_type", string(conflict.Type)),
		zap.String("severity", string(conflict.Severity)))
	
	// Check if the strategy is configurable based on conflict type
	finalStrategy := cr.selectStrategyWithConfig(conflict, strategy)
	
	// Analyze impact before resolution
	impact, err := cr.AnalyzeResolutionImpact(conflict, finalStrategy)
	if err != nil {
		cr.logger.Warn("Failed to analyze resolution impact", zap.Error(err))
		// Continue with resolution anyway
	} else if impact.DataLoss && !cr.config.AllowDataLoss {
		cr.logger.Warn("Resolution would cause data loss, falling back to safe strategy")
		finalStrategy = ConflictStrategyFail
	}
	
	// Apply the selected strategy
	return cr.ResolveConflict(ctx, conflict, finalStrategy)
}

// selectStrategyWithConfig selects strategy based on configuration overrides
func (cr *ConflictResolverImpl) selectStrategyWithConfig(conflict *RobotConflict, suggestedStrategy ConflictStrategy) ConflictStrategy {
	// Check for specific conflict type overrides
	switch conflict.Type {
	case RobotConflictTypeNameExists:
		if cr.config.NameConflictStrategy != "" {
			return cr.config.NameConflictStrategy
		}
	case RobotConflictTypePermissionsDiffer:
		if cr.config.PermissionStrategy != "" {
			return cr.config.PermissionStrategy
		}
	}
	
	// Return suggested strategy if no overrides
	return suggestedStrategy
}

// ResolveConflictsInBatch resolves multiple conflicts efficiently
func (cr *ConflictResolverImpl) ResolveConflictsInBatch(ctx context.Context, conflicts []RobotConflict, defaultStrategy ConflictStrategy) ([]ResolvedConflict, error) {
	results := make([]ResolvedConflict, 0, len(conflicts))
	
	for _, conflict := range conflicts {
		startTime := time.Now()
		
		// Use auto-resolution for efficiency
		resolvedRobot, err := cr.AutoResolveConflict(ctx, &conflict)
		
		result := ResolvedConflict{
			OriginalConflict: conflict,
			ResolvedRobot:    resolvedRobot,
			ResolutionTime:   time.Since(startTime),
			Success:          err == nil,
		}
		
		if err != nil {
			result.Error = err.Error()
			cr.logger.Error("Failed to resolve conflict in batch", 
				zap.String("robot_name", conflict.SourceRobot.Name),
				zap.String("conflict_type", string(conflict.Type)),
				zap.Error(err))
		}
		
		results = append(results, result)
	}
	
	return results, nil
}