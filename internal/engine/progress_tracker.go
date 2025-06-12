package engine

import (
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
)

// DefaultProgressTracker implements the ProgressTracker interface
type DefaultProgressTracker struct {
	mu            sync.RWMutex
	logger        *zap.Logger
	progressData  map[string]*progressEntry
	subscribers   map[string][]chan *SyncProgress
	history       map[string][]*SyncProgress
	options       *ProgressTrackerOptions
	cleanupTicker *time.Ticker
	done          chan struct{}
}

// progressEntry holds internal progress tracking data
type progressEntry struct {
	progress     *SyncProgress
	startTime    time.Time
	lastUpdate   time.Time
	finished     bool
	totalBytes   int64
	processedBytes int64
	throughputHistory []throughputPoint
	etaHistory   []time.Duration
	phases       []progressPhase
	currentPhaseIndex int
}

// throughputPoint represents a throughput measurement at a specific time
type throughputPoint struct {
	timestamp  time.Time
	processed  int
	bytes      int64
}

// progressPhase represents a phase in the synchronization process
type progressPhase struct {
	name           string
	estimatedWork  int
	completedWork  int
	weight         float64 // relative weight in total progress (0.0-1.0)
	startTime      time.Time
	endTime        time.Time
}

// ProgressTrackerOptions configures the progress tracker behavior
type ProgressTrackerOptions struct {
	// How often to clean up finished tracking entries
	CleanupInterval time.Duration
	
	// How long to keep finished entries before cleanup
	RetentionPeriod time.Duration
	
	// Maximum number of throughput history points to keep
	MaxThroughputHistory int
	
	// Maximum number of ETA history points for smoothing
	MaxETAHistory int
	
	// Maximum number of progress history entries to keep
	MaxProgressHistory int
	
	// Whether to enable detailed phase tracking
	EnablePhaseTracking bool
	
	// Minimum interval between progress updates to avoid spam
	MinUpdateInterval time.Duration
	
	// Whether to persist progress to disk for recovery
	PersistProgress bool
	
	// Directory for progress persistence
	PersistenceDir string
}

// NewDefaultProgressTracker creates a new progress tracker with default options
func NewDefaultProgressTracker(logger *zap.Logger, options *ProgressTrackerOptions) *DefaultProgressTracker {
	if options == nil {
		options = &ProgressTrackerOptions{
			CleanupInterval:      5 * time.Minute,
			RetentionPeriod:      30 * time.Minute,
			MaxThroughputHistory: 100,
			MaxETAHistory:        10,
			MaxProgressHistory:   50,
			EnablePhaseTracking:  true,
			MinUpdateInterval:    1 * time.Second,
			PersistProgress:      false,
		}
	}

	pt := &DefaultProgressTracker{
		logger:       logger,
		progressData: make(map[string]*progressEntry),
		subscribers:  make(map[string][]chan *SyncProgress),
		history:      make(map[string][]*SyncProgress),
		options:      options,
		done:         make(chan struct{}),
	}

	// Start cleanup routine
	pt.startCleanupRoutine()

	return pt
}

// StartTracking starts tracking progress for a sync operation
func (pt *DefaultProgressTracker) StartTracking(syncID string, totalResources int) error {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	if _, exists := pt.progressData[syncID]; exists {
		return fmt.Errorf("progress tracking already exists for sync ID: %s", syncID)
	}

	now := time.Now()
	progress := &SyncProgress{
		SyncID:              syncID,
		TotalResources:      totalResources,
		ProcessedResources:  0,
		SuccessfulResources: 0,
		FailedResources:     0,
		SkippedResources:    0,
		CurrentPhase:        "initialization",
		PercentComplete:     0.0,
		EstimatedTimeLeft:   0,
		StartTime:           now,
		LastUpdateTime:      now,
		Details:             make(map[string]interface{}),
	}

	entry := &progressEntry{
		progress:          progress,
		startTime:         now,
		lastUpdate:        now,
		finished:          false,
		throughputHistory: make([]throughputPoint, 0, pt.options.MaxThroughputHistory),
		etaHistory:        make([]time.Duration, 0, pt.options.MaxETAHistory),
		phases:            make([]progressPhase, 0),
		currentPhaseIndex: -1,
	}

	// Initialize default phases if phase tracking is enabled
	if pt.options.EnablePhaseTracking {
		entry.initializeDefaultPhases(totalResources)
	}

	pt.progressData[syncID] = entry
	pt.history[syncID] = make([]*SyncProgress, 0, pt.options.MaxProgressHistory)

	pt.logger.Info("Started progress tracking",
		zap.String("sync_id", syncID),
		zap.Int("total_resources", totalResources))

	return nil
}

