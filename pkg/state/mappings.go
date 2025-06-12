package state

import (
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"
)

// MappingFilter defines criteria for filtering resource mappings
type MappingFilter struct {
	ResourceType     ResourceType     `json:"resource_type,omitempty"`
	SourceInstance   string           `json:"source_instance,omitempty"`
	TargetInstance   string           `json:"target_instance,omitempty"`
	Status           MappingStatus    `json:"status,omitempty"`
	SourceID         string           `json:"source_id,omitempty"`
	TargetID         string           `json:"target_id,omitempty"`
	CreatedAfter     *time.Time       `json:"created_after,omitempty"`
	CreatedBefore    *time.Time       `json:"created_before,omitempty"`
	LastSyncedAfter  *time.Time       `json:"last_synced_after,omitempty"`
	LastSyncedBefore *time.Time       `json:"last_synced_before,omitempty"`
	HasDependencies  *bool            `json:"has_dependencies,omitempty"`
}

// MappingOptions defines options for mapping operations
type MappingOptions struct {
	OverwriteExisting  bool                      `json:"overwrite_existing"`
	ValidateDuplicates bool                      `json:"validate_duplicates"`
	UpdateTimestamp    bool                      `json:"update_timestamp"`
	ConflictStrategy   ConflictStrategy          `json:"conflict_strategy,omitempty"`
	Metadata           map[string]interface{}    `json:"metadata,omitempty"`
	Dependencies       []string                  `json:"dependencies,omitempty"`
}

// MappingQueryResult contains the results of a mapping query
type MappingQueryResult struct {
	Mappings     []ResourceMapping `json:"mappings"`
	TotalCount   int               `json:"total_count"`
	Page         int               `json:"page"`
	PageSize     int               `json:"page_size"`
	HasNext      bool              `json:"has_next"`
	FilterApplied MappingFilter    `json:"filter_applied"`
}

// MappingBulkOperation represents a bulk operation on mappings
type MappingBulkOperation struct {
	Operation string                     `json:"operation"` // "create", "update", "delete", "sync_status"
	Mappings  []ResourceMapping          `json:"mappings,omitempty"`
	IDs       []string                   `json:"ids,omitempty"`
	Status    MappingStatus              `json:"status,omitempty"`
	Options   MappingOptions             `json:"options,omitempty"`
	Metadata  map[string]interface{}     `json:"metadata,omitempty"`
}

// MappingBulkResult contains the results of a bulk mapping operation
type MappingBulkResult struct {
	TotalProcessed int                        `json:"total_processed"`
	Successful     int                        `json:"successful"`
	Failed         int                        `json:"failed"`
	Skipped        int                        `json:"skipped"`
	Results        []MappingOperationResult   `json:"results"`
	Errors         []string                   `json:"errors,omitempty"`
}

// MappingOperationResult represents the result of a single mapping operation
type MappingOperationResult struct {
	MappingID string `json:"mapping_id"`
	Success   bool   `json:"success"`
	Error     string `json:"error,omitempty"`
	Skipped   bool   `json:"skipped"`
	Reason    string `json:"reason,omitempty"`
}

// AddMapping adds a new resource mapping
func (sm *StateManager) AddMapping(mapping ResourceMapping, options ...MappingOptions) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	
	opts := MappingOptions{
		OverwriteExisting:  false,
		ValidateDuplicates: true,
		UpdateTimestamp:    true,
	}
	if len(options) > 0 {
		opts = options[0]
	}
	
	// Validate mapping
	if err := sm.validateMapping(&mapping); err != nil {
		return fmt.Errorf("mapping validation failed: %w", err)
	}
	
	// Generate ID if not provided
	if mapping.ID == "" {
		mapping.ID = sm.generateMappingID(&mapping)
	}
	
	// Check for duplicates if enabled
	if opts.ValidateDuplicates {
		if existing := sm.findDuplicateMapping(&mapping); existing != nil {
			if !opts.OverwriteExisting {
				return fmt.Errorf("duplicate mapping found: %s", existing.ID)
			}
			sm.logger.Info("Overwriting existing mapping",
				zap.String("existing_id", existing.ID),
				zap.String("new_id", mapping.ID))
		}
	}
	
	// Set timestamps
	now := time.Now()
	if opts.UpdateTimestamp {
		mapping.CreatedAt = now
		mapping.LastModified = now
	}
	
	// Apply options
	if opts.ConflictStrategy != "" {
		mapping.ConflictResolution = opts.ConflictStrategy
	}
	
	if opts.Metadata != nil {
		if mapping.Metadata == nil {
			mapping.Metadata = make(map[string]interface{})
		}
		for k, v := range opts.Metadata {
			mapping.Metadata[k] = v
		}
	}
	
	if opts.Dependencies != nil {
		mapping.Dependencies = opts.Dependencies
	}
	
	// Set default status if not provided
	if mapping.SyncStatus == "" {
		mapping.SyncStatus = MappingStatusPending
	}
	
	// Store mapping
	sm.state.ResourceMappings[mapping.ID] = mapping
	sm.state.UpdatedAt = now
	
	sm.logger.Debug("Added resource mapping",
		zap.String("id", mapping.ID),
		zap.String("resource_type", mapping.ResourceType.String()),
		zap.String("source_id", mapping.SourceID),
		zap.String("target_id", mapping.TargetID))
	
	return nil
}

