package sync

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"go.uber.org/zap"

	"harbor-replicator/pkg/client"
	"harbor-replicator/pkg/state"
)

// RobotSynchronizerImpl implements the RobotSynchronizer interface
type RobotSynchronizerImpl struct {
	// Dependencies
	localClient     client.ClientInterface
	remoteClients   map[string]client.ClientInterface
	stateManager    *state.StateManager
	secretManager   SecretManager
	comparator      RobotComparator
	resolver        ConflictResolver
	logger          *zap.Logger
	
	// Configuration
	config          *SynchronizerConfig
	
	// State
	mu              sync.RWMutex
	activeSync      map[string]*RobotSyncReport
	syncHistory     []*RobotSyncReport
	lastSyncTime    time.Time
}

// SynchronizerConfig holds configuration for the robot synchronizer
type SynchronizerConfig struct {
	// Default sync options
	DefaultOptions   *RobotSyncOptions `json:"default_options"`
	
	// Performance settings
	MaxConcurrency   int               `json:"max_concurrency"`
	BatchSize        int               `json:"batch_size"`
	Timeout          time.Duration     `json:"timeout"`
	
	// Retry settings
	MaxRetries       int               `json:"max_retries"`
	RetryInterval    time.Duration     `json:"retry_interval"`
	BackoffMultiplier float64          `json:"backoff_multiplier"`
	
	// State management
	StateSync        bool              `json:"state_sync"`
	StateSyncInterval time.Duration    `json:"state_sync_interval"`
	
	// History and cleanup
	MaxHistorySize   int               `json:"max_history_size"`
	CleanupInterval  time.Duration     `json:"cleanup_interval"`
	
	// Monitoring
	MetricsEnabled   bool              `json:"metrics_enabled"`
	DetailedLogging  bool              `json:"detailed_logging"`
}

// NewRobotSynchronizer creates a new RobotSynchronizer instance
func NewRobotSynchronizer(
	localClient client.ClientInterface,
	remoteClients map[string]client.ClientInterface,
	stateManager *state.StateManager,
	secretManager SecretManager,
	logger *zap.Logger,
	config *SynchronizerConfig,
) (*RobotSynchronizerImpl, error) {
	
	if localClient == nil {
		return nil, fmt.Errorf("local client cannot be nil")
	}
	
	if stateManager == nil {
		return nil, fmt.Errorf("state manager cannot be nil")
	}
	
	if logger == nil {
		logger = zap.NewNop()
	}
	
	if config == nil {
		config = DefaultSynchronizerConfig()
	}
	
	if secretManager == nil {
		var err error
		secretManager, err = NewSecretManager(stateManager, logger, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create secret manager: %w", err)
		}
	}
	
	// Create comparator and resolver
	comparator := NewRobotComparator(logger, nil)
	resolver := NewConflictResolver(logger, nil)
	
	rs := &RobotSynchronizerImpl{
		localClient:   localClient,
		remoteClients: remoteClients,
		stateManager:  stateManager,
		secretManager: secretManager,
		comparator:    comparator,
		resolver:      resolver,
		logger:        logger,
		config:        config,
		activeSync:    make(map[string]*RobotSyncReport),
		syncHistory:   make([]*RobotSyncReport, 0),
	}
	
	// Start background tasks
	if config.StateSync {
		go rs.startStateSyncScheduler()
	}
	
	if config.CleanupInterval > 0 {
		go rs.startCleanupScheduler()
	}
	
	logger.Info("Robot synchronizer initialized", 
		zap.Int("remote_clients", len(remoteClients)),
		zap.Int("max_concurrency", config.MaxConcurrency),
		zap.Int("batch_size", config.BatchSize))
	
	return rs, nil
}

// DefaultSynchronizerConfig returns a default synchronizer configuration
func DefaultSynchronizerConfig() *SynchronizerConfig {
	return &SynchronizerConfig{
		DefaultOptions: &RobotSyncOptions{
			DryRun:           false,
			ConflictStrategy: ConflictStrategySourceWins,
			CreateMissing:    true,
			UpdateExisting:   true,
			DeleteOrphaned:   false,
			BatchSize:        50,
			MaxConcurrency:   5,
			Timeout:          5 * time.Minute,
			StopOnError:      false,
			MaxRetries:       3,
			RetryInterval:    time.Second,
			RotateSecrets:    false,
			PreserveSecrets:  true,
			GenerateReport:   true,
			ReportFormat:     "json",
		},
		MaxConcurrency:    10,
		BatchSize:         100,
		Timeout:           10 * time.Minute,
		MaxRetries:        3,
		RetryInterval:     time.Second,
		BackoffMultiplier: 2.0,
		StateSync:         true,
		StateSyncInterval: 30 * time.Second,
		MaxHistorySize:    100,
		CleanupInterval:   time.Hour,
		MetricsEnabled:    true,
		DetailedLogging:   false,
	}
}

// SyncSystemRobots synchronizes system-level robot accounts
func (rs *RobotSynchronizerImpl) SyncSystemRobots(ctx context.Context, options *RobotSyncOptions) (*RobotSyncReport, error) {
	if options == nil {
		options = rs.config.DefaultOptions
	}
	
	// Generate sync ID
	syncID := rs.generateSyncID("system", options.SourceInstance)
	
	// Initialize sync report
	report := &RobotSyncReport{
		SyncID:         syncID,
		SourceInstance: options.SourceInstance,
		TargetInstance: options.TargetInstance,
		StartTime:      time.Now(),
		Options:        options,
		Results:        make([]RobotSyncResult, 0),
		Conflicts:      make([]RobotConflict, 0),
		Errors:         make([]RobotSyncError, 0),
	}
	
	// Track active sync
	rs.mu.Lock()
	rs.activeSync[syncID] = report
	rs.mu.Unlock()
	
	defer func() {
		rs.mu.Lock()
		delete(rs.activeSync, syncID)
		rs.addToHistory(report)
		rs.mu.Unlock()
	}()
	
	rs.logger.Info("Starting system robot synchronization", 
		zap.String("sync_id", syncID),
		zap.String("source", options.SourceInstance),
		zap.String("target", options.TargetInstance),
		zap.Bool("dry_run", options.DryRun))
	
	// Start sync operation in state manager
	resourceTypes := []state.ResourceType{"robot_account"}
	syncOpID, err := rs.stateManager.StartSync(resourceTypes, options.SourceInstance, options.TargetInstance, "manual")
	if err != nil {
		return nil, fmt.Errorf("failed to start sync operation: %w", err)
	}
	
	defer func() {
		if err != nil {
			rs.stateManager.FailSyncForResource(syncOpID, "system_robots", state.ResourceType("robot_account"), state.ErrorTypeTransient, err.Error())
		} else {
			rs.stateManager.CompleteSyncForResource(syncOpID, "system_robots", state.ResourceType("robot_account"))
		}
	}()
	
	// Get remote client
	remoteClient, exists := rs.remoteClients[options.SourceInstance]
	if !exists {
		err = fmt.Errorf("remote client not found for instance: %s", options.SourceInstance)
		return report, err
	}
	
	// Fetch remote robots
	rs.logger.Debug("Fetching remote system robots", zap.String("source", options.SourceInstance))
	remoteRobots, err := remoteClient.ListSystemRobotAccounts(ctx)
	if err != nil {
		err = fmt.Errorf("failed to list remote system robots: %w", err)
		return report, err
	}
	
	// Fetch local robots
	rs.logger.Debug("Fetching local system robots")
	localRobots, err := rs.localClient.ListSystemRobotAccounts(ctx)
	if err != nil {
		err = fmt.Errorf("failed to list local system robots: %w", err)
		return report, err
	}
	
	// Apply filters if specified
	filteredRemoteRobots := rs.applyFilters(remoteRobots, options.Filter)
	
	report.TotalRobots = len(filteredRemoteRobots)
	
	// Create name-based index of local robots
	localIndex := make(map[string]*client.RobotAccount)
	for _, robot := range localRobots {
		localIndex[robot.Name] = &robot
	}
	
	// Process robots in batches
	batchSize := options.BatchSize
	if batchSize <= 0 {
		batchSize = rs.config.BatchSize
	}
	
	for i := 0; i < len(filteredRemoteRobots); i += batchSize {
		end := i + batchSize
		if end > len(filteredRemoteRobots) {
			end = len(filteredRemoteRobots)
		}
		
		batch := filteredRemoteRobots[i:end]
		batchResults := rs.processBatch(ctx, batch, localIndex, options)
		
		// Add batch results to report
		for _, result := range batchResults {
			report.Results = append(report.Results, result)
			report.ProcessedRobots++
			
			if result.Success {
				report.SuccessfulRobots++
				switch result.Operation {
				case RobotSyncOperationCreate:
					report.CreatedRobots++
				case RobotSyncOperationUpdate:
					report.UpdatedRobots++
				case RobotSyncOperationDelete:
					report.DeletedRobots++
				}
			} else {
				report.FailedRobots++
				
				// Record error
				robotError := RobotSyncError{
					RobotName:     result.Robot.Name,
					RobotID:       result.Robot.ID,
					Operation:     result.Operation,
					ErrorType:     "sync_error",
					ErrorMessage:  result.Error,
					Timestamp:     result.Timestamp,
					Retryable:     true,
				}
				report.Errors = append(report.Errors, robotError)
			}
			
			if result.ConflictInfo != nil {
				report.Conflicts = append(report.Conflicts, *result.ConflictInfo)
				report.ConflictRobots++
			}
			
			if result.Operation == RobotSyncOperationSkip {
				report.SkippedRobots++
			}
		}
		
		// Update sync progress
		progress := map[string]interface{}{
			"percentage": float64(report.ProcessedRobots) / float64(report.TotalRobots) * 100,
			"processed": report.ProcessedRobots,
			"total": report.TotalRobots,
		}
		rs.stateManager.UpdateSyncProgress(syncID, progress)
		
		// Check for context cancellation
		select {
		case <-ctx.Done():
			err = ctx.Err()
			rs.logger.Warn("Sync cancelled", zap.String("sync_id", syncID), zap.Int("processed", report.ProcessedRobots))
			return report, err
		default:
		}
		
		// Stop on error if configured
		if options.StopOnError && report.FailedRobots > 0 {
			break
		}
	}
	
	// Finalize report
	report.EndTime = time.Now()
	report.Duration = report.EndTime.Sub(report.StartTime)
	
	if report.ProcessedRobots > 0 {
		report.AverageLatency = report.Duration / time.Duration(report.ProcessedRobots)
		report.Throughput = float64(report.ProcessedRobots) / report.Duration.Seconds()
	}
	
	rs.logger.Info("System robot synchronization completed",
		zap.String("sync_id", syncID),
		zap.Int("total", report.TotalRobots),
		zap.Int("processed", report.ProcessedRobots),
		zap.Int("successful", report.SuccessfulRobots),
		zap.Int("failed", report.FailedRobots),
		zap.Int("conflicts", report.ConflictRobots),
		zap.Duration("duration", report.Duration))
	
	return report, nil
}