// UpdateProgress updates the progress for a sync operation
func (pt *DefaultProgressTracker) UpdateProgress(syncID string, progress *SyncProgress) error {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	entry, exists := pt.progressData[syncID]
	if !exists {
		return fmt.Errorf("no progress tracking found for sync ID: %s", syncID)
	}

	if entry.finished {
		return fmt.Errorf("progress tracking already finished for sync ID: %s", syncID)
	}

	now := time.Now()
	
	// Check minimum update interval
	if now.Sub(entry.lastUpdate) < pt.options.MinUpdateInterval {
		return nil // Skip update to avoid spam
	}

	// Update basic progress data
	entry.progress.ProcessedResources = progress.ProcessedResources
	entry.progress.SuccessfulResources = progress.SuccessfulResources
	entry.progress.FailedResources = progress.FailedResources
	entry.progress.SkippedResources = progress.SkippedResources
	entry.progress.LastUpdateTime = now

	// Update current phase if provided
	if progress.CurrentPhase != "" {
		entry.progress.CurrentPhase = progress.CurrentPhase
		if pt.options.EnablePhaseTracking {
			entry.updateCurrentPhase(progress.CurrentPhase, progress.ProcessedResources)
		}
	}

	// Calculate percentage complete
	if entry.progress.TotalResources > 0 {
		entry.progress.PercentComplete = float64(entry.progress.ProcessedResources) / float64(entry.progress.TotalResources) * 100.0
	}

	// Update throughput history
	pt.updateThroughputHistory(entry, now)

	// Calculate and smooth ETA
	pt.calculateETA(entry, now)

	// Update details if provided
	if progress.Details != nil {
		for k, v := range progress.Details {
			entry.progress.Details[k] = v
		}
	}

	entry.lastUpdate = now

	// Add to history
	pt.addToHistory(syncID, entry.progress)

	// Notify subscribers
	pt.notifySubscribers(syncID, entry.progress)

	// Persist if enabled
	if pt.options.PersistProgress {
		pt.persistProgress(syncID, entry.progress)
	}

	pt.logger.Debug("Updated progress",
		zap.String("sync_id", syncID),
		zap.Int("processed", progress.ProcessedResources),
		zap.Int("total", progress.TotalResources),
		zap.Float64("percent", entry.progress.PercentComplete),
		zap.Duration("eta", entry.progress.EstimatedTimeLeft))

	return nil
}

// GetProgress retrieves the current progress for a sync operation
func (pt *DefaultProgressTracker) GetProgress(syncID string) (*SyncProgress, error) {
	pt.mu.RLock()
	defer pt.mu.RUnlock()

	entry, exists := pt.progressData[syncID]
	if !exists {
		return nil, fmt.Errorf("no progress tracking found for sync ID: %s", syncID)
	}

	// Return a copy to avoid external modifications
	progressCopy := *entry.progress
	progressCopy.Details = make(map[string]interface{})
	for k, v := range entry.progress.Details {
		progressCopy.Details[k] = v
	}

	return &progressCopy, nil
}

// FinishTracking marks tracking as finished for a sync operation
func (pt *DefaultProgressTracker) FinishTracking(syncID string) error {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	entry, exists := pt.progressData[syncID]
	if !exists {
		return fmt.Errorf("no progress tracking found for sync ID: %s", syncID)
	}

	now := time.Now()
	entry.finished = true
	entry.progress.LastUpdateTime = now
	entry.progress.PercentComplete = 100.0
	entry.progress.EstimatedTimeLeft = 0

	// Finalize current phase
	if pt.options.EnablePhaseTracking && entry.currentPhaseIndex >= 0 && entry.currentPhaseIndex < len(entry.phases) {
		entry.phases[entry.currentPhaseIndex].endTime = now
	}

	// Add final progress to history
	pt.addToHistory(syncID, entry.progress)

	// Notify subscribers of completion
	pt.notifySubscribers(syncID, entry.progress)

	// Close subscriber channels
	if channels, exists := pt.subscribers[syncID]; exists {
		for _, ch := range channels {
			close(ch)
		}
		delete(pt.subscribers, syncID)
	}

	pt.logger.Info("Finished progress tracking",
		zap.String("sync_id", syncID),
		zap.Duration("total_duration", now.Sub(entry.startTime)))

	return nil
}

