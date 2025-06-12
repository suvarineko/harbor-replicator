package state

import (
	"fmt"
	"math"
	"time"

	"go.uber.org/zap"
)

// ErrorFilter defines criteria for filtering sync errors
type ErrorFilter struct {
	ResourceType     ResourceType  `json:"resource_type,omitempty"`
	ErrorType        ErrorType     `json:"error_type,omitempty"`
	ResourceID       string        `json:"resource_id,omitempty"`
	SyncID          string        `json:"sync_id,omitempty"`
	Resolved         *bool         `json:"resolved,omitempty"`
	OccurredAfter   *time.Time    `json:"occurred_after,omitempty"`
	OccurredBefore  *time.Time    `json:"occurred_before,omitempty"`
	RetryEligible   *bool         `json:"retry_eligible,omitempty"`
	MinRetryCount   *int          `json:"min_retry_count,omitempty"`
	MaxRetryCount   *int          `json:"max_retry_count,omitempty"`
}

// ErrorQueryResult contains the results of an error query
type ErrorQueryResult struct {
	Errors        []SyncError   `json:"errors"`
	TotalCount    int           `json:"total_count"`
	Page          int           `json:"page"`
	PageSize      int           `json:"page_size"`
	HasNext       bool          `json:"has_next"`
	FilterApplied ErrorFilter   `json:"filter_applied"`
}

// RetryConfiguration defines retry behavior settings
type RetryConfiguration struct {
	MaxRetries          int           `json:"max_retries"`
	BaseInterval        time.Duration `json:"base_interval"`
	MaxInterval         time.Duration `json:"max_interval"`
	ExponentialBackoff  bool          `json:"exponential_backoff"`
	BackoffMultiplier   float64       `json:"backoff_multiplier"`
	Jitter              bool          `json:"jitter"`
	RetryableErrorTypes []ErrorType   `json:"retryable_error_types"`
}

// ErrorAnalysisResult contains the results of error pattern analysis
type ErrorAnalysisResult struct {
	AnalyzedAt        time.Time                    `json:"analyzed_at"`
	TimeRange         ErrorTimeRange               `json:"time_range"`
	TotalErrors       int                          `json:"total_errors"`
	UnresolvedErrors  int                          `json:"unresolved_errors"`
	ErrorsByType      map[ErrorType]int            `json:"errors_by_type"`
	ErrorsByResource  map[ResourceType]int         `json:"errors_by_resource"`
	ErrorTrends       []ErrorTrend                 `json:"error_trends"`
	TopErrors         []ErrorFrequency             `json:"top_errors"`
	Recommendations   []string                     `json:"recommendations"`
	CriticalPatterns  []CriticalErrorPattern       `json:"critical_patterns"`
	RetrySuccessRate  float64                      `json:"retry_success_rate"`
}

// ErrorTimeRange defines a time range for error analysis
type ErrorTimeRange struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// ErrorTrend represents error trends over time
type ErrorTrend struct {
	Period      string    `json:"period"`
	ErrorCount  int       `json:"error_count"`
	Timestamp   time.Time `json:"timestamp"`
}

// ErrorFrequency represents error frequency data
type ErrorFrequency struct {
	ErrorMessage string `json:"error_message"`
	Count        int    `json:"count"`
	ErrorType    ErrorType `json:"error_type"`
	LastOccurred time.Time `json:"last_occurred"`
}

// CriticalErrorPattern represents patterns that require attention
type CriticalErrorPattern struct {
	Pattern     string    `json:"pattern"`
	Frequency   int       `json:"frequency"`
	Severity    string    `json:"severity"`
	FirstSeen   time.Time `json:"first_seen"`
	LastSeen    time.Time `json:"last_seen"`
	Description string    `json:"description"`
	Impact      string    `json:"impact"`
}

