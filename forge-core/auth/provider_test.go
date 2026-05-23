package auth

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"reflect"
	"sort"
	"sync"
	"testing"
)

// stubProvider is an in-package test provider that lets us script the
// outcome of Verify deterministically.
type stubProvider struct {
	name     string
	identity *Identity
	err      error
	calls    int
}

func (s *stubProvider) Name() string { return s.name }
func (s *stubProvider) Verify(_ context.Context, _ string, _ Headers) (*Identity, error) {
	s.calls++
	return s.identity, s.err
}

// --- ChainProvider ---

func TestChainProvider_FirstMatchWins(t *testing.T) {
	idA := &Identity{UserID: "a", Source: "first"}
	idB := &Identity{UserID: "b", Source: "second"}
	a := &stubProvider{name: "first", identity: idA}
	b := &stubProvider{name: "second", identity: idB}

	chain := NewChainProvider(a, b)
	got, err := chain.Verify(context.Background(), "tok", nil)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got != idA {
		t.Errorf("identity = %+v, want %+v", got, idA)
	}
	if a.calls != 1 {
		t.Errorf("first provider calls = %d, want 1", a.calls)
	}
	if b.calls != 0 {
		t.Errorf("second provider should not be called, got %d calls", b.calls)
	}
}

func TestChainProvider_NotForMeYieldsToNext(t *testing.T) {
	idB := &Identity{UserID: "b"}
	a := &stubProvider{name: "first", err: ErrTokenNotForMe}
	b := &stubProvider{name: "second", identity: idB}

	chain := NewChainProvider(a, b)
	got, err := chain.Verify(context.Background(), "tok", nil)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got != idB {
		t.Errorf("identity = %+v, want %+v", got, idB)
	}
	if a.calls != 1 || b.calls != 1 {
		t.Errorf("call counts: a=%d b=%d, want 1,1", a.calls, b.calls)
	}
}

func TestChainProvider_RejectedStopsChain(t *testing.T) {
	a := &stubProvider{name: "first", err: ErrTokenRejected}
	b := &stubProvider{name: "second", identity: &Identity{UserID: "b"}}

	chain := NewChainProvider(a, b)
	_, err := chain.Verify(context.Background(), "tok", nil)
	if !errors.Is(err, ErrTokenRejected) {
		t.Fatalf("err = %v, want ErrTokenRejected", err)
	}
	if b.calls != 0 {
		t.Errorf("second provider should not be called after rejection, got %d calls", b.calls)
	}
}

func TestChainProvider_InvalidStopsChain(t *testing.T) {
	a := &stubProvider{name: "first", err: ErrInvalidToken}
	b := &stubProvider{name: "second", identity: &Identity{UserID: "b"}}

	chain := NewChainProvider(a, b)
	_, err := chain.Verify(context.Background(), "tok", nil)
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("err = %v, want ErrInvalidToken", err)
	}
	if b.calls != 0 {
		t.Errorf("second provider should not be called after invalid, got %d calls", b.calls)
	}
}

func TestChainProvider_GenericErrorFailsClosed(t *testing.T) {
	// Infrastructure errors (network, etc.) must NOT fall through to the
	// next provider — that would let attackers evade a temporarily-down
	// upstream by waiting for it to fail.
	netErr := errors.New("connection refused")
	a := &stubProvider{name: "first", err: netErr}
	b := &stubProvider{name: "second", identity: &Identity{UserID: "b"}}

	chain := NewChainProvider(a, b)
	_, err := chain.Verify(context.Background(), "tok", nil)
	if !errors.Is(err, netErr) {
		t.Fatalf("err = %v, want network error", err)
	}
	if b.calls != 0 {
		t.Errorf("fail-closed violated: second provider was called after generic error")
	}
}

func TestChainProvider_EmptyChainYields(t *testing.T) {
	chain := NewChainProvider()
	_, err := chain.Verify(context.Background(), "tok", nil)
	if !errors.Is(err, ErrTokenNotForMe) {
		t.Errorf("empty chain err = %v, want ErrTokenNotForMe", err)
	}
}

func TestChainProvider_AllYieldFinalErrorIsNotForMe(t *testing.T) {
	a := &stubProvider{name: "a", err: ErrTokenNotForMe}
	b := &stubProvider{name: "b", err: ErrTokenNotForMe}
	chain := NewChainProvider(a, b)

	_, err := chain.Verify(context.Background(), "tok", nil)
	if !errors.Is(err, ErrTokenNotForMe) {
		t.Errorf("err = %v, want ErrTokenNotForMe", err)
	}
}

