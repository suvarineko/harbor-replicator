package state

import (
	"fmt"
	"time"

	"go.uber.org/zap"
)

// SyncProgressTracker manages sync progress tracking
type SyncProgressTracker struct {
	manager   *StateManager
	logger    *zap.Logger
	startTime time.Time
}

// NewSyncProgressTracker creates a new sync progress tracker
func NewSyncProgressTracker(manager *StateManager, logger *zap.Logger) *SyncProgressTracker {
	return &SyncProgressTracker{
		manager: manager,
		logger:  logger,
	}
}

// StartSync initiates a new sync operation and returns the operation ID
func (sm *StateManager) StartSync(resourceTypes []ResourceType, sourceInstance, targetInstance string, trigger string) (string, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	
	if sm.state.SyncInProgress {
		return "", fmt.Errorf("sync operation already in progress: %s", sm.state.CurrentSyncID)
	}
	
	// Generate unique sync ID
	syncID := sm.generateSyncID()
	now := time.Now()
	
	// Create sync operation
	operation := SyncOperation{
		ID:                  syncID,
		StartTime:           now,
		Status:              OperationStatusRunning,
		ResourceTypes:       resourceTypes,
		SourceInstance:      sourceInstance,
		TargetInstance:      targetInstance,
		Progress:            0.0,
		TotalResources:      0,
		ProcessedResources:  0,
		SuccessfulResources: 0,
		FailedResources:     0,
		SkippedResources:    0,
		ErrorSummary:        make(map[ErrorType]int),
		Trigger:             trigger,
		Configuration:       make(map[string]interface{}),
	}
	
	// Update state
	sm.state.SyncInProgress = true
	sm.state.CurrentSyncID = syncID
	sm.state.SyncHistory = append(sm.state.SyncHistory, operation)
	sm.state.UpdatedAt = now
	
	// Clean up old history if needed
	sm.cleanupSyncHistory()
	
	sm.logger.Info("Started sync operation",
		zap.String("sync_id", syncID),
		zap.Strings("resource_types", resourceTypesToStrings(resourceTypes)),
		zap.String("source", sourceInstance),
		zap.String("target", targetInstance),
		zap.String("trigger", trigger))
	
	return syncID, nil
}

// UpdateSyncProgress updates the progress of the current sync operation
func (sm *StateManager) UpdateSyncProgress(syncID string, updates map[string]interface{}) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	
	if sm.state.CurrentSyncID != syncID {
		return fmt.Errorf("sync ID mismatch: expected %s, got %s", sm.state.CurrentSyncID, syncID)
	}
	
	// Find the current operation in history
	operationIndex := -1
	for i := len(sm.state.SyncHistory) - 1; i >= 0; i-- {
		if sm.state.SyncHistory[i].ID == syncID {
			operationIndex = i
			break
		}
	}
	
	if operationIndex == -1 {
		return fmt.Errorf("sync operation not found: %s", syncID)
	}
	
	operation := sm.state.SyncHistory[operationIndex]
	
	// Apply updates
	updated := false
	for field, value := range updates {
		switch field {
		case "total_resources":
			if v, ok := value.(int); ok {
				operation.TotalResources = v
				updated = true
			}
		case "processed_resources":
			if v, ok := value.(int); ok {
				operation.ProcessedResources = v
				updated = true
			}
		case "successful_resources":
			if v, ok := value.(int); ok {
				operation.SuccessfulResources = v
				updated = true
			}
		case "failed_resources":
			if v, ok := value.(int); ok {
				operation.FailedResources = v
				updated = true
			}
		case "skipped_resources":
			if v, ok := value.(int); ok {
				operation.SkippedResources = v
				updated = true
			}
		case "progress":
			if v, ok := value.(float64); ok {
				operation.Progress = v
				updated = true
			}
		case "error_summary":
			if v, ok := value.(map[ErrorType]int); ok {
				operation.ErrorSummary = v
				updated = true
			}
		case "configuration":
			if v, ok := value.(map[string]interface{}); ok {
				for k, val := range v {
					operation.Configuration[k] = val
				}
				updated = true
			}
		}
	}
	
	if !updated {
		return fmt.Errorf("no valid updates provided")
	}
	
	// Calculate progress if not explicitly provided
	if operation.TotalResources > 0 {
		operation.Progress = float64(operation.ProcessedResources) / float64(operation.TotalResources) * 100.0
	}
	
	// Update the operation in history
	sm.state.SyncHistory[operationIndex] = operation
	sm.state.UpdatedAt = time.Now()
	
	sm.logger.Debug("Updated sync progress",
		zap.String("sync_id", syncID),
		zap.Float64("progress", operation.Progress),
		zap.Int("processed", operation.ProcessedResources),
		zap.Int("total", operation.TotalResources))
	
	return nil
}