// DefaultRetryConfiguration returns a default retry configuration
func DefaultRetryConfiguration() RetryConfiguration {
	return RetryConfiguration{
		MaxRetries:          DefaultMaxRetries,
		BaseInterval:        DefaultRetryInterval,
		MaxInterval:         30 * time.Minute,
		ExponentialBackoff:  true,
		BackoffMultiplier:   2.0,
		Jitter:              true,
		RetryableErrorTypes: []ErrorType{
			ErrorTypeTransient,
			ErrorTypeNetworkError,
			ErrorTypeTimeout,
			ErrorTypeRateLimit,
		},
	}
}

// RecordError records a new synchronization error
func (sm *StateManager) RecordError(resourceType ResourceType, resourceID, mappingID, syncID, errorMessage string, errorType ErrorType, context map[string]interface{}) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	
	now := time.Now()
	errorID := sm.generateErrorID()
	
	syncError := SyncError{
		ID:           errorID,
		Timestamp:    now,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		MappingID:    mappingID,
		SyncID:       syncID,
		ErrorMessage: errorMessage,
		ErrorType:    errorType,
		RetryCount:   0,
		MaxRetries:   DefaultMaxRetries,
		Resolved:     false,
		Context:      context,
	}
	
	// Set next retry time for retryable errors
	if errorType.IsRetryable() {
		syncError.NextRetryAt = sm.calculateNextRetryTime(0, DefaultRetryConfiguration())
	}
	
	sm.state.SyncErrors = append(sm.state.SyncErrors, syncError)
	sm.state.UpdatedAt = now
	
	// Update statistics
	sm.state.Statistics.TotalErrors++
	
	sm.logger.Error("Recorded sync error",
		zap.String("error_id", errorID),
		zap.String("resource_type", resourceType.String()),
		zap.String("resource_id", resourceID),
		zap.String("error_type", errorType.String()),
		zap.String("error_message", errorMessage),
		zap.String("sync_id", syncID))
	
	return nil
}

