package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/initializ/forge/forge-core/observability"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

// installSpanRecorder swaps the global tracer provider for one that
// records every emitted span into a SpanRecorder for the duration of
// the test. Restores the prior provider on test cleanup so parallel /
// later tests still see the no-op default.
func installSpanRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	otel.SetTracerProvider(tp)
	coreruntime.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTracerProvider(prev)
		coreruntime.SetTracerProvider(prev)
	})
	return rec
}

// staticOKProvider is a provider that accepts every token and returns
// a fixed Identity. Used by the success-path span test.
type staticOKProvider struct {
	identity Identity
}

func (p *staticOKProvider) Name() string { return "static_ok" }
func (p *staticOKProvider) Verify(_ context.Context, token string, _ Headers) (*Identity, error) {
	id := p.identity
	return &id, nil
}

// authSpanRejectingProvider returns a fixed sentinel error so the failure-path
// tests can confirm the matching FailReason classification surfaces on
// the span.
type authSpanRejectingProvider struct{ err error }

func (p *authSpanRejectingProvider) Name() string { return "rejecting" }
func (p *authSpanRejectingProvider) Verify(_ context.Context, token string, _ Headers) (*Identity, error) {
	return nil, p.err
}

func newAuthedRequest(token string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/tasks/send", nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}

// findSpanByName locates the first recorded span whose Name matches
// `want` and returns it. Helper so each test stays focused on the
// assertion that matters.
func findSpanByName(t *testing.T, rec *tracetest.SpanRecorder, want string) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, s := range rec.Ended() {
		if s.Name() == want {
			return s
		}
	}
	t.Fatalf("no span named %q recorded; got %d spans", want, len(rec.Ended()))
	return nil
}

func attrValue(s sdktrace.ReadOnlySpan, key string) string {
	for _, a := range s.Attributes() {
		if string(a.Key) == key {
			return a.Value.AsString()
		}
	}
	return ""
}

// TestAuthVerifySpan_SuccessRecordsProviderTokenKindDecision is the
// core issue #187 invariant on the success path. A request with a
// valid token must produce an `auth.verify` span whose attributes
// mirror the audit `auth_verify` event fields — same `provider` and
// `token_kind`, plus `decision="verify"`. The audit row and the trace
// span then cross-link cleanly by trace_id.
func TestAuthVerifySpan_SuccessRecordsProviderTokenKindDecision(t *testing.T) {
	rec := installSpanRecorder(t)

	mw := Middleware(MiddlewareOptions{
		Chain: &staticOKProvider{identity: Identity{
			Source: "static_ok",
			UserID: "u-42",
			OrgID:  "o-7",
		}},
	})
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, newAuthedRequest("eyJ.fake.jwt"))

	span := findSpanByName(t, rec, "auth.verify")
	if got := attrValue(span, observability.AttrForgeAuthProvider); got != "static_ok" {
		t.Errorf("forge.auth.provider = %q, want static_ok", got)
	}
	if got := attrValue(span, observability.AttrForgeAuthDecision); got != "verify" {
		t.Errorf("forge.auth.decision = %q, want verify", got)
	}
	if got := attrValue(span, observability.AttrForgeAuthTokenKind); got == "" {
		t.Errorf("forge.auth.token_kind missing; got %v", span.Attributes())
	}
	if got := attrValue(span, observability.AttrForgeAuthUserID); got != "u-42" {
		t.Errorf("forge.auth.user_id = %q, want u-42", got)
	}
	if got := attrValue(span, observability.AttrForgeAuthOrgID); got != "o-7" {
		t.Errorf("forge.auth.org_id = %q, want o-7", got)
	}
}

// TestAuthVerifySpan_FailureSetsErrorStatusAndFailReason pins the
// failure-path contract. Each chain-error sentinel maps to a
// distinct fail_reason code (matches the audit auth_fail vocabulary)
// AND sets the span status to Error so the error-rate dashboards work
// across span types uniformly.
func TestAuthVerifySpan_FailureSetsErrorStatusAndFailReason(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantReason string
	}{
		{"rejected", ErrTokenRejected, "rejected"},
		{"invalid", ErrInvalidToken, "invalid"},
		{"provider_unavailable", ErrProviderUnavailable, "provider_unavailable"},
		{"not_for_me", ErrTokenNotForMe, "not_for_me"},
		{"infrastructure_fallback", errors.New("unexpected"), "infrastructure"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := installSpanRecorder(t)
			mw := Middleware(MiddlewareOptions{Chain: &authSpanRejectingProvider{err: c.err}})
			h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Fatalf("downstream handler must not run on failure path")
			}))

			w := httptest.NewRecorder()
			h.ServeHTTP(w, newAuthedRequest("eyJ.fake.jwt"))

			span := findSpanByName(t, rec, "auth.verify")
			if got := attrValue(span, observability.AttrForgeAuthDecision); got != "fail" {
				t.Errorf("decision = %q, want fail", got)
			}
			if got := attrValue(span, observability.AttrForgeAuthFailReason); got != c.wantReason {
				t.Errorf("fail_reason = %q, want %q", got, c.wantReason)
			}
			if status := span.Status().Code; status != codes.Error {
				t.Errorf("status.code = %v, want %v", status, codes.Error)
			}
			// Provider attribute must NOT be set on failure — the
			// chain ran but no provider claimed the token.
			if got := attrValue(span, observability.AttrForgeAuthProvider); got != "" {
				t.Errorf("provider should be unset on failure; got %q", got)
			}
		})
	}
}

