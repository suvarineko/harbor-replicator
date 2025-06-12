package state

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.uber.org/zap"
)

// StateManager manages the synchronization state
type StateManager struct {
	mu       sync.RWMutex
	state    *SyncState
	filePath string
	logger   *zap.Logger
	config   StateManagerConfig
}

// StateManagerConfig contains configuration for the state manager
type StateManagerConfig struct {
	// EnableCompression enables gzip compression for state files
	EnableCompression bool
	
	// CreateBackups enables automatic backup creation before saving
	CreateBackups bool
	
	// MaxBackups is the maximum number of backups to keep
	MaxBackups int
	
	// EnableFileLocking enables file locking to prevent concurrent access
	EnableFileLocking bool
	
	// AutoSaveInterval is the interval for automatic state saving
	AutoSaveInterval time.Duration
	
	// ValidateOnLoad enables state validation when loading from disk
	ValidateOnLoad bool
	
	// CorruptionRecovery enables automatic recovery from corruption
	CorruptionRecovery bool
}

// DefaultStateManagerConfig returns a default configuration
func DefaultStateManagerConfig() StateManagerConfig {
	return StateManagerConfig{
		EnableCompression:  false,
		CreateBackups:      true,
		MaxBackups:         5,
		EnableFileLocking:  true,
		AutoSaveInterval:   5 * time.Minute,
		ValidateOnLoad:     true,
		CorruptionRecovery: true,
	}
}

// NewStateManager creates a new state manager
func NewStateManager(filePath string, logger *zap.Logger, config ...StateManagerConfig) (*StateManager, error) {
	if logger == nil {
		return nil, fmt.Errorf("logger is required")
	}
	
	if filePath == "" {
		return nil, fmt.Errorf("file path is required")
	}
	
	cfg := DefaultStateManagerConfig()
	if len(config) > 0 {
		cfg = config[0]
	}
	
	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create state directory: %w", err)
	}
	
	sm := &StateManager{
		filePath: filePath,
		logger:   logger,
		config:   cfg,
	}
	
	// Load existing state or create new one
	if err := sm.Load(); err != nil {
		if !os.IsNotExist(err) {
			// Try to recover from corruption if enabled
			if cfg.CorruptionRecovery {
				logger.Warn("State file appears corrupted, attempting recovery",
					zap.String("file", filePath),
					zap.Error(err))
				
				if recoverErr := sm.recoverFromCorruption(); recoverErr != nil {
					return nil, fmt.Errorf("failed to recover from corruption: %w", recoverErr)
				}
			} else {
				return nil, fmt.Errorf("failed to load state: %w", err)
			}
		} else {
			// Create new state
			sm.initializeNewState()
		}
	}
	
	// Start auto-save if enabled
	if cfg.AutoSaveInterval > 0 {
		go sm.autoSaveWorker()
	}
	
	logger.Info("State manager initialized",
		zap.String("file", filePath),
		zap.Bool("compression", cfg.EnableCompression),
		zap.Bool("backups", cfg.CreateBackups))
	
	return sm, nil
}

// initializeNewState creates a new empty state
func (sm *StateManager) initializeNewState() {
	now := time.Now()
	sm.state = &SyncState{
		LastSync:         make(map[string]time.Time),
		ResourceMappings: make(map[string]ResourceMapping),
		SyncErrors:       make([]SyncError, 0),
		SyncInProgress:   false,
		ResourceVersions: make(map[string]ResourceVersion),
		SyncHistory:      make([]SyncOperation, 0),
		Statistics: SyncStatistics{
			ResourceTypeStats: make(map[ResourceType]ResourceTypeStatistics),
		},
		CreatedAt: now,
		UpdatedAt: now,
		Version:   DefaultStateVersion,
	}
}

