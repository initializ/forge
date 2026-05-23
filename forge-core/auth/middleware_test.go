package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// tokenProvider is a minimal test-only Provider that compares against a fixed
// token. We define it here so middleware tests don't need to import the
// statictoken provider package (which would create a cycle since it imports
// this package).
type tokenProvider struct {
	expected string
	identity Identity
}

func (t *tokenProvider) Name() string { return "test_token" }

func (t *tokenProvider) Verify(_ context.Context, token string, _ Headers) (*Identity, error) {
	if token != t.expected {
		return nil, ErrTokenNotForMe
	}
	id := t.identity
	return &id, nil
}

// rejectingProvider returns ErrTokenRejected for any token, simulating an
// external verifier that recognized the token but denied it.
type rejectingProvider struct{}

func (rejectingProvider) Name() string { return "test_reject" }
func (rejectingProvider) Verify(_ context.Context, _ string, _ Headers) (*Identity, error) {
	return nil, ErrTokenRejected
}

// brokenProvider returns ErrInvalidToken (e.g., signature failure).
type brokenProvider struct{}

func (brokenProvider) Name() string { return "test_broken" }
func (brokenProvider) Verify(_ context.Context, _ string, _ Headers) (*Identity, error) {
	return nil, ErrInvalidToken
}

func TestMiddleware(t *testing.T) {
	const validToken = "test-secret-token"

	okHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	chain := NewChainProvider(&tokenProvider{
		expected: validToken,
		identity: Identity{UserID: "u1", Source: "test_token"},
	})

	tests := []struct {
		name       string
		opts       MiddlewareOptions
		method     string
		path       string
		authHeader string
		wantStatus int
	}{
		{
			name:       "nil chain with AllowAnonymous passes through",
			opts:       MiddlewareOptions{Chain: nil, AllowAnonymous: true},
			method:     "POST",
			path:       "/",
			wantStatus: http.StatusOK,
		},
		{
			name:       "valid token accepted",
			opts:       MiddlewareOptions{Chain: chain, SkipPaths: DefaultSkipPaths()},
			method:     "POST",
			path:       "/",
			authHeader: "Bearer " + validToken,
			wantStatus: http.StatusOK,
		},
		{
			name:       "missing token rejected",
			opts:       MiddlewareOptions{Chain: chain, SkipPaths: DefaultSkipPaths()},
			method:     "POST",
			path:       "/",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "wrong token rejected",
			opts:       MiddlewareOptions{Chain: chain, SkipPaths: DefaultSkipPaths()},
			method:     "POST",
			path:       "/",
			authHeader: "Bearer wrong-token",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "GET / is public",
			opts:       MiddlewareOptions{Chain: chain, SkipPaths: DefaultSkipPaths()},
			method:     "GET",
			path:       "/",
			wantStatus: http.StatusOK,
		},
		{
			name:       "GET /.well-known/agent.json is public",
			opts:       MiddlewareOptions{Chain: chain, SkipPaths: DefaultSkipPaths()},
			method:     "GET",
			path:       "/.well-known/agent.json",
			wantStatus: http.StatusOK,
		},
		{
			name:       "GET /healthz is public",
			opts:       MiddlewareOptions{Chain: chain, SkipPaths: DefaultSkipPaths()},
			method:     "GET",
			path:       "/healthz",
			wantStatus: http.StatusOK,
		},
		{
			name:       "POST /tasks/send requires auth",
			opts:       MiddlewareOptions{Chain: chain, SkipPaths: DefaultSkipPaths()},
			method:     "POST",
			path:       "/tasks/send",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "case insensitive Bearer prefix",
			opts:       MiddlewareOptions{Chain: chain, SkipPaths: DefaultSkipPaths()},
			method:     "POST",
			path:       "/",
			authHeader: "bearer " + validToken,
			wantStatus: http.StatusOK,
		},
		{
			name:       "rejected by provider returns 401",
			opts:       MiddlewareOptions{Chain: NewChainProvider(rejectingProvider{}), SkipPaths: DefaultSkipPaths()},
			method:     "POST",
			path:       "/",
			authHeader: "Bearer anything",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "invalid token from provider returns 401",
			opts:       MiddlewareOptions{Chain: NewChainProvider(brokenProvider{}), SkipPaths: DefaultSkipPaths()},
			method:     "POST",
			path:       "/",
			authHeader: "Bearer malformed",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "nil SkipPaths uses defaults",
			opts:       MiddlewareOptions{Chain: chain},
			method:     "GET",
			path:       "/healthz",
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mw := Middleware(tt.opts)
			handler := mw(okHandler)

			req := httptest.NewRequest(tt.method, tt.path, nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}

			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			if rr.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rr.Code, tt.wantStatus)
			}

			// Verify JSON error body on 401.
			if tt.wantStatus == http.StatusUnauthorized {
				var resp errorResponse
				if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
					t.Fatalf("failed to decode error response: %v", err)
				}
				if resp.Error != "unauthorized" {
					t.Errorf("error = %q, want %q", resp.Error, "unauthorized")
				}
			}
		})
	}
}