// UpdateMapping updates an existing resource mapping
func (sm *StateManager) UpdateMapping(mappingID string, updates map[string]interface{}, options ...MappingOptions) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	
	opts := MappingOptions{
		UpdateTimestamp: true,
	}
	if len(options) > 0 {
		opts = options[0]
	}
	
	mapping, exists := sm.state.ResourceMappings[mappingID]
	if !exists {
		return fmt.Errorf("mapping not found: %s", mappingID)
	}
	
	// Apply updates
	originalMapping := mapping
	updated := false
	
	for field, value := range updates {
		switch field {
		case "source_id":
			if v, ok := value.(string); ok && v != mapping.SourceID {
				mapping.SourceID = v
				updated = true
			}
		case "target_id":
			if v, ok := value.(string); ok && v != mapping.TargetID {
				mapping.TargetID = v
				updated = true
			}
		case "resource_type":
			if v, ok := value.(ResourceType); ok && v != mapping.ResourceType {
				mapping.ResourceType = v
				updated = true
			}
		case "sync_status":
			if v, ok := value.(MappingStatus); ok && v != mapping.SyncStatus {
				mapping.SyncStatus = v
				updated = true
			}
		case "conflict_resolution":
			if v, ok := value.(ConflictStrategy); ok && v != mapping.ConflictResolution {
				mapping.ConflictResolution = v
				updated = true
			}
		case "dependencies":
			if v, ok := value.([]string); ok {
				mapping.Dependencies = v
				updated = true
			}
		case "metadata":
			if v, ok := value.(map[string]interface{}); ok {
				if mapping.Metadata == nil {
					mapping.Metadata = make(map[string]interface{})
				}
				for k, val := range v {
					mapping.Metadata[k] = val
				}
				updated = true
			}
		case "last_synced":
			if v, ok := value.(time.Time); ok {
				mapping.LastSynced = v
				updated = true
			}
		}
	}
	
	if !updated {
		return fmt.Errorf("no valid updates provided")
	}
	
	// Update timestamp
	if opts.UpdateTimestamp {
		mapping.LastModified = time.Now()
	}
	
	// Validate updated mapping
	if err := sm.validateMapping(&mapping); err != nil {
		// Restore original mapping
		sm.state.ResourceMappings[mappingID] = originalMapping
		return fmt.Errorf("updated mapping validation failed: %w", err)
	}
	
	// Store updated mapping
	sm.state.ResourceMappings[mappingID] = mapping
	sm.state.UpdatedAt = time.Now()
	
	sm.logger.Debug("Updated resource mapping",
		zap.String("id", mappingID),
		zap.Any("updates", updates))
	
	return nil
}

// GetMapping retrieves a resource mapping by ID
func (sm *StateManager) GetMapping(mappingID string) (*ResourceMapping, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	
	mapping, exists := sm.state.ResourceMappings[mappingID]
	if !exists {
		return nil, fmt.Errorf("mapping not found: %s", mappingID)
	}
	
	// Return a copy to prevent external modification
	mappingCopy := mapping
	return &mappingCopy, nil
}

