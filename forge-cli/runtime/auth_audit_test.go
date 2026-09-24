package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/auth"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

// PR4 audit-event contract tests. These guarantee:
//   1. auth_verify is emitted on success with the right fields.
//   2. auth_fail is emitted on failure with a stable reason code.
//   3. Neither event ever carries PII (token bytes, email, claim payloads).

// captureAudit decodes each NDJSON line emitted to buf into an AuditEvent.
func captureAudit(t *testing.T, buf *bytes.Buffer) []coreruntime.AuditEvent {
	t.Helper()
	var events []coreruntime.AuditEvent
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var ev coreruntime.AuditEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("decode audit line %q: %v", line, err)
		}
		events = append(events, ev)
	}
	return events
}

func TestAuthAudit_EmitsAuthVerifyOnSuccess(t *testing.T) {
	var buf bytes.Buffer
	logger := coreruntime.NewAuditLogger(&buf)

	cb := makeAuthAuditCallback(logger)
	if cb == nil {
		t.Fatal("callback nil with non-nil logger")
	}

	req := httptest.NewRequest("POST", "/tasks", nil)
	id := &auth.Identity{
		UserID: "alice-123",
		Email:  "alice@example.com", // recorded — audit must capture WHO authenticated
		OrgID:  "tenant-1",
		Groups: []string{"a", "b", "c"},
		Source: "oidc",
	}
	cb(req, id, nil, "jwt")

	events := captureAudit(t, &buf)
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	ev := events[0]

	if ev.Event != coreruntime.EventAuthVerify {
		t.Errorf("Event = %q, want %q", ev.Event, coreruntime.EventAuthVerify)
	}
	if ev.Fields["provider"] != "oidc" {
		t.Errorf("provider = %v, want oidc", ev.Fields["provider"])
	}
	if ev.Fields["user_id"] != "alice-123" {
		t.Errorf("user_id = %v, want alice-123", ev.Fields["user_id"])
	}
	if ev.Fields["org_id"] != "tenant-1" {
		t.Errorf("org_id = %v, want tenant-1", ev.Fields["org_id"])
	}
	// JSON unmarshal puts numbers in float64.
	if g, _ := ev.Fields["groups_count"].(float64); g != 3 {
		t.Errorf("groups_count = %v, want 3", ev.Fields["groups_count"])
	}
	if ev.Fields["token_kind"] != "jwt" {
		t.Errorf("token_kind = %v, want jwt", ev.Fields["token_kind"])
	}
	// The invoker's email is recorded so the audit answers "who authenticated?".
	if ev.Fields["email"] != "alice@example.com" {
		t.Errorf("email = %v, want alice@example.com", ev.Fields["email"])
	}
}

// TestAuthAudit_EmitsChannelInvoker verifies that for a channel-originated
// request — where the transport credential is the runtime loopback token and
// the human is asserted via X-Forge-Channel-* headers — auth_verify records
// both the transport credential (provider/user_id) AND the channel invoker.
func TestAuthAudit_EmitsChannelInvoker(t *testing.T) {
	cases := []struct {
		name             string
		channel          string
		user, email      string
		wantEmailPresent bool
	}{
		// Slack/Teams resolve a profile email.
		{"slack with email", "slack", "U08ABC", "bob@example.com", true},
		// Telegram carries only a numeric id; WhatsApp only a phone number.
		{"telegram id only", "telegram", "987654321", "", false},
		{"whatsapp number only", "whatsapp", "14155550123", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			cb := makeAuthAuditCallback(coreruntime.NewAuditLogger(&buf))

			req := httptest.NewRequest("POST", "/tasks", nil)
			req.Header.Set("X-Forge-Channel", tc.channel)
			req.Header.Set("X-Forge-Channel-User", tc.user)
			if tc.email != "" {
				req.Header.Set("X-Forge-Channel-Email", tc.email)
			}
			// The transport identity is the runtime loopback token — the same
			// trust marker applyChannelOnBehalfOf gates the graft on.
			id := auth.MarkRuntimeInternal(auth.Identity{UserID: "forge-internal", Source: "internal"})

			cb(req, &id, nil, "opaque")

			ev := captureAudit(t, &buf)[0]
			if ev.Fields["channel"] != tc.channel {
				t.Errorf("channel = %v, want %q", ev.Fields["channel"], tc.channel)
			}
			if ev.Fields["channel_user"] != tc.user {
				t.Errorf("channel_user = %v, want %q", ev.Fields["channel_user"], tc.user)
			}
			if tc.wantEmailPresent {
				if ev.Fields["channel_email"] != tc.email {
					t.Errorf("channel_email = %v, want %q", ev.Fields["channel_email"], tc.email)
				}
			} else if _, ok := ev.Fields["channel_email"]; ok {
				t.Errorf("channel_email should be absent when the channel has no email, got %v", ev.Fields["channel_email"])
			}
			// Transport credential still recorded truthfully.
			if ev.Fields["user_id"] != "forge-internal" || ev.Fields["provider"] != "internal" {
				t.Errorf("transport credential not recorded: provider=%v user_id=%v", ev.Fields["provider"], ev.Fields["user_id"])
			}
		})
	}
}