// ListActiveProgress lists all currently tracked sync operations
func (pt *DefaultProgressTracker) ListActiveProgress() ([]string, error) {
	pt.mu.RLock()
	defer pt.mu.RUnlock()

	var activeIDs []string
	for syncID, entry := range pt.progressData {
		if !entry.finished {
			activeIDs = append(activeIDs, syncID)
		}
	}

	return activeIDs, nil
}

// Subscribe to progress updates
func (pt *DefaultProgressTracker) Subscribe(syncID string) (<-chan *SyncProgress, error) {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	_, exists := pt.progressData[syncID]
	if !exists {
		return nil, fmt.Errorf("no progress tracking found for sync ID: %s", syncID)
	}

	ch := make(chan *SyncProgress, 10) // Buffer to prevent blocking
	if pt.subscribers[syncID] == nil {
		pt.subscribers[syncID] = make([]chan *SyncProgress, 0)
	}
	pt.subscribers[syncID] = append(pt.subscribers[syncID], ch)

	pt.logger.Debug("Added progress subscriber", zap.String("sync_id", syncID))

	return ch, nil
}

// Unsubscribe from progress updates
func (pt *DefaultProgressTracker) Unsubscribe(syncID string) error {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	// Note: This is a simplified implementation. In practice, you'd want to 
	// track individual channels to close the specific one.
	if channels, exists := pt.subscribers[syncID]; exists {
		for _, ch := range channels {
			close(ch)
		}
		delete(pt.subscribers, syncID)
	}

	return nil
}

// GetProgressHistory returns the progress history for a sync operation
func (pt *DefaultProgressTracker) GetProgressHistory(syncID string) ([]*SyncProgress, error) {
	pt.mu.RLock()
	defer pt.mu.RUnlock()

	history, exists := pt.history[syncID]
	if !exists {
		return nil, fmt.Errorf("no progress history found for sync ID: %s", syncID)
	}

	// Return copies to avoid external modifications
	historyCopy := make([]*SyncProgress, len(history))
	for i, progress := range history {
		progressCopy := *progress
		progressCopy.Details = make(map[string]interface{})
		for k, v := range progress.Details {
			progressCopy.Details[k] = v
		}
		historyCopy[i] = &progressCopy
	}

	return historyCopy, nil
}

// GetPhases returns the phases for a sync operation (if phase tracking is enabled)
func (pt *DefaultProgressTracker) GetPhases(syncID string) ([]progressPhase, error) {
	pt.mu.RLock()
	defer pt.mu.RUnlock()

	entry, exists := pt.progressData[syncID]
	if !exists {
		return nil, fmt.Errorf("no progress tracking found for sync ID: %s", syncID)
	}

	if !pt.options.EnablePhaseTracking {
		return nil, fmt.Errorf("phase tracking is not enabled")
	}

	// Return copies to avoid external modifications
	phases := make([]progressPhase, len(entry.phases))
	copy(phases, entry.phases)

	return phases, nil
}

// Stop stops the progress tracker and cleans up resources
func (pt *DefaultProgressTracker) Stop() error {
	close(pt.done)
	
	if pt.cleanupTicker != nil {
		pt.cleanupTicker.Stop()
	}

	pt.mu.Lock()
	defer pt.mu.Unlock()

	// Close all subscriber channels
	for syncID, channels := range pt.subscribers {
		for _, ch := range channels {
			close(ch)
		}
		delete(pt.subscribers, syncID)
	}

	pt.logger.Info("Progress tracker stopped")
	return nil
}

// Private helper methods