// CompleteSyncForResource marks a specific resource as successfully synced
func (sm *StateManager) CompleteSyncForResource(syncID, mappingID string, resourceType ResourceType) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	
	// Update mapping status
	if mapping, exists := sm.state.ResourceMappings[mappingID]; exists {
		mapping.SyncStatus = MappingStatusSynced
		mapping.LastSynced = time.Now()
		mapping.LastModified = time.Now()
		sm.state.ResourceMappings[mappingID] = mapping
	}
	
	// Update sync operation counters
	if err := sm.updateSyncCounters(syncID, "successful", resourceType); err != nil {
		return err
	}
	
	// Update resource type statistics
	sm.updateResourceTypeStats(resourceType, true)
	
	sm.logger.Debug("Completed sync for resource",
		zap.String("sync_id", syncID),
		zap.String("mapping_id", mappingID),
		zap.String("resource_type", resourceType.String()))
	
	return nil
}

// FailSyncForResource marks a specific resource sync as failed
func (sm *StateManager) FailSyncForResource(syncID, mappingID string, resourceType ResourceType, errorType ErrorType, errorMessage string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	
	// Update mapping status
	if mapping, exists := sm.state.ResourceMappings[mappingID]; exists {
		mapping.SyncStatus = MappingStatusFailed
		mapping.LastModified = time.Now()
		sm.state.ResourceMappings[mappingID] = mapping
	}
	
	// Update sync operation counters
	if err := sm.updateSyncCounters(syncID, "failed", resourceType); err != nil {
		return err
	}
	
	// Update error summary
	if err := sm.updateErrorSummary(syncID, errorType); err != nil {
		return err
	}
	
	// Record the error
	syncError := SyncError{
		ID:           sm.generateErrorID(),
		Timestamp:    time.Now(),
		ResourceType: resourceType,
		ResourceID:   mappingID,
		MappingID:    mappingID,
		SyncID:       syncID,
		ErrorMessage: errorMessage,
		ErrorType:    errorType,
		RetryCount:   0,
		MaxRetries:   DefaultMaxRetries,
		Resolved:     false,
		Context:      make(map[string]interface{}),
	}
	
	// Set next retry time for retryable errors
	if errorType.IsRetryable() {
		syncError.NextRetryAt = time.Now().Add(DefaultRetryInterval)
	}
	
	sm.state.SyncErrors = append(sm.state.SyncErrors, syncError)
	
	// Update resource type statistics
	sm.updateResourceTypeStats(resourceType, false)
	
	sm.logger.Warn("Failed sync for resource",
		zap.String("sync_id", syncID),
		zap.String("mapping_id", mappingID),
		zap.String("resource_type", resourceType.String()),
		zap.String("error_type", errorType.String()),
		zap.String("error", errorMessage))
	
	return nil
}

// SkipSyncForResource marks a specific resource as skipped
func (sm *StateManager) SkipSyncForResource(syncID, mappingID string, resourceType ResourceType, reason string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	
	// Update mapping status
	if mapping, exists := sm.state.ResourceMappings[mappingID]; exists {
		mapping.SyncStatus = MappingStatusSkipped
		mapping.LastModified = time.Now()
		sm.state.ResourceMappings[mappingID] = mapping
	}
	
	// Update sync operation counters
	if err := sm.updateSyncCounters(syncID, "skipped", resourceType); err != nil {
		return err
	}
	
	sm.logger.Debug("Skipped sync for resource",
		zap.String("sync_id", syncID),
		zap.String("mapping_id", mappingID),
		zap.String("resource_type", resourceType.String()),
		zap.String("reason", reason))
	
	return nil
}