// TestAuthAudit_ChannelHeadersIgnoredForNonLoopback ensures a non-internal
// identity (e.g. a real OIDC caller) cannot inject a spoofed channel invoker
// via X-Forge-Channel-* headers — the fields are gated on IsRuntimeInternal.
func TestAuthAudit_ChannelHeadersIgnoredForNonLoopback(t *testing.T) {
	var buf bytes.Buffer
	cb := makeAuthAuditCallback(coreruntime.NewAuditLogger(&buf))

	req := httptest.NewRequest("POST", "/tasks", nil)
	req.Header.Set("X-Forge-Channel", "slack")
	req.Header.Set("X-Forge-Channel-Email", "attacker@evil.example")

	cb(req, &auth.Identity{UserID: "u", Source: "oidc"}, nil, "jwt")

	ev := captureAudit(t, &buf)[0]
	for _, k := range []string{"channel", "channel_user", "channel_email"} {
		if _, ok := ev.Fields[k]; ok {
			t.Errorf("field %q must be ignored for a non-loopback identity, got %v", k, ev.Fields[k])
		}
	}
}

func TestAuthAudit_EmitsAuthFailOnError(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantReason string
	}{
		{"missing token", auth.ErrMissingBearer, "missing_token"},
		{"rejected", auth.ErrTokenRejected, "rejected"},
		{"invalid", auth.ErrInvalidToken, "invalid"},
		{"not for me", auth.ErrTokenNotForMe, "not_for_me"},
		// Review #6: provider downtime gets its own stable reason code
		// so operators can dashboard "IdP down" separately from "token bad".
		{"provider unavailable", auth.ErrProviderUnavailable, "provider_unavailable"},
		{"infrastructure", errBoom, "infrastructure"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			cb := makeAuthAuditCallback(coreruntime.NewAuditLogger(&buf))

			req := httptest.NewRequest("POST", "/tasks", nil)
			cb(req, nil, tt.err, "opaque")

			events := captureAudit(t, &buf)
			if len(events) != 1 {
				t.Fatalf("got %d events, want 1", len(events))
			}
			ev := events[0]
			if ev.Event != coreruntime.EventAuthFail {
				t.Errorf("Event = %q, want %q", ev.Event, coreruntime.EventAuthFail)
			}
			if got := ev.Fields["reason"]; got != tt.wantReason {
				t.Errorf("reason = %v, want %q", got, tt.wantReason)
			}
			if ev.Fields["token_kind"] != "opaque" {
				t.Errorf("token_kind = %v, want opaque", ev.Fields["token_kind"])
			}
		})
	}
}