// GetErrors retrieves errors based on filter criteria with pagination
func (sm *StateManager) GetErrors(filter ErrorFilter, page, pageSize int) (*ErrorQueryResult, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 50
	}
	
	// Apply filters
	var filteredErrors []SyncError
	for _, err := range sm.state.SyncErrors {
		if sm.errorMatchesFilter(&err, &filter) {
			filteredErrors = append(filteredErrors, err)
		}
	}
	
	totalCount := len(filteredErrors)
	
	// Apply pagination
	startIndex := (page - 1) * pageSize
	endIndex := startIndex + pageSize
	
	if startIndex >= totalCount {
		return &ErrorQueryResult{
			Errors:        []SyncError{},
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
	
	// Return errors in reverse order (newest first)
	paginatedErrors := make([]SyncError, 0, endIndex-startIndex)
	for i := totalCount - 1 - startIndex; i >= totalCount - endIndex; i-- {
		paginatedErrors = append(paginatedErrors, filteredErrors[i])
	}
	
	hasNext := endIndex < totalCount
	
	return &ErrorQueryResult{
		Errors:        paginatedErrors,
		TotalCount:    totalCount,
		Page:          page,
		PageSize:      pageSize,
		HasNext:       hasNext,
		FilterApplied: filter,
	}, nil
}

// GetErrorsByResource retrieves all errors for a specific resource
func (sm *StateManager) GetErrorsByResource(resourceType ResourceType, resourceID string) ([]SyncError, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	
	var errors []SyncError
	for _, err := range sm.state.SyncErrors {
		if err.ResourceType == resourceType && err.ResourceID == resourceID {
			errors = append(errors, err)
		}
	}
	
	return errors, nil
}

// ClearErrors removes resolved errors older than the specified duration
func (sm *StateManager) ClearErrors(olderThan time.Duration) (int, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	
	cutoff := time.Now().Add(-olderThan)
	var remainingErrors []SyncError
	removedCount := 0
	
	for _, err := range sm.state.SyncErrors {
		// Keep error if it's unresolved or recent
		if !err.Resolved || err.Timestamp.After(cutoff) {
			remainingErrors = append(remainingErrors, err)
		} else {
			removedCount++
		}
	}
	
	sm.state.SyncErrors = remainingErrors
	sm.state.UpdatedAt = time.Now()
	
	if removedCount > 0 {
		sm.logger.Info("Cleared old resolved errors",
			zap.Int("removed_count", removedCount),
			zap.Int("remaining_count", len(remainingErrors)))
	}
	
	return removedCount, nil
}

// ResolveError marks an error as resolved
func (sm *StateManager) ResolveError(errorID string, resolution string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	
	for i, err := range sm.state.SyncErrors {
		if err.ID == errorID {
			sm.state.SyncErrors[i].Resolved = true
			sm.state.SyncErrors[i].ResolvedAt = time.Now()
			if sm.state.SyncErrors[i].Context == nil {
				sm.state.SyncErrors[i].Context = make(map[string]interface{})
			}
			sm.state.SyncErrors[i].Context["resolution"] = resolution
			sm.state.UpdatedAt = time.Now()
			
			sm.logger.Info("Resolved error",
				zap.String("error_id", errorID),
				zap.String("resolution", resolution))
			
			return nil
		}
	}
	
	return fmt.Errorf("error not found: %s", errorID)
}

// GetRetryableErrors returns errors that are eligible for retry
func (sm *StateManager) GetRetryableErrors() ([]SyncError, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	
	now := time.Now()
	var retryableErrors []SyncError
	
	for _, err := range sm.state.SyncErrors {
		if sm.isErrorRetryable(&err, now) {
			retryableErrors = append(retryableErrors, err)
		}
	}
	
	return retryableErrors, nil
}

// RetryError attempts to retry a failed operation
func (sm *StateManager) RetryError(errorID string, config ...RetryConfiguration) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	
	retryConfig := DefaultRetryConfiguration()
	if len(config) > 0 {
		retryConfig = config[0]
	}
	
	// Find the error
	errorIndex := -1
	for i, err := range sm.state.SyncErrors {
		if err.ID == errorID {
			errorIndex = i
			break
		}
	}
	
	if errorIndex == -1 {
		return fmt.Errorf("error not found: %s", errorID)
	}
	
	err := sm.state.SyncErrors[errorIndex]
	
	// Check if error is retryable
	if !sm.isErrorRetryable(&err, time.Now()) {
		return fmt.Errorf("error is not retryable: %s", errorID)
	}
	
	// Increment retry count
	err.RetryCount++
	err.NextRetryAt = sm.calculateNextRetryTime(err.RetryCount, retryConfig)
	
	// Check if max retries exceeded
	if err.RetryCount >= retryConfig.MaxRetries {
		err.NextRetryAt = time.Time{} // Clear next retry time
		if err.Context == nil {
			err.Context = make(map[string]interface{})
		}
		err.Context["max_retries_exceeded"] = true
	}
	
	sm.state.SyncErrors[errorIndex] = err
	sm.state.UpdatedAt = time.Now()
	
	sm.logger.Info("Retrying error",
		zap.String("error_id", errorID),
		zap.Int("retry_count", err.RetryCount),
		zap.Time("next_retry", err.NextRetryAt))
	
	return nil
}