// CompleteSyncOperation marks the entire sync operation as complete
func (sm *StateManager) CompleteSyncOperation(syncID string, status OperationStatus) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	
	if sm.state.CurrentSyncID != syncID {
		return fmt.Errorf("sync ID mismatch: expected %s, got %s", sm.state.CurrentSyncID, syncID)
	}
	
	// Find and update the operation
	operationIndex := -1
	for i := len(sm.state.SyncHistory) - 1; i >= 0; i-- {
		if sm.state.SyncHistory[i].ID == syncID {
			operationIndex = i
			break
		}
	}
	
	if operationIndex == -1 {
		return fmt.Errorf("sync operation not found: %s", syncID)
	}
	
	now := time.Now()
	operation := sm.state.SyncHistory[operationIndex]
	operation.Status = status
	operation.EndTime = now
	operation.Progress = 100.0 // Mark as complete
	
	// Update state
	sm.state.SyncInProgress = false
	sm.state.CurrentSyncID = ""
	sm.state.SyncHistory[operationIndex] = operation
	sm.state.UpdatedAt = now
	
	// Update statistics
	sm.updateOverallStatistics(&operation)
	
	// Update last sync times per resource type
	for _, resourceType := range operation.ResourceTypes {
		sm.state.LastSync[resourceType.String()] = now
	}
	
	sm.logger.Info("Completed sync operation",
		zap.String("sync_id", syncID),
		zap.String("status", status.String()),
		zap.Duration("duration", operation.EndTime.Sub(operation.StartTime)),
		zap.Int("successful", operation.SuccessfulResources),
		zap.Int("failed", operation.FailedResources),
		zap.Int("skipped", operation.SkippedResources))
	
	return nil
}

// GetCurrentSyncProgress returns the current sync progress information
func (sm *StateManager) GetCurrentSyncProgress() (*SyncProgressInfo, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	
	if !sm.state.SyncInProgress {
		return nil, fmt.Errorf("no sync operation in progress")
	}
	
	// Find current operation
	var currentOp *SyncOperation
	for i := len(sm.state.SyncHistory) - 1; i >= 0; i-- {
		if sm.state.SyncHistory[i].ID == sm.state.CurrentSyncID {
			currentOp = &sm.state.SyncHistory[i]
			break
		}
	}
	
	if currentOp == nil {
		return nil, fmt.Errorf("current sync operation not found")
	}
	
	now := time.Now()
	elapsed := now.Sub(currentOp.StartTime)
	
	// Calculate estimated time remaining
	var estimatedRemaining time.Duration
	if currentOp.Progress > 0 && currentOp.Progress < 100 {
		totalEstimated := elapsed * time.Duration(100.0/currentOp.Progress)
		estimatedRemaining = totalEstimated - elapsed
	}
	
	// Calculate throughput
	var throughput float64
	if elapsed.Seconds() > 0 {
		throughput = float64(currentOp.ProcessedResources) / elapsed.Seconds()
	}
	
	// Build resource progress
	resourceProgress := make(map[ResourceType]ResourceProgress)
	for _, resourceType := range currentOp.ResourceTypes {
		// Calculate progress for this resource type from mappings
		progress := sm.calculateResourceTypeProgress(resourceType, currentOp.ID)
		resourceProgress[resourceType] = progress
	}
	
	// Get recent errors
	recentErrors := sm.getRecentErrors(currentOp.ID, 5)
	
	progressInfo := &SyncProgressInfo{
		OperationID:            currentOp.ID,
		StartTime:              currentOp.StartTime,
		ElapsedTime:            elapsed,
		EstimatedTimeRemaining: estimatedRemaining,
		OverallProgress:        currentOp.Progress,
		Phase:                  sm.getCurrentSyncPhase(currentOp),
		ResourceProgress:       resourceProgress,
		RecentErrors:           recentErrors,
		ThroughputPerSecond:    throughput,
	}
	
	return progressInfo, nil
}

// GetSyncHistory returns the sync operation history with pagination
func (sm *StateManager) GetSyncHistory(page, pageSize int) ([]SyncOperation, int, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 50
	}
	
	total := len(sm.state.SyncHistory)
	startIndex := (page - 1) * pageSize
	endIndex := startIndex + pageSize
	
	if startIndex >= total {
		return []SyncOperation{}, total, nil
	}
	
	if endIndex > total {
		endIndex = total
	}
	
	// Return slice in reverse order (newest first)
	result := make([]SyncOperation, 0, endIndex-startIndex)
	for i := total - 1 - startIndex; i >= total-endIndex; i-- {
		result = append(result, sm.state.SyncHistory[i])
	}
	
	return result, total, nil
}

