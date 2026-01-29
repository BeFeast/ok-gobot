package errorx

import (
	"context"
	"fmt"
	"log"
	"runtime/debug"
	"time"
)

// ErrorLevel represents the severity of an error
type ErrorLevel int

const (
	// InfoLevel for informational messages
	InfoLevel ErrorLevel = iota
	// WarningLevel for warnings
	WarningLevel
	// ErrLevel for errors
	ErrLevel
	// CriticalLevel for critical errors
	CriticalLevel
)

// Handler provides centralized error handling
type Handler struct {
	// RecoveryEnabled determines if panics should be recovered
	RecoveryEnabled bool
	// LogStackTraces determines if stack traces should be logged
	LogStackTraces bool
	// OnCritical callback for critical errors
	OnCritical func(error)
}

// NewHandler creates a new error handler
func NewHandler() *Handler {
	return &Handler{
		RecoveryEnabled: true,
		LogStackTraces:  true,
	}
}

// Handle processes an error with the given level
func (h *Handler) Handle(err error, level ErrorLevel, msg string) {
	if err == nil {
		return
	}

	formatted := fmt.Sprintf("[%s] %s: %v", h.levelString(level), msg, err)

	switch level {
	case InfoLevel:
		log.Printf("ℹ️  %s", formatted)
	case WarningLevel:
		log.Printf("⚠️  %s", formatted)
	case ErrLevel:
		log.Printf("❌ %s", formatted)
		if h.LogStackTraces {
			log.Printf("Stack trace:\n%s", debug.Stack())
		}
	case CriticalLevel:
		log.Printf("🚨 %s", formatted)
		if h.LogStackTraces {
			log.Printf("Stack trace:\n%s", debug.Stack())
		}
		if h.OnCritical != nil {
			h.OnCritical(err)
		}
	}
}

// HandleWithRecovery wraps a function with panic recovery
func (h *Handler) HandleWithRecovery(fn func() error) (err error) {
	if !h.RecoveryEnabled {
		return fn()
	}

	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic recovered: %v\n%s", r, debug.Stack())
			h.Handle(err, CriticalLevel, "Panic recovered")
		}
	}()

	return fn()
}

// HandleWithTimeout runs a function with a timeout
func (h *Handler) HandleWithTimeout(ctx context.Context, timeout time.Duration, fn func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- fn(ctx)
	}()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		err := fmt.Errorf("operation timed out after %v", timeout)
		h.Handle(err, ErrLevel, "Timeout")
		return err
	}
}

// levelString returns the string representation of an error level
func (h *Handler) levelString(level ErrorLevel) string {
	switch level {
	case InfoLevel:
		return "INFO"
	case WarningLevel:
		return "WARN"
	case ErrLevel:
		return "ERROR"
	case CriticalLevel:
		return "CRITICAL"
	default:
		return "UNKNOWN"
	}
}

// UserError represents an error that can be shown to users
type UserError struct {
	Message string
	Err     error
}

// Error implements the error interface
func (e UserError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Err)
	}
	return e.Message
}

// NewUserError creates a new user-friendly error
func NewUserError(msg string, err error) UserError {
	return UserError{Message: msg, Err: err}
}

// IsUserError checks if an error is a UserError
func IsUserError(err error) bool {
	_, ok := err.(UserError)
	return ok
}

// DefaultHandler is the default error handler instance
var DefaultHandler = NewHandler()

// Handle is a convenience function using the default handler
func Handle(err error, level ErrorLevel, msg string) {
	DefaultHandler.Handle(err, level, msg)
}

// HandleWithRecovery is a convenience function using the default handler
func HandleWithRecovery(fn func() error) error {
	return DefaultHandler.HandleWithRecovery(fn)
}