// Load loads the state from disk
func (sm *StateManager) Load() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	
	file, err := os.Open(sm.filePath)
	if err != nil {
		return err
	}
	defer file.Close()
	
	var reader io.Reader = file
	
	// Check if file is compressed by reading first few bytes
	header := make([]byte, 2)
	if _, err := file.Read(header); err != nil {
		return fmt.Errorf("failed to read file header: %w", err)
	}
	
	// Reset file position
	if _, err := file.Seek(0, 0); err != nil {
		return fmt.Errorf("failed to reset file position: %w", err)
	}
	
	// Check for gzip magic number
	if header[0] == 0x1f && header[1] == 0x8b {
		gzipReader, err := gzip.NewReader(file)
		if err != nil {
			return fmt.Errorf("failed to create gzip reader: %w", err)
		}
		defer gzipReader.Close()
		reader = gzipReader
	}
	
	// Decode JSON
	decoder := json.NewDecoder(reader)
	var state SyncState
	if err := decoder.Decode(&state); err != nil {
		return fmt.Errorf("failed to decode state: %w", err)
	}
	
	// Validate state if enabled
	if sm.config.ValidateOnLoad {
		if validationResult := sm.validateState(&state); !validationResult.Valid {
			sm.logger.Warn("Loaded state has validation issues",
				zap.Strings("errors", validationResult.Errors),
				zap.Strings("warnings", validationResult.Warnings))
			
			// Auto-fix if possible
			sm.autoFixState(&state)
		}
	}
	
	sm.state = &state
	sm.logger.Info("State loaded successfully",
		zap.String("file", sm.filePath),
		zap.String("version", state.Version),
		zap.Time("created", state.CreatedAt),
		zap.Time("updated", state.UpdatedAt))
	
	return nil
}

// Save saves the state to disk with atomic operations
func (sm *StateManager) Save() error {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	
	return sm.saveState()
}

// saveState performs the actual save operation (internal method)
func (sm *StateManager) saveState() error {
	if sm.state == nil {
		return fmt.Errorf("no state to save")
	}
	
	// Update timestamp
	sm.state.UpdatedAt = time.Now()
	
	// Create backup if enabled
	if sm.config.CreateBackups {
		if err := sm.createBackup(); err != nil {
			sm.logger.Warn("Failed to create backup", zap.Error(err))
			// Continue with save even if backup fails
		}
	}
	
	// Use temporary file for atomic write
	tempFile := sm.filePath + ".tmp"
	file, err := os.Create(tempFile)
	if err != nil {
		return fmt.Errorf("failed to create temporary file: %w", err)
	}
	defer func() {
		file.Close()
		os.Remove(tempFile) // Clean up on error
	}()
	
	var writer io.Writer = file
	
	// Apply compression if enabled
	if sm.config.EnableCompression {
		gzipWriter := gzip.NewWriter(file)
		defer gzipWriter.Close()
		writer = gzipWriter
	}
	
	// Encode JSON with indentation for readability
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(sm.state); err != nil {
		return fmt.Errorf("failed to encode state: %w", err)
	}
	
	// Close gzip writer if used
	if sm.config.EnableCompression {
		if gzipWriter, ok := writer.(*gzip.Writer); ok {
			if err := gzipWriter.Close(); err != nil {
				return fmt.Errorf("failed to close gzip writer: %w", err)
			}
		}
	}
	
	// Sync to disk
	if err := file.Sync(); err != nil {
		return fmt.Errorf("failed to sync file: %w", err)
	}
	
	// Close temporary file
	if err := file.Close(); err != nil {
		return fmt.Errorf("failed to close temporary file: %w", err)
	}
	
	// Atomic rename
	if err := os.Rename(tempFile, sm.filePath); err != nil {
		return fmt.Errorf("failed to rename temporary file: %w", err)
	}
	
	sm.logger.Debug("State saved successfully", zap.String("file", sm.filePath))
	return nil
}

