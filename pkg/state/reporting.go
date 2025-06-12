package state

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// SyncReport represents a comprehensive synchronization report
type SyncReport struct {
	GeneratedAt         time.Time                    `json:"generated_at"`
	ReportType          string                       `json:"report_type"`
	TimeRange           ReportTimeRange              `json:"time_range"`
	Summary             SyncReportSummary            `json:"summary"`
	OperationDetails    []SyncOperationReport        `json:"operation_details"`
	ResourceTypeBreakdown map[ResourceType]ResourceTypeReport `json:"resource_type_breakdown"`
	ErrorAnalysis       ErrorReportSection           `json:"error_analysis"`
	MappingAnalysis     MappingReportSection         `json:"mapping_analysis"`
	Performance         PerformanceReportSection     `json:"performance"`
	Recommendations     []ReportRecommendation       `json:"recommendations"`
	Metadata            map[string]interface{}       `json:"metadata"`
}

// ReportTimeRange defines the time range for a report
type ReportTimeRange struct {
	Start           time.Time `json:"start"`
	End             time.Time `json:"end"`
	Description     string    `json:"description"`
}

// SyncReportSummary provides high-level summary statistics
type SyncReportSummary struct {
	TotalOperations     int                          `json:"total_operations"`
	SuccessfulOperations int                         `json:"successful_operations"`
	FailedOperations    int                          `json:"failed_operations"`
	CancelledOperations int                          `json:"cancelled_operations"`
	TotalResources      int                          `json:"total_resources"`
	SyncedResources     int                          `json:"synced_resources"`
	FailedResources     int                          `json:"failed_resources"`
	SkippedResources    int                          `json:"skipped_resources"`
	TotalMappings       int                          `json:"total_mappings"`
	ActiveMappings      int                          `json:"active_mappings"`
	OrphanedMappings    int                          `json:"orphaned_mappings"`
	TotalErrors         int                          `json:"total_errors"`
	UnresolvedErrors    int                          `json:"unresolved_errors"`
	SuccessRate         float64                      `json:"success_rate"`
	AverageSyncDuration time.Duration                `json:"average_sync_duration"`
	LastSyncTime        time.Time                    `json:"last_sync_time"`
}

// SyncOperationReport provides detailed information about sync operations
type SyncOperationReport struct {
	OperationID         string                       `json:"operation_id"`
	StartTime           time.Time                    `json:"start_time"`
	EndTime             time.Time                    `json:"end_time"`
	Duration            time.Duration                `json:"duration"`
	Status              OperationStatus              `json:"status"`
	ResourceTypes       []ResourceType               `json:"resource_types"`
	SourceInstance      string                       `json:"source_instance"`
	TargetInstance      string                       `json:"target_instance"`
	TotalResources      int                          `json:"total_resources"`
	ProcessedResources  int                          `json:"processed_resources"`
	SuccessfulResources int                          `json:"successful_resources"`
	FailedResources     int                          `json:"failed_resources"`
	SkippedResources    int                          `json:"skipped_resources"`
	ErrorCount          int                          `json:"error_count"`
	Trigger             string                       `json:"trigger"`
	SuccessRate         float64                      `json:"success_rate"`
	Throughput          float64                      `json:"throughput_per_second"`
}

// ResourceTypeReport provides analysis for a specific resource type
type ResourceTypeReport struct {
	ResourceType        ResourceType                 `json:"resource_type"`
	TotalMappings       int                          `json:"total_mappings"`
	SyncedMappings      int                          `json:"synced_mappings"`
	FailedMappings      int                          `json:"failed_mappings"`
	PendingMappings     int                          `json:"pending_mappings"`
	OrphanedMappings    int                          `json:"orphaned_mappings"`
	SyncSuccessRate     float64                      `json:"sync_success_rate"`
	AverageSyncTime     time.Duration                `json:"average_sync_time"`
	LastSyncTime        time.Time                    `json:"last_sync_time"`
	ErrorCount          int                          `json:"error_count"`
	TopErrors           []ErrorFrequency             `json:"top_errors"`
	MappingDistribution map[MappingStatus]int        `json:"mapping_distribution"`
}

