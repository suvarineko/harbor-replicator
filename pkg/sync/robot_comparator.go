package sync

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"

	"harbor-replicator/pkg/client"
)

// RobotComparatorImpl implements the RobotComparator interface
type RobotComparatorImpl struct {
	logger *zap.Logger
	config *ComparatorConfig
}

// ComparatorConfig holds configuration for the robot comparator
type ComparatorConfig struct {
	// Comparison settings
	IgnoreTimestamps     bool     `json:"ignore_timestamps"`
	IgnoreSecrets        bool     `json:"ignore_secrets"`
	IgnoreIDs            bool     `json:"ignore_ids"`
	IgnoreDisabledStatus bool     `json:"ignore_disabled_status"`
	
	// Field sensitivity
	CaseSensitiveNames   bool     `json:"case_sensitive_names"`
	TrimWhitespace       bool     `json:"trim_whitespace"`
	
	// Permission comparison
	NormalizePermissions bool     `json:"normalize_permissions"`
	IgnorePermissionOrder bool    `json:"ignore_permission_order"`
	
	// Conflict analysis
	ConflictThreshold    float64  `json:"conflict_threshold"`
	CriticalFields       []string `json:"critical_fields"`
}

// NewRobotComparator creates a new RobotComparator instance
func NewRobotComparator(logger *zap.Logger, config *ComparatorConfig) *RobotComparatorImpl {
	if logger == nil {
		logger = zap.NewNop()
	}
	
	if config == nil {
		config = DefaultComparatorConfig()
	}
	
	return &RobotComparatorImpl{
		logger: logger,
		config: config,
	}
}

// DefaultComparatorConfig returns a default comparator configuration
func DefaultComparatorConfig() *ComparatorConfig {
	return &ComparatorConfig{
		IgnoreTimestamps:      true,
		IgnoreSecrets:         true,
		IgnoreIDs:             true,
		IgnoreDisabledStatus:  false,
		CaseSensitiveNames:    false,
		TrimWhitespace:        true,
		NormalizePermissions:  true,
		IgnorePermissionOrder: true,
		ConflictThreshold:     0.5,
		CriticalFields:        []string{"name", "level", "permissions"},
	}
}

// Equal compares two robot accounts for equality
func (rc *RobotComparatorImpl) Equal(source, target *client.RobotAccount) bool {
	if source == nil || target == nil {
		return source == target
	}
	
	// Normalize robots for comparison
	normalizedSource := rc.NormalizeRobot(source)
	normalizedTarget := rc.NormalizeRobot(target)
	
	// Compare all fields except those configured to ignore
	return rc.compareRobotFields(normalizedSource, normalizedTarget)
}

// Compare performs detailed comparison and returns conflict information
func (rc *RobotComparatorImpl) Compare(source, target *client.RobotAccount) (*RobotConflict, error) {
	if source == nil || target == nil {
		return nil, fmt.Errorf("cannot compare nil robot accounts")
	}
	
	conflicts := make([]string, 0)
	details := make(map[string]interface{})
	
	// Normalize robots for comparison
	normalizedSource := rc.NormalizeRobot(source)
	normalizedTarget := rc.NormalizeRobot(target)
	
	// Compare name
	if !rc.compareNames(normalizedSource.Name, normalizedTarget.Name) {
		conflicts = append(conflicts, "name")
		details["name_source"] = normalizedSource.Name
		details["name_target"] = normalizedTarget.Name
	}
	
	// Compare description
	if normalizedSource.Description != normalizedTarget.Description {
		conflicts = append(conflicts, "description")
		details["description_source"] = normalizedSource.Description
		details["description_target"] = normalizedTarget.Description
	}
	
	// Compare level
	if normalizedSource.Level != normalizedTarget.Level {
		conflicts = append(conflicts, "level")
		details["level_source"] = normalizedSource.Level
		details["level_target"] = normalizedTarget.Level
	}
	
	// Compare duration
	if normalizedSource.Duration != normalizedTarget.Duration {
		conflicts = append(conflicts, "duration")
		details["duration_source"] = normalizedSource.Duration
		details["duration_target"] = normalizedTarget.Duration
	}
	
	// Compare disabled status
	if !rc.config.IgnoreDisabledStatus && normalizedSource.Disabled != normalizedTarget.Disabled {
		conflicts = append(conflicts, "disabled")
		details["disabled_source"] = normalizedSource.Disabled
		details["disabled_target"] = normalizedTarget.Disabled
	}
	
	// Compare permissions
	if permEqual, permConflicts := rc.ComparePermissions(normalizedSource.Permissions, normalizedTarget.Permissions); !permEqual {
		conflicts = append(conflicts, "permissions")
		details["permission_conflicts"] = permConflicts
	}
	
	// If no conflicts, return nil
	if len(conflicts) == 0 {
		return nil, nil
	}
	
	// Determine conflict type and severity
	conflictType := rc.determineConflictType(conflicts, normalizedSource, normalizedTarget)
	severity := rc.analyzeSeverity(conflicts, details)
	
	conflict := &RobotConflict{
		Type:           conflictType,
		SourceRobot:    source,
		TargetRobot:    target,
		ConflictFields: conflicts,
		Severity:       severity,
		Details:        details,
		Timestamp:      time.Now(),
	}
	
	rc.logger.Debug("Robot comparison completed", 
		zap.String("source_name", source.Name),
		zap.String("target_name", target.Name),
		zap.Int("conflict_count", len(conflicts)),
		zap.String("severity", string(severity)))
	
	return conflict, nil
}