// createBackup creates a backup of the current state file
func (sm *StateManager) createBackup() error {
	// Check if original file exists
	if _, err := os.Stat(sm.filePath); os.IsNotExist(err) {
		return nil // No file to backup
	}
	
	// Generate backup filename with timestamp
	timestamp := time.Now().Format("20060102-150405")
	backupPath := fmt.Sprintf("%s.backup.%s", sm.filePath, timestamp)
	
	// Copy file
	if err := sm.copyFile(sm.filePath, backupPath); err != nil {
		return fmt.Errorf("failed to create backup: %w", err)
	}
	
	// Clean up old backups
	if err := sm.cleanupOldBackups(); err != nil {
		sm.logger.Warn("Failed to cleanup old backups", zap.Error(err))
	}
	
	sm.logger.Debug("Backup created", zap.String("backup", backupPath))
	return nil
}

// copyFile copies a file from src to dst
func (sm *StateManager) copyFile(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()
	
	destFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destFile.Close()
	
	_, err = io.Copy(destFile, sourceFile)
	if err != nil {
		return err
	}
	
	return destFile.Sync()
}

// cleanupOldBackups removes old backup files beyond the configured limit
func (sm *StateManager) cleanupOldBackups() error {
	if sm.config.MaxBackups <= 0 {
		return nil
	}
	
	pattern := sm.filePath + ".backup.*"
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return err
	}
	
	if len(matches) <= sm.config.MaxBackups {
		return nil
	}
	
	// Sort by modification time (oldest first)
	for i := 0; i < len(matches)-sm.config.MaxBackups; i++ {
		if err := os.Remove(matches[i]); err != nil {
			sm.logger.Warn("Failed to remove old backup", 
				zap.String("file", matches[i]), 
				zap.Error(err))
		}
	}
	
	return nil
}

// recoverFromCorruption attempts to recover from a corrupted state file
func (sm *StateManager) recoverFromCorruption() error {
	// Try to load from backups
	pattern := sm.filePath + ".backup.*"
	backups, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Errorf("failed to find backups: %w", err)
	}
	
	if len(backups) == 0 {
		sm.logger.Info("No backups found, creating new state")
		sm.initializeNewState()
		return nil
	}
	
	// Try backups from newest to oldest
	for i := len(backups) - 1; i >= 0; i-- {
		backupPath := backups[i]
		sm.logger.Info("Attempting recovery from backup", zap.String("backup", backupPath))
		
		// Copy backup to main file
		if err := sm.copyFile(backupPath, sm.filePath); err != nil {
			sm.logger.Warn("Failed to copy backup", zap.String("backup", backupPath), zap.Error(err))
			continue
		}
		
		// Try to load
		if err := sm.Load(); err != nil {
			sm.logger.Warn("Failed to load from backup", zap.String("backup", backupPath), zap.Error(err))
			continue
		}
		
		sm.logger.Info("Successfully recovered from backup", zap.String("backup", backupPath))
		return nil
	}
	
	// If all backups failed, create new state
	sm.logger.Warn("All recovery attempts failed, creating new state")
	sm.initializeNewState()
	return nil
}

// validateState validates the integrity of the state
func (sm *StateManager) validateState(state *SyncState) *StateValidationResult {
	result := &StateValidationResult{
		Valid:     true,
		Errors:    make([]string, 0),
		Warnings:  make([]string, 0),
		CheckedAt: time.Now(),
	}
	
	// Check required fields
	if state == nil {
		result.Valid = false
		result.Errors = append(result.Errors, "state is nil")
		return result
	}
	
	if state.Version == "" {
		result.Valid = false
		result.Errors = append(result.Errors, "state version is empty")
	}
	
	if state.LastSync == nil {
		result.Valid = false
		result.Errors = append(result.Errors, "last sync map is nil")
	}
	
	if state.ResourceMappings == nil {
		result.Valid = false
		result.Errors = append(result.Errors, "resource mappings map is nil")
	}
	
	// Check for orphaned mappings
	orphanedMappings := make([]string, 0)
	duplicateMappings := make([]string, 0)
	seenMappings := make(map[string]string)
	
	for id, mapping := range state.ResourceMappings {
		// Check for required fields
		if mapping.ID == "" {
			result.Warnings = append(result.Warnings, fmt.Sprintf("mapping %s has empty ID", id))
		}
		
		if mapping.SourceID == "" || mapping.TargetID == "" {
			orphanedMappings = append(orphanedMappings, id)
		}
		
		// Check for duplicates
		key := fmt.Sprintf("%s:%s:%s", mapping.ResourceType, mapping.SourceID, mapping.TargetID)
		if existingID, exists := seenMappings[key]; exists {
			duplicateMappings = append(duplicateMappings, fmt.Sprintf("%s (duplicate of %s)", id, existingID))
		} else {
			seenMappings[key] = id
		}
	}
	
	result.OrphanedMappings = orphanedMappings
	result.DuplicateMappings = duplicateMappings
	
	if len(orphanedMappings) > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("found %d orphaned mappings", len(orphanedMappings)))
	}
	
	if len(duplicateMappings) > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("found %d duplicate mappings", len(duplicateMappings)))
	}
	
	// Check version consistency
	inconsistentVersions := make([]string, 0)
	for id, version := range state.ResourceVersions {
		if version.ResourceID == "" {
			inconsistentVersions = append(inconsistentVersions, id)
		}
	}
	
	result.InconsistentVersions = inconsistentVersions
	
	return result
}