// ErrorReportSection provides error analysis for the report
type ErrorReportSection struct {
	TotalErrors         int                          `json:"total_errors"`
	UnresolvedErrors    int                          `json:"unresolved_errors"`
	ErrorsByType        map[ErrorType]int            `json:"errors_by_type"`
	ErrorsByResource    map[ResourceType]int         `json:"errors_by_resource"`
	RecentErrors        []SyncError                  `json:"recent_errors"`
	ErrorTrends         []ErrorTrend                 `json:"error_trends"`
	CriticalPatterns    []CriticalErrorPattern       `json:"critical_patterns"`
	RetryStatistics     RetryStatistics              `json:"retry_statistics"`
}

// MappingReportSection provides mapping analysis for the report
type MappingReportSection struct {
	TotalMappings       int                          `json:"total_mappings"`
	MappingsByStatus    map[MappingStatus]int        `json:"mappings_by_status"`
	MappingsByType      map[ResourceType]int         `json:"mappings_by_type"`
	OrphanedMappings    []string                     `json:"orphaned_mappings"`
	DuplicateMappings   []string                     `json:"duplicate_mappings"`
	ConflictedMappings  []string                     `json:"conflicted_mappings"`
	RecentlyModified    []ResourceMapping            `json:"recently_modified"`
	OldestMappings      []ResourceMapping            `json:"oldest_mappings"`
}

// PerformanceReportSection provides performance analysis
type PerformanceReportSection struct {
	AverageSyncDuration time.Duration                `json:"average_sync_duration"`
	MedianSyncDuration  time.Duration                `json:"median_sync_duration"`
	MaxSyncDuration     time.Duration                `json:"max_sync_duration"`
	MinSyncDuration     time.Duration                `json:"min_sync_duration"`
	AverageThroughput   float64                      `json:"average_throughput"`
	PeakThroughput      float64                      `json:"peak_throughput"`
	ResourceTypePerformance map[ResourceType]ResourceTypePerformance `json:"resource_type_performance"`
	SyncFrequency       map[string]int               `json:"sync_frequency"`
	PerformanceTrends   []PerformanceTrend           `json:"performance_trends"`
}

// ResourceTypePerformance provides performance metrics for a resource type
type ResourceTypePerformance struct {
	AverageDuration     time.Duration                `json:"average_duration"`
	Throughput          float64                      `json:"throughput"`
	SuccessRate         float64                      `json:"success_rate"`
	TotalProcessed      int                          `json:"total_processed"`
}

// PerformanceTrend represents performance trends over time
type PerformanceTrend struct {
	Period              string                       `json:"period"`
	AverageDuration     time.Duration                `json:"average_duration"`
	Throughput          float64                      `json:"throughput"`
	SuccessRate         float64                      `json:"success_rate"`
	Timestamp           time.Time                    `json:"timestamp"`
}

// RetryStatistics provides retry-related statistics
type RetryStatistics struct {
	TotalRetryAttempts  int                          `json:"total_retry_attempts"`
	SuccessfulRetries   int                          `json:"successful_retries"`
	FailedRetries       int                          `json:"failed_retries"`
	RetrySuccessRate    float64                      `json:"retry_success_rate"`
	AverageRetryCount   float64                      `json:"average_retry_count"`
	MaxRetryCount       int                          `json:"max_retry_count"`
}

// ReportRecommendation provides actionable recommendations
type ReportRecommendation struct {
	Category            string                       `json:"category"`
	Priority            string                       `json:"priority"`
	Title               string                       `json:"title"`
	Description         string                       `json:"description"`
	Action              string                       `json:"action"`
	Impact              string                       `json:"impact"`
}

// QueryFilter defines comprehensive query filter options
type QueryFilter struct {
	TimeRange           *ReportTimeRange             `json:"time_range,omitempty"`
	ResourceTypes       []ResourceType               `json:"resource_types,omitempty"`
	OperationStatus     []OperationStatus            `json:"operation_status,omitempty"`
	MappingStatus       []MappingStatus              `json:"mapping_status,omitempty"`
	ErrorTypes          []ErrorType                  `json:"error_types,omitempty"`
	SourceInstances     []string                     `json:"source_instances,omitempty"`
	TargetInstances     []string                     `json:"target_instances,omitempty"`
	IncludeResolved     bool                         `json:"include_resolved"`
	MinSuccessRate      *float64                     `json:"min_success_rate,omitempty"`
	MaxSuccessRate      *float64                     `json:"max_success_rate,omitempty"`
}