func TestAuthAudit_NoClaimsOrSecretsInPayload(t *testing.T) {
	// The identity's email IS recorded (audit must capture who authenticated),
	// but the raw Claims bag — which can carry arbitrary sensitive payloads
	// (SSN, secrets) — MUST NEVER be emitted.
	var buf bytes.Buffer
	cb := makeAuthAuditCallback(coreruntime.NewAuditLogger(&buf))

	req := httptest.NewRequest("POST", "/tasks", nil)
	id := &auth.Identity{
		UserID: "u",
		Email:  "user@example.com",
		Claims: map[string]any{
			"ssn":    "999-99-9999",
			"secret": "deadbeef",
		},
		Source: "oidc",
	}
	cb(req, id, nil, "jwt")

	line := buf.String()
	forbidden := []string{
		"999-99-9999",
		"deadbeef",
		"\"claims\"",
	}
	for _, f := range forbidden {
		if strings.Contains(line, f) {
			t.Errorf("audit line leaked %q:\n%s", f, line)
		}
	}
	// Email is intentionally present now.
	if !strings.Contains(line, "user@example.com") {
		t.Errorf("expected email to be recorded in the auth_verify event:\n%s", line)
	}
}

func TestAuthAudit_NilLoggerReturnsNilCallback(t *testing.T) {
	if cb := makeAuthAuditCallback(nil); cb != nil {
		t.Error("expected nil callback for nil logger")
	}
}

func TestAuthAudit_CorrelationIDPropagated(t *testing.T) {
	var buf bytes.Buffer
	cb := makeAuthAuditCallback(coreruntime.NewAuditLogger(&buf))

	req := httptest.NewRequest("POST", "/tasks", nil)
	ctx := coreruntime.WithCorrelationID(req.Context(), "corr-abc-123")
	req = req.WithContext(ctx)

	cb(req, &auth.Identity{UserID: "u", Source: "oidc"}, nil, "jwt")

	events := captureAudit(t, &buf)
	if events[0].CorrelationID != "corr-abc-123" {
		t.Errorf("CorrelationID = %q, want corr-abc-123", events[0].CorrelationID)
	}
}

func TestAuthFailReason_UnknownError(t *testing.T) {
	// Unknown error types map to "infrastructure" (catch-all for things
	// like network failures, JWKS-fetch errors).
	if got := authFailReason(errBoom); got != "infrastructure" {
		t.Errorf("authFailReason(custom err) = %q, want infrastructure", got)
	}
	if got := authFailReason(nil); got != "unknown" {
		t.Errorf("authFailReason(nil) = %q, want unknown", got)
	}
}

// Smoke test: making sure the OnAuth callback hooks into auth.Middleware
// end-to-end and produces audit events on real HTTP traffic.
func TestAuthAudit_E2EThroughMiddleware(t *testing.T) {
	var buf bytes.Buffer
	logger := coreruntime.NewAuditLogger(&buf)

	chain := auth.NewChainProvider(&testProvider{
		token:    "ok",
		identity: auth.Identity{UserID: "u", OrgID: "o", Source: "test"},
	})

	handler := auth.Middleware(auth.MiddlewareOptions{
		Chain:     chain,
		SkipPaths: auth.DefaultSkipPaths(),
		OnAuth:    makeAuthAuditCallback(logger),
	})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// 1. Successful request.
	req := httptest.NewRequest("POST", "/tasks", nil)
	req.Header.Set("Authorization", "Bearer ok")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	// 2. Missing token.
	req2 := httptest.NewRequest("POST", "/tasks", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req2)

	events := captureAudit(t, &buf)
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	if events[0].Event != coreruntime.EventAuthVerify {
		t.Errorf("first event = %q, want auth_verify", events[0].Event)
	}
	if events[1].Event != coreruntime.EventAuthFail {
		t.Errorf("second event = %q, want auth_fail", events[1].Event)
	}
	if events[1].Fields["reason"] != "missing_token" {
		t.Errorf("missing-token reason = %v, want missing_token", events[1].Fields["reason"])
	}
}

// --- helpers ---

// errBoom is an opaque non-sentinel error for testing the catch-all
// "infrastructure" reason mapping.
type customErr struct{ s string }

func (e customErr) Error() string { return e.s }

var errBoom error = customErr{s: "network is on fire"}

// testProvider is a tiny Provider that accepts one token.
type testProvider struct {
	token    string
	identity auth.Identity
}

func (t *testProvider) Name() string { return "test" }
func (t *testProvider) Verify(_ context.Context, token string, _ auth.Headers) (*auth.Identity, error) {
	if token != t.token {
		return nil, auth.ErrTokenNotForMe
	}
	id := t.identity
	return &id, nil
}