// autoFixState attempts to automatically fix common state issues
func (sm *StateManager) autoFixState(state *SyncState) {
	// Initialize nil maps
	if state.LastSync == nil {
		state.LastSync = make(map[string]time.Time)
	}
	
	if state.ResourceMappings == nil {
		state.ResourceMappings = make(map[string]ResourceMapping)
	}
	
	if state.ResourceVersions == nil {
		state.ResourceVersions = make(map[string]ResourceVersion)
	}
	
	if state.SyncHistory == nil {
		state.SyncHistory = make([]SyncOperation, 0)
	}
	
	if state.Statistics.ResourceTypeStats == nil {
		state.Statistics.ResourceTypeStats = make(map[ResourceType]ResourceTypeStatistics)
	}
	
	// Set default version if empty
	if state.Version == "" {
		state.Version = DefaultStateVersion
	}
	
	// Set creation time if empty
	if state.CreatedAt.IsZero() {
		state.CreatedAt = time.Now()
	}
	
	sm.logger.Info("Auto-fixed state issues")
}

// autoSaveWorker runs in a goroutine to periodically save state
func (sm *StateManager) autoSaveWorker() {
	ticker := time.NewTicker(sm.config.AutoSaveInterval)
	defer ticker.Stop()
	
	for range ticker.C {
		if err := sm.Save(); err != nil {
			sm.logger.Error("Auto-save failed", zap.Error(err))
		}
	}
}

// GetChecksum calculates a checksum of the current state for integrity verification
func (sm *StateManager) GetChecksum() (string, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	
	if sm.state == nil {
		return "", fmt.Errorf("no state available")
	}
	
	data, err := json.Marshal(sm.state)
	if err != nil {
		return "", fmt.Errorf("failed to marshal state: %w", err)
	}
	
	hash := sha256.Sum256(data)
	return fmt.Sprintf("%x", hash), nil
}

// ValidateState validates the current state and returns the validation result
func (sm *StateManager) ValidateState() *StateValidationResult {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	
	return sm.validateState(sm.state)
}

// RepairState attempts to repair the current state by fixing common issues
func (sm *StateManager) RepairState() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	
	if sm.state == nil {
		return fmt.Errorf("no state to repair")
	}
	
	sm.autoFixState(sm.state)
	
	// Save repaired state
	if err := sm.saveState(); err != nil {
		return fmt.Errorf("failed to save repaired state: %w", err)
	}
	
	sm.logger.Info("State repaired successfully")
	return nil
}

// Group State Management Methods

// SaveGroupState saves the state of an OIDC group
func (sm *StateManager) SaveGroupState(groupID string, groupState GroupState) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	
	if sm.state == nil {
		return fmt.Errorf("state not initialized")
	}
	
	// Initialize group states map if needed
	if sm.state.GroupStates == nil {
		sm.state.GroupStates = make(map[string]GroupState)
	}
	
	// Update the group state
	groupState.LastUpdated = time.Now()
	sm.state.GroupStates[groupID] = groupState
	
	// Update overall state timestamp
	sm.state.UpdatedAt = time.Now()
	
	sm.logger.Debug("Saved group state", 
		zap.String("group_id", groupID),
		zap.String("sync_status", string(groupState.SyncStatus)))
	
	return sm.saveState()
}