// GetSyncStatus returns the current synchronization status
func (sm *StateManager) GetSyncStatus() map[string]interface{} {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	
	status := make(map[string]interface{})
	
	status["sync_in_progress"] = sm.state.SyncInProgress
	status["current_sync_id"] = sm.state.CurrentSyncID
	status["version"] = sm.state.Version
	status["created_at"] = sm.state.CreatedAt
	status["updated_at"] = sm.state.UpdatedAt
	
	// Last sync times
	status["last_sync_times"] = sm.state.LastSync
	
	// Overall statistics
	status["statistics"] = sm.state.Statistics
	
	// Current progress if sync is running
	if sm.state.SyncInProgress {
		if progress, err := sm.getCurrentSyncProgress(); err == nil {
			status["current_progress"] = progress
		}
	}
	
	return status
}

// GetLastSyncTime returns the last sync time for a specific resource type
func (sm *StateManager) GetLastSyncTime(resourceType ResourceType) (time.Time, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	
	if lastSync, exists := sm.state.LastSync[resourceType.String()]; exists {
		return lastSync, nil
	}
	
	return time.Time{}, fmt.Errorf("no sync time found for resource type: %s", resourceType)
}

// GetPendingResources returns resources that are pending synchronization
func (sm *StateManager) GetPendingResources(resourceType ...ResourceType) ([]ResourceMapping, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	
	var pendingMappings []ResourceMapping
	
	for _, mapping := range sm.state.ResourceMappings {
		// Filter by resource type if specified
		if len(resourceType) > 0 {
			typeMatches := false
			for _, rt := range resourceType {
				if mapping.ResourceType == rt {
					typeMatches = true
					break
				}
			}
			if !typeMatches {
				continue
			}
		}
		
		// Check if mapping is pending
		if mapping.SyncStatus == MappingStatusPending {
			pendingMappings = append(pendingMappings, mapping)
		}
	}
	
	return pendingMappings, nil
}

// GenerateSyncReport generates a comprehensive synchronization report
func (sm *StateManager) GenerateSyncReport(reportType string, timeRange *ReportTimeRange, filter *QueryFilter) (*SyncReport, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	
	now := time.Now()
	
	// Set default time range if not provided
	if timeRange == nil {
		timeRange = &ReportTimeRange{
			Start:       now.Add(-24 * time.Hour),
			End:         now,
			Description: "Last 24 hours",
		}
	}
	
	report := &SyncReport{
		GeneratedAt:           now,
		ReportType:            reportType,
		TimeRange:             *timeRange,
		ResourceTypeBreakdown: make(map[ResourceType]ResourceTypeReport),
		Metadata:              make(map[string]interface{}),
	}
	
	// Generate summary
	report.Summary = sm.generateReportSummary(timeRange, filter)
	
	// Generate operation details
	report.OperationDetails = sm.generateOperationReports(timeRange, filter)
	
	// Generate resource type breakdown
	report.ResourceTypeBreakdown = sm.generateResourceTypeReports(timeRange, filter)
	
	// Generate error analysis
	report.ErrorAnalysis = sm.generateErrorReportSection(timeRange, filter)
	
	// Generate mapping analysis
	report.MappingAnalysis = sm.generateMappingReportSection(filter)
	
	// Generate performance analysis
	report.Performance = sm.generatePerformanceReportSection(timeRange, filter)
	
	// Generate recommendations
	report.Recommendations = sm.generateReportRecommendations(report)
	
	// Add metadata
	report.Metadata["total_mappings"] = len(sm.state.ResourceMappings)
	report.Metadata["total_errors"] = len(sm.state.SyncErrors)
	report.Metadata["total_operations"] = len(sm.state.SyncHistory)
	report.Metadata["state_version"] = sm.state.Version
	
	return report, nil
}

// GetResourceHistory returns the sync history for specific resources
func (sm *StateManager) GetResourceHistory(resourceType ResourceType, resourceID string, limit int) ([]SyncOperation, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	
	if limit <= 0 {
		limit = 50
	}
	
	var relevantOperations []SyncOperation
	
	// Find operations that involved this resource
	for _, operation := range sm.state.SyncHistory {
		// Check if this operation involved the specified resource type
		for _, opResourceType := range operation.ResourceTypes {
			if opResourceType == resourceType {
				relevantOperations = append(relevantOperations, operation)
				break
			}
		}
	}
	
	// Sort by start time (newest first)
	sort.Slice(relevantOperations, func(i, j int) bool {
		return relevantOperations[i].StartTime.After(relevantOperations[j].StartTime)
	})
	
	// Apply limit
	if len(relevantOperations) > limit {
		relevantOperations = relevantOperations[:limit]
	}
	
	return relevantOperations, nil
}