func TestChainProvider_DefensiveSliceCopy(t *testing.T) {
	a := &stubProvider{name: "a", identity: &Identity{UserID: "a"}}
	b := &stubProvider{name: "b", identity: &Identity{UserID: "b"}}
	srcSlice := []Provider{a, b}

	chain := NewChainProvider(srcSlice...)
	// Mutate the source — chain must be unaffected.
	srcSlice[0] = &stubProvider{name: "tamper", err: ErrTokenRejected}

	got, err := chain.Verify(context.Background(), "tok", nil)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.UserID != "a" {
		t.Errorf("chain saw mutated source; identity = %+v", got)
	}
}

func TestChainProvider_ProvidersReturnsCopy(t *testing.T) {
	a := &stubProvider{name: "a"}
	chain := NewChainProvider(a)

	listed := chain.Providers()
	// Mutate the returned slice — internal state must be unchanged.
	listed[0] = &stubProvider{name: "tamper"}

	listed2 := chain.Providers()
	if listed2[0].Name() != "a" {
		t.Errorf("Providers() did not return a defensive copy; got %q", listed2[0].Name())
	}
}

func TestChainProvider_NameIsChain(t *testing.T) {
	if got := (&ChainProvider{}).Name(); got != "chain" {
		t.Errorf("Name = %q, want chain", got)
	}
}

// --- PrependChain ---

func TestPrependChain_FlattenedChain(t *testing.T) {
	inner := NewChainProvider(
		&stubProvider{name: "b"},
		&stubProvider{name: "c"},
	)
	combined := PrependChain(inner, &stubProvider{name: "a"})

	names := providerNames(combined.Providers())
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("names = %v, want %v (flatten failed)", names, want)
	}
}

func TestPrependChain_NonChainSingle(t *testing.T) {
	single := &stubProvider{name: "single"}
	combined := PrependChain(single, &stubProvider{name: "first"})
	names := providerNames(combined.Providers())
	want := []string{"first", "single"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}
}

func TestPrependChain_NilChain(t *testing.T) {
	combined := PrependChain(nil, &stubProvider{name: "first"})
	names := providerNames(combined.Providers())
	want := []string{"first"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}
}

func TestPrependChain_NoPrependProviders(t *testing.T) {
	inner := NewChainProvider(&stubProvider{name: "x"})
	combined := PrependChain(inner)
	names := providerNames(combined.Providers())
	want := []string{"x"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}
}

// --- Headers ---