// GetGroupState retrieves the state of an OIDC group
func (sm *StateManager) GetGroupState(groupID string) (GroupState, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	
	if sm.state == nil {
		return GroupState{}, fmt.Errorf("state not initialized")
	}
	
	if sm.state.GroupStates == nil {
		return GroupState{}, fmt.Errorf("group state not found: %s", groupID)
	}
	
	groupState, exists := sm.state.GroupStates[groupID]
	if !exists {
		return GroupState{}, fmt.Errorf("group state not found: %s", groupID)
	}
	
	sm.logger.Debug("Retrieved group state", 
		zap.String("group_id", groupID),
		zap.String("sync_status", string(groupState.SyncStatus)))
	
	return groupState, nil
}

// CompareGroupStates compares two group states and returns a comparison result
func (sm *StateManager) CompareGroupStates(state1, state2 GroupState) GroupStateComparison {
	comparison := GroupStateComparison{
		GroupsEqual:        true,
		PermissionsChanged: false,
		ProjectsChanged:    false,
		ChangedFields:      make([]string, 0),
		ComparedAt:         time.Now(),
	}
	
	// Compare basic group properties
	if state1.GroupName != state2.GroupName {
		comparison.GroupsEqual = false
		comparison.ChangedFields = append(comparison.ChangedFields, "group_name")
	}
	
	if state1.GroupType != state2.GroupType {
		comparison.GroupsEqual = false
		comparison.ChangedFields = append(comparison.ChangedFields, "group_type")
	}
	
	if state1.LdapGroupDN != state2.LdapGroupDN {
		comparison.GroupsEqual = false
		comparison.ChangedFields = append(comparison.ChangedFields, "ldap_group_dn")
	}
	
	// Compare permissions
	if !comparePermissions(state1.Permissions, state2.Permissions) {
		comparison.GroupsEqual = false
		comparison.PermissionsChanged = true
		comparison.ChangedFields = append(comparison.ChangedFields, "permissions")
	}
	
	// Compare project associations
	if !compareProjectMappings(state1.ProjectMappings, state2.ProjectMappings) {
		comparison.GroupsEqual = false
		comparison.ProjectsChanged = true
		comparison.ChangedFields = append(comparison.ChangedFields, "project_mappings")
	}
	
	// Compare checksums
	if state1.GroupChecksum != state2.GroupChecksum {
		comparison.GroupsEqual = false
		comparison.ChangedFields = append(comparison.ChangedFields, "checksum")
	}
	
	return comparison
}

// ListGroupStates returns all group states, optionally filtered by status
func (sm *StateManager) ListGroupStates(filter GroupStateFilter) (map[string]GroupState, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	
	if sm.state == nil {
		return nil, fmt.Errorf("state not initialized")
	}
	
	if sm.state.GroupStates == nil {
		return make(map[string]GroupState), nil
	}
	
	result := make(map[string]GroupState)
	
	for groupID, groupState := range sm.state.GroupStates {
		include := true
		
		// Apply filters
		if filter.SyncStatus != "" && groupState.SyncStatus != filter.SyncStatus {
			include = false
		}
		
		if filter.LastSyncBefore != nil && groupState.LastSyncTime.After(*filter.LastSyncBefore) {
			include = false
		}
		
		if filter.LastSyncAfter != nil && groupState.LastSyncTime.Before(*filter.LastSyncAfter) {
			include = false
		}
		
		if filter.GroupType != 0 && groupState.GroupType != filter.GroupType {
			include = false
		}
		
		if include {
			result[groupID] = groupState
		}
	}
	
	sm.logger.Debug("Listed group states", 
		zap.Int("total_groups", len(sm.state.GroupStates)),
		zap.Int("filtered_groups", len(result)))
	
	return result, nil
}

