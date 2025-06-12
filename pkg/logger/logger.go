package logger

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"

	"harbor-replicator/pkg/config"
)

type Logger struct {
	*zap.Logger
	config     *config.LoggingConfig
	sanitizer  *FieldSanitizer
}

type FieldSanitizer struct {
	sensitiveFields map[string]bool
}

func NewLogger(cfg *config.LoggingConfig) (*Logger, error) {
	if cfg == nil {
		return nil, fmt.Errorf("logging config is nil")
	}

	level, err := parseLogLevel(cfg.Level)
	if err != nil {
		return nil, fmt.Errorf("invalid log level: %w", err)
	}

	encoder, err := createEncoder(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create encoder: %w", err)
	}

	cores := make([]zapcore.Core, 0, len(cfg.Output))
	
	for _, output := range cfg.Output {
		var writer zapcore.WriteSyncer
		
		switch output {
		case "stdout":
			writer = zapcore.AddSync(os.Stdout)
		case "stderr":
			writer = zapcore.AddSync(os.Stderr)
		case "file":
			if cfg.FilePath == "" {
				return nil, fmt.Errorf("file_path is required when using file output")
			}
			
			if err := os.MkdirAll(filepath.Dir(cfg.FilePath), 0755); err != nil {
				return nil, fmt.Errorf("failed to create log directory: %w", err)
			}
			
			fileWriter := &lumberjack.Logger{
				Filename:   cfg.FilePath,
				MaxSize:    cfg.MaxSize,
				MaxBackups: cfg.MaxBackups,
				MaxAge:     cfg.MaxAge,
				Compress:   cfg.Compress,
			}
			writer = zapcore.AddSync(fileWriter)
		default:
			return nil, fmt.Errorf("unsupported output type: %s", output)
		}
		
		core := zapcore.NewCore(encoder, writer, level)
		cores = append(cores, core)
	}

	combinedCore := zapcore.NewTee(cores...)
	
	zapLogger := zap.New(combinedCore, zap.AddCaller(), zap.AddCallerSkip(1))

	sanitizer := NewFieldSanitizer(cfg.SanitizeFields)
	
	logger := &Logger{
		Logger:    zapLogger,
		config:    cfg,
		sanitizer: sanitizer,
	}

	return logger, nil
}

func (l *Logger) SafeLog(level zapcore.Level, msg string, fields ...zap.Field) {
	sanitizedFields := l.sanitizer.SanitizeFields(fields)
	l.Logger.Log(level, msg, sanitizedFields...)
}

func (l *Logger) Debug(msg string, fields ...interface{}) {
	l.SafeLog(zapcore.DebugLevel, msg, interfacesToZapFields(fields...)...)
}

func (l *Logger) Info(msg string, fields ...interface{}) {
	l.SafeLog(zapcore.InfoLevel, msg, interfacesToZapFields(fields...)...)
}

func (l *Logger) Warn(msg string, fields ...interface{}) {
	l.SafeLog(zapcore.WarnLevel, msg, interfacesToZapFields(fields...)...)
}

func (l *Logger) Error(msg string, fields ...interface{}) {
	l.SafeLog(zapcore.ErrorLevel, msg, interfacesToZapFields(fields...)...)
}

func (l *Logger) WithCorrelationID(correlationID string) *Logger {
	newLogger := l.Logger.With(zap.String(l.config.CorrelationID.FieldName, correlationID))
	return &Logger{
		Logger:    newLogger,
		config:    l.config,
		sanitizer: l.sanitizer,
	}
}

func (l *Logger) WithFields(fields ...zap.Field) *Logger {
	sanitizedFields := l.sanitizer.SanitizeFields(fields)
	newLogger := l.Logger.With(sanitizedFields...)
	return &Logger{
		Logger:    newLogger,
		config:    l.config,
		sanitizer: l.sanitizer,
	}
}

func NewFieldSanitizer(sensitiveFields []string) *FieldSanitizer {
	fieldMap := make(map[string]bool)
	for _, field := range sensitiveFields {
		fieldMap[strings.ToLower(field)] = true
	}
	
	return &FieldSanitizer{
		sensitiveFields: fieldMap,
	}
}