// DeleteMapping removes a resource mapping
func (sm *StateManager) DeleteMapping(mappingID string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	
	if _, exists := sm.state.ResourceMappings[mappingID]; !exists {
		return fmt.Errorf("mapping not found: %s", mappingID)
	}
	
	// Check for dependencies
	dependencies := sm.findMappingDependencies(mappingID)
	if len(dependencies) > 0 {
		return fmt.Errorf("cannot delete mapping with dependencies: %v", dependencies)
	}
	
	delete(sm.state.ResourceMappings, mappingID)
	sm.state.UpdatedAt = time.Now()
	
	sm.logger.Debug("Deleted resource mapping", zap.String("id", mappingID))
	return nil
}

// GetMappingsByType retrieves all mappings of a specific resource type
func (sm *StateManager) GetMappingsByType(resourceType ResourceType) ([]ResourceMapping, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	
	var mappings []ResourceMapping
	for _, mapping := range sm.state.ResourceMappings {
		if mapping.ResourceType == resourceType {
			mappings = append(mappings, mapping)
		}
	}
	
	return mappings, nil
}

// QueryMappings retrieves mappings based on filter criteria with pagination
func (sm *StateManager) QueryMappings(filter MappingFilter, page, pageSize int) (*MappingQueryResult, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 50
	}
	
	// Apply filters
	var filteredMappings []ResourceMapping
	for _, mapping := range sm.state.ResourceMappings {
		if sm.mappingMatchesFilter(&mapping, &filter) {
			filteredMappings = append(filteredMappings, mapping)
		}
	}
	
	totalCount := len(filteredMappings)
	
	// Apply pagination
	startIndex := (page - 1) * pageSize
	endIndex := startIndex + pageSize
	
	if startIndex >= totalCount {
		return &MappingQueryResult{
			Mappings:      []ResourceMapping{},
			TotalCount:    totalCount,
			Page:          page,
			PageSize:      pageSize,
			HasNext:       false,
			FilterApplied: filter,
		}, nil
	}
	
	if endIndex > totalCount {
		endIndex = totalCount
	}
	
	paginatedMappings := filteredMappings[startIndex:endIndex]
	hasNext := endIndex < totalCount
	
	return &MappingQueryResult{
		Mappings:      paginatedMappings,
		TotalCount:    totalCount,
		Page:          page,
		PageSize:      pageSize,
		HasNext:       hasNext,
		FilterApplied: filter,
	}, nil
}

// FindMappingBySource finds a mapping by source instance and source ID
func (sm *StateManager) FindMappingBySource(sourceInstance, sourceID string, resourceType ResourceType) (*ResourceMapping, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	
	for _, mapping := range sm.state.ResourceMappings {
		if mapping.SourceInstance == sourceInstance &&
		   mapping.SourceID == sourceID &&
		   mapping.ResourceType == resourceType {
			mappingCopy := mapping
			return &mappingCopy, nil
		}
	}
	
	return nil, fmt.Errorf("mapping not found for source %s:%s", sourceInstance, sourceID)
}

// BulkOperations performs bulk operations on mappings
func (sm *StateManager) BulkMappingOperations(operations []MappingBulkOperation) (*MappingBulkResult, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	
	result := &MappingBulkResult{
		Results: make([]MappingOperationResult, 0),
		Errors:  make([]string, 0),
	}
	
	for _, op := range operations {
		switch op.Operation {
		case "create":
			sm.processBulkCreate(&op, result)
		case "update":
			sm.processBulkUpdate(&op, result)
		case "delete":
			sm.processBulkDelete(&op, result)
		case "sync_status":
			sm.processBulkSyncStatus(&op, result)
		default:
			result.Errors = append(result.Errors, fmt.Sprintf("unknown operation: %s", op.Operation))
		}
	}
	
	sm.state.UpdatedAt = time.Now()
	
	return result, nil
}

// CleanupOrphanedMappings removes mappings that reference non-existent resources
func (sm *StateManager) CleanupOrphanedMappings() ([]string, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	
	var orphanedIDs []string
	
	for id, mapping := range sm.state.ResourceMappings {
		if sm.isMappingOrphaned(&mapping) {
			orphanedIDs = append(orphanedIDs, id)
			delete(sm.state.ResourceMappings, id)
		}
	}
	
	if len(orphanedIDs) > 0 {
		sm.state.UpdatedAt = time.Now()
		sm.logger.Info("Cleaned up orphaned mappings",
			zap.Strings("ids", orphanedIDs),
			zap.Int("count", len(orphanedIDs)))
	}
	
	return orphanedIDs, nil
}