// RemoveGroupState removes a group state from tracking
func (sm *StateManager) RemoveGroupState(groupID string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	
	if sm.state == nil {
		return fmt.Errorf("state not initialized")
	}
	
	if sm.state.GroupStates == nil {
		return nil // Nothing to remove
	}
	
	delete(sm.state.GroupStates, groupID)
	sm.state.UpdatedAt = time.Now()
	
	sm.logger.Debug("Removed group state", zap.String("group_id", groupID))
	
	return sm.saveState()
}

// GetGroupSyncStatistics returns statistics about group synchronization
func (sm *StateManager) GetGroupSyncStatistics() (GroupSyncStatistics, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	
	if sm.state == nil {
		return GroupSyncStatistics{}, fmt.Errorf("state not initialized")
	}
	
	stats := GroupSyncStatistics{
		TotalGroups:      0,
		SyncedGroups:     0,
		FailedGroups:     0,
		PendingGroups:    0,
		ConflictGroups:   0,
		StatusCounts:     make(map[MappingStatus]int),
		LastCalculated:   time.Now(),
	}
	
	if sm.state.GroupStates == nil {
		return stats, nil
	}
	
	for _, groupState := range sm.state.GroupStates {
		stats.TotalGroups++
		
		switch groupState.SyncStatus {
		case MappingStatusSynced:
			stats.SyncedGroups++
		case MappingStatusFailed:
			stats.FailedGroups++
		case MappingStatusPending:
			stats.PendingGroups++
		case MappingStatusConflict:
			stats.ConflictGroups++
		}
		
		stats.StatusCounts[groupState.SyncStatus]++
	}
	
	// Calculate success rate
	if stats.TotalGroups > 0 {
		stats.SuccessRate = float64(stats.SyncedGroups) / float64(stats.TotalGroups) * 100
	}
	
	return stats, nil
}

// Helper functions for group state comparison

func comparePermissions(perms1, perms2 []GroupPermission) bool {
	if len(perms1) != len(perms2) {
		return false
	}
	
	// Create maps for easy comparison
	permsMap1 := make(map[string]GroupPermission)
	permsMap2 := make(map[string]GroupPermission)
	
	for _, perm := range perms1 {
		key := fmt.Sprintf("%s:%s", perm.Resource, perm.Action)
		permsMap1[key] = perm
	}
	
	for _, perm := range perms2 {
		key := fmt.Sprintf("%s:%s", perm.Resource, perm.Action)
		permsMap2[key] = perm
	}
	
	// Compare maps
	for key, perm1 := range permsMap1 {
		if perm2, exists := permsMap2[key]; !exists || perm1 != perm2 {
			return false
		}
	}
	
	return true
}

func compareProjectMappings(mappings1, mappings2 []GroupProjectMapping) bool {
	if len(mappings1) != len(mappings2) {
		return false
	}
	
	// Create maps for easy comparison
	mappingsMap1 := make(map[string]GroupProjectMapping)
	mappingsMap2 := make(map[string]GroupProjectMapping)
	
	for _, mapping := range mappings1 {
		key := fmt.Sprintf("%d:%d", mapping.SourceProjectID, mapping.TargetProjectID)
		mappingsMap1[key] = mapping
	}
	
	for _, mapping := range mappings2 {
		key := fmt.Sprintf("%d:%d", mapping.SourceProjectID, mapping.TargetProjectID)
		mappingsMap2[key] = mapping
	}
	
	// Compare maps
	for key, mapping1 := range mappingsMap1 {
		if mapping2, exists := mappingsMap2[key]; !exists || mapping1 != mapping2 {
			return false
		}
	}
	
	return true
}

// Close closes the state manager and performs final cleanup
func (sm *StateManager) Close() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	
	// Perform final save
	if sm.state != nil {
		if err := sm.saveState(); err != nil {
			sm.logger.Error("Failed to save state on close", zap.Error(err))
			return err
		}
	}
	
	sm.logger.Info("State manager closed")
	return nil
}