func (fs *FieldSanitizer) SanitizeFields(fields []zap.Field) []zap.Field {
	if len(fs.sensitiveFields) == 0 {
		return fields
	}
	
	sanitized := make([]zap.Field, len(fields))
	
	for i, field := range fields {
		if fs.isSensitiveField(field.Key) {
			sanitized[i] = zap.String(field.Key, "***REDACTED***")
		} else {
			sanitized[i] = field
		}
	}
	
	return sanitized
}

func (fs *FieldSanitizer) isSensitiveField(fieldName string) bool {
	lowerFieldName := strings.ToLower(fieldName)
	
	if fs.sensitiveFields[lowerFieldName] {
		return true
	}
	
	for sensitiveField := range fs.sensitiveFields {
		if strings.Contains(lowerFieldName, sensitiveField) {
			return true
		}
	}
	
	return false
}

func parseLogLevel(level string) (zapcore.Level, error) {
	switch strings.ToLower(level) {
	case "debug":
		return zapcore.DebugLevel, nil
	case "info":
		return zapcore.InfoLevel, nil
	case "warn", "warning":
		return zapcore.WarnLevel, nil
	case "error":
		return zapcore.ErrorLevel, nil
	case "fatal":
		return zapcore.FatalLevel, nil
	default:
		return zapcore.InfoLevel, fmt.Errorf("unknown log level: %s", level)
	}
}

func createEncoder(cfg *config.LoggingConfig) (zapcore.Encoder, error) {
	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.TimeKey = "timestamp"
	encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	
	switch strings.ToLower(cfg.Format) {
	case "json":
		return zapcore.NewJSONEncoder(encoderConfig), nil
	case "console":
		return zapcore.NewConsoleEncoder(encoderConfig), nil
	default:
		return nil, fmt.Errorf("unsupported log format: %s", cfg.Format)
	}
}

func interfacesToZapFields(fields ...interface{}) []zap.Field {
	if len(fields)%2 != 0 {
		return []zap.Field{zap.String("error", "odd number of fields provided")}
	}
	
	zapFields := make([]zap.Field, 0, len(fields)/2)
	
	for i := 0; i < len(fields); i += 2 {
		key, ok := fields[i].(string)
		if !ok {
			zapFields = append(zapFields, zap.String("error", fmt.Sprintf("non-string key at index %d", i)))
			continue
		}
		
		value := fields[i+1]
		zapFields = append(zapFields, zap.Any(key, value))
	}
	
	return zapFields
}

type NoOpLogger struct{}

func (l *NoOpLogger) Debug(msg string, fields ...interface{}) {}
func (l *NoOpLogger) Info(msg string, fields ...interface{})  {}
func (l *NoOpLogger) Warn(msg string, fields ...interface{})  {}
func (l *NoOpLogger) Error(msg string, fields ...interface{}) {}

func NewNoOpLogger() *NoOpLogger {
	return &NoOpLogger{}
}

// Context keys for logger correlation
type contextKey string

const (
	correlationIDKey contextKey = "correlation_id"
	loggerKey        contextKey = "logger"
)

// GenerateCorrelationID generates a new correlation ID based on the configured generator
func GenerateCorrelationID(cfg *config.CorrelationIDConfig) string {
	if cfg == nil || !cfg.Enabled {
		return ""
	}

	switch strings.ToLower(cfg.Generator) {
	case "uuid":
		return uuid.New().String()
	case "random":
		// Generate a random 8-character string
		const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
		result := make([]byte, 8)
		for i := range result {
			num, _ := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
			result[i] = charset[num.Int64()]
		}
		return string(result)
	case "sequential":
		// Use timestamp-based sequential ID
		return fmt.Sprintf("%d", time.Now().UnixNano())
	default:
		return uuid.New().String()
	}
}

// WithCorrelationIDFromContext creates a logger with correlation ID from context
func (l *Logger) WithCorrelationIDFromContext(ctx context.Context) *Logger {
	if correlationID, ok := ctx.Value(correlationIDKey).(string); ok && correlationID != "" {
		return l.WithCorrelationID(correlationID)
	}
	return l
}