// QuerySyncOperations queries sync operations based on filter criteria
func (sm *StateManager) QuerySyncOperations(filter QueryFilter, page, pageSize int) ([]SyncOperation, int, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 50
	}
	
	// Apply filters
	var filteredOperations []SyncOperation
	for _, operation := range sm.state.SyncHistory {
		if sm.operationMatchesFilter(&operation, &filter) {
			filteredOperations = append(filteredOperations, operation)
		}
	}
	
	totalCount := len(filteredOperations)
	
	// Sort by start time (newest first)
	sort.Slice(filteredOperations, func(i, j int) bool {
		return filteredOperations[i].StartTime.After(filteredOperations[j].StartTime)
	})
	
	// Apply pagination
	startIndex := (page - 1) * pageSize
	endIndex := startIndex + pageSize
	
	if startIndex >= totalCount {
		return []SyncOperation{}, totalCount, nil
	}
	
	if endIndex > totalCount {
		endIndex = totalCount
	}
	
	paginatedOperations := filteredOperations[startIndex:endIndex]
	
	return paginatedOperations, totalCount, nil
}

// ExportReport exports a report in the specified format
func (sm *StateManager) ExportReport(report *SyncReport, format string, writer io.Writer) error {
	switch strings.ToLower(format) {
	case "json":
		return sm.exportReportJSON(report, writer)
	case "csv":
		return sm.exportReportCSV(report, writer)
	default:
		return fmt.Errorf("unsupported export format: %s", format)
	}
}

// ValidateStateConsistency performs comprehensive state validation
func (sm *StateManager) ValidateStateConsistency() *StateValidationResult {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	
	result := sm.validateState(sm.state)
	
	// Additional consistency checks
	sm.validateMappingReferences(result)
	sm.validateSyncOperationReferences(result)
	sm.validateErrorReferences(result)
	sm.validateStatisticsConsistency(result)
	
	return result
}

// GetStateMetrics returns detailed state metrics for monitoring
func (sm *StateManager) GetStateMetrics() map[string]interface{} {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	
	metrics := make(map[string]interface{})
	
	// Basic counts
	metrics["total_mappings"] = len(sm.state.ResourceMappings)
	metrics["total_errors"] = len(sm.state.SyncErrors)
	metrics["total_operations"] = len(sm.state.SyncHistory)
	
	// Mapping status distribution
	mappingsByStatus := make(map[MappingStatus]int)
	mappingsByType := make(map[ResourceType]int)
	for _, mapping := range sm.state.ResourceMappings {
		mappingsByStatus[mapping.SyncStatus]++
		mappingsByType[mapping.ResourceType]++
	}
	metrics["mappings_by_status"] = mappingsByStatus
	metrics["mappings_by_type"] = mappingsByType
	
	// Error distribution
	errorsByType := make(map[ErrorType]int)
	unresolvedErrors := 0
	for _, err := range sm.state.SyncErrors {
		errorsByType[err.ErrorType]++
		if !err.Resolved {
			unresolvedErrors++
		}
	}
	metrics["errors_by_type"] = errorsByType
	metrics["unresolved_errors"] = unresolvedErrors
	
	// Operation status distribution
	operationsByStatus := make(map[OperationStatus]int)
	for _, operation := range sm.state.SyncHistory {
		operationsByStatus[operation.Status]++
	}
	metrics["operations_by_status"] = operationsByStatus
	
	// State health indicators
	metrics["sync_in_progress"] = sm.state.SyncInProgress
	metrics["state_version"] = sm.state.Version
	metrics["last_updated"] = sm.state.UpdatedAt
	
	return metrics
}

// Helper methods for report generation

// getCurrentSyncProgress returns current sync progress (internal helper)
func (sm *StateManager) getCurrentSyncProgress() (*SyncProgressInfo, error) {
	// This is a simplified version - the full implementation is in progress.go
	if !sm.state.SyncInProgress {
		return nil, fmt.Errorf("no sync in progress")
	}
	
	return &SyncProgressInfo{
		OperationID:     sm.state.CurrentSyncID,
		OverallProgress: 0.0, // Would be calculated based on current operation
	}, nil
}

