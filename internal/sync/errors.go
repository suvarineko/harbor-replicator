package sync

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"
)

// ErrorCategory represents different categories of errors for classification
type ErrorCategory int

const (
	// Transient errors that should be retried
	ErrorCategoryTransient ErrorCategory = iota
	// Permanent errors that should not be retried
	ErrorCategoryPermanent
	// Rate limiting errors with specific handling
	ErrorCategoryRateLimit
	// Authentication/authorization errors
	ErrorCategoryAuth
	// Network-related errors
	ErrorCategoryNetwork
	// Timeout errors
	ErrorCategoryTimeout
	// Database connection errors
	ErrorCategoryDatabase
	// Validation errors
	ErrorCategoryValidation
	// Business logic errors
	ErrorCategoryBusiness
	// Circuit breaker errors
	ErrorCategoryCircuitBreaker
)

func (ec ErrorCategory) String() string {
	switch ec {
	case ErrorCategoryTransient:
		return "transient"
	case ErrorCategoryPermanent:
		return "permanent"
	case ErrorCategoryRateLimit:
		return "rate_limit"
	case ErrorCategoryAuth:
		return "auth"
	case ErrorCategoryNetwork:
		return "network"
	case ErrorCategoryTimeout:
		return "timeout"
	case ErrorCategoryDatabase:
		return "database"
	case ErrorCategoryValidation:
		return "validation"
	case ErrorCategoryBusiness:
		return "business"
	case ErrorCategoryCircuitBreaker:
		return "circuit_breaker"
	default:
		return "unknown"
	}
}

// RetryEligibility defines whether an error should be retried
type RetryEligibility int

const (
	RetryEligible RetryEligibility = iota
	RetryNotEligible
	RetryConditional // Depends on additional context
)

// ClassifiedError represents an error with classification metadata
type ClassifiedError struct {
	// Original error
	OriginalError error
	// Error category
	Category ErrorCategory
	// Whether this error should be retried
	Eligibility RetryEligibility
	// HTTP status code if applicable
	StatusCode int
	// Suggested retry delay
	RetryAfter time.Duration
	// Operation that caused the error
	Operation string
	// Additional context
	Context map[string]interface{}
	// Timestamp when error occurred
	Timestamp time.Time
	// Error code for programmatic handling
	Code string
}

func (ce *ClassifiedError) Error() string {
	if ce.OriginalError != nil {
		return ce.OriginalError.Error()
	}
	return fmt.Sprintf("classified error: category=%s, operation=%s, code=%s", 
		ce.Category.String(), ce.Operation, ce.Code)
}

func (ce *ClassifiedError) Unwrap() error {
	return ce.OriginalError
}

func (ce *ClassifiedError) IsRetryable() bool {
	return ce.Eligibility == RetryEligible
}

func (ce *ClassifiedError) GetRetryAfter() time.Duration {
	return ce.RetryAfter
}

func (ce *ClassifiedError) GetCategory() ErrorCategory {
	return ce.Category
}

func (ce *ClassifiedError) GetStatusCode() int {
	return ce.StatusCode
}

func (ce *ClassifiedError) GetCode() string {
	return ce.Code
}

func (ce *ClassifiedError) WithContext(key string, value interface{}) *ClassifiedError {
	if ce.Context == nil {
		ce.Context = make(map[string]interface{})
	}
	ce.Context[key] = value
	return ce
}

// ErrorClassifier interface for classifying errors
type ErrorClassifier interface {
	// Classify analyzes an error and returns classification information
	Classify(err error, operation string) *ClassifiedError
	// IsRetryEligible determines if an error should be retried
	IsRetryEligible(err error) bool
	// ExtractStatusCode extracts HTTP status code from error if available
	ExtractStatusCode(err error) int
	// GetErrorCategory returns the category of the error
	GetErrorCategory(err error) ErrorCategory
}

// DefaultErrorClassifier implements the ErrorClassifier interface
type DefaultErrorClassifier struct {
	customRules []ClassificationRule
}

// ClassificationRule defines custom error classification logic
type ClassificationRule struct {
	// Matcher function to identify if this rule applies
	Matcher func(error) bool
	// Category to assign if matcher returns true
	Category ErrorCategory
	// Retry eligibility
	Eligibility RetryEligibility
	// Optional retry delay
	RetryAfter time.Duration
	// Error code
	Code string
}

