package logger

import (
	"context"
	"net/http"
	"time"

	"harbor-replicator/pkg/config"
)

// HTTPMiddleware provides correlation ID and logging middleware for HTTP handlers
type HTTPMiddleware struct {
	logger *Logger
	config *config.CorrelationIDConfig
}

// NewHTTPMiddleware creates a new HTTP middleware instance
func NewHTTPMiddleware(logger *Logger, config *config.CorrelationIDConfig) *HTTPMiddleware {
	return &HTTPMiddleware{
		logger: logger,
		config: config,
	}
}

// CorrelationIDMiddleware extracts or generates correlation IDs and adds them to request context
func (m *HTTPMiddleware) CorrelationIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		
		// Try to get correlation ID from header
		correlationID := r.Header.Get(m.config.Header)
		
		// Generate if not present and enabled
		if correlationID == "" && m.config.Enabled {
			correlationID = GenerateCorrelationID(m.config)
		}
		
		// Add correlation ID to context
		if correlationID != "" {
			ctx = WithCorrelationID(ctx, correlationID)
			// Echo back in response header
			w.Header().Set(m.config.Header, correlationID)
		}
		
		// Create request-scoped logger
		requestLogger := m.logger.WithCorrelationIDFromContext(ctx)
		ctx = WithLogger(ctx, requestLogger)
		
		// Continue with the request
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// LoggingMiddleware logs HTTP request and response details
func (m *HTTPMiddleware) LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startTime := time.Now()
		
		// Get logger from context (set by CorrelationIDMiddleware)
		logger := FromContext(r.Context())
		
		// Log request
		logger.LogRequest(r.Method, r.URL.Path, r.ContentLength)
		
		// Wrap response writer to capture status and size
		wrapped := &responseWriter{
			ResponseWriter: w,
			statusCode:     http.StatusOK,
		}
		
		// Process request
		next.ServeHTTP(wrapped, r)
		
		// Log response
		duration := time.Since(startTime)
		logger.LogResponse(wrapped.statusCode, wrapped.size, duration)
	})
}

// CombinedMiddleware combines correlation ID and logging middleware
func (m *HTTPMiddleware) CombinedMiddleware(next http.Handler) http.Handler {
	return m.CorrelationIDMiddleware(m.LoggingMiddleware(next))
}

// responseWriter wraps http.ResponseWriter to capture response details
type responseWriter struct {
	http.ResponseWriter
	statusCode int
	size       int64
}

func (rw *responseWriter) WriteHeader(statusCode int) {
	rw.statusCode = statusCode
	rw.ResponseWriter.WriteHeader(statusCode)
}

func (rw *responseWriter) Write(data []byte) (int, error) {
	size, err := rw.ResponseWriter.Write(data)
	rw.size += int64(size)
	return size, err
}

// Gin middleware helpers for the Gin framework used in health endpoints

// GinCorrelationIDMiddleware is a Gin-compatible correlation ID middleware
func GinCorrelationIDMiddleware(config *config.CorrelationIDConfig, logger *Logger) func(c interface{}) {
	return func(c interface{}) {
		// This is a generic interface{} to avoid importing gin
		// In actual usage, this would be *gin.Context
		// Implementation would be done when integrating with specific HTTP frameworks
	}
}

// ContextFromHTTPRequest extracts context information from HTTP request
func ContextFromHTTPRequest(r *http.Request) context.Context {
	ctx := r.Context()
	
	// Add common HTTP request fields to context for logging
	ctx = context.WithValue(ctx, "method", r.Method)
	ctx = context.WithValue(ctx, "path", r.URL.Path)
	ctx = context.WithValue(ctx, "remote_addr", r.RemoteAddr)
	ctx = context.WithValue(ctx, "user_agent", r.UserAgent())
	
	return ctx
}

// WithHTTPContext creates a logger with HTTP request context
func (l *Logger) WithHTTPContext(r *http.Request) *Logger {
	fields := []interface{}{
		"method", r.Method,
		"path", r.URL.Path,
		"remote_addr", r.RemoteAddr,
		"user_agent", r.UserAgent(),
	}
	
	// Add correlation ID if present
	if correlationID := GetCorrelationID(r.Context()); correlationID != "" {
		fields = append(fields, l.config.CorrelationID.FieldName, correlationID)
	}
	
	return l.WithFields(interfacesToZapFields(fields...)...)
}