// ResolveConflicts attempts to resolve mapping conflicts based on strategy
func (sm *StateManager) ResolveConflicts() ([]string, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	
	var resolvedIDs []string
	
	for id, mapping := range sm.state.ResourceMappings {
		if mapping.SyncStatus == MappingStatusConflict {
			if sm.attemptConflictResolution(&mapping) {
				mapping.SyncStatus = MappingStatusPending
				mapping.LastModified = time.Now()
				sm.state.ResourceMappings[id] = mapping
				resolvedIDs = append(resolvedIDs, id)
			}
		}
	}
	
	if len(resolvedIDs) > 0 {
		sm.state.UpdatedAt = time.Now()
		sm.logger.Info("Resolved mapping conflicts",
			zap.Strings("ids", resolvedIDs),
			zap.Int("count", len(resolvedIDs)))
	}
	
	return resolvedIDs, nil
}

// Helper methods

// validateMapping validates a resource mapping
func (sm *StateManager) validateMapping(mapping *ResourceMapping) error {
	if mapping.SourceID == "" {
		return fmt.Errorf("source ID is required")
	}
	
	if mapping.TargetID == "" {
		return fmt.Errorf("target ID is required")
	}
	
	if mapping.ResourceType == "" {
		return fmt.Errorf("resource type is required")
	}
	
	if mapping.SourceInstance == "" {
		return fmt.Errorf("source instance is required")
	}
	
	if mapping.TargetInstance == "" {
		return fmt.Errorf("target instance is required")
	}
	
	// Validate dependencies exist
	for _, depID := range mapping.Dependencies {
		if _, exists := sm.state.ResourceMappings[depID]; !exists {
			return fmt.Errorf("dependency mapping not found: %s", depID)
		}
	}
	
	return nil
}

// generateMappingID generates a unique ID for a mapping
func (sm *StateManager) generateMappingID(mapping *ResourceMapping) string {
	return fmt.Sprintf("%s_%s_%s_%s",
		mapping.ResourceType,
		mapping.SourceInstance,
		mapping.TargetInstance,
		mapping.SourceID)
}

// findDuplicateMapping finds an existing mapping with the same key properties
func (sm *StateManager) findDuplicateMapping(mapping *ResourceMapping) *ResourceMapping {
	for _, existing := range sm.state.ResourceMappings {
		if existing.ResourceType == mapping.ResourceType &&
		   existing.SourceInstance == mapping.SourceInstance &&
		   existing.TargetInstance == mapping.TargetInstance &&
		   existing.SourceID == mapping.SourceID {
			return &existing
		}
	}
	return nil
}

// findMappingDependencies finds mappings that depend on the given mapping ID
func (sm *StateManager) findMappingDependencies(mappingID string) []string {
	var dependencies []string
	
	for id, mapping := range sm.state.ResourceMappings {
		for _, depID := range mapping.Dependencies {
			if depID == mappingID {
				dependencies = append(dependencies, id)
				break
			}
		}
	}
	
	return dependencies
}

// mappingMatchesFilter checks if a mapping matches the given filter
func (sm *StateManager) mappingMatchesFilter(mapping *ResourceMapping, filter *MappingFilter) bool {
	if filter.ResourceType != "" && mapping.ResourceType != filter.ResourceType {
		return false
	}
	
	if filter.SourceInstance != "" && mapping.SourceInstance != filter.SourceInstance {
		return false
	}
	
	if filter.TargetInstance != "" && mapping.TargetInstance != filter.TargetInstance {
		return false
	}
	
	if filter.Status != "" && mapping.SyncStatus != filter.Status {
		return false
	}
	
	if filter.SourceID != "" && !strings.Contains(mapping.SourceID, filter.SourceID) {
		return false
	}
	
	if filter.TargetID != "" && !strings.Contains(mapping.TargetID, filter.TargetID) {
		return false
	}
	
	if filter.CreatedAfter != nil && mapping.CreatedAt.Before(*filter.CreatedAfter) {
		return false
	}
	
	if filter.CreatedBefore != nil && mapping.CreatedAt.After(*filter.CreatedBefore) {
		return false
	}
	
	if filter.LastSyncedAfter != nil && mapping.LastSynced.Before(*filter.LastSyncedAfter) {
		return false
	}
	
	if filter.LastSyncedBefore != nil && mapping.LastSynced.After(*filter.LastSyncedBefore) {
		return false
	}
	
	if filter.HasDependencies != nil {
		hasDeps := len(mapping.Dependencies) > 0
		if *filter.HasDependencies != hasDeps {
			return false
		}
	}
	
	return true
}