// ComparePermissions compares robot permissions and returns differences
func (rc *RobotComparatorImpl) ComparePermissions(source, target []client.RobotPermission) (bool, []string) {
	if len(source) == 0 && len(target) == 0 {
		return true, nil
	}
	
	conflicts := make([]string, 0)
	
	// Normalize permissions if configured
	if rc.config.NormalizePermissions {
		source = rc.normalizePermissions(source)
		target = rc.normalizePermissions(target)
	}
	
	// Convert to maps for easier comparison
	sourceMap := rc.permissionsToMap(source)
	targetMap := rc.permissionsToMap(target)
	
	// Check for permissions in source but not in target
	for key := range sourceMap {
		if _, exists := targetMap[key]; !exists {
			conflicts = append(conflicts, fmt.Sprintf("missing_in_target: %s", key))
		}
	}
	
	// Check for permissions in target but not in source
	for key := range targetMap {
		if _, exists := sourceMap[key]; !exists {
			conflicts = append(conflicts, fmt.Sprintf("extra_in_target: %s", key))
		}
	}
	
	// Check for differences in existing permissions
	for key, sourcePerm := range sourceMap {
		if targetPerm, exists := targetMap[key]; exists {
			if !rc.permissionsEqual(sourcePerm, targetPerm) {
				conflicts = append(conflicts, fmt.Sprintf("different: %s", key))
			}
		}
	}
	
	return len(conflicts) == 0, conflicts
}

// CompareMetadata compares robot metadata fields
func (rc *RobotComparatorImpl) CompareMetadata(source, target *client.RobotAccount) (bool, []string) {
	conflicts := make([]string, 0)
	
	// Compare non-permission, non-secret fields
	if rc.compareNames(source.Name, target.Name) {
		conflicts = append(conflicts, "name")
	}
	
	if source.Description != target.Description {
		conflicts = append(conflicts, "description")
	}
	
	if source.Level != target.Level {
		conflicts = append(conflicts, "level")
	}
	
	if source.Duration != target.Duration {
		conflicts = append(conflicts, "duration")
	}
	
	if !rc.config.IgnoreDisabledStatus && source.Disabled != target.Disabled {
		conflicts = append(conflicts, "disabled")
	}
	
	return len(conflicts) == 0, conflicts
}

// DetectConflicts detects all types of conflicts between robots
func (rc *RobotComparatorImpl) DetectConflicts(source, target *client.RobotAccount) ([]RobotConflict, error) {
	conflicts := make([]RobotConflict, 0)
	
	// Main comparison conflict
	if mainConflict, err := rc.Compare(source, target); err != nil {
		return nil, err
	} else if mainConflict != nil {
		conflicts = append(conflicts, *mainConflict)
	}
	
	// Additional specific conflict checks
	
	// Name exists but different robot
	if rc.compareNames(source.Name, target.Name) && source.ID != target.ID {
		conflicts = append(conflicts, RobotConflict{
			Type:           RobotConflictTypeNameExists,
			SourceRobot:    source,
			TargetRobot:    target,
			ConflictFields: []string{"name", "id"},
			Severity:       ConflictSeverityHigh,
			Details: map[string]interface{}{
				"message": "Robots with same name but different IDs",
			},
			Timestamp: time.Now(),
		})
	}
	
	// Level mismatch
	if source.Level != target.Level {
		conflicts = append(conflicts, RobotConflict{
			Type:           RobotConflictTypeLevelMismatch,
			SourceRobot:    source,
			TargetRobot:    target,
			ConflictFields: []string{"level"},
			Severity:       ConflictSeverityCritical,
			Details: map[string]interface{}{
				"source_level": source.Level,
				"target_level": target.Level,
			},
			Timestamp: time.Now(),
		})
	}
	
	// Project mismatch for project-level robots
	if source.Level == "project" && target.Level == "project" {
		sourceProjectID := extractProjectIDFromRobot(source)
		targetProjectID := extractProjectIDFromRobot(target)
		
		if sourceProjectID != targetProjectID && sourceProjectID != 0 && targetProjectID != 0 {
			conflicts = append(conflicts, RobotConflict{
				Type:           RobotConflictTypeProjectMismatch,
				SourceRobot:    source,
				TargetRobot:    target,
				ConflictFields: []string{"project_id"},
				Severity:       ConflictSeverityHigh,
				Details: map[string]interface{}{
					"source_project_id": sourceProjectID,
					"target_project_id": targetProjectID,
				},
				Timestamp: time.Now(),
			})
		}
	}
	
	return conflicts, nil
}