func (entry *progressEntry) initializeDefaultPhases(totalResources int) {
	entry.phases = []progressPhase{
		{
			name:          "initialization",
			estimatedWork: 1,
			weight:        0.05, // 5% of total progress
		},
		{
			name:          "discovery",
			estimatedWork: totalResources / 10, // Assume 10% overhead for discovery
			weight:        0.15,                // 15% of total progress
		},
		{
			name:          "synchronization",
			estimatedWork: totalResources,
			weight:        0.70, // 70% of total progress
		},
		{
			name:          "finalization",
			estimatedWork: 1,
			weight:        0.10, // 10% of total progress
		},
	}
	entry.currentPhaseIndex = 0
	entry.phases[0].startTime = entry.startTime
}

func (entry *progressEntry) updateCurrentPhase(phaseName string, processedResources int) {
	// Find the phase and update current phase index
	for i, phase := range entry.phases {
		if phase.name == phaseName {
			if i != entry.currentPhaseIndex {
				// Phase changed
				if entry.currentPhaseIndex >= 0 && entry.currentPhaseIndex < len(entry.phases) {
					entry.phases[entry.currentPhaseIndex].endTime = time.Now()
				}
				entry.currentPhaseIndex = i
				entry.phases[i].startTime = time.Now()
			}
			
			// Update work completed for current phase
			if phaseName == "synchronization" {
				entry.phases[i].completedWork = processedResources
			} else {
				entry.phases[i].completedWork = entry.phases[i].estimatedWork
			}
			break
		}
	}
}

func (pt *DefaultProgressTracker) updateThroughputHistory(entry *progressEntry, now time.Time) {
	point := throughputPoint{
		timestamp: now,
		processed: entry.progress.ProcessedResources,
		bytes:     entry.processedBytes,
	}

	entry.throughputHistory = append(entry.throughputHistory, point)

	// Keep only recent history
	if len(entry.throughputHistory) > pt.options.MaxThroughputHistory {
		entry.throughputHistory = entry.throughputHistory[1:]
	}
}

func (pt *DefaultProgressTracker) calculateETA(entry *progressEntry, now time.Time) {
	if entry.progress.TotalResources <= 0 || entry.progress.ProcessedResources <= 0 {
		entry.progress.EstimatedTimeLeft = 0
		return
	}

	remainingResources := entry.progress.TotalResources - entry.progress.ProcessedResources
	if remainingResources <= 0 {
		entry.progress.EstimatedTimeLeft = 0
		return
	}

	// Calculate throughput from recent history
	throughput := pt.calculateThroughput(entry, now)
	if throughput <= 0 {
		entry.progress.EstimatedTimeLeft = 0
		return
	}

	// Calculate basic ETA
	etaSeconds := float64(remainingResources) / throughput
	eta := time.Duration(etaSeconds * float64(time.Second))

	// Apply phase weighting if enabled
	if pt.options.EnablePhaseTracking && entry.currentPhaseIndex >= 0 {
		eta = pt.applyPhaseWeighting(entry, eta)
	}

	// Smooth ETA using historical values
	entry.etaHistory = append(entry.etaHistory, eta)
	if len(entry.etaHistory) > pt.options.MaxETAHistory {
		entry.etaHistory = entry.etaHistory[1:]
	}

	// Use moving average for smoothing
	var totalETA time.Duration
	for _, histETA := range entry.etaHistory {
		totalETA += histETA
	}
	entry.progress.EstimatedTimeLeft = totalETA / time.Duration(len(entry.etaHistory))
}

func (pt *DefaultProgressTracker) calculateThroughput(entry *progressEntry, now time.Time) float64 {
	if len(entry.throughputHistory) < 2 {
		// Not enough data, use simple calculation
		elapsed := now.Sub(entry.startTime)
		if elapsed.Seconds() <= 0 {
			return 0
		}
		return float64(entry.progress.ProcessedResources) / elapsed.Seconds()
	}

	// Use recent throughput data for more accurate calculation
	recentWindow := 5 * time.Minute
	cutoff := now.Add(-recentWindow)

	var totalProcessed int
	var earliestTime time.Time
	
	for i := len(entry.throughputHistory) - 1; i >= 0; i-- {
		point := entry.throughputHistory[i]
		if point.timestamp.Before(cutoff) {
			break
		}
		if earliestTime.IsZero() || point.timestamp.Before(earliestTime) {
			earliestTime = point.timestamp
		}
	}

	if !earliestTime.IsZero() {
		if len(entry.throughputHistory) > 0 {
			latestPoint := entry.throughputHistory[len(entry.throughputHistory)-1]
			for _, point := range entry.throughputHistory {
				if point.timestamp.After(cutoff) {
					totalProcessed = latestPoint.processed - point.processed
					elapsed := latestPoint.timestamp.Sub(point.timestamp)
					if elapsed.Seconds() > 0 {
						return float64(totalProcessed) / elapsed.Seconds()
					}
					break
				}
			}
		}
	}

	// Fallback to overall throughput
	elapsed := now.Sub(entry.startTime)
	if elapsed.Seconds() <= 0 {
		return 0
	}
	return float64(entry.progress.ProcessedResources) / elapsed.Seconds()
}