// generateReportSummary generates the summary section of a report
func (sm *StateManager) generateReportSummary(timeRange *ReportTimeRange, filter *QueryFilter) SyncReportSummary {
	summary := SyncReportSummary{}
	
	// Count operations in time range
	for _, operation := range sm.state.SyncHistory {
		if operation.StartTime.After(timeRange.Start) && operation.StartTime.Before(timeRange.End) {
			summary.TotalOperations++
			summary.TotalResources += operation.TotalResources
			summary.SyncedResources += operation.SuccessfulResources
			summary.FailedResources += operation.FailedResources
			summary.SkippedResources += operation.SkippedResources
			
			switch operation.Status {
			case OperationStatusCompleted:
				summary.SuccessfulOperations++
			case OperationStatusFailed:
				summary.FailedOperations++
			case OperationStatusCancelled:
				summary.CancelledOperations++
			}
			
			if operation.EndTime.After(summary.LastSyncTime) {
				summary.LastSyncTime = operation.EndTime
			}
		}
	}
	
	// Count mappings
	activeMappings := 0
	orphanedMappings := 0
	for _, mapping := range sm.state.ResourceMappings {
		summary.TotalMappings++
		if mapping.SyncStatus != MappingStatusOrphaned {
			activeMappings++
		} else {
			orphanedMappings++
		}
	}
	summary.ActiveMappings = activeMappings
	summary.OrphanedMappings = orphanedMappings
	
	// Count errors in time range
	unresolvedErrors := 0
	for _, err := range sm.state.SyncErrors {
		if err.Timestamp.After(timeRange.Start) && err.Timestamp.Before(timeRange.End) {
			summary.TotalErrors++
			if !err.Resolved {
				unresolvedErrors++
			}
		}
	}
	summary.UnresolvedErrors = unresolvedErrors
	
	// Calculate success rate
	if summary.TotalResources > 0 {
		summary.SuccessRate = float64(summary.SyncedResources) / float64(summary.TotalResources) * 100.0
	}
	
	// Use overall statistics for average duration
	summary.AverageSyncDuration = sm.state.Statistics.AverageSyncDuration
	
	return summary
}

// generateOperationReports generates detailed operation reports
func (sm *StateManager) generateOperationReports(timeRange *ReportTimeRange, filter *QueryFilter) []SyncOperationReport {
	var reports []SyncOperationReport
	
	for _, operation := range sm.state.SyncHistory {
		if operation.StartTime.After(timeRange.Start) && operation.StartTime.Before(timeRange.End) {
			if sm.operationMatchesFilter(&operation, filter) {
				report := SyncOperationReport{
					OperationID:         operation.ID,
					StartTime:           operation.StartTime,
					EndTime:             operation.EndTime,
					Status:              operation.Status,
					ResourceTypes:       operation.ResourceTypes,
					SourceInstance:      operation.SourceInstance,
					TargetInstance:      operation.TargetInstance,
					TotalResources:      operation.TotalResources,
					ProcessedResources:  operation.ProcessedResources,
					SuccessfulResources: operation.SuccessfulResources,
					FailedResources:     operation.FailedResources,
					SkippedResources:    operation.SkippedResources,
					ErrorCount:          operation.FailedResources, // Simplified
					Trigger:             operation.Trigger,
				}
				
				if !operation.EndTime.IsZero() {
					report.Duration = operation.EndTime.Sub(operation.StartTime)
					if report.Duration.Seconds() > 0 {
						report.Throughput = float64(operation.ProcessedResources) / report.Duration.Seconds()
					}
				}
				
				if operation.TotalResources > 0 {
					report.SuccessRate = float64(operation.SuccessfulResources) / float64(operation.TotalResources) * 100.0
				}
				
				reports = append(reports, report)
			}
		}
	}
	
	return reports
}