// SyncProjectRobots synchronizes project-specific robot accounts
func (rs *RobotSynchronizerImpl) SyncProjectRobots(ctx context.Context, projectID int64, options *RobotSyncOptions) (*RobotSyncReport, error) {
	if options == nil {
		options = rs.config.DefaultOptions
	}
	
	// Generate sync ID
	syncID := rs.generateSyncID(fmt.Sprintf("project_%d", projectID), options.SourceInstance)
	
	// Initialize sync report
	report := &RobotSyncReport{
		SyncID:         syncID,
		SourceInstance: options.SourceInstance,
		TargetInstance: options.TargetInstance,
		StartTime:      time.Now(),
		Options:        options,
		Results:        make([]RobotSyncResult, 0),
		Conflicts:      make([]RobotConflict, 0),
		Errors:         make([]RobotSyncError, 0),
	}
	
	// Track active sync
	rs.mu.Lock()
	rs.activeSync[syncID] = report
	rs.mu.Unlock()
	
	defer func() {
		rs.mu.Lock()
		delete(rs.activeSync, syncID)
		rs.addToHistory(report)
		rs.mu.Unlock()
	}()
	
	rs.logger.Info("Starting project robot synchronization", 
		zap.String("sync_id", syncID),
		zap.Int64("project_id", projectID),
		zap.String("source", options.SourceInstance),
		zap.String("target", options.TargetInstance),
		zap.Bool("dry_run", options.DryRun))
	
	// Get remote client
	remoteClient, exists := rs.remoteClients[options.SourceInstance]
	if !exists {
		return report, fmt.Errorf("remote client not found for instance: %s", options.SourceInstance)
	}
	
	// Validate that the target project exists locally
	targetProjectID := projectID
	if options.ProjectMappings != nil {
		if mappedID, exists := options.ProjectMappings[projectID]; exists {
			targetProjectID = mappedID
		}
	}
	
	// Check if target project exists
	projectExists, err := rs.validateProjectExists(ctx, targetProjectID)
	if err != nil {
		return report, fmt.Errorf("failed to validate project existence: %w", err)
	}
	
	if !projectExists {
		if options.CreateMissingProjects {
			if !options.DryRun {
				if err := rs.createMissingProject(ctx, projectID, targetProjectID, options); err != nil {
					return report, fmt.Errorf("failed to create missing project: %w", err)
				}
			}
			rs.logger.Info("Created missing project", 
				zap.Int64("source_project_id", projectID),
				zap.Int64("target_project_id", targetProjectID),
				zap.Bool("dry_run", options.DryRun))
		} else if options.SkipMissingProjects {
			report.EndTime = time.Now()
			report.Duration = report.EndTime.Sub(report.StartTime)
			rs.logger.Info("Skipping project robots due to missing target project", 
				zap.Int64("project_id", targetProjectID))
			return report, nil
		} else {
			return report, fmt.Errorf("target project %d does not exist and create_missing_projects is false", targetProjectID)
		}
	}
	
	// Fetch remote project robots
	remoteRobots, err := remoteClient.ListProjectRobotAccounts(ctx, projectID)
	if err != nil {
		return report, fmt.Errorf("failed to list remote project robots: %w", err)
	}
	
	// Fetch local project robots using target project ID
	localRobots, err := rs.localClient.ListProjectRobotAccounts(ctx, targetProjectID)
	if err != nil {
		return report, fmt.Errorf("failed to list local project robots: %w", err)
	}
	
	// Apply filters
	filteredRemoteRobots := rs.applyFilters(remoteRobots, options.Filter)
	report.TotalRobots = len(filteredRemoteRobots)
	
	// Create name-based index of local robots
	localIndex := make(map[string]*client.RobotAccount)
	for _, robot := range localRobots {
		localIndex[robot.Name] = &robot
	}
	
	// Process robots with project context
	for _, remoteRobot := range filteredRemoteRobots {
		result := rs.processSingleRobotWithProject(ctx, &remoteRobot, localIndex[remoteRobot.Name], targetProjectID, options)
		
		report.Results = append(report.Results, result)
		report.ProcessedRobots++
		
		if result.Success {
			report.SuccessfulRobots++
		} else {
			report.FailedRobots++
		}
		
		if result.ConflictInfo != nil {
			report.Conflicts = append(report.Conflicts, *result.ConflictInfo)
			report.ConflictRobots++
		}
	}
	
	// Finalize report
	report.EndTime = time.Now()
	report.Duration = report.EndTime.Sub(report.StartTime)
	
	return report, nil
}

// SyncRobot synchronizes a single robot account
func (rs *RobotSynchronizerImpl) SyncRobot(ctx context.Context, robotName string, options *RobotSyncOptions) (*RobotSyncResult, error) {
	if options == nil {
		options = rs.config.DefaultOptions
	}
	
	rs.logger.Info("Synchronizing single robot", 
		zap.String("robot_name", robotName),
		zap.String("source", options.SourceInstance),
		zap.Bool("dry_run", options.DryRun))
	
	// Get remote client
	remoteClient, exists := rs.remoteClients[options.SourceInstance]
	if !exists {
		return nil, fmt.Errorf("remote client not found for instance: %s", options.SourceInstance)
	}
	
	// Search for remote robot by listing and filtering
	allRemoteRobots, err := remoteClient.ListSystemRobotAccounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list remote robots: %w", err)
	}
	
	var remoteRobot *client.RobotAccount
	for _, robot := range allRemoteRobots {
		if robot.Name == robotName {
			remoteRobot = &robot
			break
		}
	}
	
	if remoteRobot == nil {
		return nil, fmt.Errorf("robot not found on remote instance: %s", robotName)
	}
	
	// Search for local robot by listing and filtering
	allLocalRobots, err := rs.localClient.ListSystemRobotAccounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list local robots: %w", err)
	}
	
	var localRobot *client.RobotAccount
	for _, robot := range allLocalRobots {
		if robot.Name == robotName {
			localRobot = &robot
			break
		}
	}
	
	// Process the single robot
	result := rs.processSingleRobot(ctx, remoteRobot, localRobot, options)
	return &result, nil
}