// AnalyzeErrorPatterns analyzes error patterns and provides insights
func (sm *StateManager) AnalyzeErrorPatterns(timeRange ErrorTimeRange) (*ErrorAnalysisResult, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	
	now := time.Now()
	
	// If no time range specified, use last 24 hours
	if timeRange.Start.IsZero() {
		timeRange.Start = now.Add(-24 * time.Hour)
	}
	if timeRange.End.IsZero() {
		timeRange.End = now
	}
	
	// Filter errors by time range
	var relevantErrors []SyncError
	for _, err := range sm.state.SyncErrors {
		if err.Timestamp.After(timeRange.Start) && err.Timestamp.Before(timeRange.End) {
			relevantErrors = append(relevantErrors, err)
		}
	}
	
	result := &ErrorAnalysisResult{
		AnalyzedAt:       now,
		TimeRange:        timeRange,
		TotalErrors:      len(relevantErrors),
		ErrorsByType:     make(map[ErrorType]int),
		ErrorsByResource: make(map[ResourceType]int),
		Recommendations:  make([]string, 0),
	}
	
	// Count unresolved errors
	unresolvedCount := 0
	errorMessages := make(map[string]int)
	retryStats := struct{ attempts, successes int }{}
	
	for _, err := range relevantErrors {
		if !err.Resolved {
			unresolvedCount++
		}
		
		result.ErrorsByType[err.ErrorType]++
		result.ErrorsByResource[err.ResourceType]++
		errorMessages[err.ErrorMessage]++
		
		// Track retry statistics
		if err.RetryCount > 0 {
			retryStats.attempts++
			if err.Resolved {
				retryStats.successes++
			}
		}
	}
	
	result.UnresolvedErrors = unresolvedCount
	
	// Calculate retry success rate
	if retryStats.attempts > 0 {
		result.RetrySuccessRate = float64(retryStats.successes) / float64(retryStats.attempts) * 100.0
	}
	
	// Generate top errors
	result.TopErrors = sm.generateTopErrors(errorMessages, relevantErrors)
	
	// Generate error trends
	result.ErrorTrends = sm.generateErrorTrends(relevantErrors, timeRange)
	
	// Detect critical patterns
	result.CriticalPatterns = sm.detectCriticalPatterns(relevantErrors)
	
	// Generate recommendations
	result.Recommendations = sm.generateErrorRecommendations(result)
	
	return result, nil
}

// AutoRetryErrors automatically retries eligible errors
func (sm *StateManager) AutoRetryErrors(config ...RetryConfiguration) (int, error) {
	retryableErrors, err := sm.GetRetryableErrors()
	if err != nil {
		return 0, err
	}
	
	retryConfig := DefaultRetryConfiguration()
	if len(config) > 0 {
		retryConfig = config[0]
	}
	
	successCount := 0
	for _, err := range retryableErrors {
		if retryErr := sm.RetryError(err.ID, retryConfig); retryErr != nil {
			sm.logger.Warn("Failed to retry error",
				zap.String("error_id", err.ID),
				zap.Error(retryErr))
		} else {
			successCount++
		}
	}
	
	sm.logger.Info("Auto-retry completed",
		zap.Int("eligible_errors", len(retryableErrors)),
		zap.Int("successfully_retried", successCount))
	
	return successCount, nil
}

// CleanupResolvedErrors removes resolved errors older than the retention period
func (sm *StateManager) CleanupResolvedErrors() (int, error) {
	return sm.ClearErrors(DefaultErrorRetention)
}

// GetErrorStatistics returns aggregated error statistics
func (sm *StateManager) GetErrorStatistics() map[string]interface{} {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	
	stats := make(map[string]interface{})
	
	totalErrors := len(sm.state.SyncErrors)
	unresolvedErrors := 0
	errorsByType := make(map[ErrorType]int)
	errorsByResource := make(map[ResourceType]int)
	
	for _, err := range sm.state.SyncErrors {
		if !err.Resolved {
			unresolvedErrors++
		}
		errorsByType[err.ErrorType]++
		errorsByResource[err.ResourceType]++
	}
	
	stats["total_errors"] = totalErrors
	stats["unresolved_errors"] = unresolvedErrors
	stats["resolved_errors"] = totalErrors - unresolvedErrors
	stats["errors_by_type"] = errorsByType
	stats["errors_by_resource"] = errorsByResource
	
	if totalErrors > 0 {
		stats["resolution_rate"] = float64(totalErrors-unresolvedErrors) / float64(totalErrors) * 100.0
	} else {
		stats["resolution_rate"] = 100.0
	}
	
	return stats
}

// Helper methods