func TestHeaders_GetCaseInsensitive(t *testing.T) {
	h := Headers{"X-Org-ID": "acme"}
	tests := []struct {
		key  string
		want string
	}{
		{"X-Org-ID", "acme"},
		{"x-org-id", "acme"},
		{"X-ORG-ID", "acme"},
		{"X-Other", ""},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			if got := h.Get(tt.key); got != tt.want {
				t.Errorf("Get(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}

func TestHeaders_EmptyMap(t *testing.T) {
	var h Headers
	if got := h.Get("anything"); got != "" {
		t.Errorf("Get on nil Headers = %q, want empty", got)
	}
}

// --- Context ---

func TestContext_RoundTrip(t *testing.T) {
	want := &Identity{UserID: "alice", Source: "test"}
	ctx := WithIdentity(context.Background(), want)
	got := IdentityFromContext(ctx)
	if got != want {
		t.Errorf("IdentityFromContext = %+v, want %+v", got, want)
	}
}

func TestContext_NilIdentityIsNoOp(t *testing.T) {
	parent := context.Background()
	ctx := WithIdentity(parent, nil)
	if ctx != parent {
		t.Error("WithIdentity(nil) should return parent ctx unchanged")
	}
}

func TestContext_MissingReturnsNil(t *testing.T) {
	if got := IdentityFromContext(context.Background()); got != nil {
		t.Errorf("IdentityFromContext on empty ctx = %+v, want nil", got)
	}
}

func TestContext_NilContextReturnsNil(t *testing.T) {
	// Intentional: confirm we don't panic on a defensive nil-context call.
	var ctx context.Context //nolint:gocritic // intentional nil-context safety check
	if got := IdentityFromContext(ctx); got != nil {
		t.Errorf("IdentityFromContext(nil) = %+v, want nil", got)
	}
}

// --- Registry ---

// registryGuard isolates registry mutations from the global state so the
// concurrently-running provider package tests (httpverifier, statictoken)
// don't interfere.
//
// Strategy: snapshot the registry, run the test against a wiped registry,
// then restore.
func registryGuard(t *testing.T) {
	t.Helper()
	registryMu.Lock()
	snapshot := maps.Clone(registry)
	registryMu.Unlock()
	t.Cleanup(func() {
		registryMu.Lock()
		registry = snapshot
		registryMu.Unlock()
	})
	resetRegistryForTest()
}

func TestRegister_RoundTrip(t *testing.T) {
	registryGuard(t)

	Register("custom", func(_ map[string]any) (Provider, error) {
		return &stubProvider{name: "custom"}, nil
	})

	p, err := Build("custom", nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if p.Name() != "custom" {
		t.Errorf("Name = %q, want custom", p.Name())
	}
}

func TestRegister_DuplicatePanics(t *testing.T) {
	registryGuard(t)

	Register("dup", func(_ map[string]any) (Provider, error) {
		return &stubProvider{}, nil
	})

	defer func() {
		if r := recover(); r == nil {
			t.Error("duplicate Register did not panic")
		}
	}()
	Register("dup", func(_ map[string]any) (Provider, error) {
		return &stubProvider{}, nil
	})
}

func TestRegister_EmptyNamePanics(t *testing.T) {
	registryGuard(t)

	defer func() {
		if r := recover(); r == nil {
			t.Error("Register with empty name did not panic")
		}
	}()
	Register("", func(_ map[string]any) (Provider, error) {
		return &stubProvider{}, nil
	})
}

func TestRegister_NilFactoryPanics(t *testing.T) {
	registryGuard(t)

	defer func() {
		if r := recover(); r == nil {
			t.Error("Register with nil factory did not panic")
		}
	}()
	Register("nil_fac", nil)
}

func TestBuild_UnknownTypeReturnsError(t *testing.T) {
	registryGuard(t)

	_, err := Build("does-not-exist", nil)
	if err == nil {
		t.Fatal("Build of unknown type returned nil error")
	}
}

func TestBuild_FactoryErrorIsWrapped(t *testing.T) {
	registryGuard(t)

	custom := errors.New("custom failure")
	Register("failing", func(_ map[string]any) (Provider, error) {
		return nil, custom
	})

	_, err := Build("failing", nil)
	if !errors.Is(err, custom) {
		t.Fatalf("err = %v, want wrapped custom failure", err)
	}
}

func TestRegisteredTypes_SortedAndDeduplicated(t *testing.T) {
	registryGuard(t)

	Register("zeta", func(_ map[string]any) (Provider, error) { return &stubProvider{}, nil })
	Register("alpha", func(_ map[string]any) (Provider, error) { return &stubProvider{}, nil })
	Register("mu", func(_ map[string]any) (Provider, error) { return &stubProvider{}, nil })

	got := RegisteredTypes()
	want := []string{"alpha", "mu", "zeta"}
	if !sort.StringsAreSorted(got) {
		t.Errorf("RegisteredTypes not sorted: %v", got)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("RegisteredTypes = %v, want %v", got, want)
	}
}

func TestRegistry_ConcurrentReads(t *testing.T) {
	registryGuard(t)

	Register("a", func(_ map[string]any) (Provider, error) { return &stubProvider{name: "a"}, nil })
	Register("b", func(_ map[string]any) (Provider, error) { return &stubProvider{name: "b"}, nil })

	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			_, _ = Build("a", nil)
			_, _ = Build("b", nil)
			_ = RegisteredTypes()
		})
	}
	wg.Wait()
}

// --- UnmarshalSettings ---

type sampleSettings struct {
	Issuer   string         `yaml:"issuer"`
	Audience string         `yaml:"audience"`
	Nested   map[string]any `yaml:"nested,omitempty"`
}

func TestUnmarshalSettings(t *testing.T) {
	in := map[string]any{
		"issuer":   "https://example.com",
		"audience": "api://forge",
		"nested": map[string]any{
			"k": "v",
		},
	}

	var out sampleSettings
	if err := UnmarshalSettings(in, &out); err != nil {
		t.Fatalf("UnmarshalSettings: %v", err)
	}
	if out.Issuer != "https://example.com" {
		t.Errorf("Issuer = %q, want https://example.com", out.Issuer)
	}
	if out.Audience != "api://forge" {
		t.Errorf("Audience = %q, want api://forge", out.Audience)
	}
	if out.Nested["k"] != "v" {
		t.Errorf("Nested[k] = %v, want v", out.Nested["k"])
	}
}

func TestUnmarshalSettings_NilOut(t *testing.T) {
	if err := UnmarshalSettings(map[string]any{"x": 1}, nil); err == nil {
		t.Error("UnmarshalSettings with nil out returned nil error")
	}
}

// --- TokenKind ---

func TestTokenKind(t *testing.T) {
	tests := []struct {
		name  string
		token string
		want  string
	}{
		{"empty", "", "empty"},
		{"jwt three segments", "eyJhbGciOiJSUzI1NiJ9.eyJpc3MiOiJ4In0.signature", "jwt"},
		{"opaque random", "abc123xyz", "opaque"},
		{"opaque one dot", "abc.def", "opaque"},
		{"opaque four segments", "a.b.c.d", "opaque"},
		{"jwt with empty segments still has 2 dots", "..", "jwt"},
		// Phase 2: aws_sigv4 — the Authorization header's algorithm prefix
		// is enough to classify; middleware routes the raw header here when
		// the Bearer extractor returns "".
		{"sigv4 prefix", "AWS4-HMAC-SHA256 Credential=AKIA.../20260523/us-east-1/sts/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc", "sigv4"},
		{"sigv4 prefix only", "AWS4-HMAC-SHA256 ", "sigv4"},
		// Defensive: a token that LOOKS like Sigv4 but lacks the trailing
		// space must NOT be classified as sigv4 — we want a clean prefix match.
		{"sigv4-like without trailing space", "AWS4-HMAC-SHA256xyz", "opaque"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TokenKind(tt.token); got != tt.want {
				t.Errorf("TokenKind(%q) = %q, want %q", tt.token, got, tt.want)
			}
		})
	}
}

// --- HeadersFromRequest ---

func TestHeadersFromRequest_Phase1Headers(t *testing.T) {
	req, _ := http.NewRequest("POST", "/", nil)
	req.Header.Set("X-Org-ID", "acme")
	req.Header.Set("X-Request-ID", "req-1")
	req.Header.Set("org-id", "lower")
	req.Header.Set("org_id", "snake")

	h := HeadersFromRequest(req)
	if h["X-Org-ID"] != "acme" {
		t.Errorf("X-Org-ID = %q, want acme", h["X-Org-ID"])
	}
	if h["X-Request-ID"] != "req-1" {
		t.Errorf("X-Request-ID = %q, want req-1", h["X-Request-ID"])
	}
	if h["org-id"] != "lower" {
		t.Errorf("org-id = %q, want lower", h["org-id"])
	}
	if h["org_id"] != "snake" {
		t.Errorf("org_id = %q, want snake", h["org_id"])
	}
}

func TestHeadersFromRequest_Phase2Headers(t *testing.T) {
	// Phase 2: HeadersFromRequest widened so providers consuming non-Bearer
	// formats (aws_sigv4, gcp_iap) can read what they need without breaking
	// the Provider.Verify signature. See PHASE2_TEST_STRATEGY.md §4 PR1.
	req, _ := http.NewRequest("POST", "/", nil)
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=AKIA.../20260523/us-east-1/sts/aws4_request, SignedHeaders=host, Signature=ab")
	req.Header.Set("X-Goog-Iap-Jwt-Assertion", "eyJabc.eyJdef.sig")
	req.Header.Set("X-Amz-Date", "20260523T120000Z")
	req.Header.Set("X-Amz-Security-Token", "FwoGZX...")

	h := HeadersFromRequest(req)

	if got := h.Get("Authorization"); got == "" || !startsWith(got, "AWS4-HMAC-SHA256 ") {
		t.Errorf("Authorization = %q, want Sigv4-shaped string", got)
	}
	if got := h.Get("X-Goog-Iap-Jwt-Assertion"); got != "eyJabc.eyJdef.sig" {
		t.Errorf("X-Goog-Iap-Jwt-Assertion = %q, want eyJabc.eyJdef.sig", got)
	}
	if got := h.Get("X-Amz-Date"); got != "20260523T120000Z" {
		t.Errorf("X-Amz-Date = %q, want 20260523T120000Z", got)
	}
	if got := h.Get("X-Amz-Security-Token"); got == "" {
		t.Error("X-Amz-Security-Token not extracted")
	}
}

func TestHeadersFromRequest_AbsentHeadersAreEmpty(t *testing.T) {
	// Providers must not assume any header is present — absence is normal
	// and means "this format isn't here, yield to the next provider."
	req, _ := http.NewRequest("POST", "/", nil)
	h := HeadersFromRequest(req)
	for _, k := range []string{
		"Authorization", "X-Goog-Iap-Jwt-Assertion", "X-Amz-Date", "X-Amz-Security-Token",
	} {
		if got := h.Get(k); got != "" {
			t.Errorf("%s should be empty on request with no headers, got %q", k, got)
		}
	}
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// --- helpers ---

func providerNames(ps []Provider) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name()
	}
	return out
}