// PauseSyncOperation pauses the current sync operation
func (sm *StateManager) PauseSyncOperation(syncID string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	
	if sm.state.CurrentSyncID != syncID {
		return fmt.Errorf("sync ID mismatch: expected %s, got %s", sm.state.CurrentSyncID, syncID)
	}
	
	// Update operation status
	for i := len(sm.state.SyncHistory) - 1; i >= 0; i-- {
		if sm.state.SyncHistory[i].ID == syncID {
			sm.state.SyncHistory[i].Status = OperationStatusPaused
			break
		}
	}
	
	sm.state.UpdatedAt = time.Now()
	
	sm.logger.Info("Paused sync operation", zap.String("sync_id", syncID))
	return nil
}

// ResumeSyncOperation resumes a paused sync operation
func (sm *StateManager) ResumeSyncOperation(syncID string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	
	if sm.state.CurrentSyncID != syncID {
		return fmt.Errorf("sync ID mismatch: expected %s, got %s", sm.state.CurrentSyncID, syncID)
	}
	
	// Update operation status
	for i := len(sm.state.SyncHistory) - 1; i >= 0; i-- {
		if sm.state.SyncHistory[i].ID == syncID {
			if sm.state.SyncHistory[i].Status != OperationStatusPaused {
				return fmt.Errorf("operation is not paused")
			}
			sm.state.SyncHistory[i].Status = OperationStatusRunning
			break
		}
	}
	
	sm.state.UpdatedAt = time.Now()
	
	sm.logger.Info("Resumed sync operation", zap.String("sync_id", syncID))
	return nil
}

// CancelSyncOperation cancels the current sync operation
func (sm *StateManager) CancelSyncOperation(syncID string) error {
	return sm.CompleteSyncOperation(syncID, OperationStatusCancelled)
}

// Helper methods

// generateSyncID generates a unique sync operation ID
func (sm *StateManager) generateSyncID() string {
	return fmt.Sprintf("sync_%d", time.Now().UnixNano())
}

// generateErrorID generates a unique error ID
func (sm *StateManager) generateErrorID() string {
	return fmt.Sprintf("error_%d", time.Now().UnixNano())
}

// cleanupSyncHistory removes old sync operations beyond retention period
func (sm *StateManager) cleanupSyncHistory() {
	if len(sm.state.SyncHistory) == 0 {
		return
	}
	
	cutoff := time.Now().Add(-DefaultSyncHistoryRetention)
	var validHistory []SyncOperation
	
	for _, op := range sm.state.SyncHistory {
		if op.StartTime.After(cutoff) {
			validHistory = append(validHistory, op)
		}
	}
	
	if len(validHistory) != len(sm.state.SyncHistory) {
		sm.state.SyncHistory = validHistory
		sm.logger.Debug("Cleaned up old sync history",
			zap.Int("removed", len(sm.state.SyncHistory)-len(validHistory)),
			zap.Int("remaining", len(validHistory)))
	}
}

// updateSyncCounters updates the sync operation counters
func (sm *StateManager) updateSyncCounters(syncID, counterType string, resourceType ResourceType) error {
	for i := len(sm.state.SyncHistory) - 1; i >= 0; i-- {
		if sm.state.SyncHistory[i].ID == syncID {
			operation := sm.state.SyncHistory[i]
			
			switch counterType {
			case "successful":
				operation.SuccessfulResources++
			case "failed":
				operation.FailedResources++
			case "skipped":
				operation.SkippedResources++
			}
			
			operation.ProcessedResources = operation.SuccessfulResources + operation.FailedResources + operation.SkippedResources
			
			// Update progress
			if operation.TotalResources > 0 {
				operation.Progress = float64(operation.ProcessedResources) / float64(operation.TotalResources) * 100.0
			}
			
			sm.state.SyncHistory[i] = operation
			return nil
		}
	}
	
	return fmt.Errorf("sync operation not found: %s", syncID)
}

// updateErrorSummary updates the error summary for a sync operation
func (sm *StateManager) updateErrorSummary(syncID string, errorType ErrorType) error {
	for i := len(sm.state.SyncHistory) - 1; i >= 0; i-- {
		if sm.state.SyncHistory[i].ID == syncID {
			if sm.state.SyncHistory[i].ErrorSummary == nil {
				sm.state.SyncHistory[i].ErrorSummary = make(map[ErrorType]int)
			}
			sm.state.SyncHistory[i].ErrorSummary[errorType]++
			return nil
		}
	}
	
	return fmt.Errorf("sync operation not found: %s", syncID)
}