// TestAuthVerifySpan_MissingBearerOpensZeroDurationSpan confirms the
// path where the auth header is entirely absent still produces an
// auth.verify span (Status=Error, fail_reason=missing_token). Without
// this span the audit auth_fail event has no trace parent and SIEM
// trace↔audit pivots silently break for the most common failure mode.
func TestAuthVerifySpan_MissingBearerOpensZeroDurationSpan(t *testing.T) {
	rec := installSpanRecorder(t)
	mw := Middleware(MiddlewareOptions{Chain: &authSpanRejectingProvider{err: errors.New("should not be called")}})
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("downstream handler must not run when bearer is missing")
	}))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/tasks/send", nil))

	span := findSpanByName(t, rec, "auth.verify")
	if got := attrValue(span, observability.AttrForgeAuthFailReason); got != "missing_token" {
		t.Errorf("fail_reason = %q, want missing_token", got)
	}
	if status := span.Status().Code; status != codes.Error {
		t.Errorf("status.code = %v, want Error", status)
	}
}

// TestAuthVerifySpan_ParentsProviderHTTPClientSpans pins the issue's
// motivating use case: JWKS / STS / IAP / Graph outbound HTTP calls
// the provider issues during Verify must nest UNDER auth.verify. We
// stand up a real http.Client+Transport that opens a child span on
// every round-trip and confirm its parent SpanContext is the
// auth.verify span we just opened.
func TestAuthVerifySpan_ParentsProviderHTTPClientSpans(t *testing.T) {
	rec := installSpanRecorder(t)

	providerHTTPCallParent := trace.SpanContext{}
	roundTripper := childSpanRoundTripper{onParent: func(sc trace.SpanContext) {
		providerHTTPCallParent = sc
	}}
	httpCallingProvider := &httpCallingProvider{client: &http.Client{Transport: roundTripper}}
	mw := Middleware(MiddlewareOptions{Chain: httpCallingProvider})
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, newAuthedRequest("eyJ.fake.jwt"))

	authSpan := findSpanByName(t, rec, "auth.verify")
	if !providerHTTPCallParent.IsValid() {
		t.Fatalf("provider's outbound call captured no parent; auth.verify did not parent it")
	}
	if providerHTTPCallParent.TraceID() != authSpan.SpanContext().TraceID() {
		t.Errorf("provider call trace_id = %s, want %s",
			providerHTTPCallParent.TraceID(), authSpan.SpanContext().TraceID())
	}
	if providerHTTPCallParent.SpanID() != authSpan.SpanContext().SpanID() {
		t.Errorf("provider call parent span_id = %s, want %s (auth.verify span)",
			providerHTTPCallParent.SpanID(), authSpan.SpanContext().SpanID())
	}
}

// httpCallingProvider mimics oidc/aws_sigv4/gcp_iap/http_verifier:
// each Verify call issues at least one outbound HTTP request. The
// test asserts the round-trip's ctx still carries the auth.verify
// span so it parents whatever http.client span gets opened
// downstream by an otelhttp-wrapped transport.
type httpCallingProvider struct{ client *http.Client }

func (p *httpCallingProvider) Name() string { return "http_caller" }
func (p *httpCallingProvider) Verify(ctx context.Context, token string, _ Headers) (*Identity, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://jwks.invalid", nil)
	// We don't actually do the network call — the round tripper is a
	// stub that captures the ctx's active span and returns 200.
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	return &Identity{Source: "http_caller", UserID: "u-1"}, nil
}

type childSpanRoundTripper struct {
	onParent func(trace.SpanContext)
}

func (r childSpanRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// In real production this is `otelhttp.Transport` which opens an
	// http.client span using the parent from req.Context(). We
	// emulate that by capturing the current span's SpanContext from
	// the request's ctx — which is the parent the wrapped transport
	// would record.
	parent := trace.SpanContextFromContext(req.Context())
	r.onParent(parent)
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       http.NoBody,
		Header:     http.Header{},
		Request:    req,
	}, nil
}

// keep strings used so test imports stay tidy across edits.
var _ = strings.TrimSpace
