package azure_ad

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/initializ/forge/forge-core/auth"
	"github.com/initializ/forge/forge-core/auth/providers/oidc"
)

// fakeAAD is a tiny in-memory AAD: serves an OIDC discovery doc + RS256
// JWKS, lets tests sign tokens with the matching key.
type fakeAAD struct {
	t    *testing.T
	priv *rsa.PrivateKey
	pub  *rsa.PublicKey
	kid  string
	srv  *httptest.Server
}

func newFakeAAD(t *testing.T) *fakeAAD {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	f := &fakeAAD{t: t, priv: priv, pub: &priv.PublicKey, kid: "kid-1"}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", f.serveDiscovery)
	mux.HandleFunc("/keys", f.serveJWKS)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAAD) issuerURL() string { return f.srv.URL }

func (f *fakeAAD) serveDiscovery(w http.ResponseWriter, _ *http.Request) {
	doc := map[string]any{
		"issuer":   f.srv.URL,
		"jwks_uri": f.srv.URL + "/keys",
	}
	_ = json.NewEncoder(w).Encode(doc)
}

func (f *fakeAAD) serveJWKS(w http.ResponseWriter, _ *http.Request) {
	n := base64.RawURLEncoding.EncodeToString(f.pub.N.Bytes())
	eBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(eBytes, uint64(f.pub.E))
	i := 0
	for i < len(eBytes)-1 && eBytes[i] == 0 {
		i++
	}
	e := base64.RawURLEncoding.EncodeToString(eBytes[i:])
	_ = json.NewEncoder(w).Encode(map[string]any{
		"keys": []map[string]any{
			{"kty": "RSA", "kid": f.kid, "alg": "RS256", "use": "sig", "n": n, "e": e},
		},
	})
}

func (f *fakeAAD) sign(claims jwt.MapClaims) string {
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = f.kid
	str, err := tok.SignedString(f.priv)
	if err != nil {
		panic(err)
	}
	return str
}

func validAADClaims(iss, tenant, audience string) jwt.MapClaims {
	now := time.Now()
	return jwt.MapClaims{
		"iss":   iss,
		"aud":   audience,
		"tid":   tenant,
		"sub":   "user-1",
		"email": "alice@example.com",
		"exp":   now.Add(time.Hour).Unix(),
		"iat":   now.Unix(),
		"nbf":   now.Unix(),
	}
}

// newWithIssuer builds an azure_ad Provider whose composed OIDC points
// at a test-supplied issuer URL (rather than the AAD authority host).
// Production code uses the AAD authority template via New(); this helper
// lets tests substitute a fakeAAD without piping a TEST_ONLY field
// through the public Config surface.
func newWithIssuer(cfg Config, issuer string) (*Provider, error) {
	if cfg.Audience == "" {
		return nil, fmt.Errorf("audience required")
	}
	if cfg.GroupsMode == "" {
		cfg.GroupsMode = "claim"
	}
	if cfg.GraphTimeout == 0 {
		cfg.GraphTimeout = defaultGraphTimeout
	}
	if cfg.JWKSCacheTTL == 0 {
		cfg.JWKSCacheTTL = defaultJWKSCacheTTL
	}
	inner, err := oidc.New(oidc.Config{
		Issuer:          issuer,
		Audience:        cfg.Audience,
		JWKSCacheTTL:    cfg.JWKSCacheTTL,
		SkipIssuerCheck: cfg.AllowMultiTenant,
	})
	if err != nil {
		return nil, fmt.Errorf("compose oidc: %w", err)
	}
	p := &Provider{cfg: cfg, oidc: inner}
	if cfg.GroupsMode == "graph" {
		if cfg.GraphEndpoint != "" {
			p.graph = NewGraphClientWithEndpoint(cfg.GraphEndpoint, cfg.GraphTimeout)
		} else {
			p.graph = NewGraphClient(cfg.GraphTimeout)
		}
		p.cache = NewGraphCache(defaultGraphCacheTTL)
	}
	return p, nil
}

func newTestProviderAADSingleTenant(t *testing.T, f *fakeAAD, tid string) *Provider {
	t.Helper()
	p, err := newWithIssuer(Config{
		Audience: "api://forge",
		TenantID: tid,
	}, f.issuerURL())
	if err != nil {
		t.Fatalf("newWithIssuer: %v", err)
	}
	return p
}

func newTestProviderAADMultiTenant(t *testing.T, f *fakeAAD) *Provider {
	t.Helper()
	p, err := newWithIssuer(Config{
		Audience:         "api://forge",
		AllowMultiTenant: true,
	}, f.issuerURL())
	if err != nil {
		t.Fatalf("newWithIssuer: %v", err)
	}
	return p
}

