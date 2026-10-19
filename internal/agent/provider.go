package agent

import (
	"context"
	"errors"
	"fmt"
	"github.com/LionJr/expenses/internal/expense"
	"log/slog"
	"net"
	"time"
)

// ProviderError preserves diagnostics without exposing request URLs, credentials,
// provider response bodies or conversation content through Error().
type ProviderError struct {
	Kind     string
	Status   int
	Attempts int
	cause    error
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("LLM request failed: kind=%s status=%d attempts=%d", e.Kind, e.Status, e.Attempts)
}
func (e *ProviderError) Unwrap() error { return e.cause }
func providerFailure(kind string, status, attempts int, cause error) error {
	if errors.Is(cause, context.DeadlineExceeded) {
		kind = "timeout"
	} else if errors.Is(cause, context.Canceled) {
		kind = "canceled"
	} else {
		var network net.Error
		if errors.As(cause, &network) && network.Timeout() {
			kind = "timeout"
		}
	}
	return &ProviderError{Kind: kind, Status: status, Attempts: attempts, cause: cause}
}
func statusKind(status int) string {
	switch {
	case status == 401 || status == 403:
		return "authentication"
	case status == 404:
		return "not_found"
	case status == 429:
		return "rate_limited"
	case status >= 500:
		return "provider_unavailable"
	default:
		return "invalid_request"
	}
}

// ProcessingError leaves the Telegram update retryable when storage state is
// uncertain. Its cause remains inspectable without leaking it through logs.
type ProcessingError struct {
	Operation string
	cause     error
}

func (e *ProcessingError) Error() string { return fmt.Sprintf("agent %s failed", e.Operation) }
func (e *ProcessingError) Unwrap() error { return e.cause }

func logFailure(update int64, operation string, err error, elapsed time.Duration) {
	if errors.Is(err, expense.ErrInvalid) || errors.Is(err, expense.ErrNotFound) {
		return
	}
	args := []any{"update_id", update, "operation", operation, "duration_ms", elapsed.Milliseconds()}
	var provider *ProviderError
	if errors.As(err, &provider) {
		args = append(args, "error_kind", provider.Kind, "status", provider.Status, "attempts", provider.Attempts, "retry_count", max(0, provider.Attempts-1))
	} else {
		kind := "storage"
		if operation == "complete" {
			kind = "provider"
		}
		if errors.Is(err, context.DeadlineExceeded) {
			kind = "timeout"
		} else if errors.Is(err, context.Canceled) {
			kind = "canceled"
		}
		args = append(args, "error_kind", kind, "error_type", fmt.Sprintf("%T", err))
		var sqlError interface{ SQLState() string }
		if errors.As(err, &sqlError) {
			args = append(args, "sqlstate", sqlError.SQLState())
		}
	}
	slog.Warn("agent operation failed", args...)
}
