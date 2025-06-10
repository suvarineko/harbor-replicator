package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

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