// CompareRobots compares two robot accounts
func (rs *RobotSynchronizerImpl) CompareRobots(source, target *client.RobotAccount) (*RobotConflict, error) {
	return rs.comparator.Compare(source, target)
}

// ResolveConflict resolves a robot conflict
func (rs *RobotSynchronizerImpl) ResolveConflict(ctx context.Context, conflict *RobotConflict, strategy ConflictStrategy) (*RobotSyncResult, error) {
	startTime := time.Now()
	
	resolvedRobot, err := rs.resolver.ResolveConflict(ctx, conflict, strategy)
	if err != nil {
		return &RobotSyncResult{
			Operation:        RobotSyncOperationConflict,
			Success:          false,
			Error:            err.Error(),
			ConflictResolved: false,
			ConflictInfo:     conflict,
			Duration:         time.Since(startTime),
			Timestamp:        time.Now(),
		}, err
	}
	
	return &RobotSyncResult{
		Operation:        RobotSyncOperationUpdate,
		Robot:            &Robot{RobotAccount: resolvedRobot},
		Success:          true,
		ConflictResolved: true,
		ConflictInfo:     conflict,
		Duration:         time.Since(startTime),
		Timestamp:        time.Now(),
	}, nil
}

// ListConflicts lists robot conflicts based on filter criteria
func (rs *RobotSynchronizerImpl) ListConflicts(ctx context.Context, filter *RobotFilter) ([]RobotConflict, error) {
	// This would typically query stored conflicts from state manager
	// For now, return empty list
	return []RobotConflict{}, nil
}

// ApplyFilter applies filters to a list of robots
func (rs *RobotSynchronizerImpl) ApplyFilter(robots []*Robot, filter *RobotFilter) (*RobotFilterResult, error) {
	startTime := time.Now()
	
	if filter == nil {
		return &RobotFilterResult{
			Robots:        robots,
			TotalCount:    len(robots),
			FilteredCount: len(robots),
			Filters:       filter,
			ExecutionTime: time.Since(startTime),
		}, nil
	}
	
	// Apply filtering logic with detailed tracking
	filtered := rs.filterRobots(robots, filter)
	executionTime := time.Since(startTime)
	
	// Generate filtering details
	details := map[string]interface{}{
		"original_count": len(robots),
		"filtered_count": len(filtered),
		"excluded_count": len(robots) - len(filtered),
		"filter_summary": rs.generateFilterSummary(filter),
	}
	
	return &RobotFilterResult{
		Robots:        filtered,
		TotalCount:    len(robots),
		FilteredCount: len(filtered),
		Filters:       filter,
		ExecutionTime: executionTime,
		Details:       details,
	}, nil
}

// SearchRobots searches for robots on a specific instance
func (rs *RobotSynchronizerImpl) SearchRobots(ctx context.Context, instance string, query string) ([]*Robot, error) {
	var harborClient client.ClientInterface
	
	if instance == "local" || instance == "" {
		harborClient = rs.localClient
	} else {
		var exists bool
		harborClient, exists = rs.remoteClients[instance]
		if !exists {
			return nil, fmt.Errorf("client not found for instance: %s", instance)
		}
	}
	
	// Search by listing all robots and filtering by name
	systemRobots, err := harborClient.ListSystemRobotAccounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list system robots: %w", err)
	}
	
	robots := make([]client.RobotAccount, 0)
	for _, robot := range systemRobots {
		if strings.Contains(robot.Name, query) {
			robots = append(robots, robot)
		}
	}
	
	// Convert to Robot type
	result := make([]*Robot, len(robots))
	for i, robot := range robots {
		result[i] = &Robot{
			RobotAccount:   &robot,
			SourceInstance: instance,
		}
	}
	
	return result, nil
}

// ValidateRobotSync validates sync options
func (rs *RobotSynchronizerImpl) ValidateRobotSync(ctx context.Context, options *RobotSyncOptions) ([]string, error) {
	warnings := make([]string, 0)
	
	if options == nil {
		return []string{"sync options are nil"}, nil
	}
	
	if options.SourceInstance == "" {
		warnings = append(warnings, "source instance is not specified")
	}
	
	if _, exists := rs.remoteClients[options.SourceInstance]; !exists {
		warnings = append(warnings, fmt.Sprintf("remote client not found for instance: %s", options.SourceInstance))
	}
	
	if options.ConflictStrategy == "" {
		warnings = append(warnings, "conflict strategy is not specified")
	}
	
	if options.BatchSize <= 0 {
		warnings = append(warnings, "batch size should be positive")
	}
	
	if options.MaxConcurrency <= 0 {
		warnings = append(warnings, "max concurrency should be positive")
	}
	
	return warnings, nil
}

// GetSyncStatus gets the status of a sync operation
func (rs *RobotSynchronizerImpl) GetSyncStatus(ctx context.Context, syncID string) (*RobotSyncReport, error) {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	
	// Check active syncs
	if report, exists := rs.activeSync[syncID]; exists {
		return report, nil
	}
	
	// Check history
	for _, report := range rs.syncHistory {
		if report.SyncID == syncID {
			return report, nil
		}
	}
	
	return nil, fmt.Errorf("sync report not found: %s", syncID)
}

// GenerateReport generates a sync report in the specified format
func (rs *RobotSynchronizerImpl) GenerateReport(ctx context.Context, syncID string, format string) ([]byte, error) {
	report, err := rs.GetSyncStatus(ctx, syncID)
	if err != nil {
		return nil, err
	}
	
	switch strings.ToLower(format) {
	case "json":
		return rs.generateJSONReport(report)
	case "csv":
		return rs.generateCSVReport(report)
	case "table":
		return rs.generateTableReport(report)
	default:
		return nil, fmt.Errorf("unsupported report format: %s", format)
	}
}

// GetSyncHistory gets sync history based on filter criteria
func (rs *RobotSynchronizerImpl) GetSyncHistory(ctx context.Context, filter *RobotFilter) ([]*RobotSyncReport, error) {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	
	// For now, return all history. In a real implementation,
	// this would apply the filter criteria
	result := make([]*RobotSyncReport, len(rs.syncHistory))
	copy(result, rs.syncHistory)
	
	return result, nil
}

// UpdateSyncConfig updates the synchronizer configuration
func (rs *RobotSynchronizerImpl) UpdateSyncConfig(options *RobotSyncOptions) error {
	if options == nil {
		return fmt.Errorf("sync options cannot be nil")
	}
	
	rs.mu.Lock()
	defer rs.mu.Unlock()
	
	rs.config.DefaultOptions = options
	
	rs.logger.Info("Sync configuration updated")
	
	return nil
}

// GetSyncConfig gets the current synchronizer configuration
func (rs *RobotSynchronizerImpl) GetSyncConfig() *RobotSyncOptions {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	
	return rs.config.DefaultOptions
}

// ListSystemRobots lists system robots from a specific instance
func (rs *RobotSynchronizerImpl) ListSystemRobots(ctx context.Context, instance string, filter *RobotFilter) (*RobotFilterResult, error) {
	var harborClient client.ClientInterface
	
	if instance == "local" || instance == "" {
		harborClient = rs.localClient
	} else {
		var exists bool
		harborClient, exists = rs.remoteClients[instance]
		if !exists {
			return nil, fmt.Errorf("client not found for instance: %s", instance)
		}
	}
	
	robots, err := harborClient.ListSystemRobotAccounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list system robots: %w", err)
	}
	
	// Convert to Robot type and apply filters
	robotList := make([]*Robot, len(robots))
	for i, robot := range robots {
		robotList[i] = &Robot{
			RobotAccount:   &robot,
			SourceInstance: instance,
		}
	}
	
	return rs.ApplyFilter(robotList, filter)
}