// NewDefaultErrorClassifier creates a new default error classifier
func NewDefaultErrorClassifier() *DefaultErrorClassifier {
	return &DefaultErrorClassifier{
		customRules: []ClassificationRule{},
	}
}

// AddCustomRule adds a custom classification rule
func (dec *DefaultErrorClassifier) AddCustomRule(rule ClassificationRule) {
	dec.customRules = append(dec.customRules, rule)
}

// Classify analyzes an error and returns classification information
func (dec *DefaultErrorClassifier) Classify(err error, operation string) *ClassifiedError {
	if err == nil {
		return nil
	}

	classified := &ClassifiedError{
		OriginalError: err,
		Operation:     operation,
		Timestamp:     time.Now(),
		Context:       make(map[string]interface{}),
	}

	// First check custom rules
	for _, rule := range dec.customRules {
		if rule.Matcher(err) {
			classified.Category = rule.Category
			classified.Eligibility = rule.Eligibility
			classified.RetryAfter = rule.RetryAfter
			classified.Code = rule.Code
			return classified
		}
	}

	// Apply default classification logic
	dec.applyDefaultClassification(classified, err)
	return classified
}

// applyDefaultClassification applies built-in classification rules
func (dec *DefaultErrorClassifier) applyDefaultClassification(classified *ClassifiedError, err error) {
	// Check for context errors
	if err == context.DeadlineExceeded {
		classified.Category = ErrorCategoryTimeout
		classified.Eligibility = RetryEligible
		classified.Code = "TIMEOUT_DEADLINE_EXCEEDED"
		return
	}

	if err == context.Canceled {
		classified.Category = ErrorCategoryTimeout
		classified.Eligibility = RetryNotEligible
		classified.Code = "TIMEOUT_CONTEXT_CANCELED"
		return
	}

	// Check for network errors
	if dec.isNetworkError(err) {
		classified.Category = ErrorCategoryNetwork
		classified.Eligibility = RetryEligible
		classified.Code = "NETWORK_ERROR"
		classified.RetryAfter = 2 * time.Second
		return
	}

	// Check for database errors
	if dec.isDatabaseError(err) {
		classified.Category = ErrorCategoryDatabase
		classified.Eligibility = RetryEligible
		classified.Code = "DATABASE_ERROR"
		classified.RetryAfter = 5 * time.Second
		return
	}

	// Check for authentication errors
	if dec.isAuthError(err) {
		classified.Category = ErrorCategoryAuth
		classified.Eligibility = RetryNotEligible
		classified.Code = "AUTH_ERROR"
		return
	}

	// Check for validation errors
	if dec.isValidationError(err) {
		classified.Category = ErrorCategoryValidation
		classified.Eligibility = RetryNotEligible
		classified.Code = "VALIDATION_ERROR"
		return
	}

	// Check for rate limit errors
	if dec.isRateLimitError(err) {
		classified.Category = ErrorCategoryRateLimit
		classified.Eligibility = RetryEligible
		classified.Code = "RATE_LIMIT_ERROR"
		classified.RetryAfter = 60 * time.Second
		return
	}

	// Check for circuit breaker errors
	if dec.isCircuitBreakerError(err) {
		classified.Category = ErrorCategoryCircuitBreaker
		classified.Eligibility = RetryEligible
		classified.Code = "CIRCUIT_BREAKER_ERROR"
		classified.RetryAfter = 30 * time.Second
		return
	}

	// Default to permanent error for unknown types
	classified.Category = ErrorCategoryPermanent
	classified.Eligibility = RetryNotEligible
	classified.Code = "UNKNOWN_ERROR"
}

// IsRetryEligible determines if an error should be retried
func (dec *DefaultErrorClassifier) IsRetryEligible(err error) bool {
	classified := dec.Classify(err, "unknown")
	return classified.IsRetryable()
}

// ExtractStatusCode extracts HTTP status code from error if available
func (dec *DefaultErrorClassifier) ExtractStatusCode(err error) int {
	// Check if it's an HTTP error with status code
	if httpErr, ok := err.(*HTTPError); ok {
		return httpErr.StatusCode
	}

	// Check for response in error message
	errMsg := err.Error()
	if strings.Contains(errMsg, "status code") {
		// Try to extract status code from error message
		// This is a simplified implementation
		if strings.Contains(errMsg, "404") {
			return 404
		}
		if strings.Contains(errMsg, "500") {
			return 500
		}
		if strings.Contains(errMsg, "503") {
			return 503
		}
		if strings.Contains(errMsg, "429") {
			return 429
		}
	}

	return 0
}