func TestMiddleware_OnAuthCallback(t *testing.T) {
	const token = "callback-token"

	var successCount, failCount atomic.Int32

	opts := MiddlewareOptions{
		Chain: NewChainProvider(&tokenProvider{
			expected: token,
			identity: Identity{UserID: "u", Source: "test"},
		}),
		SkipPaths: DefaultSkipPaths(),
		OnAuth: func(_ *http.Request, id *Identity, err error, _ string) {
			if err == nil && id != nil {
				successCount.Add(1)
			} else {
				failCount.Add(1)
			}
		},
	}

	handler := Middleware(opts)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Successful auth.
	req := httptest.NewRequest("POST", "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	// Failed auth.
	req2 := httptest.NewRequest("POST", "/", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req2)

	if got := successCount.Load(); got != 1 {
		t.Errorf("success callbacks = %d, want 1", got)
	}
	if got := failCount.Load(); got != 1 {
		t.Errorf("failure callbacks = %d, want 1", got)
	}
}

func TestMiddleware_IdentityIsAttachedToContext(t *testing.T) {
	const token = "ctx-token"

	wantID := Identity{UserID: "ctx-user", Email: "ctx@example.com", Source: "test_token"}

	opts := MiddlewareOptions{
		Chain:     NewChainProvider(&tokenProvider{expected: token, identity: wantID}),
		SkipPaths: DefaultSkipPaths(),
	}

	var gotID *Identity
	handler := Middleware(opts)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotID = IdentityFromContext(r.Context())
	}))

	req := httptest.NewRequest("POST", "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if gotID == nil {
		t.Fatal("identity not attached to context")
	}
	if gotID.UserID != wantID.UserID || gotID.Email != wantID.Email {
		t.Errorf("identity = %+v, want %+v", gotID, wantID)
	}
}

func TestMiddleware_OnAuthReceivesIdentityAndError(t *testing.T) {
	const goodToken = "good"
	wantID := Identity{UserID: "alice", Source: "test_token"}

	var (
		gotIDOnSuccess *Identity
		gotErrOnFail   error
		gotKindSuccess string
		gotKindFail    string
	)

	opts := MiddlewareOptions{
		Chain:     NewChainProvider(&tokenProvider{expected: goodToken, identity: wantID}),
		SkipPaths: DefaultSkipPaths(),
		OnAuth: func(_ *http.Request, id *Identity, err error, kind string) {
			if err == nil && id != nil {
				gotIDOnSuccess = id
				gotKindSuccess = kind
			} else {
				gotErrOnFail = err
				gotKindFail = kind
			}
		},
	}

	handler := Middleware(opts)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Success path — OnAuth gets the Identity, kind = "opaque" (no dots).
	req := httptest.NewRequest("POST", "/", nil)
	req.Header.Set("Authorization", "Bearer "+goodToken)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if gotIDOnSuccess == nil {
		t.Fatal("OnAuth did not receive Identity on success")
	}
	if gotIDOnSuccess.UserID != "alice" {
		t.Errorf("OnAuth identity = %+v, want UserID=alice", gotIDOnSuccess)
	}
	if gotKindSuccess != "opaque" {
		t.Errorf("token kind on success = %q, want opaque", gotKindSuccess)
	}

	// Failure path — no Authorization header → ErrMissingBearer.
	req2 := httptest.NewRequest("POST", "/", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req2)
	if !errors.Is(gotErrOnFail, ErrMissingBearer) {
		t.Errorf("OnAuth fail err = %v, want ErrMissingBearer", gotErrOnFail)
	}
	if gotKindFail != "empty" {
		t.Errorf("token kind on missing-token fail = %q, want empty", gotKindFail)
	}
}

func TestMiddleware_OnAuthGetsChainError(t *testing.T) {
	// When the chain rejects, OnAuth receives the precise sentinel so
	// the caller can map to a stable reason code.
	opts := MiddlewareOptions{
		Chain:     NewChainProvider(rejectingProvider{}),
		SkipPaths: DefaultSkipPaths(),
	}
	var gotErr error
	opts.OnAuth = func(_ *http.Request, _ *Identity, err error, _ string) {
		gotErr = err
	}
	handler := Middleware(opts)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "/", nil)
	req.Header.Set("Authorization", "Bearer x")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if !errors.Is(gotErr, ErrTokenRejected) {
		t.Errorf("OnAuth got err = %v, want ErrTokenRejected", gotErr)
	}
}