// updateResourceTypeStats updates statistics for a specific resource type
func (sm *StateManager) updateResourceTypeStats(resourceType ResourceType, success bool) {
	if sm.state.Statistics.ResourceTypeStats == nil {
		sm.state.Statistics.ResourceTypeStats = make(map[ResourceType]ResourceTypeStatistics)
	}
	
	stats := sm.state.Statistics.ResourceTypeStats[resourceType]
	stats.TotalResources++
	stats.LastSyncTime = time.Now()
	
	if success {
		stats.SyncedResources++
	} else {
		stats.FailedResources++
		stats.ErrorCount++
	}
	
	sm.state.Statistics.ResourceTypeStats[resourceType] = stats
}

// updateOverallStatistics updates the overall sync statistics
func (sm *StateManager) updateOverallStatistics(operation *SyncOperation) {
	sm.state.Statistics.TotalSyncOperations++
	
	if operation.Status == OperationStatusCompleted {
		sm.state.Statistics.SuccessfulSyncOperations++
		sm.state.Statistics.LastSuccessfulSync = operation.EndTime
	} else if operation.Status == OperationStatusFailed {
		sm.state.Statistics.FailedSyncOperations++
	}
	
	sm.state.Statistics.TotalResourcesSynced += int64(operation.SuccessfulResources)
	sm.state.Statistics.TotalErrors += int64(operation.FailedResources)
	
	// Update average sync duration
	duration := operation.EndTime.Sub(operation.StartTime)
	if sm.state.Statistics.AverageSyncDuration == 0 {
		sm.state.Statistics.AverageSyncDuration = duration
	} else {
		// Simple moving average
		sm.state.Statistics.AverageSyncDuration = (sm.state.Statistics.AverageSyncDuration + duration) / 2
	}
	
	// Update error rate
	if sm.state.Statistics.TotalSyncOperations > 0 {
		sm.state.Statistics.ErrorRatePercent = float64(sm.state.Statistics.FailedSyncOperations) / float64(sm.state.Statistics.TotalSyncOperations) * 100.0
	}
	
	// Update uptime percentage
	if sm.state.Statistics.TotalSyncOperations > 0 {
		sm.state.Statistics.UptimePercent = float64(sm.state.Statistics.SuccessfulSyncOperations) / float64(sm.state.Statistics.TotalSyncOperations) * 100.0
	}
}

// calculateResourceTypeProgress calculates progress for a specific resource type
func (sm *StateManager) calculateResourceTypeProgress(resourceType ResourceType, syncID string) ResourceProgress {
	var progress ResourceProgress
	
	// Count mappings by status for this resource type
	for _, mapping := range sm.state.ResourceMappings {
		if mapping.ResourceType == resourceType {
			progress.Total++
			
			switch mapping.SyncStatus {
			case MappingStatusSynced:
				progress.Successful++
				progress.Processed++
			case MappingStatusFailed:
				progress.Failed++
				progress.Processed++
			case MappingStatusSkipped:
				progress.Skipped++
				progress.Processed++
			case MappingStatusSyncing:
				progress.Processed++
			}
		}
	}
	
	// Calculate progress percentage
	if progress.Total > 0 {
		progress.Progress = float64(progress.Processed) / float64(progress.Total) * 100.0
	}
	
	return progress
}

// getCurrentSyncPhase determines the current phase of the sync operation
func (sm *StateManager) getCurrentSyncPhase(operation *SyncOperation) string {
	if operation.Progress < 10 {
		return "initializing"
	} else if operation.Progress < 30 {
		return "discovering_resources"
	} else if operation.Progress < 90 {
		return "synchronizing"
	} else if operation.Progress < 100 {
		return "finalizing"
	}
	return "completed"
}

// getRecentErrors returns recent errors for a sync operation
func (sm *StateManager) getRecentErrors(syncID string, limit int) []string {
	var errors []string
	count := 0
	
	// Get errors for this sync operation (newest first)
	for i := len(sm.state.SyncErrors) - 1; i >= 0 && count < limit; i-- {
		if sm.state.SyncErrors[i].SyncID == syncID {
			errors = append(errors, sm.state.SyncErrors[i].ErrorMessage)
			count++
		}
	}
	
	return errors
}

// resourceTypesToStrings converts resource types to strings
func resourceTypesToStrings(types []ResourceType) []string {
	result := make([]string, len(types))
	for i, t := range types {
		result[i] = t.String()
	}
	return result
}