// GetErrorCategory returns the category of the error
func (dec *DefaultErrorClassifier) GetErrorCategory(err error) ErrorCategory {
	classified := dec.Classify(err, "unknown")
	return classified.GetCategory()
}

// Helper methods for error type detection

func (dec *DefaultErrorClassifier) isNetworkError(err error) bool {
	if err == nil {
		return false
	}

	// Check for net.Error interface
	if netErr, ok := err.(net.Error); ok {
		return netErr.Timeout() || netErr.Temporary()
	}

	// Check for specific network error types
	if _, ok := err.(*net.OpError); ok {
		return true
	}

	if _, ok := err.(*net.DNSError); ok {
		return true
	}

	// Check for syscall errors
	if opErr, ok := err.(*net.OpError); ok {
		if syscallErr, ok := opErr.Err.(*syscall.Errno); ok {
			switch *syscallErr {
			case syscall.ECONNREFUSED, syscall.ECONNRESET, syscall.ETIMEDOUT:
				return true
			}
		}
	}

	// Check error message for network-related keywords
	errMsg := strings.ToLower(err.Error())
	networkKeywords := []string{
		"connection refused", "connection reset", "connection timeout",
		"network unreachable", "host unreachable", "no route to host",
		"broken pipe", "i/o timeout", "network error",
	}

	for _, keyword := range networkKeywords {
		if strings.Contains(errMsg, keyword) {
			return true
		}
	}

	return false
}

func (dec *DefaultErrorClassifier) isDatabaseError(err error) bool {
	if err == nil {
		return false
	}

	errMsg := strings.ToLower(err.Error())
	dbKeywords := []string{
		"database", "sql", "connection pool", "deadlock",
		"table", "column", "constraint", "transaction",
	}

	for _, keyword := range dbKeywords {
		if strings.Contains(errMsg, keyword) {
			return true
		}
	}

	return false
}

func (dec *DefaultErrorClassifier) isAuthError(err error) bool {
	if err == nil {
		return false
	}

	errMsg := strings.ToLower(err.Error())
	authKeywords := []string{
		"unauthorized", "authentication", "invalid credentials",
		"access denied", "forbidden", "token expired",
		"invalid token", "permission denied",
	}

	for _, keyword := range authKeywords {
		if strings.Contains(errMsg, keyword) {
			return true
		}
	}

	// Check for HTTP status codes
	statusCode := dec.ExtractStatusCode(err)
	return statusCode == 401 || statusCode == 403
}

func (dec *DefaultErrorClassifier) isValidationError(err error) bool {
	if err == nil {
		return false
	}

	errMsg := strings.ToLower(err.Error())
	validationKeywords := []string{
		"validation", "invalid", "required field", "format error",
		"schema", "constraint violation", "bad request",
	}

	for _, keyword := range validationKeywords {
		if strings.Contains(errMsg, keyword) {
			return true
		}
	}

	// Check for HTTP 400 status code
	statusCode := dec.ExtractStatusCode(err)
	return statusCode == 400
}

func (dec *DefaultErrorClassifier) isRateLimitError(err error) bool {
	if err == nil {
		return false
	}

	errMsg := strings.ToLower(err.Error())
	rateLimitKeywords := []string{
		"rate limit", "too many requests", "quota exceeded",
		"throttled", "request limit",
	}

	for _, keyword := range rateLimitKeywords {
		if strings.Contains(errMsg, keyword) {
			return true
		}
	}

	// Check for HTTP 429 status code
	statusCode := dec.ExtractStatusCode(err)
	return statusCode == 429
}

func (dec *DefaultErrorClassifier) isCircuitBreakerError(err error) bool {
	if err == nil {
		return false
	}

	errMsg := strings.ToLower(err.Error())
	circuitBreakerKeywords := []string{
		"circuit breaker", "circuit open", "too many failures",
		"service unavailable",
	}

	for _, keyword := range circuitBreakerKeywords {
		if strings.Contains(errMsg, keyword) {
			return true
		}
	}

	return false
}

// Custom error types

// HTTPError represents an HTTP-related error with status code
type HTTPError struct {
	StatusCode int
	Status     string
	Message    string
	URL        string
	Method     string
}

