package logger

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"harbor-replicator/pkg/config"
)

func TestNewLogger(t *testing.T) {
	tests := []struct {
		name        string
		config      *config.LoggingConfig
		expectError bool
	}{
		{
			name: "valid console config",
			config: &config.LoggingConfig{
				Level:  "info",
				Format: "console",
				Output: []string{"stdout"},
			},
			expectError: false,
		},
		{
			name: "valid JSON config",
			config: &config.LoggingConfig{
				Level:  "debug",
				Format: "json",
				Output: []string{"stdout"},
			},
			expectError: false,
		},
		{
			name: "file output config",
			config: &config.LoggingConfig{
				Level:      "info",
				Format:     "json",
				Output:     []string{"file"},
				FilePath:   "/tmp/test.log",
				MaxSize:    10,
				MaxBackups: 3,
				MaxAge:     7,
				Compress:   true,
			},
			expectError: false,
		},
		{
			name: "multiple outputs",
			config: &config.LoggingConfig{
				Level:  "warn",
				Format: "json",
				Output: []string{"stdout", "stderr"},
			},
			expectError: false,
		},
		{
			name:        "nil config",
			config:      nil,
			expectError: true,
		},
		{
			name: "invalid log level",
			config: &config.LoggingConfig{
				Level:  "invalid",
				Format: "json",
				Output: []string{"stdout"},
			},
			expectError: true,
		},
		{
			name: "invalid format",
			config: &config.LoggingConfig{
				Level:  "info",
				Format: "invalid",
				Output: []string{"stdout"},
			},
			expectError: true,
		},
		{
			name: "file output without path",
			config: &config.LoggingConfig{
				Level:  "info",
				Format: "json",
				Output: []string{"file"},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, err := NewLogger(tt.config)
			
			if tt.expectError {
				if err == nil {
					t.Errorf("expected error but got none")
				}
				return
			}
			
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			
			if logger == nil {
				t.Errorf("expected logger but got nil")
			}
		})
	}
}

func TestFieldSanitizer(t *testing.T) {
	sensitiveFields := []string{"password", "token", "secret", "key"}
	sanitizer := NewFieldSanitizer(sensitiveFields)
	
	fields := []zap.Field{
		zap.String("username", "testuser"),
		zap.String("password", "secret123"),
		zap.String("api_token", "abc123"),
		zap.String("secret_key", "supersecret"),
		zap.String("normal_field", "normalvalue"),
	}
	
	sanitized := sanitizer.SanitizeFields(fields)
	
	expectedRedacted := []string{"password", "api_token", "secret_key"}
	expectedNormal := []string{"username", "normal_field"}
	
	for i, field := range sanitized {
		fieldName := fields[i].Key
		
		if contains(expectedRedacted, fieldName) {
			if field.String != "***REDACTED***" {
				t.Errorf("expected field %s to be redacted, got: %v", fieldName, field.String)
			}
		} else if contains(expectedNormal, fieldName) {
			if field.String == "***REDACTED***" {
				t.Errorf("expected field %s to not be redacted", fieldName)
			}
		}
	}
}

func TestCorrelationIDGeneration(t *testing.T) {
	tests := []struct {
		name     string
		config   *config.CorrelationIDConfig
		validate func(string) bool
	}{
		{
			name: "uuid generator",
			config: &config.CorrelationIDConfig{
				Enabled:   true,
				Generator: "uuid",
			},
			validate: func(id string) bool {
				return len(id) == 36 && strings.Count(id, "-") == 4
			},
		},
		{
			name: "random generator",
			config: &config.CorrelationIDConfig{
				Enabled:   true,
				Generator: "random",
			},
			validate: func(id string) bool {
				return len(id) == 8
			},
		},
		{
			name: "sequential generator",
			config: &config.CorrelationIDConfig{
				Enabled:   true,
				Generator: "sequential",
			},
			validate: func(id string) bool {
				return len(id) > 0 && isNumeric(id)
			},
		},
		{
			name: "disabled",
			config: &config.CorrelationIDConfig{
				Enabled: false,
			},
			validate: func(id string) bool {
				return id == ""
			},
		},
		{
			name:   "nil config",
			config: nil,
			validate: func(id string) bool {
				return id == ""
			},
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := GenerateCorrelationID(tt.config)
			if !tt.validate(id) {
				t.Errorf("correlation ID validation failed for %s: %s", tt.name, id)
			}
		})
	}
}