func newTestProviderAADGraphMode(t *testing.T, f *fakeAAD, tid, graphURL string) *Provider {
	t.Helper()
	p, err := newWithIssuer(Config{
		Audience:      "api://forge",
		TenantID:      tid,
		GroupsMode:    "graph",
		GraphEndpoint: graphURL,
	}, f.issuerURL())
	if err != nil {
		t.Fatalf("newWithIssuer: %v", err)
	}
	return p
}

// --- Tests ---

func TestFactory_RejectsMissingAudience(t *testing.T) {
	if _, err := auth.Build("azure_ad", map[string]any{
		"tenant_id": "00000000-0000-0000-0000-000000000000",
	}); err == nil {
		t.Fatal("expected error when audience is missing")
	}
}

func TestFactory_RejectsMissingTenantUnlessMultiTenant(t *testing.T) {
	if _, err := auth.Build("azure_ad", map[string]any{
		"audience": "api://forge",
	}); err == nil {
		t.Fatal("expected error when tenant_id missing and not multi-tenant")
	}
}

func TestFactory_AcceptsMultiTenantWithoutTenant(t *testing.T) {
	if _, err := auth.Build("azure_ad", map[string]any{
		"audience":           "api://forge",
		"allow_multi_tenant": true,
	}); err != nil {
		t.Errorf("expected success, got %v", err)
	}
}

func TestFactory_RejectsBadGroupsMode(t *testing.T) {
	_, err := auth.Build("azure_ad", map[string]any{
		"audience":    "api://forge",
		"tenant_id":   "00000000-0000-0000-0000-000000000000",
		"groups_mode": "bogus",
	})
	if err == nil {
		t.Fatal("expected error for invalid groups_mode")
	}
}

func TestProvider_HappyPath_ClaimMode(t *testing.T) {
	f := newFakeAAD(t)
	wantTID := "11111111-1111-1111-1111-111111111111"
	p := newTestProviderAADSingleTenant(t, f, wantTID)

	claims := validAADClaims(f.issuerURL(), wantTID, "api://forge")
	claims["groups"] = []string{"g1", "g2"}
	tok := f.sign(claims)

	id, err := p.Verify(context.Background(), tok, auth.Headers{})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if id.Source != "azure_ad" {
		t.Errorf("Source = %q, want azure_ad (composition must overwrite oidc stamp)", id.Source)
	}
	if id.UserID != "user-1" {
		t.Errorf("UserID = %q", id.UserID)
	}
	if len(id.Groups) != 2 {
		t.Errorf("Groups = %v", id.Groups)
	}
}

func TestProvider_WrongTenant_Rejected(t *testing.T) {
	f := newFakeAAD(t)
	wantTID := "11111111-1111-1111-1111-111111111111"
	p := newTestProviderAADSingleTenant(t, f, wantTID)

	claims := validAADClaims(f.issuerURL(), "22222222-2222-2222-2222-222222222222", "api://forge")
	tok := f.sign(claims)

	_, err := p.Verify(context.Background(), tok, auth.Headers{})
	if !errors.Is(err, auth.ErrTokenRejected) {
		t.Errorf("err = %v, want ErrTokenRejected (tid mismatch)", err)
	}
}

func TestProvider_MultiTenant_AcceptsArbitraryTenant(t *testing.T) {
	f := newFakeAAD(t)
	p := newTestProviderAADMultiTenant(t, f)

	claims := validAADClaims(f.issuerURL(), "any-tenant-uuid", "api://forge")
	tok := f.sign(claims)

	if _, err := p.Verify(context.Background(), tok, auth.Headers{}); err != nil {
		t.Errorf("expected multi-tenant success, got %v", err)
	}
}

func TestProvider_MissingTid_Invalid(t *testing.T) {
	f := newFakeAAD(t)
	p := newTestProviderAADSingleTenant(t, f, "11111111-1111-1111-1111-111111111111")

	claims := validAADClaims(f.issuerURL(), "ignored", "api://forge")
	delete(claims, "tid")
	tok := f.sign(claims)

	_, err := p.Verify(context.Background(), tok, auth.Headers{})
	if !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("err = %v, want ErrInvalidToken", err)
	}
}

