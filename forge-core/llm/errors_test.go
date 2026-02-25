package llm

import (
	"fmt"
	"testing"
)

func TestClassifyError_StatusCodes(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantReason FailoverReason
		wantStatus int
	}{
		{
			name:       "openai 429 rate limit",
			err:        fmt.Errorf("openai error (status 429): rate limit exceeded"),
			wantReason: FailoverRateLimit,
			wantStatus: 429,
		},
		{
			name:       "anthropic 503 overloaded",
			err:        fmt.Errorf("anthropic error (status 503): service unavailable"),
			wantReason: FailoverOverloaded,
			wantStatus: 503,
		},
		{
			name:       "openai stream 429",
			err:        fmt.Errorf("openai stream error (status 429): too many requests"),
			wantReason: FailoverRateLimit,
			wantStatus: 429,
		},
		{
			name:       "anthropic stream 503",
			err:        fmt.Errorf("anthropic stream error (status 503): overloaded"),
			wantReason: FailoverOverloaded,
			wantStatus: 503,
		},
		{
			name:       "400 bad request",
			err:        fmt.Errorf("openai error (status 400): invalid request"),
			wantReason: FailoverFormat,
			wantStatus: 400,
		},
		{
			name:       "401 unauthorized",
			err:        fmt.Errorf("openai error (status 401): invalid api key"),
			wantReason: FailoverAuth,
			wantStatus: 401,
		},
		{
			name:       "403 forbidden",
			err:        fmt.Errorf("anthropic error (status 403): forbidden"),
			wantReason: FailoverAuth,
			wantStatus: 403,
		},
		{
			name:       "402 billing",
			err:        fmt.Errorf("openai error (status 402): payment required"),
			wantReason: FailoverBilling,
			wantStatus: 402,
		},
		{
			name:       "500 internal server error",
			err:        fmt.Errorf("openai error (status 500): internal error"),
			wantReason: FailoverOverloaded,
			wantStatus: 500,
		},
		{
			name:       "502 bad gateway",
			err:        fmt.Errorf("openai error (status 502): bad gateway"),
			wantReason: FailoverOverloaded,
			wantStatus: 502,
		},
		{
			name:       "529 overloaded",
			err:        fmt.Errorf("anthropic error (status 529): overloaded"),
			wantReason: FailoverOverloaded,
			wantStatus: 529,
		},
		{
			name:       "408 timeout",
			err:        fmt.Errorf("openai error (status 408): request timeout"),
			wantReason: FailoverTimeout,
			wantStatus: 408,
		},
		{
			name:       "504 gateway timeout",
			err:        fmt.Errorf("openai error (status 504): gateway timeout"),
			wantReason: FailoverTimeout,
			wantStatus: 504,
		},
		{
			name:       "unknown status",
			err:        fmt.Errorf("openai error (status 418): I'm a teapot"),
			wantReason: FailoverUnknown,
			wantStatus: 418,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fe := ClassifyError(tt.err, "test-provider", "test-model")
			if fe.Reason != tt.wantReason {
				t.Errorf("reason = %q, want %q", fe.Reason, tt.wantReason)
			}
			if fe.Status != tt.wantStatus {
				t.Errorf("status = %d, want %d", fe.Status, tt.wantStatus)
			}
		})
	}
}

func TestClassifyError_MessagePatterns(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantReason FailoverReason
	}{
		{
			name:       "timeout message",
			err:        fmt.Errorf("openai request: context deadline exceeded"),
			wantReason: FailoverTimeout,
		},
		{
			name:       "rate limit message",
			err:        fmt.Errorf("rate limit exceeded, try again later"),
			wantReason: FailoverRateLimit,
		},
		{
			name:       "unauthorized message",
			err:        fmt.Errorf("unauthorized: invalid api key"),
			wantReason: FailoverAuth,
		},
		{
			name:       "service unavailable message",
			err:        fmt.Errorf("service unavailable"),
			wantReason: FailoverOverloaded,
		},
		{
			name:       "unknown error",
			err:        fmt.Errorf("something completely unexpected"),
			wantReason: FailoverUnknown,
		},
		{
			name:       "deadline exceeded",
			err:        fmt.Errorf("deadline exceeded while waiting"),
			wantReason: FailoverTimeout,
		},
		{
			name:       "too many requests message",
			err:        fmt.Errorf("too many requests"),
			wantReason: FailoverRateLimit,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fe := ClassifyError(tt.err, "test-provider", "test-model")
			if fe.Reason != tt.wantReason {
				t.Errorf("reason = %q, want %q", fe.Reason, tt.wantReason)
			}
			if fe.Status != 0 {
				t.Errorf("status = %d, want 0 (no status in message)", fe.Status)
			}
		})
	}
}

func TestFailoverError_IsRetriable(t *testing.T) {
	tests := []struct {
		reason    FailoverReason
		retriable bool
	}{
		{FailoverRateLimit, true},
		{FailoverOverloaded, true},
		{FailoverTimeout, true},
		{FailoverUnknown, true},
		{FailoverAuth, false},
		{FailoverFormat, false},
		{FailoverBilling, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.reason), func(t *testing.T) {
			fe := &FailoverError{Reason: tt.reason}
			if fe.IsRetriable() != tt.retriable {
				t.Errorf("IsRetriable() = %v, want %v", fe.IsRetriable(), tt.retriable)
			}
		})
	}
}

func TestFailoverError_Error(t *testing.T) {
	fe := &FailoverError{
		Reason:   FailoverRateLimit,
		Provider: "openai",
		Model:    "gpt-4o",
		Status:   429,
		Wrapped:  fmt.Errorf("rate limit exceeded"),
	}
	got := fe.Error()
	want := "openai/gpt-4o failover (rate_limit, status 429): rate limit exceeded"
	if got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}

	// Without status
	fe2 := &FailoverError{
		Reason:   FailoverTimeout,
		Provider: "anthropic",
		Model:    "claude",
		Wrapped:  fmt.Errorf("deadline exceeded"),
	}
	got2 := fe2.Error()
	want2 := "anthropic/claude failover (timeout): deadline exceeded"
	if got2 != want2 {
		t.Errorf("Error() = %q, want %q", got2, want2)
	}
}

func TestFailoverError_Unwrap(t *testing.T) {
	inner := fmt.Errorf("original error")
	fe := &FailoverError{Wrapped: inner}
	if fe.Unwrap() != inner {
		t.Error("Unwrap() did not return the wrapped error")
	}
}

func TestFallbackExhaustedError(t *testing.T) {
	// Empty errors
	e := &FallbackExhaustedError{}
	if e.Error() != "all fallback candidates exhausted" {
		t.Errorf("unexpected error: %s", e.Error())
	}

	// With errors
	e2 := &FallbackExhaustedError{
		Errors: []*FailoverError{
			{Reason: FailoverRateLimit, Provider: "openai", Model: "gpt-4o", Status: 429, Wrapped: fmt.Errorf("rate limited")},
			{Reason: FailoverOverloaded, Provider: "anthropic", Model: "claude", Status: 503, Wrapped: fmt.Errorf("overloaded")},
		},
	}
	got := e2.Error()
	if got == "" {
		t.Error("expected non-empty error message")
	}
}