func TestMiddleware_TokenKindDetection(t *testing.T) {
	// JWT-shaped tokens are reported as "jwt" even when the chain
	// doesn't recognize them — this is structural, not validity.
	jwtToken := "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ4In0.sig"

	var gotKind string
	opts := MiddlewareOptions{
		Chain:     NewChainProvider(rejectingProvider{}),
		SkipPaths: DefaultSkipPaths(),
		OnAuth: func(_ *http.Request, _ *Identity, _ error, kind string) {
			gotKind = kind
		},
	}
	handler := Middleware(opts)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "/", nil)
	req.Header.Set("Authorization", "Bearer "+jwtToken)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if gotKind != "jwt" {
		t.Errorf("token kind = %q, want jwt", gotKind)
	}
}

// --- Nil chain panic guard (review finding #3) ---

func TestMiddleware_NilChainPanicsWithoutAllowAnonymous(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic when Chain==nil and AllowAnonymous==false")
		}
		msg, _ := r.(string)
		if msg == "" {
			t.Fatalf("panic value = %v, want a descriptive string", r)
		}
		// The message should mention the option name so callers know
		// how to opt in.
		if !contains(msg, "AllowAnonymous") {
			t.Errorf("panic message %q does not reference AllowAnonymous", msg)
		}
	}()

	// This call MUST panic — silent anonymous passthrough is the bug
	// the AllowAnonymous flag exists to prevent.
	_ = Middleware(MiddlewareOptions{Chain: nil, AllowAnonymous: false})
}

func TestMiddleware_NilChainWithAllowAnonymousPassesThrough(t *testing.T) {
	// Counterpart: explicit opt-in does NOT panic; serves all requests.
	handler := Middleware(MiddlewareOptions{
		Chain:          nil,
		AllowAnonymous: true,
	})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "/anything", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d (AllowAnonymous should pass through)", rr.Code, http.StatusOK)
	}
}

func TestMiddleware_NonNilChainIgnoresAllowAnonymous(t *testing.T) {
	// AllowAnonymous is only consulted when Chain == nil. When a chain
	// is configured, the flag has no effect — requests are still
	// validated through the chain.
	const goodToken = "ok"
	chain := NewChainProvider(&tokenProvider{
		expected: goodToken,
		identity: Identity{UserID: "u", Source: "test"},
	})
	handler := Middleware(MiddlewareOptions{
		Chain:          chain,
		AllowAnonymous: true, // should be ignored
		SkipPaths:      DefaultSkipPaths(),
	})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// No token → 401 (chain still enforced).
	req := httptest.NewRequest("POST", "/tasks", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("non-nil chain + AllowAnonymous=true: status = %d, want 401 (chain must still enforce)", rr.Code)
	}
}

// contains is a tiny strings.Contains substitute for the panic-message test
// (kept inline to avoid pulling in strings just for the assertion).
func contains(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// --- Phase 2: non-Bearer auth formats reach the chain ---

// headerCapturingProvider records the headers it sees and lets the test
// script the response. Used by Phase 2 middleware tests to assert that
// providers consuming non-Bearer formats (Sigv4, IAP) actually receive
// what they need.
type headerCapturingProvider struct {
	sawToken   string
	sawHeaders Headers
	identity   *Identity
	err        error
}

func (p *headerCapturingProvider) Name() string { return "test_capture" }
func (p *headerCapturingProvider) Verify(_ context.Context, token string, h Headers) (*Identity, error) {
	p.sawToken = token
	p.sawHeaders = h
	return p.identity, p.err
}

func TestMiddleware_Sigv4HeaderReachesChain(t *testing.T) {
	// Phase 2 change: a Sigv4-shaped Authorization reaches the chain even
	// though extractBearerToken returns "". The middleware no longer
	// short-circuits to 401 when there's an auth-shaped header present.
	spy := &headerCapturingProvider{err: ErrTokenNotForMe}
	opts := MiddlewareOptions{
		Chain:     NewChainProvider(spy),
		SkipPaths: DefaultSkipPaths(),
	}
	handler := Middleware(opts)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "/tasks", nil)
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=AKIA.../20260523/us-east-1/sts/aws4_request, SignedHeaders=host, Signature=ab")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if spy.sawHeaders == nil {
		t.Fatal("chain was never invoked — middleware short-circuited on empty Bearer")
	}
	got := spy.sawHeaders.Get("Authorization")
	if got == "" || !startsWithLocal(got, "AWS4-HMAC-SHA256 ") {
		t.Errorf("chain saw Authorization = %q, want Sigv4-prefixed value", got)
	}
}