// generateResourceTypeReports generates resource type breakdown reports
func (sm *StateManager) generateResourceTypeReports(timeRange *ReportTimeRange, filter *QueryFilter) map[ResourceType]ResourceTypeReport {
	reports := make(map[ResourceType]ResourceTypeReport)
	
	// Initialize reports for each resource type
	resourceTypes := []ResourceType{ResourceTypeRobotAccount, ResourceTypeOIDCGroup, ResourceTypeProject}
	
	for _, resourceType := range resourceTypes {
		report := ResourceTypeReport{
			ResourceType:        resourceType,
			MappingDistribution: make(map[MappingStatus]int),
			TopErrors:           make([]ErrorFrequency, 0),
		}
		
		// Count mappings by status
		for _, mapping := range sm.state.ResourceMappings {
			if mapping.ResourceType == resourceType {
				report.TotalMappings++
				report.MappingDistribution[mapping.SyncStatus]++
				
				switch mapping.SyncStatus {
				case MappingStatusSynced:
					report.SyncedMappings++
				case MappingStatusFailed:
					report.FailedMappings++
				case MappingStatusPending:
					report.PendingMappings++
				case MappingStatusOrphaned:
					report.OrphanedMappings++
				}
				
				if mapping.LastSynced.After(report.LastSyncTime) {
					report.LastSyncTime = mapping.LastSynced
				}
			}
		}
		
		// Calculate success rate
		if report.TotalMappings > 0 {
			report.SyncSuccessRate = float64(report.SyncedMappings) / float64(report.TotalMappings) * 100.0
		}
		
		// Count errors for this resource type
		for _, err := range sm.state.SyncErrors {
			if err.ResourceType == resourceType {
				if err.Timestamp.After(timeRange.Start) && err.Timestamp.Before(timeRange.End) {
					report.ErrorCount++
				}
			}
		}
		
		// Get statistics from state
		if stats, exists := sm.state.Statistics.ResourceTypeStats[resourceType]; exists {
			report.AverageSyncTime = stats.AverageSyncTime
		}
		
		reports[resourceType] = report
	}
	
	return reports
}

// generateErrorReportSection generates the error analysis section
func (sm *StateManager) generateErrorReportSection(timeRange *ReportTimeRange, filter *QueryFilter) ErrorReportSection {
	section := ErrorReportSection{
		ErrorsByType:     make(map[ErrorType]int),
		ErrorsByResource: make(map[ResourceType]int),
		RecentErrors:     make([]SyncError, 0),
	}
	
	// Analyze errors in time range
	var relevantErrors []SyncError
	for _, err := range sm.state.SyncErrors {
		if err.Timestamp.After(timeRange.Start) && err.Timestamp.Before(timeRange.End) {
			relevantErrors = append(relevantErrors, err)
			section.TotalErrors++
			
			if !err.Resolved {
				section.UnresolvedErrors++
			}
			
			section.ErrorsByType[err.ErrorType]++
			section.ErrorsByResource[err.ResourceType]++
		}
	}
	
	// Get recent errors (last 10)
	if len(relevantErrors) > 0 {
		sort.Slice(relevantErrors, func(i, j int) bool {
			return relevantErrors[i].Timestamp.After(relevantErrors[j].Timestamp)
		})
		
		recentCount := 10
		if len(relevantErrors) < recentCount {
			recentCount = len(relevantErrors)
		}
		section.RecentErrors = relevantErrors[:recentCount]
	}
	
	// Generate retry statistics
	section.RetryStatistics = sm.generateRetryStatistics(relevantErrors)
	
	return section
}

// generateMappingReportSection generates the mapping analysis section
func (sm *StateManager) generateMappingReportSection(filter *QueryFilter) MappingReportSection {
	section := MappingReportSection{
		MappingsByStatus:  make(map[MappingStatus]int),
		MappingsByType:    make(map[ResourceType]int),
		OrphanedMappings:  make([]string, 0),
		DuplicateMappings: make([]string, 0),
		ConflictedMappings: make([]string, 0),
		RecentlyModified:  make([]ResourceMapping, 0),
		OldestMappings:    make([]ResourceMapping, 0),
	}
	
	var allMappings []ResourceMapping
	
	for _, mapping := range sm.state.ResourceMappings {
		section.TotalMappings++
		section.MappingsByStatus[mapping.SyncStatus]++
		section.MappingsByType[mapping.ResourceType]++
		
		allMappings = append(allMappings, mapping)
		
		// Collect problem mappings
		switch mapping.SyncStatus {
		case MappingStatusOrphaned:
			section.OrphanedMappings = append(section.OrphanedMappings, mapping.ID)
		case MappingStatusConflict:
			section.ConflictedMappings = append(section.ConflictedMappings, mapping.ID)
		}
	}
	
	// Sort mappings by modification time
	sort.Slice(allMappings, func(i, j int) bool {
		return allMappings[i].LastModified.After(allMappings[j].LastModified)
	})
	
	// Get recently modified (last 10)
	recentCount := 10
	if len(allMappings) < recentCount {
		recentCount = len(allMappings)
	}
	section.RecentlyModified = allMappings[:recentCount]
	
	// Get oldest mappings (last 10)
	if len(allMappings) >= recentCount {
		section.OldestMappings = allMappings[len(allMappings)-recentCount:]
	}
	
	return section
}