// ListProjectRobots lists project robots from a specific instance
func (rs *RobotSynchronizerImpl) ListProjectRobots(ctx context.Context, instance string, projectID int64, filter *RobotFilter) (*RobotFilterResult, error) {
	var harborClient client.ClientInterface
	
	if instance == "local" || instance == "" {
		harborClient = rs.localClient
	} else {
		var exists bool
		harborClient, exists = rs.remoteClients[instance]
		if !exists {
			return nil, fmt.Errorf("client not found for instance: %s", instance)
		}
	}
	
	robots, err := harborClient.ListProjectRobotAccounts(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to list project robots: %w", err)
	}
	
	// Convert to Robot type and apply filters
	robotList := make([]*Robot, len(robots))
	for i, robot := range robots {
		robotList[i] = &Robot{
			RobotAccount:   &robot,
			SourceInstance: instance,
		}
	}
	
	return rs.ApplyFilter(robotList, filter)
}

// Private helper methods

func (rs *RobotSynchronizerImpl) generateSyncID(prefix, source string) string {
	timestamp := time.Now().Format("20060102_150405")
	return fmt.Sprintf("%s_%s_%s", prefix, source, timestamp)
}

func (rs *RobotSynchronizerImpl) applyFilters(robots []client.RobotAccount, filter *RobotFilter) []client.RobotAccount {
	if filter == nil {
		return robots
	}
	
	filtered := make([]client.RobotAccount, 0)
	
	for _, robot := range robots {
		if rs.matchesFilter(&robot, filter) {
			filtered = append(filtered, robot)
		}
	}
	
	return filtered
}

