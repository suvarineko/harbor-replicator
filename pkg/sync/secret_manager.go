package sync

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"go.uber.org/zap"

	"harbor-replicator/pkg/state"
)

// SecretManagerImpl implements the SecretManager interface
type SecretManagerImpl struct {
	mu               sync.RWMutex
	stateManager     *state.StateManager
	logger           *zap.Logger
	encryptionKey    []byte
	gcm              cipher.AEAD
	rotationSchedule map[int64]time.Time
	secretCache      map[int64]*EncryptedSecret
	stats            SecretStats
	
	// Configuration
	config *SecretManagerConfig
}

// SecretManagerConfig holds configuration for the secret manager
type SecretManagerConfig struct {
	// Encryption settings
	EncryptionKeySize int           `json:"encryption_key_size"`
	NonceSize         int           `json:"nonce_size"`
	
	// Cache settings
	CacheEnabled      bool          `json:"cache_enabled"`
	CacheSize         int           `json:"cache_size"`
	CacheTTL          time.Duration `json:"cache_ttl"`
	
	// Rotation settings
	DefaultRotationInterval time.Duration `json:"default_rotation_interval"`
	MaxSecretAge            time.Duration `json:"max_secret_age"`
	RotationRetryInterval   time.Duration `json:"rotation_retry_interval"`
	
	// Security settings
	SecretMinLength    int  `json:"secret_min_length"`
	SecretMaxLength    int  `json:"secret_max_length"`
	RequireStrongSecrets bool `json:"require_strong_secrets"`
	
	// Cleanup settings
	CleanupInterval    time.Duration `json:"cleanup_interval"`
	RetainHistory      time.Duration `json:"retain_history"`
}