func TestMiddleware_IAPHeaderReachesChain(t *testing.T) {
	// Phase 2 change: gcp_iap's X-Goog-Iap-Jwt-Assertion is enough — no
	// Authorization header at all is needed for the chain to be consulted.
	spy := &headerCapturingProvider{err: ErrTokenNotForMe}
	opts := MiddlewareOptions{
		Chain:     NewChainProvider(spy),
		SkipPaths: DefaultSkipPaths(),
	}
	handler := Middleware(opts)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "/tasks", nil)
	req.Header.Set("X-Goog-Iap-Jwt-Assertion", "eyJ.eyJ.sig")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if spy.sawHeaders == nil {
		t.Fatal("chain was never invoked — middleware short-circuited on empty Bearer")
	}
	if got := spy.sawHeaders.Get("X-Goog-Iap-Jwt-Assertion"); got != "eyJ.eyJ.sig" {
		t.Errorf("chain saw IAP header = %q, want eyJ.eyJ.sig", got)
	}
}

func TestMiddleware_NoAuthHeaders_PreservesMissingTokenReason(t *testing.T) {
	// Review #4 contract regression check: when the caller did NOT attempt
	// auth at all (no Bearer, no Sigv4 Authorization, no IAP header), the
	// audit reason MUST stay ErrMissingBearer — not be widened to "not_for_me".
	// This lets ops dashboards still differentiate "client didn't auth" from
	// "client tried a format we don't speak."
	spyCalled := false
	chain := NewChainProvider(&headerCapturingProvider{
		err:      ErrTokenNotForMe,
		identity: nil,
	})

	var gotErr error
	opts := MiddlewareOptions{
		Chain:     chain,
		SkipPaths: DefaultSkipPaths(),
		OnAuth: func(_ *http.Request, _ *Identity, err error, _ string) {
			spyCalled = true
			gotErr = err
		},
	}
	handler := Middleware(opts)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Request with NO auth-shaped headers at all.
	req := httptest.NewRequest("POST", "/tasks", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if !spyCalled {
		t.Fatal("OnAuth callback never fired")
	}
	if !errors.Is(gotErr, ErrMissingBearer) {
		t.Errorf("OnAuth err = %v, want ErrMissingBearer (review #4 regression)", gotErr)
	}
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestMiddleware_TokenKind_Sigv4OnEmptyBearer(t *testing.T) {
	// Phase 2: the audit token_kind reads "sigv4" when the request has a
	// Sigv4-shaped Authorization header, even though Bearer extraction
	// returned "". Audit dashboards need this signal to count Sigv4
	// requests distinctly from "empty" (no auth attempt at all).
	var gotKind string
	opts := MiddlewareOptions{
		Chain:     NewChainProvider(&headerCapturingProvider{err: ErrTokenNotForMe}),
		SkipPaths: DefaultSkipPaths(),
		OnAuth: func(_ *http.Request, _ *Identity, _ error, kind string) {
			gotKind = kind
		},
	}
	handler := Middleware(opts)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "/tasks", nil)
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=AKIA, SignedHeaders=host, Signature=ab")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if gotKind != "sigv4" {
		t.Errorf("token kind = %q, want sigv4", gotKind)
	}
}

func TestMiddleware_TokenKind_EmptyWhenTrulyNoAuth(t *testing.T) {
	// Counterpart to the test above: when the caller didn't attempt auth
	// at all, token_kind should be "empty" — not silently widened.
	var gotKind string
	opts := MiddlewareOptions{
		Chain:     NewChainProvider(&headerCapturingProvider{err: ErrTokenNotForMe}),
		SkipPaths: DefaultSkipPaths(),
		OnAuth: func(_ *http.Request, _ *Identity, _ error, kind string) {
			gotKind = kind
		},
	}
	handler := Middleware(opts)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "/tasks", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if gotKind != "empty" {
		t.Errorf("token kind = %q, want empty", gotKind)
	}
}

// startsWithLocal is the middleware_test.go equivalent of provider_test.go's
// startsWith helper. Kept local so the two test files don't depend on each
// other's helpers.
func startsWithLocal(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func TestClassifyAuthFailure(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, "valid bearer token required"},
		{"not for me", ErrTokenNotForMe, "valid bearer token required"},
		{"rejected", ErrTokenRejected, "token rejected by auth provider"},
		{"invalid", ErrInvalidToken, "invalid token"},
		// Review #6: ErrProviderUnavailable gets its own user-visible
		// message — distinct from "invalid token" so client retry logic
		// and operator alerting can differentiate.
		{"provider unavailable", ErrProviderUnavailable, "auth provider unavailable"},
		{"unexpected", context.Canceled, "auth provider error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyAuthFailure(tt.err); got != tt.want {
				t.Errorf("classifyAuthFailure(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}