// Bulk operation processors

func (sm *StateManager) processBulkCreate(op *MappingBulkOperation, result *MappingBulkResult) {
	for _, mapping := range op.Mappings {
		result.TotalProcessed++
		
		if err := sm.validateMapping(&mapping); err != nil {
			result.Failed++
			result.Results = append(result.Results, MappingOperationResult{
				MappingID: mapping.ID,
				Success:   false,
				Error:     err.Error(),
			})
			continue
		}
		
		// Check for duplicates
		if existing := sm.findDuplicateMapping(&mapping); existing != nil {
			if !op.Options.OverwriteExisting {
				result.Skipped++
				result.Results = append(result.Results, MappingOperationResult{
					MappingID: mapping.ID,
					Success:   false,
					Skipped:   true,
					Reason:    "duplicate mapping exists",
				})
				continue
			}
		}
		
		// Generate ID if needed
		if mapping.ID == "" {
			mapping.ID = sm.generateMappingID(&mapping)
		}
		
		// Set timestamps
		now := time.Now()
		mapping.CreatedAt = now
		mapping.LastModified = now
		
		// Apply options
		if op.Options.ConflictStrategy != "" {
			mapping.ConflictResolution = op.Options.ConflictStrategy
		}
		
		if op.Options.Metadata != nil {
			if mapping.Metadata == nil {
				mapping.Metadata = make(map[string]interface{})
			}
			for k, v := range op.Options.Metadata {
				mapping.Metadata[k] = v
			}
		}
		
		sm.state.ResourceMappings[mapping.ID] = mapping
		result.Successful++
		result.Results = append(result.Results, MappingOperationResult{
			MappingID: mapping.ID,
			Success:   true,
		})
	}
}

func (sm *StateManager) processBulkUpdate(op *MappingBulkOperation, result *MappingBulkResult) {
	// Implementation similar to processBulkCreate but for updates
	// This would iterate through op.IDs and apply updates from op.Metadata
}

func (sm *StateManager) processBulkDelete(op *MappingBulkOperation, result *MappingBulkResult) {
	for _, id := range op.IDs {
		result.TotalProcessed++
		
		if _, exists := sm.state.ResourceMappings[id]; !exists {
			result.Skipped++
			result.Results = append(result.Results, MappingOperationResult{
				MappingID: id,
				Success:   false,
				Skipped:   true,
				Reason:    "mapping not found",
			})
			continue
		}
		
		// Check dependencies
		dependencies := sm.findMappingDependencies(id)
		if len(dependencies) > 0 {
			result.Failed++
			result.Results = append(result.Results, MappingOperationResult{
				MappingID: id,
				Success:   false,
				Error:     fmt.Sprintf("mapping has dependencies: %v", dependencies),
			})
			continue
		}
		
		delete(sm.state.ResourceMappings, id)
		result.Successful++
		result.Results = append(result.Results, MappingOperationResult{
			MappingID: id,
			Success:   true,
		})
	}
}

func (sm *StateManager) processBulkSyncStatus(op *MappingBulkOperation, result *MappingBulkResult) {
	for _, id := range op.IDs {
		result.TotalProcessed++
		
		mapping, exists := sm.state.ResourceMappings[id]
		if !exists {
			result.Skipped++
			result.Results = append(result.Results, MappingOperationResult{
				MappingID: id,
				Success:   false,
				Skipped:   true,
				Reason:    "mapping not found",
			})
			continue
		}
		
		mapping.SyncStatus = op.Status
		mapping.LastModified = time.Now()
		sm.state.ResourceMappings[id] = mapping
		
		result.Successful++
		result.Results = append(result.Results, MappingOperationResult{
			MappingID: id,
			Success:   true,
		})
	}
}

func (sm *StateManager) isMappingOrphaned(mapping *ResourceMapping) bool {
	// This would check if the source or target resources still exist
	// For now, we'll use simple heuristics
	return mapping.SourceID == "" || mapping.TargetID == ""
}

func (sm *StateManager) attemptConflictResolution(mapping *ResourceMapping) bool {
	// This would implement conflict resolution based on the strategy
	// For now, we'll just mark it as resolvable
	return mapping.ConflictResolution != ConflictStrategyManual
}