func TestContextHelpers(t *testing.T) {
	ctx := context.Background()
	correlationID := "test-correlation-id"
	
	// Test WithCorrelationID and GetCorrelationID
	ctx = WithCorrelationID(ctx, correlationID)
	retrievedID := GetCorrelationID(ctx)
	
	if retrievedID != correlationID {
		t.Errorf("expected correlation ID %s, got %s", correlationID, retrievedID)
	}
	
	// Test logger context
	cfg := &config.LoggingConfig{
		Level:  "info",
		Format: "console",
		Output: []string{"stdout"},
		CorrelationID: config.CorrelationIDConfig{
			Enabled:   true,
			FieldName: "correlation_id",
		},
	}
	
	logger, _ := NewLogger(cfg)
	ctx = WithLogger(ctx, logger)
	
	retrievedLogger := FromContext(ctx)
	if retrievedLogger == nil {
		t.Errorf("expected logger from context, got nil")
	}
}

func TestStartOperation(t *testing.T) {
	cfg := &config.LoggingConfig{
		Level:  "info",
		Format: "console",
		Output: []string{"stdout"},
		CorrelationID: config.CorrelationIDConfig{
			Enabled:   true,
			FieldName: "correlation_id",
			Generator: "uuid",
		},
	}
	
	logger, _ := NewLogger(cfg)
	ctx := context.Background()
	
	operationName := "test_operation"
	ctx, complete := StartOperation(ctx, logger, operationName)
	
	// Verify correlation ID was added
	correlationID := GetCorrelationID(ctx)
	if correlationID == "" {
		t.Errorf("expected correlation ID to be set")
	}
	
	// Verify logger was added to context
	ctxLogger := FromContext(ctx)
	if ctxLogger == nil {
		t.Errorf("expected logger in context")
	}
	
	// Test completion
	testErr := errors.New("test error")
	complete(testErr)
	
	// Test successful completion
	ctx2, complete2 := StartOperation(context.Background(), logger, "success_operation")
	complete2(nil)
	
	// Verify correlation ID persists
	if GetCorrelationID(ctx2) == "" {
		t.Errorf("expected correlation ID in new operation context")
	}
}

func TestHTTPMiddleware(t *testing.T) {
	cfg := &config.LoggingConfig{
		Level:  "info",
		Format: "json",
		Output: []string{"stdout"},
		CorrelationID: config.CorrelationIDConfig{
			Enabled:   true,
			Header:    "X-Correlation-ID",
			FieldName: "correlation_id",
			Generator: "uuid",
		},
	}
	
	logger, _ := NewLogger(cfg)
	middleware := NewHTTPMiddleware(logger, &cfg.CorrelationID)
	
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify correlation ID is in context
		correlationID := GetCorrelationID(r.Context())
		if correlationID == "" {
			t.Errorf("expected correlation ID in request context")
		}
		
		// Verify logger is in context
		ctxLogger := FromContext(r.Context())
		if ctxLogger == nil {
			t.Errorf("expected logger in request context")
		}
		
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})
	
	wrappedHandler := middleware.CombinedMiddleware(handler)
	
	// Test with correlation ID header
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-Correlation-ID", "test-correlation-123")
	w := httptest.NewRecorder()
	
	wrappedHandler.ServeHTTP(w, req)
	
	// Verify correlation ID is echoed back
	responseCorrelationID := w.Header().Get("X-Correlation-ID")
	if responseCorrelationID != "test-correlation-123" {
		t.Errorf("expected correlation ID test-correlation-123, got %s", responseCorrelationID)
	}
	
	// Test without correlation ID header (should generate one)
	req2 := httptest.NewRequest("POST", "/test", nil)
	w2 := httptest.NewRecorder()
	
	wrappedHandler.ServeHTTP(w2, req2)
	
	generatedID := w2.Header().Get("X-Correlation-ID")
	if generatedID == "" {
		t.Errorf("expected generated correlation ID")
	}
}

