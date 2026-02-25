package llm

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// FailoverReason describes why a provider failed.
type FailoverReason string

const (
	FailoverAuth       FailoverReason = "auth"       // 401/403
	FailoverRateLimit  FailoverReason = "rate_limit" // 429
	FailoverBilling    FailoverReason = "billing"    // 402
	FailoverTimeout    FailoverReason = "timeout"    // 408/504/deadline
	FailoverOverloaded FailoverReason = "overloaded" // 500/502/503/529
	FailoverFormat     FailoverReason = "format"     // 400
	FailoverUnknown    FailoverReason = "unknown"    // unknown error (treated as retriable)
)

// FailoverError wraps an LLM provider error with classification metadata.
type FailoverError struct {
	Reason   FailoverReason
	Provider string
	Model    string
	Status   int
	Wrapped  error
}

func (e *FailoverError) Error() string {
	if e.Status > 0 {
		return fmt.Sprintf("%s/%s failover (%s, status %d): %v",
			e.Provider, e.Model, e.Reason, e.Status, e.Wrapped)
	}
	return fmt.Sprintf("%s/%s failover (%s): %v",
		e.Provider, e.Model, e.Reason, e.Wrapped)
}

func (e *FailoverError) Unwrap() error {
	return e.Wrapped
}

// IsRetriable returns true if this error should trigger a fallback attempt.
// Auth and format errors are never retriable.
func (e *FailoverError) IsRetriable() bool {
	return e.Reason != FailoverFormat && e.Reason != FailoverAuth && e.Reason != FailoverBilling
}

// FallbackExhaustedError is returned when all candidates have been tried and failed.
type FallbackExhaustedError struct {
	Errors []*FailoverError
}

func (e *FallbackExhaustedError) Error() string {
	if len(e.Errors) == 0 {
		return "all fallback candidates exhausted"
	}
	parts := make([]string, len(e.Errors))
	for i, fe := range e.Errors {
		parts[i] = fe.Error()
	}
	return fmt.Sprintf("all fallback candidates exhausted: [%s]", strings.Join(parts, "; "))
}

// statusRegex matches provider error patterns like "openai error (status 429): ..."
var statusRegex = regexp.MustCompile(`\(status (\d+)\)`)

// ClassifyError wraps a raw provider error into a FailoverError with the
// appropriate reason. It extracts HTTP status codes from known provider error
// formats and falls back to message pattern matching.
func ClassifyError(err error, provider, model string) *FailoverError {
	fe := &FailoverError{
		Provider: provider,
		Model:    model,
		Wrapped:  err,
	}

	msg := err.Error()

	// Try to extract HTTP status code from provider error format
	if matches := statusRegex.FindStringSubmatch(msg); len(matches) == 2 {
		if status, parseErr := strconv.Atoi(matches[1]); parseErr == nil {
			fe.Status = status
			fe.Reason = reasonFromStatus(status)
			return fe
		}
	}

	// Fallback: message pattern matching
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "unauthorized") || strings.Contains(lower, "authentication") ||
		strings.Contains(lower, "invalid api key") || strings.Contains(lower, "permission denied"):
		fe.Reason = FailoverAuth
	case strings.Contains(lower, "rate limit") || strings.Contains(lower, "too many requests"):
		fe.Reason = FailoverRateLimit
	case strings.Contains(lower, "timeout") || strings.Contains(lower, "deadline exceeded") ||
		strings.Contains(lower, "context deadline"):
		fe.Reason = FailoverTimeout
	case strings.Contains(lower, "overloaded") || strings.Contains(lower, "service unavailable") ||
		strings.Contains(lower, "bad gateway"):
		fe.Reason = FailoverOverloaded
	default:
		fe.Reason = FailoverUnknown
	}

	return fe
}

func reasonFromStatus(status int) FailoverReason {
	switch status {
	case 400:
		return FailoverFormat
	case 401, 403:
		return FailoverAuth
	case 402:
		return FailoverBilling
	case 429:
		return FailoverRateLimit
	case 408, 504:
		return FailoverTimeout
	case 500, 502, 503, 529:
		return FailoverOverloaded
	default:
		return FailoverUnknown
	}
}