// WithContext extracts common contextual information and creates a logger with those fields
func (l *Logger) WithContext(ctx context.Context) *Logger {
	fields := make([]zap.Field, 0, 4)
	
	// Add correlation ID if present
	if correlationID, ok := ctx.Value(correlationIDKey).(string); ok && correlationID != "" {
		fields = append(fields, zap.String(l.config.CorrelationID.FieldName, correlationID))
	}
	
	// Add user ID if present (common pattern)
	if userID, ok := ctx.Value("user_id").(string); ok && userID != "" {
		fields = append(fields, zap.String("user_id", userID))
	}
	
	// Add tenant ID if present
	if tenantID, ok := ctx.Value("tenant_id").(string); ok && tenantID != "" {
		fields = append(fields, zap.String("tenant_id", tenantID))
	}
	
	// Add operation name if present
	if operation, ok := ctx.Value("operation").(string); ok && operation != "" {
		fields = append(fields, zap.String("operation", operation))
	}

	if len(fields) > 0 {
		return l.WithFields(fields...)
	}
	return l
}

// WithError adds error information with optional stack trace
func (l *Logger) WithError(err error) *Logger {
	if err == nil {
		return l
	}
	return l.WithFields(zap.Error(err))
}

// WithOperation adds operation tracking fields
func (l *Logger) WithOperation(operationName string) *Logger {
	return l.WithFields(
		zap.String("operation", operationName),
		zap.Time("operation_start", time.Now()),
	)
}

// LogOperationComplete logs operation completion with duration
func (l *Logger) LogOperationComplete(operationName string, startTime time.Time, err error) {
	duration := time.Since(startTime)
	fields := []zap.Field{
		zap.String("operation", operationName),
		zap.Duration("duration", duration),
		zap.Time("completed_at", time.Now()),
	}
	
	if err != nil {
		fields = append(fields, zap.Error(err))
		l.SafeLog(zapcore.ErrorLevel, "Operation failed", fields...)
	} else {
		l.SafeLog(zapcore.InfoLevel, "Operation completed", fields...)
	}
}

// Context helpers for setting and getting correlation ID and logger

// WithCorrelationID adds correlation ID to context
func WithCorrelationID(ctx context.Context, correlationID string) context.Context {
	return context.WithValue(ctx, correlationIDKey, correlationID)
}

// GetCorrelationID retrieves correlation ID from context
func GetCorrelationID(ctx context.Context) string {
	if correlationID, ok := ctx.Value(correlationIDKey).(string); ok {
		return correlationID
	}
	return ""
}

// WithLogger adds logger to context
func WithLogger(ctx context.Context, logger *Logger) context.Context {
	return context.WithValue(ctx, loggerKey, logger)
}

// FromContext retrieves logger from context, returns a no-op logger if not found
func FromContext(ctx context.Context) *Logger {
	if logger, ok := ctx.Value(loggerKey).(*Logger); ok {
		return logger
	}
	// Return a basic logger if none found in context
	cfg := &config.LoggingConfig{
		Level:  "info",
		Format: "console",
		Output: []string{"stdout"},
	}
	logger, _ := NewLogger(cfg)
	return logger
}

// Operation tracking helpers

// StartOperation begins an operation and returns a context with logger and start time
func StartOperation(ctx context.Context, logger *Logger, operationName string) (context.Context, func(error)) {
	startTime := time.Now()
	correlationID := GetCorrelationID(ctx)
	if correlationID == "" {
		correlationID = GenerateCorrelationID(&logger.config.CorrelationID)
		ctx = WithCorrelationID(ctx, correlationID)
	}
	
	opLogger := logger.WithCorrelationID(correlationID).WithOperation(operationName)
	ctx = WithLogger(ctx, opLogger)
	
	opLogger.Info("Operation started", zap.String("operation", operationName))
	
	return ctx, func(err error) {
		opLogger.LogOperationComplete(operationName, startTime, err)
	}
}

// RequestLogging helpers for HTTP request/response logging
func (l *Logger) LogRequest(method, path string, size int64) {
	l.Info("HTTP request",
		zap.String("method", method),
		zap.String("path", path),
		zap.Int64("request_size", size),
	)
}

func (l *Logger) LogResponse(statusCode int, size int64, duration time.Duration) {
	level := zapcore.InfoLevel
	if statusCode >= 400 {
		level = zapcore.WarnLevel
	}
	if statusCode >= 500 {
		level = zapcore.ErrorLevel
	}
	
	l.SafeLog(level, "HTTP response",
		zap.Int("status_code", statusCode),
		zap.Int64("response_size", size),
		zap.Duration("duration", duration),
	)
}