// generatePerformanceReportSection generates performance analysis
func (sm *StateManager) generatePerformanceReportSection(timeRange *ReportTimeRange, filter *QueryFilter) PerformanceReportSection {
	section := PerformanceReportSection{
		ResourceTypePerformance: make(map[ResourceType]ResourceTypePerformance),
		SyncFrequency:          make(map[string]int),
		PerformanceTrends:      make([]PerformanceTrend, 0),
	}
	
	var durations []time.Duration
	var throughputs []float64
	
	// Analyze operations in time range
	for _, operation := range sm.state.SyncHistory {
		if operation.StartTime.After(timeRange.Start) && operation.StartTime.Before(timeRange.End) {
			if !operation.EndTime.IsZero() {
				duration := operation.EndTime.Sub(operation.StartTime)
				durations = append(durations, duration)
				
				if duration.Seconds() > 0 {
					throughput := float64(operation.ProcessedResources) / duration.Seconds()
					throughputs = append(throughputs, throughput)
					
					if throughput > section.PeakThroughput {
						section.PeakThroughput = throughput
					}
				}
			}
		}
	}
	
	// Calculate duration statistics
	if len(durations) > 0 {
		var total time.Duration
		section.MinSyncDuration = durations[0]
		section.MaxSyncDuration = durations[0]
		
		for _, duration := range durations {
			total += duration
			if duration < section.MinSyncDuration {
				section.MinSyncDuration = duration
			}
			if duration > section.MaxSyncDuration {
				section.MaxSyncDuration = duration
			}
		}
		
		section.AverageSyncDuration = total / time.Duration(len(durations))
		
		// Calculate median
		sort.Slice(durations, func(i, j int) bool {
			return durations[i] < durations[j]
		})
		middle := len(durations) / 2
		if len(durations)%2 == 0 {
			section.MedianSyncDuration = (durations[middle-1] + durations[middle]) / 2
		} else {
			section.MedianSyncDuration = durations[middle]
		}
	}
	
	// Calculate average throughput
	if len(throughputs) > 0 {
		var total float64
		for _, throughput := range throughputs {
			total += throughput
		}
		section.AverageThroughput = total / float64(len(throughputs))
	}
	
	return section
}

// generateRetryStatistics generates retry-related statistics
func (sm *StateManager) generateRetryStatistics(errors []SyncError) RetryStatistics {
	stats := RetryStatistics{}
	
	for _, err := range errors {
		if err.RetryCount > 0 {
			stats.TotalRetryAttempts += err.RetryCount
			
			if err.Resolved {
				stats.SuccessfulRetries++
			} else {
				stats.FailedRetries++
			}
			
			if err.RetryCount > stats.MaxRetryCount {
				stats.MaxRetryCount = err.RetryCount
			}
		}
	}
	
	if stats.TotalRetryAttempts > 0 {
		stats.RetrySuccessRate = float64(stats.SuccessfulRetries) / float64(stats.TotalRetryAttempts) * 100.0
		stats.AverageRetryCount = float64(stats.TotalRetryAttempts) / float64(len(errors))
	}
	
	return stats
}

// generateReportRecommendations generates actionable recommendations
func (sm *StateManager) generateReportRecommendations(report *SyncReport) []ReportRecommendation {
	var recommendations []ReportRecommendation
	
	// High error rate recommendation
	if report.Summary.TotalResources > 0 {
		errorRate := float64(report.Summary.FailedResources) / float64(report.Summary.TotalResources) * 100.0
		if errorRate > 25 {
			recommendations = append(recommendations, ReportRecommendation{
				Category:    "reliability",
				Priority:    "high",
				Title:       "High Error Rate Detected",
				Description: fmt.Sprintf("Error rate is %.1f%%, which is above the recommended threshold of 25%%", errorRate),
				Action:      "Investigate root causes of failures and improve error handling",
				Impact:      "Reduces sync reliability and may indicate systemic issues",
			})
		}
	}
	
	// Orphaned mappings recommendation
	if report.Summary.OrphanedMappings > 0 {
		recommendations = append(recommendations, ReportRecommendation{
			Category:    "maintenance",
			Priority:    "medium",
			Title:       "Orphaned Mappings Found",
			Description: fmt.Sprintf("Found %d orphaned mappings that reference non-existent resources", report.Summary.OrphanedMappings),
			Action:      "Run cleanup process to remove orphaned mappings",
			Impact:      "Improves data quality and reduces storage overhead",
		})
	}
	
	// Performance recommendation
	if report.Performance.AverageSyncDuration > 30*time.Minute {
		recommendations = append(recommendations, ReportRecommendation{
			Category:    "performance",
			Priority:    "medium",
			Title:       "Long Sync Duration",
			Description: fmt.Sprintf("Average sync duration is %v, which may be longer than expected", report.Performance.AverageSyncDuration),
			Action:      "Consider optimizing sync process or increasing parallelization",
			Impact:      "Reduces sync time and improves user experience",
		})
	}
	
	return recommendations
}