func (pt *DefaultProgressTracker) applyPhaseWeighting(entry *progressEntry, eta time.Duration) time.Duration {
	if entry.currentPhaseIndex < 0 || entry.currentPhaseIndex >= len(entry.phases) {
		return eta
	}

	// Calculate remaining work weight
	var remainingWeight float64
	for i := entry.currentPhaseIndex; i < len(entry.phases); i++ {
		phase := entry.phases[i]
		if i == entry.currentPhaseIndex {
			// Current phase - calculate remaining work
			remaining := float64(phase.estimatedWork - phase.completedWork)
			if phase.estimatedWork > 0 {
				remainingWeight += phase.weight * (remaining / float64(phase.estimatedWork))
			}
		} else {
			// Future phases
			remainingWeight += phase.weight
		}
	}

	if remainingWeight > 0 && remainingWeight <= 1.0 {
		// Adjust ETA based on remaining phase weight
		return time.Duration(float64(eta) * remainingWeight / (1.0 - remainingWeight + remainingWeight))
	}

	return eta
}

func (pt *DefaultProgressTracker) addToHistory(syncID string, progress *SyncProgress) {
	// Create a copy for history
	progressCopy := *progress
	progressCopy.Details = make(map[string]interface{})
	for k, v := range progress.Details {
		progressCopy.Details[k] = v
	}

	pt.history[syncID] = append(pt.history[syncID], &progressCopy)

	// Keep only recent history
	if len(pt.history[syncID]) > pt.options.MaxProgressHistory {
		pt.history[syncID] = pt.history[syncID][1:]
	}
}

func (pt *DefaultProgressTracker) notifySubscribers(syncID string, progress *SyncProgress) {
	if channels, exists := pt.subscribers[syncID]; exists {
		// Create a copy for subscribers
		progressCopy := *progress
		progressCopy.Details = make(map[string]interface{})
		for k, v := range progress.Details {
			progressCopy.Details[k] = v
		}

		for _, ch := range channels {
			select {
			case ch <- &progressCopy:
			default:
				// Channel is full, skip this update to avoid blocking
				pt.logger.Warn("Progress subscriber channel is full, skipping update",
					zap.String("sync_id", syncID))
			}
		}
	}
}

func (pt *DefaultProgressTracker) persistProgress(syncID string, progress *SyncProgress) {
	// Implementation would write progress to disk for recovery
	// This is a placeholder for the actual persistence logic
	pt.logger.Debug("Progress persistence not implemented",
		zap.String("sync_id", syncID))
}

func (pt *DefaultProgressTracker) startCleanupRoutine() {
	pt.cleanupTicker = time.NewTicker(pt.options.CleanupInterval)
	
	go func() {
		for {
			select {
			case <-pt.cleanupTicker.C:
				pt.performCleanup()
			case <-pt.done:
				return
			}
		}
	}()
}

func (pt *DefaultProgressTracker) performCleanup() {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-pt.options.RetentionPeriod)

	for syncID, entry := range pt.progressData {
		if entry.finished && entry.lastUpdate.Before(cutoff) {
			delete(pt.progressData, syncID)
			delete(pt.history, syncID)
			
			// Close any remaining subscriber channels
			if channels, exists := pt.subscribers[syncID]; exists {
				for _, ch := range channels {
					close(ch)
				}
				delete(pt.subscribers, syncID)
			}

			pt.logger.Debug("Cleaned up finished progress tracking",
				zap.String("sync_id", syncID),
				zap.Duration("age", now.Sub(entry.lastUpdate)))
		}
	}
}