// EncryptedSecret represents an encrypted robot secret with metadata
type EncryptedSecret struct {
	RobotID        int64             `json:"robot_id"`
	EncryptedData  string            `json:"encrypted_data"`
	Nonce          string            `json:"nonce"`
	Checksum       string            `json:"checksum"`
	Metadata       map[string]string `json:"metadata"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
	ExpiresAt      *time.Time        `json:"expires_at,omitempty"`
	RotationCount  int               `json:"rotation_count"`
	LastAccessed   time.Time         `json:"last_accessed"`
	AccessCount    int64             `json:"access_count"`
}

// SecretMetadata contains additional information about secrets
type SecretMetadata struct {
	Source         string            `json:"source"`
	Purpose        string            `json:"purpose"`
	Tags           map[string]string `json:"tags"`
	RotationPolicy string            `json:"rotation_policy"`
	BackupLocation string            `json:"backup_location,omitempty"`
}

// NewSecretManager creates a new SecretManager instance
func NewSecretManager(stateManager *state.StateManager, logger *zap.Logger, config *SecretManagerConfig) (*SecretManagerImpl, error) {
	if stateManager == nil {
		return nil, fmt.Errorf("state manager cannot be nil")
	}
	
	if logger == nil {
		return nil, fmt.Errorf("logger cannot be nil")
	}
	
	if config == nil {
		config = DefaultSecretManagerConfig()
	}
	
	// Generate or load encryption key
	encryptionKey, err := generateEncryptionKey(config.EncryptionKeySize)
	if err != nil {
		return nil, fmt.Errorf("failed to generate encryption key: %w", err)
	}
	
	// Create AES-GCM cipher
	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create AES cipher: %w", err)
	}
	
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM cipher: %w", err)
	}
	
	sm := &SecretManagerImpl{
		stateManager:     stateManager,
		logger:           logger,
		encryptionKey:    encryptionKey,
		gcm:              gcm,
		rotationSchedule: make(map[int64]time.Time),
		secretCache:      make(map[int64]*EncryptedSecret),
		config:           config,
		stats: SecretStats{
			LastRotation: time.Time{},
			LastCleanup:  time.Time{},
		},
	}
	
	// Start background tasks
	go sm.startRotationScheduler()
	go sm.startCleanupScheduler()
	
	logger.Info("Secret manager initialized", 
		zap.Bool("encryption_enabled", true),
		zap.Bool("cache_enabled", config.CacheEnabled),
		zap.Int("cache_size", config.CacheSize))
	
	return sm, nil
}

// DefaultSecretManagerConfig returns a default configuration
func DefaultSecretManagerConfig() *SecretManagerConfig {
	return &SecretManagerConfig{
		EncryptionKeySize:       32, // 256-bit AES
		NonceSize:              12, // 96-bit nonce for GCM
		CacheEnabled:           true,
		CacheSize:              1000,
		CacheTTL:               time.Hour,
		DefaultRotationInterval: 30 * 24 * time.Hour, // 30 days
		MaxSecretAge:           90 * 24 * time.Hour,   // 90 days
		RotationRetryInterval:  time.Hour,
		SecretMinLength:        16,
		SecretMaxLength:        256,
		RequireStrongSecrets:   true,
		CleanupInterval:        24 * time.Hour, // Daily cleanup
		RetainHistory:          30 * 24 * time.Hour, // 30 days history
	}
}

// StoreSecret encrypts and stores a robot secret
func (sm *SecretManagerImpl) StoreSecret(robotID int64, secret string, metadata map[string]string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	
	if err := sm.validateSecret(secret); err != nil {
		return fmt.Errorf("secret validation failed: %w", err)
	}
	
	// Generate nonce
	nonce := make([]byte, sm.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return fmt.Errorf("failed to generate nonce: %w", err)
	}
	
	// Encrypt the secret
	encryptedData := sm.gcm.Seal(nil, nonce, []byte(secret), nil)
	
	// Generate checksum
	checksum := sm.generateChecksum(secret)
	
	// Create encrypted secret
	encryptedSecret := &EncryptedSecret{
		RobotID:       robotID,
		EncryptedData: base64.StdEncoding.EncodeToString(encryptedData),
		Nonce:         base64.StdEncoding.EncodeToString(nonce),
		Checksum:      checksum,
		Metadata:      metadata,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
		RotationCount: 0,
		LastAccessed:  time.Now(),
		AccessCount:   0,
	}
	
	// Store in state manager
	if err := sm.storeEncryptedSecret(encryptedSecret); err != nil {
		return fmt.Errorf("failed to store encrypted secret: %w", err)
	}
	
	// Update cache
	if sm.config.CacheEnabled {
		sm.secretCache[robotID] = encryptedSecret
	}
	
	// Update stats
	sm.stats.TotalSecrets++
	sm.stats.EncryptedSecrets++
	
	sm.logger.Info("Secret stored successfully", 
		zap.Int64("robot_id", robotID),
		zap.Int("metadata_keys", len(metadata)))
	
	return nil
}

// RetrieveSecret decrypts and retrieves a robot secret
func (sm *SecretManagerImpl) RetrieveSecret(robotID int64) (string, error) {
	sm.mu.RLock()
	
	// Try cache first
	if sm.config.CacheEnabled {
		if cached, exists := sm.secretCache[robotID]; exists {
			sm.mu.RUnlock()
			return sm.decryptSecret(cached)
		}
	}
	sm.mu.RUnlock()
	
	// Load from state manager
	encryptedSecret, err := sm.loadEncryptedSecret(robotID)
	if err != nil {
		return "", fmt.Errorf("failed to load encrypted secret: %w", err)
	}
	
	if encryptedSecret == nil {
		return "", fmt.Errorf("secret not found for robot %d", robotID)
	}
	
	// Update access tracking
	sm.mu.Lock()
	encryptedSecret.LastAccessed = time.Now()
	encryptedSecret.AccessCount++
	
	// Update cache
	if sm.config.CacheEnabled {
		sm.secretCache[robotID] = encryptedSecret
	}
	sm.mu.Unlock()
	
	// Decrypt and return
	secret, err := sm.decryptSecret(encryptedSecret)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt secret: %w", err)
	}
	
	sm.logger.Debug("Secret retrieved successfully", 
		zap.Int64("robot_id", robotID),
		zap.Int64("access_count", encryptedSecret.AccessCount))
	
	return secret, nil
}

// DeleteSecret removes a robot secret
func (sm *SecretManagerImpl) DeleteSecret(robotID int64) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	
	// Remove from cache
	delete(sm.secretCache, robotID)
	
	// Remove from state manager
	if err := sm.deleteEncryptedSecret(robotID); err != nil {
		return fmt.Errorf("failed to delete encrypted secret: %w", err)
	}
	
	// Remove from rotation schedule
	delete(sm.rotationSchedule, robotID)
	
	// Update stats
	if sm.stats.TotalSecrets > 0 {
		sm.stats.TotalSecrets--
	}
	if sm.stats.EncryptedSecrets > 0 {
		sm.stats.EncryptedSecrets--
	}
	
	sm.logger.Info("Secret deleted successfully", zap.Int64("robot_id", robotID))
	
	return nil
}

// ValidateSecret validates a secret against stored checksum
func (sm *SecretManagerImpl) ValidateSecret(robotID int64, secret string) bool {
	encryptedSecret, err := sm.loadEncryptedSecret(robotID)
	if err != nil {
		return false
	}
	
	if encryptedSecret == nil {
		return false
	}
	
	expectedChecksum := sm.generateChecksum(secret)
	return encryptedSecret.Checksum == expectedChecksum
}

// CompareSecrets compares a secret with the stored one without exposing it
func (sm *SecretManagerImpl) CompareSecrets(robotID int64, secret string) bool {
	storedSecret, err := sm.RetrieveSecret(robotID)
	if err != nil {
		return false
	}
	
	// Use constant-time comparison to prevent timing attacks
	return constantTimeEqual([]byte(secret), []byte(storedSecret))
}

// RotateSecret generates a new secret for a robot
func (sm *SecretManagerImpl) RotateSecret(ctx context.Context, robotID int64) (string, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	
	// Load existing secret metadata
	encryptedSecret, err := sm.loadEncryptedSecret(robotID)
	if err != nil {
		return "", fmt.Errorf("failed to load existing secret: %w", err)
	}
	
	if encryptedSecret == nil {
		return "", fmt.Errorf("no existing secret found for robot %d", robotID)
	}
	
	// Generate new secret
	newSecret, err := sm.generateSecret()
	if err != nil {
		return "", fmt.Errorf("failed to generate new secret: %w", err)
	}
	
	// Update metadata
	metadata := encryptedSecret.Metadata
	if metadata == nil {
		metadata = make(map[string]string)
	}
	metadata["rotated_at"] = time.Now().Format(time.RFC3339)
	metadata["rotation_reason"] = "scheduled"
	
	// Store new secret
	if err := sm.StoreSecret(robotID, newSecret, metadata); err != nil {
		return "", fmt.Errorf("failed to store rotated secret: %w", err)
	}
	
	// Update rotation count
	encryptedSecret.RotationCount++
	
	// Update stats
	sm.stats.LastRotation = time.Now()
	
	sm.logger.Info("Secret rotated successfully", 
		zap.Int64("robot_id", robotID),
		zap.Int("rotation_count", encryptedSecret.RotationCount))
	
	return newSecret, nil
}

// ScheduleRotation schedules a secret rotation
func (sm *SecretManagerImpl) ScheduleRotation(robotID int64, rotateAt time.Time) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	
	sm.rotationSchedule[robotID] = rotateAt
	sm.stats.RotationsPending++
	
	sm.logger.Info("Secret rotation scheduled", 
		zap.Int64("robot_id", robotID),
		zap.Time("rotate_at", rotateAt))
	
	return nil
}

// GetRotationSchedule gets the scheduled rotation time for a robot
func (sm *SecretManagerImpl) GetRotationSchedule(robotID int64) (time.Time, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	
	if rotateAt, exists := sm.rotationSchedule[robotID]; exists {
		return rotateAt, nil
	}
	
	return time.Time{}, fmt.Errorf("no rotation scheduled for robot %d", robotID)
}

// GetSecretMetadata retrieves metadata for a robot secret
func (sm *SecretManagerImpl) GetSecretMetadata(robotID int64) (map[string]string, error) {
	encryptedSecret, err := sm.loadEncryptedSecret(robotID)
	if err != nil {
		return nil, fmt.Errorf("failed to load secret metadata: %w", err)
	}
	
	if encryptedSecret == nil {
		return nil, fmt.Errorf("secret not found for robot %d", robotID)
	}
	
	// Return a copy of metadata to prevent modification
	metadata := make(map[string]string)
	for k, v := range encryptedSecret.Metadata {
		metadata[k] = v
	}
	
	return metadata, nil
}

// UpdateSecretMetadata updates metadata for a robot secret
func (sm *SecretManagerImpl) UpdateSecretMetadata(robotID int64, metadata map[string]string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	
	encryptedSecret, err := sm.loadEncryptedSecret(robotID)
	if err != nil {
		return fmt.Errorf("failed to load secret: %w", err)
	}
	
	if encryptedSecret == nil {
		return fmt.Errorf("secret not found for robot %d", robotID)
	}
	
	// Update metadata
	if encryptedSecret.Metadata == nil {
		encryptedSecret.Metadata = make(map[string]string)
	}
	
	for k, v := range metadata {
		encryptedSecret.Metadata[k] = v
	}
	
	encryptedSecret.UpdatedAt = time.Now()
	
	// Store updated secret
	if err := sm.storeEncryptedSecret(encryptedSecret); err != nil {
		return fmt.Errorf("failed to update secret metadata: %w", err)
	}
	
	// Update cache
	if sm.config.CacheEnabled {
		sm.secretCache[robotID] = encryptedSecret
	}
	
	sm.logger.Info("Secret metadata updated", 
		zap.Int64("robot_id", robotID),
		zap.Int("metadata_keys", len(metadata)))
	
	return nil
}

// CleanupExpiredSecrets removes expired secrets
func (sm *SecretManagerImpl) CleanupExpiredSecrets(ctx context.Context) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	
	now := time.Now()
	expired := make([]int64, 0)
	
	// Find expired secrets
	secrets, err := sm.getAllEncryptedSecrets()
	if err != nil {
		return fmt.Errorf("failed to load secrets for cleanup: %w", err)
	}
	
	for _, secret := range secrets {
		if secret.ExpiresAt != nil && secret.ExpiresAt.Before(now) {
			expired = append(expired, secret.RobotID)
		} else if now.Sub(secret.CreatedAt) > sm.config.MaxSecretAge {
			expired = append(expired, secret.RobotID)
		}
	}
	
	// Remove expired secrets
	for _, robotID := range expired {
		if err := sm.deleteEncryptedSecret(robotID); err != nil {
			sm.logger.Error("Failed to delete expired secret", 
				zap.Int64("robot_id", robotID),
				zap.Error(err))
			continue
		}
		
		// Remove from cache and rotation schedule
		delete(sm.secretCache, robotID)
		delete(sm.rotationSchedule, robotID)
		
		if sm.stats.TotalSecrets > 0 {
			sm.stats.TotalSecrets--
		}
		if sm.stats.EncryptedSecrets > 0 {
			sm.stats.EncryptedSecrets--
		}
		if sm.stats.ExpiredSecrets > 0 {
			sm.stats.ExpiredSecrets--
		}
	}
	
	sm.stats.LastCleanup = now
	
	sm.logger.Info("Expired secrets cleanup completed", 
		zap.Int("expired_count", len(expired)))
	
	return nil
}

// GetSecretStats returns statistics about secret management
func (sm *SecretManagerImpl) GetSecretStats() SecretStats {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	
	// Update pending rotations count
	pending := 0
	now := time.Now()
	for _, rotateAt := range sm.rotationSchedule {
		if rotateAt.Before(now) || rotateAt.Equal(now) {
			pending++
		}
	}
	sm.stats.RotationsPending = pending
	
	return sm.stats
}

// Private helper methods

func (sm *SecretManagerImpl) validateSecret(secret string) error {
	if len(secret) < sm.config.SecretMinLength {
		return fmt.Errorf("secret too short: minimum length is %d", sm.config.SecretMinLength)
	}
	
	if len(secret) > sm.config.SecretMaxLength {
		return fmt.Errorf("secret too long: maximum length is %d", sm.config.SecretMaxLength)
	}
	
	if sm.config.RequireStrongSecrets {
		if !sm.isStrongSecret(secret) {
			return fmt.Errorf("secret does not meet strength requirements")
		}
	}
	
	return nil
}

func (sm *SecretManagerImpl) isStrongSecret(secret string) bool {
	// Check for minimum complexity: at least 3 of 4 character types
	hasLower := false
	hasUpper := false
	hasDigit := false
	hasSpecial := false
	
	for _, char := range secret {
		switch {
		case char >= 'a' && char <= 'z':
			hasLower = true
		case char >= 'A' && char <= 'Z':
			hasUpper = true
		case char >= '0' && char <= '9':
			hasDigit = true
		default:
			hasSpecial = true
		}
	}
	
	complexity := 0
	if hasLower {
		complexity++
	}
	if hasUpper {
		complexity++
	}
	if hasDigit {
		complexity++
	}
	if hasSpecial {
		complexity++
	}
	
	return complexity >= 3
}

func (sm *SecretManagerImpl) generateSecret() (string, error) {
	// Generate a strong random secret
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%^&*"
	length := 32 // Default length
	
	secret := make([]byte, length)
	for i := range secret {
		randomByte := make([]byte, 1)
		if _, err := rand.Read(randomByte); err != nil {
			return "", err
		}
		secret[i] = charset[randomByte[0]%byte(len(charset))]
	}
	
	return string(secret), nil
}

func (sm *SecretManagerImpl) generateChecksum(secret string) string {
	hash := sha256.Sum256([]byte(secret))
	return base64.StdEncoding.EncodeToString(hash[:])
}

func (sm *SecretManagerImpl) decryptSecret(encryptedSecret *EncryptedSecret) (string, error) {
	// Decode base64 data
	encryptedData, err := base64.StdEncoding.DecodeString(encryptedSecret.EncryptedData)
	if err != nil {
		return "", fmt.Errorf("failed to decode encrypted data: %w", err)
	}
	
	nonce, err := base64.StdEncoding.DecodeString(encryptedSecret.Nonce)
	if err != nil {
		return "", fmt.Errorf("failed to decode nonce: %w", err)
	}
	
	// Decrypt
	decryptedData, err := sm.gcm.Open(nil, nonce, encryptedData, nil)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt secret: %w", err)
	}
	
	secret := string(decryptedData)
	
	// Verify checksum
	expectedChecksum := sm.generateChecksum(secret)
	if encryptedSecret.Checksum != expectedChecksum {
		return "", fmt.Errorf("checksum verification failed")
	}
	
	return secret, nil
}

func (sm *SecretManagerImpl) storeEncryptedSecret(encryptedSecret *EncryptedSecret) error {
	// Convert to JSON for storage
	data, err := json.Marshal(encryptedSecret)
	if err != nil {
		return fmt.Errorf("failed to marshal encrypted secret: %w", err)
	}
	
	// Store in state manager as a mapping
	mapping := state.ResourceMapping{
		SourceID:     fmt.Sprintf("robot_secret_%d", encryptedSecret.RobotID),
		TargetID:     fmt.Sprintf("encrypted_secret_%d", encryptedSecret.RobotID),
		ResourceType: "robot_secret",
		SyncStatus:   state.MappingStatusSynced,
		CreatedAt:    encryptedSecret.CreatedAt,
		Metadata: map[string]interface{}{
			"encrypted_data": string(data),
		},
	}
	
	return sm.stateManager.AddMapping(mapping)
}

func (sm *SecretManagerImpl) loadEncryptedSecret(robotID int64) (*EncryptedSecret, error) {
	sourceID := fmt.Sprintf("robot_secret_%d", robotID)
	mapping, err := sm.stateManager.GetMapping(sourceID)
	if err != nil {
		return nil, err
	}
	
	if mapping == nil {
		return nil, nil
	}
	
	// Extract encrypted data from metadata
	encryptedData, ok := mapping.Metadata["encrypted_data"].(string)
	if !ok {
		return nil, fmt.Errorf("invalid encrypted data format")
	}
	
	// Unmarshal encrypted secret
	var encryptedSecret EncryptedSecret
	if err := json.Unmarshal([]byte(encryptedData), &encryptedSecret); err != nil {
		return nil, fmt.Errorf("failed to unmarshal encrypted secret: %w", err)
	}
	
	return &encryptedSecret, nil
}

func (sm *SecretManagerImpl) deleteEncryptedSecret(robotID int64) error {
	sourceID := fmt.Sprintf("robot_secret_%d", robotID)
	return sm.stateManager.DeleteMapping(sourceID)
}

func (sm *SecretManagerImpl) getAllEncryptedSecrets() ([]*EncryptedSecret, error) {
	mappings, err := sm.stateManager.GetMappingsByType("robot_secret")
	if err != nil {
		return nil, err
	}
	
	secrets := make([]*EncryptedSecret, 0, len(mappings))
	for _, mapping := range mappings {
		encryptedData, ok := mapping.Metadata["encrypted_data"].(string)
		if !ok {
			continue
		}
		
		var encryptedSecret EncryptedSecret
		if err := json.Unmarshal([]byte(encryptedData), &encryptedSecret); err != nil {
			sm.logger.Error("Failed to unmarshal encrypted secret", 
				zap.String("mapping_id", mapping.SourceID),
				zap.Error(err))
			continue
		}
		
		secrets = append(secrets, &encryptedSecret)
	}
	
	return secrets, nil
}

func (sm *SecretManagerImpl) startRotationScheduler() {
	ticker := time.NewTicker(sm.config.RotationRetryInterval)
	defer ticker.Stop()
	
	for range ticker.C {
		sm.processScheduledRotations()
	}
}

func (sm *SecretManagerImpl) processScheduledRotations() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	
	now := time.Now()
	for robotID, rotateAt := range sm.rotationSchedule {
		if rotateAt.Before(now) || rotateAt.Equal(now) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			_, err := sm.RotateSecret(ctx, robotID)
			cancel()
			
			if err != nil {
				sm.logger.Error("Failed to rotate scheduled secret", 
					zap.Int64("robot_id", robotID),
					zap.Error(err))
				
				// Reschedule for retry
				sm.rotationSchedule[robotID] = now.Add(sm.config.RotationRetryInterval)
			} else {
				// Remove from schedule
				delete(sm.rotationSchedule, robotID)
				sm.stats.RotationsPending--
			}
		}
	}
}

func (sm *SecretManagerImpl) startCleanupScheduler() {
	ticker := time.NewTicker(sm.config.CleanupInterval)
	defer ticker.Stop()
	
	for range ticker.C {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		if err := sm.CleanupExpiredSecrets(ctx); err != nil {
			sm.logger.Error("Failed to cleanup expired secrets", zap.Error(err))
		}
		cancel()
	}
}

// Utility functions

func generateEncryptionKey(size int) ([]byte, error) {
	key := make([]byte, size)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return key, nil
}

func constantTimeEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	
	var result byte
	for i := 0; i < len(a); i++ {
		result |= a[i] ^ b[i]
	}
	
	return result == 0
}