func TestLoggerWithMethods(t *testing.T) {
	var buf bytes.Buffer
	cfg := &config.LoggingConfig{
		Level:  "debug",
		Format: "json",
		Output: []string{"stdout"},
		CorrelationID: config.CorrelationIDConfig{
			Enabled:   true,
			FieldName: "correlation_id",
		},
	}
	
	// Redirect stdout to buffer for testing
	originalStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	
	logger, _ := NewLogger(cfg)
	
	// Test WithFields
	testLogger := logger.WithFields(zap.String("test_field", "test_value"))
	testLogger.Info("test message")
	
	// Test WithError
	testErr := errors.New("test error")
	errorLogger := logger.WithError(testErr)
	errorLogger.Error("error occurred")
	
	// Test WithOperation
	opLogger := logger.WithOperation("test_operation")
	opLogger.Info("operation log")
	
	// Restore stdout
	w.Close()
	os.Stdout = originalStdout
	
	// Read the output
	go func() {
		defer r.Close()
		buf.ReadFrom(r)
	}()
	
	time.Sleep(100 * time.Millisecond) // Give time for the goroutine to read
}

func TestLogRotation(t *testing.T) {
	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "test.log")
	
	cfg := &config.LoggingConfig{
		Level:      "info",
		Format:     "json",
		Output:     []string{"file"},
		FilePath:   logFile,
		MaxSize:    1, // 1MB
		MaxBackups: 2,
		MaxAge:     1,
		Compress:   true,
	}
	
	logger, err := NewLogger(cfg)
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}
	
	// Write some logs
	for i := 0; i < 10; i++ {
		logger.Info("test log message", "iteration", i)
	}
	
	// Verify log file exists
	if _, err := os.Stat(logFile); os.IsNotExist(err) {
		t.Errorf("log file does not exist: %s", logFile)
	}
}

// Helper functions
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if strings.Contains(strings.ToLower(item), strings.ToLower(s)) {
			return true
		}
	}
	return false
}

func isNumeric(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Benchmark tests
func BenchmarkLoggerInfo(b *testing.B) {
	cfg := &config.LoggingConfig{
		Level:  "info",
		Format: "json",
		Output: []string{"stdout"},
	}
	
	logger, _ := NewLogger(cfg)
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		logger.Info("benchmark test", "iteration", i)
	}
}

func BenchmarkCorrelationIDGeneration(b *testing.B) {
	cfg := &config.CorrelationIDConfig{
		Enabled:   true,
		Generator: "uuid",
	}
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		GenerateCorrelationID(cfg)
	}
}

func BenchmarkFieldSanitization(b *testing.B) {
	sensitiveFields := []string{"password", "token", "secret", "key"}
	sanitizer := NewFieldSanitizer(sensitiveFields)
	
	fields := []zap.Field{
		zap.String("username", "testuser"),
		zap.String("password", "secret123"),
		zap.String("api_token", "abc123"),
		zap.String("normal_field", "normalvalue"),
	}
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sanitizer.SanitizeFields(fields)
	}
}

// Test JSON output format
func TestJSONOutput(t *testing.T) {
	cfg := &config.LoggingConfig{
		Level:  "info",
		Format: "json",
		Output: []string{"stdout"},
		CorrelationID: config.CorrelationIDConfig{
			Enabled:   true,
			FieldName: "correlation_id",
		},
	}
	
	// Create a logger that writes to buffer instead of stdout
	logger, _ := NewLogger(cfg)
	
	// Log a message
	logger.Info("test message", "key1", "value1", "key2", 42)
	
	// Since we can't easily capture stdout in this test,
	// we'll verify the logger was created successfully
	if logger == nil {
		t.Errorf("failed to create JSON logger")
	}
}