// errorMatchesFilter checks if an error matches the given filter
func (sm *StateManager) errorMatchesFilter(err *SyncError, filter *ErrorFilter) bool {
	if filter.ResourceType != "" && err.ResourceType != filter.ResourceType {
		return false
	}
	
	if filter.ErrorType != "" && err.ErrorType != filter.ErrorType {
		return false
	}
	
	if filter.ResourceID != "" && err.ResourceID != filter.ResourceID {
		return false
	}
	
	if filter.SyncID != "" && err.SyncID != filter.SyncID {
		return false
	}
	
	if filter.Resolved != nil && err.Resolved != *filter.Resolved {
		return false
	}
	
	if filter.OccurredAfter != nil && err.Timestamp.Before(*filter.OccurredAfter) {
		return false
	}
	
	if filter.OccurredBefore != nil && err.Timestamp.After(*filter.OccurredBefore) {
		return false
	}
	
	if filter.RetryEligible != nil {
		isRetryable := sm.isErrorRetryable(err, time.Now())
		if *filter.RetryEligible != isRetryable {
			return false
		}
	}
	
	if filter.MinRetryCount != nil && err.RetryCount < *filter.MinRetryCount {
		return false
	}
	
	if filter.MaxRetryCount != nil && err.RetryCount > *filter.MaxRetryCount {
		return false
	}
	
	return true
}

// isErrorRetryable checks if an error is eligible for retry
func (sm *StateManager) isErrorRetryable(err *SyncError, now time.Time) bool {
	// Must be an unresolved retryable error type
	if err.Resolved || !err.ErrorType.IsRetryable() {
		return false
	}
	
	// Must not exceed max retries
	if err.RetryCount >= err.MaxRetries {
		return false
	}
	
	// Must be past the next retry time
	if !err.NextRetryAt.IsZero() && now.Before(err.NextRetryAt) {
		return false
	}
	
	return true
}

// calculateNextRetryTime calculates the next retry time using exponential backoff
func (sm *StateManager) calculateNextRetryTime(retryCount int, config RetryConfiguration) time.Time {
	if !config.ExponentialBackoff {
		return time.Now().Add(config.BaseInterval)
	}
	
	// Calculate exponential backoff
	delay := config.BaseInterval
	for i := 0; i < retryCount; i++ {
		delay = time.Duration(float64(delay) * config.BackoffMultiplier)
		if delay > config.MaxInterval {
			delay = config.MaxInterval
			break
		}
	}
	
	// Add jitter if enabled
	if config.Jitter {
		jitterPercent := 0.1 // 10% jitter
		jitter := time.Duration(float64(delay) * jitterPercent * (2*math.Sqrt(float64(time.Now().UnixNano()%1000))/1000 - 1))
		delay += jitter
	}
	
	return time.Now().Add(delay)
}

// generateTopErrors creates a list of the most frequent errors
func (sm *StateManager) generateTopErrors(errorMessages map[string]int, errors []SyncError) []ErrorFrequency {
	// Create frequency map with additional details
	errorDetails := make(map[string]ErrorFrequency)
	
	for _, err := range errors {
		if freq, exists := errorDetails[err.ErrorMessage]; exists {
			if err.Timestamp.After(freq.LastOccurred) {
				freq.LastOccurred = err.Timestamp
			}
			errorDetails[err.ErrorMessage] = freq
		} else {
			errorDetails[err.ErrorMessage] = ErrorFrequency{
				ErrorMessage: err.ErrorMessage,
				Count:        errorMessages[err.ErrorMessage],
				ErrorType:    err.ErrorType,
				LastOccurred: err.Timestamp,
			}
		}
	}
	
	// Convert to slice and sort by count (top 10)
	var topErrors []ErrorFrequency
	for _, freq := range errorDetails {
		topErrors = append(topErrors, freq)
	}
	
	// Simple bubble sort for top 10
	for i := 0; i < len(topErrors)-1; i++ {
		for j := 0; j < len(topErrors)-1-i; j++ {
			if topErrors[j].Count < topErrors[j+1].Count {
				topErrors[j], topErrors[j+1] = topErrors[j+1], topErrors[j]
			}
		}
	}
	
	// Return top 10
	if len(topErrors) > 10 {
		topErrors = topErrors[:10]
	}
	
	return topErrors
}