func (rs *RobotSynchronizerImpl) matchesFilter(robot *client.RobotAccount, filter *RobotFilter) bool {
	if filter == nil {
		return true
	}
	
	// Apply name pattern filters (glob patterns)
	if len(filter.NamePatterns) > 0 {
		matched := false
		for _, pattern := range filter.NamePatterns {
			if rs.matchesGlobPattern(robot.Name, pattern) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	
	// Apply regex pattern filter
	if filter.NameRegex != "" {
		matched, err := regexp.MatchString(filter.NameRegex, robot.Name)
		if err != nil {
			rs.logger.Warn("Invalid regex pattern", zap.String("pattern", filter.NameRegex), zap.Error(err))
			return false
		}
		if !matched {
			return false
		}
	}
	
	// Apply exclude patterns (glob patterns)
	for _, pattern := range filter.ExcludePatterns {
		if rs.matchesGlobPattern(robot.Name, pattern) {
			return false
		}
	}
	
	// Apply level filters
	if len(filter.Levels) > 0 {
		matched := false
		for _, level := range filter.Levels {
			if robot.Level == level {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	
	// Apply type filters
	if len(filter.Types) > 0 {
		matched := false
		for _, syncType := range filter.Types {
			robotType := RobotSyncTypeSystem
			if robot.Level == "project" {
				robotType = RobotSyncTypeProject
			}
			if robotType == syncType {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	
	// Apply project ID filters
	if len(filter.ProjectIDs) > 0 {
		matched := false
		for _, projectID := range filter.ProjectIDs {
			if robot.ProjectID == projectID {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	
	// Apply project pattern filters
	if len(filter.ProjectPatterns) > 0 && robot.Level == "project" {
		matched := false
		// Get project name for pattern matching
		projectName := rs.getProjectName(robot.ProjectID)
		for _, pattern := range filter.ProjectPatterns {
			if rs.matchesGlobPattern(projectName, pattern) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	
	// Apply permission filters
	if len(filter.RequiredPermissions) > 0 {
		if !rs.matchesPermissionFilters(robot, filter.RequiredPermissions) {
			return false
		}
	}
	
	// Apply time filters
	if filter.CreatedAfter != nil && robot.CreationTime.Before(*filter.CreatedAfter) {
		return false
	}
	
	if filter.CreatedBefore != nil && robot.CreationTime.After(*filter.CreatedBefore) {
		return false
	}
	
	return true
}

func (rs *RobotSynchronizerImpl) filterRobots(robots []*Robot, filter *RobotFilter) []*Robot {
	if filter == nil {
		return robots
	}
	
	filtered := make([]*Robot, 0)
	
	for _, robot := range robots {
		if rs.matchesRobotFilter(robot, filter) {
			filtered = append(filtered, robot)
		}
	}
	
	return filtered
}

func (rs *RobotSynchronizerImpl) matchesRobotFilter(robot *Robot, filter *RobotFilter) bool {
	if filter == nil {
		return true
	}
	
	// Apply name pattern filters
	if len(filter.NamePatterns) > 0 {
		matched := false
		for _, pattern := range filter.NamePatterns {
			if strings.Contains(robot.Name, pattern) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	
	// Apply sync status filters
	if len(filter.SyncStatuses) > 0 {
		matched := false
		for _, status := range filter.SyncStatuses {
			if robot.SyncStatus == status {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	
	// Apply time filters
	if filter.CreatedAfter != nil && robot.CreationTime.Before(*filter.CreatedAfter) {
		return false
	}
	
	if filter.CreatedBefore != nil && robot.CreationTime.After(*filter.CreatedBefore) {
		return false
	}
	
	return true
}

func (rs *RobotSynchronizerImpl) processBatch(ctx context.Context, batch []client.RobotAccount, localIndex map[string]*client.RobotAccount, options *RobotSyncOptions) []RobotSyncResult {
	results := make([]RobotSyncResult, 0, len(batch))
	
	// Process robots concurrently within the batch
	maxConcurrency := options.MaxConcurrency
	if maxConcurrency <= 0 {
		maxConcurrency = rs.config.MaxConcurrency
	}
	
	semaphore := make(chan struct{}, maxConcurrency)
	resultsChan := make(chan RobotSyncResult, len(batch))
	var wg sync.WaitGroup
	
	for _, remoteRobot := range batch {
		wg.Add(1)
		go func(robot client.RobotAccount) {
			defer wg.Done()
			
			semaphore <- struct{}{} // Acquire
			defer func() { <-semaphore }() // Release
			
			localRobot := localIndex[robot.Name]
			result := rs.processSingleRobot(ctx, &robot, localRobot, options)
			resultsChan <- result
		}(remoteRobot)
	}
	
	// Wait for all goroutines to complete
	go func() {
		wg.Wait()
		close(resultsChan)
	}()
	
	// Collect results
	for result := range resultsChan {
		results = append(results, result)
	}
	
	return results
}

func (rs *RobotSynchronizerImpl) processSingleRobot(ctx context.Context, remoteRobot, localRobot *client.RobotAccount, options *RobotSyncOptions) RobotSyncResult {
	startTime := time.Now()
	
	result := RobotSyncResult{
		Robot:     &Robot{RobotAccount: remoteRobot},
		Timestamp: time.Now(),
	}
	
	defer func() {
		result.Duration = time.Since(startTime)
	}()
	
	// Check if robot should be skipped
	if rs.shouldSkipRobot(remoteRobot, options) {
		result.Operation = RobotSyncOperationSkip
		result.Success = true
		return result
	}
	
	if localRobot == nil {
		// Robot doesn't exist locally - create if allowed
		if options.CreateMissing {
			result.Operation = RobotSyncOperationCreate
			if !options.DryRun {
				err := rs.createRobot(ctx, remoteRobot, options)
				if err != nil {
					result.Error = err.Error()
					result.Success = false
				} else {
					result.Success = true
				}
			} else {
				result.Success = true
			}
		} else {
			result.Operation = RobotSyncOperationSkip
			result.Success = true
		}
	} else {
		// Robot exists - check for conflicts
		conflict, err := rs.comparator.Compare(remoteRobot, localRobot)
		if err != nil {
			result.Error = fmt.Sprintf("failed to compare robots: %s", err.Error())
			result.Success = false
			return result
		}
		
		if conflict == nil {
			// No conflicts - robot is already synchronized
			result.Operation = RobotSyncOperationSkip
			result.Success = true
		} else {
			// Handle conflict
			result.ConflictInfo = conflict
			
			if options.UpdateExisting {
				resolvedRobot, err := rs.resolver.ResolveConflict(ctx, conflict, options.ConflictStrategy)
				if err != nil {
					result.Operation = RobotSyncOperationConflict
					result.Error = fmt.Sprintf("failed to resolve conflict: %s", err.Error())
					result.Success = false
				} else {
					result.Operation = RobotSyncOperationUpdate
					result.ConflictResolved = true
					
					if !options.DryRun {
						err = rs.updateRobot(ctx, localRobot.ID, resolvedRobot, options)
						if err != nil {
							result.Error = err.Error()
							result.Success = false
						} else {
							result.Success = true
						}
					} else {
						result.Success = true
					}
				}
			} else {
				result.Operation = RobotSyncOperationConflict
				result.Success = false
				result.Error = "robot update not allowed by options"
			}
		}
	}
	
	return result
}

// processSingleRobotWithProject processes a single robot with project context for project-specific synchronization
func (rs *RobotSynchronizerImpl) processSingleRobotWithProject(ctx context.Context, remoteRobot, localRobot *client.RobotAccount, targetProjectID int64, options *RobotSyncOptions) RobotSyncResult {
	startTime := time.Now()
	
	result := RobotSyncResult{
		Robot:     &Robot{RobotAccount: remoteRobot},
		Timestamp: time.Now(),
	}
	
	defer func() {
		result.Duration = time.Since(startTime)
	}()
	
	// Check if robot should be skipped
	if rs.shouldSkipRobot(remoteRobot, options) {
		result.Operation = RobotSyncOperationSkip
		result.Success = true
		return result
	}
	
	if localRobot == nil {
		// Robot doesn't exist locally - create if allowed
		if options.CreateMissing {
			result.Operation = RobotSyncOperationCreate
			if !options.DryRun {
				err := rs.createRobotWithProject(ctx, remoteRobot, targetProjectID, options)
				if err != nil {
					result.Error = err.Error()
					result.Success = false
				} else {
					result.Success = true
				}
			} else {
				result.Success = true
			}
		} else {
			result.Operation = RobotSyncOperationSkip
			result.Success = true
		}
	} else {
		// Robot exists - check for conflicts
		conflict, err := rs.comparator.Compare(remoteRobot, localRobot)
		if err != nil {
			result.Error = fmt.Sprintf("failed to compare robots: %s", err.Error())
			result.Success = false
			return result
		}
		
		if conflict == nil {
			// No conflicts - robot is already synchronized
			result.Operation = RobotSyncOperationSkip
			result.Success = true
		} else {
			// Handle conflict
			result.ConflictInfo = conflict
			
			if options.UpdateExisting {
				resolvedRobot, err := rs.resolver.ResolveConflict(ctx, conflict, options.ConflictStrategy)
				if err != nil {
					result.Operation = RobotSyncOperationConflict
					result.Error = fmt.Sprintf("failed to resolve conflict: %s", err.Error())
					result.Success = false
				} else {
					result.Operation = RobotSyncOperationUpdate
					result.ConflictResolved = true
					
					if !options.DryRun {
						err = rs.updateRobotWithProject(ctx, localRobot.ID, resolvedRobot, targetProjectID, options)
						if err != nil {
							result.Error = err.Error()
							result.Success = false
						} else {
							result.Success = true
						}
					} else {
						result.Success = true
					}
				}
			} else {
				result.Operation = RobotSyncOperationConflict
				result.Success = false
				result.Error = "robot update not allowed by options"
			}
		}
	}
	
	return result
}

func (rs *RobotSynchronizerImpl) shouldSkipRobot(robot *client.RobotAccount, options *RobotSyncOptions) bool {
	// Apply filter logic here
	if options.Filter != nil {
		return !rs.matchesFilter(robot, options.Filter)
	}
	return false
}

func (rs *RobotSynchronizerImpl) createRobot(ctx context.Context, robot *client.RobotAccount, options *RobotSyncOptions) error {
	return rs.createRobotWithProject(ctx, robot, 0, options)
}

func (rs *RobotSynchronizerImpl) createRobotWithProject(ctx context.Context, robot *client.RobotAccount, targetProjectID int64, options *RobotSyncOptions) error {
	// Create a copy to avoid modifying the original
	robotCopy := *robot
	
	// Apply project ID mapping for project-level robots
	if robotCopy.Level == "project" && targetProjectID > 0 {
		// Update permissions to use the target project ID
		for i := range robotCopy.Permissions {
			for j := range robotCopy.Permissions[i].Access {
				if robotCopy.Permissions[i].Access[j].Resource != "" {
					// Update project ID in resource path
					oldResource := robotCopy.Permissions[i].Access[j].Resource
					newResource := strings.Replace(oldResource, 
						fmt.Sprintf("/project/%d/", robot.ProjectID), 
						fmt.Sprintf("/project/%d/", targetProjectID), -1)
					robotCopy.Permissions[i].Access[j].Resource = newResource
				}
			}
		}
		robotCopy.ProjectID = targetProjectID
	}
	
	// Handle secret management if needed
	if !options.PreserveSecrets || robotCopy.Secret == "" {
		// Generate new secret or rotate
		if options.RotateSecrets {
			// Generate new secret
			robotCopy.Secret = "" // Let Harbor generate it
		}
	}
	
	// Create robot on local instance
	createdRobot, err := rs.localClient.CreateRobotAccount(ctx, &robotCopy)
	if err != nil {
		return fmt.Errorf("failed to create robot: %w", err)
	}
	
	// Store secret if secret manager is available
	if rs.secretManager != nil && createdRobot.Secret != "" {
		metadata := map[string]string{
			"source":    options.SourceInstance,
			"sync_type": "create",
		}
		
		if err := rs.secretManager.StoreSecret(createdRobot.ID, createdRobot.Secret, metadata); err != nil {
			rs.logger.Warn("Failed to store robot secret", 
				zap.Int64("robot_id", createdRobot.ID),
				zap.Error(err))
		}
	}
	
	// Update state manager
	mapping := state.ResourceMapping{
		SourceID:     fmt.Sprintf("%s_robot_%s", options.SourceInstance, robot.Name),
		TargetID:     fmt.Sprintf("local_robot_%d", createdRobot.ID),
		ResourceType: "robot_account",
		SyncStatus:   state.MappingStatusSynced,
		CreatedAt:    time.Now(),
		Metadata: map[string]interface{}{
			"robot_name":  robot.Name,
			"robot_level": robot.Level,
		},
	}
	
	return rs.stateManager.AddMapping(mapping)
}

func (rs *RobotSynchronizerImpl) updateRobot(ctx context.Context, robotID int64, robot *client.RobotAccount, options *RobotSyncOptions) error {
	// Handle secret management
	if options.RotateSecrets {
		// Rotate secret through secret manager if available
		if rs.secretManager != nil {
			newSecret, err := rs.secretManager.RotateSecret(ctx, robotID)
			if err != nil {
				rs.logger.Warn("Failed to rotate robot secret", 
					zap.Int64("robot_id", robotID),
					zap.Error(err))
			} else {
				robot.Secret = newSecret
			}
		}
	} else if options.PreserveSecrets {
		// Don't update the secret
		robot.Secret = ""
	}
	
	// Update robot on local instance
	_, err := rs.localClient.UpdateRobotAccount(ctx, robotID, robot)
	if err != nil {
		return fmt.Errorf("failed to update robot: %w", err)
	}
	
	// Update mapping timestamp
	sourceID := fmt.Sprintf("%s_robot_%s", options.SourceInstance, robot.Name)
	mapping, err := rs.stateManager.GetMapping(sourceID)
	if err != nil {
		rs.logger.Debug("Failed to get mapping for update", zap.String("source_id", sourceID), zap.Error(err))
		return nil // Don't fail the update for mapping issues
	}
	
	if mapping != nil {
		updates := map[string]interface{}{
			"last_synced": time.Now(),
		}
		return rs.stateManager.UpdateMapping(mapping.SourceID, updates)
	}
	
	return nil
}

func (rs *RobotSynchronizerImpl) updateRobotWithProject(ctx context.Context, robotID int64, robot *client.RobotAccount, targetProjectID int64, options *RobotSyncOptions) error {
	// Create a copy to avoid modifying the original
	robotCopy := *robot
	
	// Apply project ID mapping for project-level robots
	if robotCopy.Level == "project" && targetProjectID > 0 {
		// Update permissions to use the target project ID
		for i := range robotCopy.Permissions {
			for j := range robotCopy.Permissions[i].Access {
				if robotCopy.Permissions[i].Access[j].Resource != "" {
					// Update project ID in resource path
					oldResource := robotCopy.Permissions[i].Access[j].Resource
					newResource := strings.Replace(oldResource, 
						fmt.Sprintf("/project/%d/", robot.ProjectID), 
						fmt.Sprintf("/project/%d/", targetProjectID), -1)
					robotCopy.Permissions[i].Access[j].Resource = newResource
				}
			}
		}
		robotCopy.ProjectID = targetProjectID
	}
	
	// Handle secret management
	if options.RotateSecrets {
		// Rotate secret through secret manager if available
		if rs.secretManager != nil {
			newSecret, err := rs.secretManager.RotateSecret(ctx, robotID)
			if err != nil {
				rs.logger.Warn("Failed to rotate robot secret", 
					zap.Int64("robot_id", robotID),
					zap.Error(err))
			} else {
				robotCopy.Secret = newSecret
			}
		}
	} else if options.PreserveSecrets {
		// Don't update the secret
		robotCopy.Secret = ""
	}
	
	// Update robot on local instance
	_, err := rs.localClient.UpdateRobotAccount(ctx, robotID, &robotCopy)
	if err != nil {
		return fmt.Errorf("failed to update robot: %w", err)
	}
	
	// Update mapping timestamp
	sourceID := fmt.Sprintf("%s_robot_%s", options.SourceInstance, robot.Name)
	mapping, err := rs.stateManager.GetMapping(sourceID)
	if err != nil {
		rs.logger.Debug("Failed to get mapping for update", zap.String("source_id", sourceID), zap.Error(err))
		return nil // Don't fail the update for mapping issues
	}
	
	if mapping != nil {
		updates := map[string]interface{}{
			"last_synced": time.Now(),
			"target_project_id": targetProjectID,
		}
		return rs.stateManager.UpdateMapping(mapping.SourceID, updates)
	}
	
	return nil
}

func (rs *RobotSynchronizerImpl) addToHistory(report *RobotSyncReport) {
	rs.syncHistory = append(rs.syncHistory, report)
	
	// Trim history if it exceeds max size
	if len(rs.syncHistory) > rs.config.MaxHistorySize {
		rs.syncHistory = rs.syncHistory[1:]
	}
}

func (rs *RobotSynchronizerImpl) startStateSyncScheduler() {
	ticker := time.NewTicker(rs.config.StateSyncInterval)
	defer ticker.Stop()
	
	for range ticker.C {
		if err := rs.stateManager.Save(); err != nil {
			rs.logger.Error("Failed to sync state", zap.Error(err))
		}
	}
}

func (rs *RobotSynchronizerImpl) startCleanupScheduler() {
	ticker := time.NewTicker(rs.config.CleanupInterval)
	defer ticker.Stop()
	
	for range ticker.C {
		rs.performCleanup()
	}
}

func (rs *RobotSynchronizerImpl) performCleanup() {
	// Cleanup old sync history
	cutoff := time.Now().Add(-24 * time.Hour * 30) // Keep 30 days
	
	rs.mu.Lock()
	filtered := make([]*RobotSyncReport, 0)
	for _, report := range rs.syncHistory {
		if report.EndTime.After(cutoff) {
			filtered = append(filtered, report)
		}
	}
	rs.syncHistory = filtered
	rs.mu.Unlock()
	
	// Cleanup expired secrets if secret manager supports it
	if rs.secretManager != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		if err := rs.secretManager.CleanupExpiredSecrets(ctx); err != nil {
			rs.logger.Error("Failed to cleanup expired secrets", zap.Error(err))
		}
		cancel()
	}
	
	rs.logger.Debug("Cleanup completed")
}

// Report generation methods (simplified implementations)

func (rs *RobotSynchronizerImpl) generateJSONReport(report *RobotSyncReport) ([]byte, error) {
	// Create a comprehensive JSON report
	reportData := map[string]interface{}{
		"sync_id":          report.SyncID,
		"source_instance":  report.SourceInstance,
		"target_instance":  report.TargetInstance,
		"start_time":       report.StartTime,
		"end_time":         report.EndTime,
		"duration_seconds": report.Duration.Seconds(),
		"status":           rs.determineSyncStatus(report),
		
		// Robot counts
		"summary": map[string]interface{}{
			"total_robots":      report.TotalRobots,
			"processed_robots":  report.ProcessedRobots,
			"successful_robots": report.SuccessfulRobots,
			"failed_robots":     report.FailedRobots,
			"skipped_robots":    report.SkippedRobots,
			"conflict_robots":   report.ConflictRobots,
		},
		
		// Operation counts
		"operations": rs.generateOperationCounts(report),
		
		// Performance metrics
		"performance": map[string]interface{}{
			"throughput_robots_per_second": report.Throughput,
			"average_latency_ms":          report.AverageLatency.Milliseconds(),
		},
		
		// Detailed results
		"results":   report.Results,
		"conflicts": report.Conflicts,
		"errors":    report.Errors,
		
		// Configuration used
		"options": report.Options,
		"filter":  report.Filter,
		"tags":    report.Tags,
	}
	
	return json.MarshalIndent(reportData, "", "  ")
}

func (rs *RobotSynchronizerImpl) generateCSVReport(report *RobotSyncReport) ([]byte, error) {
	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)
	
	// Write header
	headers := []string{
		"sync_id", "robot_name", "operation", "status", "duration_ms", 
		"conflict_type", "error", "robot_level", "project_id",
	}
	if err := writer.Write(headers); err != nil {
		return nil, fmt.Errorf("failed to write CSV header: %w", err)
	}
	
	// Write robot results
	for _, result := range report.Results {
		record := []string{
			report.SyncID,
			result.Robot.Name,
			string(result.Operation),
			rs.getBoolStatus(result.Success),
			fmt.Sprintf("%.0f", result.Duration.Milliseconds()),
			rs.getConflictType(result.ConflictInfo),
			result.Error,
			result.Robot.Level,
			fmt.Sprintf("%d", result.Robot.ProjectID),
		}
		if err := writer.Write(record); err != nil {
			return nil, fmt.Errorf("failed to write CSV record: %w", err)
		}
	}
	
	// Write summary row
	summaryRecord := []string{
		report.SyncID,
		"SUMMARY",
		"N/A",
		rs.determineSyncStatus(report),
		fmt.Sprintf("%.0f", report.Duration.Milliseconds()),
		fmt.Sprintf("%d conflicts", report.ConflictRobots),
		fmt.Sprintf("%d errors", len(report.Errors)),
		fmt.Sprintf("%d total", report.TotalRobots),
		"N/A",
	}
	if err := writer.Write(summaryRecord); err != nil {
		return nil, fmt.Errorf("failed to write CSV summary: %w", err)
	}
	
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, fmt.Errorf("CSV writer error: %w", err)
	}
	
	return buf.Bytes(), nil
}

func (rs *RobotSynchronizerImpl) generateTableReport(report *RobotSyncReport) ([]byte, error) {
	var buf bytes.Buffer
	w := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	
	// Header information
	fmt.Fprintf(w, "ROBOT SYNCHRONIZATION REPORT\n")
	fmt.Fprintf(w, "================================\n\n")
	fmt.Fprintf(w, "Sync ID:\t%s\n", report.SyncID)
	fmt.Fprintf(w, "Source:\t%s\n", report.SourceInstance)
	fmt.Fprintf(w, "Target:\t%s\n", report.TargetInstance)
	fmt.Fprintf(w, "Started:\t%s\n", report.StartTime.Format(time.RFC3339))
	fmt.Fprintf(w, "Completed:\t%s\n", report.EndTime.Format(time.RFC3339))
	fmt.Fprintf(w, "Duration:\t%s\n", report.Duration.Round(time.Millisecond))
	fmt.Fprintf(w, "Status:\t%s\n\n", rs.determineSyncStatus(report))
	
	// Summary statistics
	fmt.Fprintf(w, "SUMMARY STATISTICS\n")
	fmt.Fprintf(w, "------------------\n")
	fmt.Fprintf(w, "Total Robots:\t%d\n", report.TotalRobots)
	fmt.Fprintf(w, "Processed:\t%d\n", report.ProcessedRobots)
	fmt.Fprintf(w, "Successful:\t%d\n", report.SuccessfulRobots)
	fmt.Fprintf(w, "Failed:\t%d\n", report.FailedRobots)
	fmt.Fprintf(w, "Skipped:\t%d\n", report.SkippedRobots)
	fmt.Fprintf(w, "Conflicts:\t%d\n\n", report.ConflictRobots)
	
	// Operation breakdown
	operations := rs.generateOperationCounts(report)
	fmt.Fprintf(w, "OPERATIONS\n")
	fmt.Fprintf(w, "----------\n")
	fmt.Fprintf(w, "Created:\t%d\n", operations["create"])
	fmt.Fprintf(w, "Updated:\t%d\n", operations["update"])
	fmt.Fprintf(w, "Skipped:\t%d\n", operations["skip"])
	fmt.Fprintf(w, "Conflicts:\t%d\n\n", operations["conflict"])
	
	// Performance metrics
	fmt.Fprintf(w, "PERFORMANCE\n")
	fmt.Fprintf(w, "-----------\n")
	fmt.Fprintf(w, "Throughput:\t%.2f robots/sec\n", report.Throughput)
	fmt.Fprintf(w, "Avg Latency:\t%s\n\n", report.AverageLatency.Round(time.Millisecond))
	
	// Error summary
	if len(report.Errors) > 0 {
		fmt.Fprintf(w, "ERRORS (%d)\n", len(report.Errors))
		fmt.Fprintf(w, "----------\n")
		for i, err := range report.Errors {
			if i >= 10 { // Limit to first 10 errors
				fmt.Fprintf(w, "... and %d more errors\n", len(report.Errors)-10)
				break
			}
			fmt.Fprintf(w, "• %s: %s\n", err.RobotName, err.ErrorMessage)
		}
		fmt.Fprintf(w, "\n")
	}
	
	// Conflict summary
	if len(report.Conflicts) > 0 {
		fmt.Fprintf(w, "CONFLICTS (%d)\n", len(report.Conflicts))
		fmt.Fprintf(w, "--------------\n")
		for i, conflict := range report.Conflicts {
			if i >= 10 { // Limit to first 10 conflicts
				fmt.Fprintf(w, "... and %d more conflicts\n", len(report.Conflicts)-10)
				break
			}
			fmt.Fprintf(w, "• %s (%s): %s\n", 
				conflict.SourceRobot.Name, 
				string(conflict.Type),
				conflict.ResolutionStrategy)
		}
		fmt.Fprintf(w, "\n")
	}
	
	w.Flush()
	return buf.Bytes(), nil
}

// Project validation and management methods

// validateProjectExists checks if a project exists in the local Harbor instance
func (rs *RobotSynchronizerImpl) validateProjectExists(ctx context.Context, projectID int64) (bool, error) {
	project, err := rs.localClient.GetProject(ctx, projectID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "404") {
			return false, nil
		}
		return false, fmt.Errorf("failed to check project existence: %w", err)
	}
	return project != nil, nil
}

// createMissingProject creates a project that doesn't exist in the target instance
func (rs *RobotSynchronizerImpl) createMissingProject(ctx context.Context, sourceProjectID, targetProjectID int64, options *RobotSyncOptions) error {
	// Get the source project details from remote client
	remoteClient, exists := rs.remoteClients[options.SourceInstance]
	if !exists {
		return fmt.Errorf("remote client not found for instance: %s", options.SourceInstance)
	}
	
	sourceProject, err := remoteClient.GetProject(ctx, sourceProjectID)
	if err != nil {
		return fmt.Errorf("failed to get source project %d: %w", sourceProjectID, err)
	}
	
	// Create project request for local instance
	projectReq := &client.ProjectRequest{
		ProjectName: sourceProject.Name,
		Metadata: map[string]string{
			"public":             sourceProject.Metadata["public"],
			"enable_content_trust": sourceProject.Metadata["enable_content_trust"],
			"prevent_vul":        sourceProject.Metadata["prevent_vul"],
			"severity":           sourceProject.Metadata["severity"],
			"auto_scan":          sourceProject.Metadata["auto_scan"],
		},
		CVEAllowlist: sourceProject.CVEAllowlist,
	}
	
	// If target project ID is different, we need to handle name conflicts
	if targetProjectID != sourceProjectID {
		// Append suffix to avoid naming conflicts
		projectReq.ProjectName = fmt.Sprintf("%s_sync_%d", sourceProject.Name, targetProjectID)
	}
	
	createdProject, err := rs.localClient.CreateProject(ctx, projectReq)
	if err != nil {
		return fmt.Errorf("failed to create project: %w", err)
	}
	
	// Update state manager with project mapping
	mapping := state.ResourceMapping{
		SourceID:     fmt.Sprintf("%s_project_%d", options.SourceInstance, sourceProjectID),
		TargetID:     fmt.Sprintf("local_project_%d", createdProject.ProjectID),
		ResourceType: "project",
		SyncStatus:   state.MappingStatusSynced,
		CreatedAt:    time.Now(),
		Metadata: map[string]interface{}{
			"project_name":        sourceProject.Name,
			"target_project_name": projectReq.ProjectName,
			"source_project_id":   sourceProjectID,
			"target_project_id":   targetProjectID,
		},
	}
	
	if err := rs.stateManager.AddMapping(mapping); err != nil {
		rs.logger.Warn("Failed to save project mapping to state", 
			zap.Int64("source_project_id", sourceProjectID),
			zap.Int64("target_project_id", targetProjectID),
			zap.Error(err))
	}
	
	rs.logger.Info("Successfully created missing project",
		zap.String("source_name", sourceProject.Name),
		zap.String("target_name", projectReq.ProjectName),
		zap.Int64("source_project_id", sourceProjectID),
		zap.Int64("target_project_id", createdProject.ProjectID))
	
	return nil
}

// Filtering helper methods

// matchesGlobPattern checks if a string matches a glob pattern
func (rs *RobotSynchronizerImpl) matchesGlobPattern(str, pattern string) bool {
	matched, err := filepath.Match(pattern, str)
	if err != nil {
		rs.logger.Warn("Invalid glob pattern", zap.String("pattern", pattern), zap.Error(err))
		// Fall back to simple string contains for invalid patterns
		return strings.Contains(str, pattern)
	}
	return matched
}

// getProjectName retrieves the project name for a given project ID
func (rs *RobotSynchronizerImpl) getProjectName(projectID int64) string {
	// This would typically query the project info from Harbor
	// For now, return a placeholder or cache lookup
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	
	project, err := rs.localClient.GetProject(ctx, projectID)
	if err != nil {
		rs.logger.Debug("Failed to get project name", zap.Int64("project_id", projectID), zap.Error(err))
		return fmt.Sprintf("project_%d", projectID)
	}
	
	return project.Name
}

// matchesPermissionFilters checks if robot permissions match the required filters
func (rs *RobotSynchronizerImpl) matchesPermissionFilters(robot *client.RobotAccount, permissionFilters []PermissionFilter) bool {
	if len(permissionFilters) == 0 {
		return true
	}
	
	// Check if robot has all required permissions
	for _, filter := range permissionFilters {
		hasPermission := false
		
		for _, robotPerm := range robot.Permissions {
			if rs.matchesPermissionFilter(robotPerm, filter) {
				hasPermission = true
				break
			}
		}
		
		if !hasPermission {
			return false
		}
	}
	
	return true
}

// matchesPermissionFilter checks if a robot permission matches a permission filter
func (rs *RobotSynchronizerImpl) matchesPermissionFilter(robotPerm client.RobotPermission, filter PermissionFilter) bool {
	// Check kind/namespace if specified
	if filter.Kind != "" && robotPerm.Kind != filter.Kind {
		return false
	}
	
	if filter.Namespace != "" && robotPerm.Namespace != filter.Namespace {
		return false
	}
	
	// Check if robot has required resources
	if len(filter.Resources) > 0 {
		hasAllResources := true
		for _, requiredResource := range filter.Resources {
			found := false
			for _, access := range robotPerm.Access {
				if access.Resource != "" && rs.matchesGlobPattern(access.Resource, requiredResource) {
					found = true
					break
				}
			}
			if !found {
				hasAllResources = false
				break
			}
		}
		if !hasAllResources {
			return false
		}
	}
	
	// Check if robot has required actions
	if len(filter.Actions) > 0 {
		hasAllActions := true
		for _, requiredAction := range filter.Actions {
			found := false
			for _, access := range robotPerm.Access {
				if access.Action != "" && access.Action == requiredAction {
					found = true
					break
				}
			}
			if !found {
				hasAllActions = false
				break
			}
		}
		if !hasAllActions {
			return false
		}
	}
	
	return true
}

// generateFilterSummary creates a summary of applied filters
func (rs *RobotSynchronizerImpl) generateFilterSummary(filter *RobotFilter) map[string]interface{} {
	summary := make(map[string]interface{})
	
	if len(filter.NamePatterns) > 0 {
		summary["name_patterns"] = filter.NamePatterns
	}
	
	if filter.NameRegex != "" {
		summary["name_regex"] = filter.NameRegex
	}
	
	if len(filter.ExcludePatterns) > 0 {
		summary["exclude_patterns"] = filter.ExcludePatterns
	}
	
	if len(filter.Types) > 0 {
		summary["types"] = filter.Types
	}
	
	if len(filter.Levels) > 0 {
		summary["levels"] = filter.Levels
	}
	
	if len(filter.ProjectIDs) > 0 {
		summary["project_ids"] = filter.ProjectIDs
	}
	
	if len(filter.ProjectPatterns) > 0 {
		summary["project_patterns"] = filter.ProjectPatterns
	}
	
	if len(filter.RequiredPermissions) > 0 {
		summary["required_permissions_count"] = len(filter.RequiredPermissions)
	}
	
	if filter.CreatedAfter != nil {
		summary["created_after"] = filter.CreatedAfter
	}
	
	if filter.CreatedBefore != nil {
		summary["created_before"] = filter.CreatedBefore
	}
	
	return summary
}

// Report helper methods

// determineSyncStatus determines the overall status of a sync operation
func (rs *RobotSynchronizerImpl) determineSyncStatus(report *RobotSyncReport) string {
	if report.EndTime.IsZero() {
		return "IN_PROGRESS"
	}
	
	if len(report.Errors) > 0 {
		if report.FailedRobots == report.TotalRobots {
			return "FAILED"
		}
		return "PARTIAL_SUCCESS"
	}
	
	if report.SuccessfulRobots == report.TotalRobots {
		return "SUCCESS"
	}
	
	if report.SuccessfulRobots > 0 {
		return "PARTIAL_SUCCESS"
	}
	
	return "NO_CHANGES"
}

// generateOperationCounts counts operations by type
func (rs *RobotSynchronizerImpl) generateOperationCounts(report *RobotSyncReport) map[string]int {
	counts := map[string]int{
		"create":   0,
		"update":   0,
		"skip":     0,
		"conflict": 0,
		"delete":   0,
	}
	
	for _, result := range report.Results {
		switch result.Operation {
		case RobotSyncOperationCreate:
			counts["create"]++
		case RobotSyncOperationUpdate:
			counts["update"]++
		case RobotSyncOperationSkip:
			counts["skip"]++
		case RobotSyncOperationConflict:
			counts["conflict"]++
		case RobotSyncOperationDelete:
			counts["delete"]++
		}
	}
	
	return counts
}

// getBoolStatus converts boolean to string status
func (rs *RobotSynchronizerImpl) getBoolStatus(success bool) string {
	if success {
		return "SUCCESS"
	}
	return "FAILED"
}

// getConflictType extracts conflict type from conflict info
func (rs *RobotSynchronizerImpl) getConflictType(conflictInfo *RobotConflict) string {
	if conflictInfo == nil {
		return "NONE"
	}
	return string(conflictInfo.Type)
}

// Metrics collection methods

// CollectSyncMetrics collects comprehensive metrics from a sync report
func (rs *RobotSynchronizerImpl) CollectSyncMetrics(report *RobotSyncReport) map[string]interface{} {
	metrics := make(map[string]interface{})
	
	// Basic counts
	metrics["total_robots"] = report.TotalRobots
	metrics["processed_robots"] = report.ProcessedRobots
	metrics["successful_robots"] = report.SuccessfulRobots
	metrics["failed_robots"] = report.FailedRobots
	metrics["skipped_robots"] = report.SkippedRobots
	metrics["conflict_robots"] = report.ConflictRobots
	
	// Operation counts
	operations := rs.generateOperationCounts(report)
	for op, count := range operations {
		metrics[fmt.Sprintf("operation_%s_count", op)] = count
	}
	
	// Performance metrics
	metrics["sync_duration_seconds"] = report.Duration.Seconds()
	metrics["throughput_robots_per_second"] = report.Throughput
	metrics["average_latency_ms"] = report.AverageLatency.Milliseconds()
	
	// Success rates
	if report.TotalRobots > 0 {
		metrics["success_rate"] = float64(report.SuccessfulRobots) / float64(report.TotalRobots)
		metrics["failure_rate"] = float64(report.FailedRobots) / float64(report.TotalRobots)
		metrics["conflict_rate"] = float64(report.ConflictRobots) / float64(report.TotalRobots)
	}
	
	// Error analysis
	errorTypes := make(map[string]int)
	for _, err := range report.Errors {
		errorTypes[err.ErrorType]++
	}
	metrics["error_types"] = errorTypes
	metrics["total_errors"] = len(report.Errors)
	
	// Conflict analysis
	conflictTypes := make(map[string]int)
	conflictResolutions := make(map[string]int)
	for _, conflict := range report.Conflicts {
		conflictTypes[string(conflict.Type)]++
		if conflict.ResolutionStrategy != "" {
			conflictResolutions[conflict.ResolutionStrategy]++
		}
	}
	metrics["conflict_types"] = conflictTypes
	metrics["conflict_resolutions"] = conflictResolutions
	
	// Robot type analysis
	robotLevels := make(map[string]int)
	for _, result := range report.Results {
		robotLevels[result.Robot.Level]++
	}
	metrics["robot_levels"] = robotLevels
	
	// Project distribution (for project-level robots)
	projectDistribution := make(map[string]int)
	for _, result := range report.Results {
		if result.Robot.Level == "project" {
			projectKey := fmt.Sprintf("project_%d", result.Robot.ProjectID)
			projectDistribution[projectKey]++
		}
	}
	if len(projectDistribution) > 0 {
		metrics["project_distribution"] = projectDistribution
	}
	
	return metrics
}

// GenerateMetricsSummary generates a metrics summary for multiple sync operations
func (rs *RobotSynchronizerImpl) GenerateMetricsSummary(ctx context.Context, filter *RobotFilter) (map[string]interface{}, error) {
	history, err := rs.GetSyncHistory(ctx, filter)
	if err != nil {
		return nil, err
	}
	
	summary := make(map[string]interface{})
	
	if len(history) == 0 {
		summary["total_syncs"] = 0
		return summary, nil
	}
	
	// Aggregate metrics across all syncs
	totalSyncs := len(history)
	totalRobots := 0
	totalSuccessful := 0
	totalFailed := 0
	totalConflicts := 0
	totalErrors := 0
	totalDuration := time.Duration(0)
	
	for _, report := range history {
		totalRobots += report.TotalRobots
		totalSuccessful += report.SuccessfulRobots
		totalFailed += report.FailedRobots
		totalConflicts += report.ConflictRobots
		totalErrors += len(report.Errors)
		totalDuration += report.Duration
	}
	
	summary["total_syncs"] = totalSyncs
	summary["total_robots_synced"] = totalRobots
	summary["total_successful"] = totalSuccessful
	summary["total_failed"] = totalFailed
	summary["total_conflicts"] = totalConflicts
	summary["total_errors"] = totalErrors
	summary["total_duration_hours"] = totalDuration.Hours()
	summary["average_sync_duration_minutes"] = totalDuration.Minutes() / float64(totalSyncs)
	
	if totalRobots > 0 {
		summary["overall_success_rate"] = float64(totalSuccessful) / float64(totalRobots)
		summary["overall_failure_rate"] = float64(totalFailed) / float64(totalRobots)
		summary["overall_conflict_rate"] = float64(totalConflicts) / float64(totalRobots)
	}
	
	if totalDuration > 0 {
		summary["overall_throughput_robots_per_hour"] = float64(totalRobots) / totalDuration.Hours()
	}
	
	// Most recent sync timestamp
	if len(history) > 0 {
		summary["last_sync_time"] = history[len(history)-1].EndTime
	}
	
	return summary, nil
}