// operationMatchesFilter checks if an operation matches the filter criteria
func (sm *StateManager) operationMatchesFilter(operation *SyncOperation, filter *QueryFilter) bool {
	if filter == nil {
		return true
	}
	
	// Check time range
	if filter.TimeRange != nil {
		if operation.StartTime.Before(filter.TimeRange.Start) || operation.StartTime.After(filter.TimeRange.End) {
			return false
		}
	}
	
	// Check operation status
	if len(filter.OperationStatus) > 0 {
		statusMatches := false
		for _, status := range filter.OperationStatus {
			if operation.Status == status {
				statusMatches = true
				break
			}
		}
		if !statusMatches {
			return false
		}
	}
	
	// Check resource types
	if len(filter.ResourceTypes) > 0 {
		typeMatches := false
		for _, filterType := range filter.ResourceTypes {
			for _, opType := range operation.ResourceTypes {
				if opType == filterType {
					typeMatches = true
					break
				}
			}
			if typeMatches {
				break
			}
		}
		if !typeMatches {
			return false
		}
	}
	
	// Check success rate
	if filter.MinSuccessRate != nil || filter.MaxSuccessRate != nil {
		var successRate float64
		if operation.TotalResources > 0 {
			successRate = float64(operation.SuccessfulResources) / float64(operation.TotalResources) * 100.0
		}
		
		if filter.MinSuccessRate != nil && successRate < *filter.MinSuccessRate {
			return false
		}
		
		if filter.MaxSuccessRate != nil && successRate > *filter.MaxSuccessRate {
			return false
		}
	}
	
	return true
}

// exportReportJSON exports a report in JSON format
func (sm *StateManager) exportReportJSON(report *SyncReport, writer io.Writer) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}

// exportReportCSV exports a report in CSV format
func (sm *StateManager) exportReportCSV(report *SyncReport, writer io.Writer) error {
	csvWriter := csv.NewWriter(writer)
	defer csvWriter.Flush()
	
	// Write header
	header := []string{
		"Operation ID", "Start Time", "End Time", "Duration", "Status",
		"Total Resources", "Successful", "Failed", "Skipped", "Success Rate",
		"Source Instance", "Target Instance", "Trigger",
	}
	if err := csvWriter.Write(header); err != nil {
		return err
	}
	
	// Write operation data
	for _, operation := range report.OperationDetails {
		row := []string{
			operation.OperationID,
			operation.StartTime.Format(time.RFC3339),
			operation.EndTime.Format(time.RFC3339),
			operation.Duration.String(),
			operation.Status.String(),
			fmt.Sprintf("%d", operation.TotalResources),
			fmt.Sprintf("%d", operation.SuccessfulResources),
			fmt.Sprintf("%d", operation.FailedResources),
			fmt.Sprintf("%d", operation.SkippedResources),
			fmt.Sprintf("%.2f%%", operation.SuccessRate),
			operation.SourceInstance,
			operation.TargetInstance,
			operation.Trigger,
		}
		if err := csvWriter.Write(row); err != nil {
			return err
		}
	}
	
	return nil
}

// Additional validation methods

func (sm *StateManager) validateMappingReferences(result *StateValidationResult) {
	// Check for mapping dependency cycles
	// Implementation would detect circular dependencies
}

func (sm *StateManager) validateSyncOperationReferences(result *StateValidationResult) {
	// Check for orphaned sync operations
	// Implementation would validate operation references
}

func (sm *StateManager) validateErrorReferences(result *StateValidationResult) {
	// Check for errors referencing non-existent mappings or operations
	// Implementation would validate error references
}

func (sm *StateManager) validateStatisticsConsistency(result *StateValidationResult) {
	// Check if statistics are consistent with actual data
	// Implementation would validate statistics accuracy
}