// generateErrorTrends creates error trend data over time
func (sm *StateManager) generateErrorTrends(errors []SyncError, timeRange ErrorTimeRange) []ErrorTrend {
	// Create hourly buckets
	duration := timeRange.End.Sub(timeRange.Start)
	bucketSize := time.Hour
	if duration > 24*time.Hour {
		bucketSize = 24 * time.Hour
	}
	
	buckets := make(map[time.Time]int)
	
	for _, err := range errors {
		bucket := err.Timestamp.Truncate(bucketSize)
		buckets[bucket]++
	}
	
	var trends []ErrorTrend
	for timestamp, count := range buckets {
		period := "hour"
		if bucketSize == 24*time.Hour {
			period = "day"
		}
		
		trends = append(trends, ErrorTrend{
			Period:     period,
			ErrorCount: count,
			Timestamp:  timestamp,
		})
	}
	
	return trends
}

// detectCriticalPatterns identifies critical error patterns that need attention
func (sm *StateManager) detectCriticalPatterns(errors []SyncError) []CriticalErrorPattern {
	var patterns []CriticalErrorPattern
	
	// Pattern 1: High frequency of same error type
	errorTypeCounts := make(map[ErrorType]int)
	for _, err := range errors {
		errorTypeCounts[err.ErrorType]++
	}
	
	for errorType, count := range errorTypeCounts {
		if count > 10 { // Threshold for high frequency
			patterns = append(patterns, CriticalErrorPattern{
				Pattern:     fmt.Sprintf("High frequency %s errors", errorType),
				Frequency:   count,
				Severity:    "high",
				Description: fmt.Sprintf("More than 10 %s errors detected", errorType),
				Impact:      "May indicate systemic issues requiring investigation",
			})
		}
	}
	
	// Pattern 2: Rapid error escalation
	if len(errors) > 5 {
		recentErrors := errors[len(errors)-5:]
		recentTime := time.Now().Add(-time.Hour)
		recentCount := 0
		
		for _, err := range recentErrors {
			if err.Timestamp.After(recentTime) {
				recentCount++
			}
		}
		
		if recentCount >= 5 {
			patterns = append(patterns, CriticalErrorPattern{
				Pattern:     "Rapid error escalation",
				Frequency:   recentCount,
				Severity:    "critical",
				Description: "5 or more errors in the last hour",
				Impact:      "System may be experiencing cascading failures",
			})
		}
	}
	
	return patterns
}

// generateErrorRecommendations creates recommendations based on error analysis
func (sm *StateManager) generateErrorRecommendations(analysis *ErrorAnalysisResult) []string {
	var recommendations []string
	
	// High error rate recommendation
	if analysis.TotalErrors > 0 {
		errorRate := float64(analysis.UnresolvedErrors) / float64(analysis.TotalErrors) * 100.0
		if errorRate > 50 {
			recommendations = append(recommendations, "High unresolved error rate detected. Consider investigating root causes.")
		}
	}
	
	// Network error recommendation
	if networkErrors, exists := analysis.ErrorsByType[ErrorTypeNetworkError]; exists && networkErrors > 5 {
		recommendations = append(recommendations, "Multiple network errors detected. Check network connectivity and Harbor instance availability.")
	}
	
	// Rate limit recommendation
	if rateLimitErrors, exists := analysis.ErrorsByType[ErrorTypeRateLimit]; exists && rateLimitErrors > 0 {
		recommendations = append(recommendations, "Rate limit errors detected. Consider reducing sync frequency or implementing better rate limiting.")
	}
	
	// Permission error recommendation
	if permissionErrors, exists := analysis.ErrorsByType[ErrorTypePermission]; exists && permissionErrors > 0 {
		recommendations = append(recommendations, "Permission errors detected. Verify Harbor credentials and access permissions.")
	}
	
	// Low retry success rate
	if analysis.RetrySuccessRate < 50 && analysis.RetrySuccessRate > 0 {
		recommendations = append(recommendations, "Low retry success rate. Consider adjusting retry configuration or investigating persistent issues.")
	}
	
	// Critical patterns
	for _, pattern := range analysis.CriticalPatterns {
		if pattern.Severity == "critical" {
			recommendations = append(recommendations, fmt.Sprintf("Critical pattern detected: %s", pattern.Description))
		}
	}
	
	return recommendations
}