func (he *HTTPError) Error() string {
	if he.Message != "" {
		return fmt.Sprintf("HTTP %d: %s (%s %s)", he.StatusCode, he.Message, he.Method, he.URL)
	}
	return fmt.Sprintf("HTTP %d: %s (%s %s)", he.StatusCode, he.Status, he.Method, he.URL)
}

// NewHTTPError creates a new HTTP error
func NewHTTPError(statusCode int, status, message, method, url string) *HTTPError {
	return &HTTPError{
		StatusCode: statusCode,
		Status:     status,
		Message:    message,
		Method:     method,
		URL:        url,
	}
}

// DatabaseError represents a database-related error
type DatabaseError struct {
	Operation string
	Query     string
	Message   string
	Transient bool
}

func (de *DatabaseError) Error() string {
	return fmt.Sprintf("database error in %s: %s", de.Operation, de.Message)
}

func (de *DatabaseError) IsTransient() bool {
	return de.Transient
}

// NewDatabaseError creates a new database error
func NewDatabaseError(operation, query, message string, transient bool) *DatabaseError {
	return &DatabaseError{
		Operation: operation,
		Query:     query,
		Message:   message,
		Transient: transient,
	}
}

// ValidationError represents a validation error
type ValidationError struct {
	Field   string
	Value   interface{}
	Message string
	Code    string
}

func (ve *ValidationError) Error() string {
	return fmt.Sprintf("validation error for field '%s': %s", ve.Field, ve.Message)
}

// NewValidationError creates a new validation error
func NewValidationError(field string, value interface{}, message, code string) *ValidationError {
	return &ValidationError{
		Field:   field,
		Value:   value,
		Message: message,
		Code:    code,
	}
}

// BusinessError represents a business logic error
type BusinessError struct {
	Operation string
	Code      string
	Message   string
	Details   map[string]interface{}
}

func (be *BusinessError) Error() string {
	return fmt.Sprintf("business error in %s [%s]: %s", be.Operation, be.Code, be.Message)
}

// NewBusinessError creates a new business error
func NewBusinessError(operation, code, message string) *BusinessError {
	return &BusinessError{
		Operation: operation,
		Code:      code,
		Message:   message,
		Details:   make(map[string]interface{}),
	}
}

func (be *BusinessError) WithDetail(key string, value interface{}) *BusinessError {
	be.Details[key] = value
	return be
}

// Error wrapping utilities

// WrapError wraps an error with additional context while preserving classification
func WrapError(err error, operation, message string) error {
	if err == nil {
		return nil
	}

	// If it's already a classified error, preserve the classification
	if classified, ok := err.(*ClassifiedError); ok {
		wrapped := *classified
		wrapped.Operation = operation
		if message != "" {
			wrapped.OriginalError = fmt.Errorf("%s: %w", message, classified.OriginalError)
		}
		return &wrapped
	}

	// Otherwise, wrap it as a new error
	if message != "" {
		return fmt.Errorf("%s: %w", message, err)
	}
	return err
}

// ChainErrors creates a chain of errors with proper unwrapping support
func ChainErrors(errs ...error) error {
	var validErrors []error
	for _, err := range errs {
		if err != nil {
			validErrors = append(validErrors, err)
		}
	}

	if len(validErrors) == 0 {
		return nil
	}

	if len(validErrors) == 1 {
		return validErrors[0]
	}

	return &ErrorChain{errors: validErrors}
}

// ErrorChain represents a chain of multiple errors
type ErrorChain struct {
	errors []error
}

func (ec *ErrorChain) Error() string {
	if len(ec.errors) == 0 {
		return "no errors"
	}

	if len(ec.errors) == 1 {
		return ec.errors[0].Error()
	}

	var msgs []string
	for _, err := range ec.errors {
		msgs = append(msgs, err.Error())
	}
	return fmt.Sprintf("multiple errors: [%s]", strings.Join(msgs, "; "))
}

func (ec *ErrorChain) Unwrap() error {
	if len(ec.errors) == 0 {
		return nil
	}
	return ec.errors[0]
}

func (ec *ErrorChain) Errors() []error {
	return ec.errors
}

// Is reports whether any error in the chain matches target
func (ec *ErrorChain) Is(target error) bool {
	for _, err := range ec.errors {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// As finds the first error in the chain that matches target
func (ec *ErrorChain) As(target interface{}) bool {
	for _, err := range ec.errors {
		if errors.As(err, target) {
			return true
		}
	}
	return false
}