// AnalyzeConflict analyzes a conflict and determines its severity
func (rc *RobotComparatorImpl) AnalyzeConflict(conflict *RobotConflict) ConflictSeverity {
	if conflict == nil {
		return ConflictSeverityLow
	}
	
	// Check for critical field conflicts
	for _, field := range conflict.ConflictFields {
		for _, criticalField := range rc.config.CriticalFields {
			if field == criticalField {
				return ConflictSeverityCritical
			}
		}
	}
	
	// Analyze based on conflict type
	switch conflict.Type {
	case RobotConflictTypeLevelMismatch:
		return ConflictSeverityCritical
	case RobotConflictTypeProjectMismatch:
		return ConflictSeverityHigh
	case RobotConflictTypeNameExists:
		return ConflictSeverityHigh
	case RobotConflictTypePermissionsDiffer:
		return ConflictSeverityMedium
	case RobotConflictTypeSecretMismatch:
		return ConflictSeverityLow
	default:
		return ConflictSeverityMedium
	}
}

// NormalizeRobot normalizes a robot account for comparison
func (rc *RobotComparatorImpl) NormalizeRobot(robot *client.RobotAccount) *client.RobotAccount {
	if robot == nil {
		return nil
	}
	
	// Create a copy to avoid modifying the original
	normalized := &client.RobotAccount{
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
	
	// Normalize name
	if rc.config.TrimWhitespace {
		normalized.Name = strings.TrimSpace(normalized.Name)
		normalized.Description = strings.TrimSpace(normalized.Description)
	}
	
	if !rc.config.CaseSensitiveNames {
		normalized.Name = strings.ToLower(normalized.Name)
	}
	
	// Clear fields that should be ignored
	if rc.config.IgnoreIDs {
		normalized.ID = 0
	}
	
	if rc.config.IgnoreSecrets {
		normalized.Secret = ""
	}
	
	if rc.config.IgnoreTimestamps {
		normalized.CreationTime = time.Time{}
		normalized.UpdateTime = time.Time{}
		normalized.ExpiresAt = 0
	}
	
	// Normalize permissions
	if robot.Permissions != nil {
		normalized.Permissions = rc.normalizePermissions(robot.Permissions)
	}
	
	return normalized
}

// GenerateChecksum generates a checksum for a robot account
func (rc *RobotComparatorImpl) GenerateChecksum(robot *client.RobotAccount) string {
	if robot == nil {
		return ""
	}
	
	// Normalize robot for consistent checksums
	normalized := rc.NormalizeRobot(robot)
	
	// Convert to JSON for hashing
	data, err := json.Marshal(normalized)
	if err != nil {
		rc.logger.Error("Failed to marshal robot for checksum", zap.Error(err))
		return ""
	}
	
	// Generate SHA-256 hash
	hash := sha256.Sum256(data)
	return fmt.Sprintf("%x", hash)
}

// Private helper methods

func (rc *RobotComparatorImpl) compareRobotFields(source, target *client.RobotAccount) bool {
	// Compare name
	if !rc.compareNames(source.Name, target.Name) {
		return false
	}
	
	// Compare description
	if source.Description != target.Description {
		return false
	}
	
	// Compare level
	if source.Level != target.Level {
		return false
	}
	
	// Compare duration
	if source.Duration != target.Duration {
		return false
	}
	
	// Compare disabled status
	if !rc.config.IgnoreDisabledStatus && source.Disabled != target.Disabled {
		return false
	}
	
	// Compare permissions
	if equal, _ := rc.ComparePermissions(source.Permissions, target.Permissions); !equal {
		return false
	}
	
	return true
}

func (rc *RobotComparatorImpl) compareNames(name1, name2 string) bool {
	if rc.config.TrimWhitespace {
		name1 = strings.TrimSpace(name1)
		name2 = strings.TrimSpace(name2)
	}
	
	if !rc.config.CaseSensitiveNames {
		name1 = strings.ToLower(name1)
		name2 = strings.ToLower(name2)
	}
	
	return name1 == name2
}

func (rc *RobotComparatorImpl) normalizePermissions(permissions []client.RobotPermission) []client.RobotPermission {
	if len(permissions) == 0 {
		return permissions
	}
	
	// Create a copy
	normalized := make([]client.RobotPermission, len(permissions))
	copy(normalized, permissions)
	
	// Sort permissions for consistent comparison
	if rc.config.IgnorePermissionOrder {
		sort.Slice(normalized, func(i, j int) bool {
			if normalized[i].Kind != normalized[j].Kind {
				return normalized[i].Kind < normalized[j].Kind
			}
			return normalized[i].Namespace < normalized[j].Namespace
		})
		
		// Sort access within each permission
		for i := range normalized {
			sort.Slice(normalized[i].Access, func(x, y int) bool {
				if normalized[i].Access[x].Resource != normalized[i].Access[y].Resource {
					return normalized[i].Access[x].Resource < normalized[i].Access[y].Resource
				}
				return normalized[i].Access[x].Action < normalized[i].Access[y].Action
			})
		}
	}
	
	return normalized
}

func (rc *RobotComparatorImpl) permissionsToMap(permissions []client.RobotPermission) map[string]client.RobotPermission {
	result := make(map[string]client.RobotPermission)
	
	for _, perm := range permissions {
		key := fmt.Sprintf("%s:%s", perm.Kind, perm.Namespace)
		result[key] = perm
	}
	
	return result
}

func (rc *RobotComparatorImpl) permissionsEqual(perm1, perm2 client.RobotPermission) bool {
	if perm1.Kind != perm2.Kind || perm1.Namespace != perm2.Namespace {
		return false
	}
	
	if len(perm1.Access) != len(perm2.Access) {
		return false
	}
	
	// Convert access to maps for comparison
	access1Map := make(map[string]bool)
	access2Map := make(map[string]bool)
	
	for _, access := range perm1.Access {
		key := fmt.Sprintf("%s:%s", access.Resource, access.Action)
		access1Map[key] = true
	}
	
	for _, access := range perm2.Access {
		key := fmt.Sprintf("%s:%s", access.Resource, access.Action)
		access2Map[key] = true
	}
	
	return reflect.DeepEqual(access1Map, access2Map)
}

func (rc *RobotComparatorImpl) determineConflictType(conflicts []string, source, target *client.RobotAccount) RobotConflictType {
	// Check for specific conflict patterns
	hasNameConflict := contains(conflicts, "name")
	hasLevelConflict := contains(conflicts, "level")
	hasPermissionConflict := contains(conflicts, "permissions")
	
	if hasLevelConflict {
		return RobotConflictTypeLevelMismatch
	}
	
	if hasNameConflict && source.ID != target.ID {
		return RobotConflictTypeNameExists
	}
	
	if hasPermissionConflict {
		return RobotConflictTypePermissionsDiffer
	}
	
	if source.Level == "project" && target.Level == "project" {
		sourceProjectID := extractProjectIDFromRobot(source)
		targetProjectID := extractProjectIDFromRobot(target)
		if sourceProjectID != targetProjectID {
			return RobotConflictTypeProjectMismatch
		}
	}
	
	// Default to permissions differ
	return RobotConflictTypePermissionsDiffer
}

func (rc *RobotComparatorImpl) analyzeSeverity(conflicts []string, details map[string]interface{}) ConflictSeverity {
	// Check for critical field conflicts
	criticalConflicts := 0
	for _, conflict := range conflicts {
		for _, criticalField := range rc.config.CriticalFields {
			if conflict == criticalField {
				criticalConflicts++
				break
			}
		}
	}
	
	// Calculate severity based on conflict count and critical conflicts
	severityScore := float64(criticalConflicts) + float64(len(conflicts))*0.1
	
	if severityScore >= 2.0 {
		return ConflictSeverityCritical
	} else if severityScore >= 1.0 {
		return ConflictSeverityHigh
	} else if severityScore >= 0.5 {
		return ConflictSeverityMedium
	}
	
	return ConflictSeverityLow
}

// Utility functions

func extractProjectIDFromRobot(robot *client.RobotAccount) int64 {
	if robot == nil || robot.Level != "project" {
		return 0
	}
	
	for _, perm := range robot.Permissions {
		if strings.HasPrefix(perm.Namespace, "/project/") {
			projectIDStr := strings.TrimPrefix(perm.Namespace, "/project/")
			// This would need proper parsing in a real implementation
			// For now, return 0 as a placeholder
			_ = projectIDStr
			return 0
		}
	}
	
	return 0
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}