func TestProvider_GraphMode_EnrichesEmptyGroups(t *testing.T) {
	f := newFakeAAD(t)
	graph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"value":[{"id":"g1"},{"id":"g2"}]}`)
	}))
	t.Cleanup(graph.Close)

	tid := "11111111-1111-1111-1111-111111111111"
	p := newTestProviderAADGraphMode(t, f, tid, graph.URL)

	claims := validAADClaims(f.issuerURL(), tid, "api://forge")
	// No groups claim → simulate overage.
	tok := f.sign(claims)

	id, err := p.Verify(context.Background(), tok, auth.Headers{"Authorization": "Bearer " + tok})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(id.Groups) != 2 {
		t.Errorf("Groups = %v, want 2 enriched ids", id.Groups)
	}
}

func TestProvider_GraphMode_SoftFailsOn5xx(t *testing.T) {
	f := newFakeAAD(t)
	graph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(graph.Close)

	tid := "11111111-1111-1111-1111-111111111111"
	p := newTestProviderAADGraphMode(t, f, tid, graph.URL)

	claims := validAADClaims(f.issuerURL(), tid, "api://forge")
	tok := f.sign(claims)

	id, err := p.Verify(context.Background(), tok, auth.Headers{"Authorization": "Bearer " + tok})
	if err != nil {
		t.Errorf("expected soft-fail (nil err), got %v", err)
	}
	if id != nil && len(id.Groups) != 0 {
		t.Errorf("Groups should be empty after Graph 5xx, got %v", id.Groups)
	}
}

func TestProvider_GraphMode_401SoftFails(t *testing.T) {
	// Even on Graph 401/403, the auth request itself proceeds — soft-fail
	// keeps the Identity flowing with empty Groups. The graph-side error
	// is surfaced separately if the operator wires audit hooks. Pinning
	// this contract here so the behavior doesn't drift.
	f := newFakeAAD(t)
	graph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(graph.Close)

	tid := "11111111-1111-1111-1111-111111111111"
	p := newTestProviderAADGraphMode(t, f, tid, graph.URL)

	claims := validAADClaims(f.issuerURL(), tid, "api://forge")
	tok := f.sign(claims)
	id, err := p.Verify(context.Background(), tok, auth.Headers{"Authorization": "Bearer " + tok})
	if err != nil {
		t.Errorf("Graph 401 should soft-fail at provider level, got err=%v", err)
	}
	if id == nil {
		t.Fatal("Identity should be returned even on Graph 401")
	}
	if len(id.Groups) != 0 {
		t.Errorf("Groups should be empty after Graph 401, got %v", id.Groups)
	}
}

func TestProvider_GraphMode_ClaimPresent_SkipsGraph(t *testing.T) {
	// When the JWT carries a groups claim, we MUST NOT call Graph (avoid
	// extra latency + unneeded Graph permission requirements).
	f := newFakeAAD(t)
	graphCalls := 0
	graph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		graphCalls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(graph.Close)

	tid := "11111111-1111-1111-1111-111111111111"
	p := newTestProviderAADGraphMode(t, f, tid, graph.URL)

	claims := validAADClaims(f.issuerURL(), tid, "api://forge")
	claims["groups"] = []string{"g-from-jwt"}
	tok := f.sign(claims)
	id, err := p.Verify(context.Background(), tok, auth.Headers{"Authorization": "Bearer " + tok})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if graphCalls != 0 {
		t.Errorf("Graph called %d times, want 0 (claim was present)", graphCalls)
	}
	if len(id.Groups) != 1 || id.Groups[0] != "g-from-jwt" {
		t.Errorf("Groups = %v, want [g-from-jwt]", id.Groups)
	}
}

func TestProvider_RegisteredInRegistry(t *testing.T) {
	p, err := auth.Build("azure_ad", map[string]any{
		"audience":           "api://forge",
		"allow_multi_tenant": true,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if p.Name() != "azure_ad" {
		t.Errorf("Name = %q", p.Name())
	}
}

func TestExtractTenantID(t *testing.T) {
	cases := []struct {
		in   map[string]any
		want string
	}{
		{map[string]any{"tid": "abc"}, "abc"},
		{map[string]any{}, ""},
		{map[string]any{"tid": 123}, ""},
		{nil, ""},
	}
	for _, tc := range cases {
		if got := ExtractTenantID(tc.in); got != tc.want {
			t.Errorf("ExtractTenantID(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNeedsEnrichment(t *testing.T) {
	if !needsEnrichment(nil) {
		t.Error("nil groups should need enrichment")
	}
	if !needsEnrichment([]string{}) {
		t.Error("empty groups should need enrichment")
	}
	if needsEnrichment([]string{"g1"}) {
		t.Error("populated groups should not need enrichment")
	}
}

func TestProvider_Name(t *testing.T) {
	f := newFakeAAD(t)
	p := newTestProviderAADMultiTenant(t, f)
	if p.Name() != "azure_ad" {
		t.Errorf("Name = %q", p.Name())
	}
}
