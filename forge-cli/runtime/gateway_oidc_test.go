package runtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/initializ/forge/forge-core/llm/oauth"
	"github.com/initializ/forge/forge-core/settings"
)

func oidcJWT(exp int64) string {
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	return enc(`{"alg":"none"}`) + "." + enc(`{"exp":`+strconv.FormatInt(exp, 10)+`}`) + "." + enc("s")
}

func TestGatewayOIDCCredKey_IdentityScoped(t *testing.T) {
	base := &settings.ModelGatewayOIDC{Grant: "client_credentials", TokenURL: "https://idp/token", ClientID: "c1", Scopes: []string{"a", "b"}}
	// Same identity (scope order-insensitive) → same key.
	same := &settings.ModelGatewayOIDC{Grant: "client_credentials", TokenURL: "https://idp/token", ClientID: "c1", Scopes: []string{"b", "a"}}
	if GatewayOIDCCredKey(base) != GatewayOIDCCredKey(same) {
		t.Error("same (grant, endpoints, client, scopes) must yield the same key")
	}
	// Different client → different key.
	other := &settings.ModelGatewayOIDC{Grant: "client_credentials", TokenURL: "https://idp/token", ClientID: "c2", Scopes: []string{"a", "b"}}
	if GatewayOIDCCredKey(base) == GatewayOIDCCredKey(other) {
		t.Error("distinct client_id must yield distinct keys")
	}
	// OIDC key namespace is distinct from the helper key namespace.
	if GatewayOIDCCredKey(base) == GatewayCredKey("x", nil) {
		t.Error("OIDC and helper key namespaces must not collide")
	}
}

func TestEnsureGatewayOIDCToken_ClientCredentialsMintAndCache(t *testing.T) {
	dir := t.TempDir()
	oauth.SetCredentialsDir(dir)
	t.Cleanup(func() { oauth.SetCredentialsDir("") })
	t.Setenv("GW_SECRET", "s3cr3t")

	var calls int
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = r.ParseForm()
		if r.FormValue("grant_type") != "client_credentials" || r.FormValue("client_id") != "c1" || r.FormValue("client_secret") != "s3cr3t" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_request"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "AT-1", "token_type": "Bearer", "expires_in": 3600})
	}))
	defer tokenSrv.Close()

	o := &settings.ModelGatewayOIDC{
		Grant: "client_credentials", TokenURL: tokenSrv.URL, ClientID: "c1", ClientSecretEnv: "GW_SECRET", Scopes: []string{"model.invoke"},
	}

	tok, err := EnsureGatewayOIDCToken(context.Background(), o)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if tok.AccessToken != "AT-1" {
		t.Errorf("access token = %q, want AT-1", tok.AccessToken)
	}
	if tok.ExpiresAt.IsZero() {
		t.Error("expected ExpiresAt from expires_in")
	}
	if calls != 1 {
		t.Fatalf("token endpoint hit %d times, want 1", calls)
	}
	// Cache hit — no second mint.
	if _, err := EnsureGatewayOIDCToken(context.Background(), o); err != nil {
		t.Fatalf("second: %v", err)
	}
	if calls != 1 {
		t.Errorf("valid cached token should not re-mint; calls=%d", calls)
	}
	if cached, _ := CachedGatewayOIDCToken(o); cached == nil || cached.AccessToken != "AT-1" {
		t.Errorf("cache read = %+v, want AT-1", cached)
	}
}

func TestEnsureGatewayOIDCToken_ClientCredentialsReMintsWhenExpired(t *testing.T) {
	dir := t.TempDir()
	oauth.SetCredentialsDir(dir)
	t.Cleanup(func() { oauth.SetCredentialsDir("") })
	t.Setenv("GW_SECRET", "s")

	var calls int
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "AT-fresh", "token_type": "Bearer", "expires_in": 3600})
	}))
	defer tokenSrv.Close()

	o := &settings.ModelGatewayOIDC{Grant: "client_credentials", TokenURL: tokenSrv.URL, ClientID: "c1", ClientSecretEnv: "GW_SECRET"}
	// Pre-seed an expired token under the OIDC key.
	if err := oauth.SaveCredentials(GatewayOIDCCredKey(o), &oauth.Token{AccessToken: "STALE", ExpiresAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	tok, err := EnsureGatewayOIDCToken(context.Background(), o)
	if err != nil {
		t.Fatalf("re-mint: %v", err)
	}
	if tok.AccessToken != "AT-fresh" {
		t.Errorf("expired token should be re-minted; got %q", tok.AccessToken)
	}
	if calls != 1 {
		t.Errorf("expected exactly one mint, got %d", calls)
	}
}

// When the token response carries no expires_in, ExpiresAt is backfilled from
// the access token's JWT exp — else it would be treated as always-expired and
// re-minted on every call.
func TestEnsureGatewayOIDCToken_ExpiryFromJWTWhenNoExpiresIn(t *testing.T) {
	dir := t.TempDir()
	oauth.SetCredentialsDir(dir)
	t.Cleanup(func() { oauth.SetCredentialsDir("") })
	t.Setenv("GW_SECRET", "s")

	exp := time.Now().Add(time.Hour).Unix()
	jwt := oidcJWT(exp)
	var calls int
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		// No expires_in — expiry must come from the JWT exp.
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": jwt, "token_type": "Bearer"})
	}))
	defer tokenSrv.Close()

	o := &settings.ModelGatewayOIDC{Grant: "client_credentials", TokenURL: tokenSrv.URL, ClientID: "c1", ClientSecretEnv: "GW_SECRET"}
	tok, err := EnsureGatewayOIDCToken(context.Background(), o)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if tok.ExpiresAt.Unix() != exp {
		t.Errorf("ExpiresAt = %d, want %d (from JWT exp)", tok.ExpiresAt.Unix(), exp)
	}
	// Cache hit — the JWT-derived expiry keeps it from re-minting.
	if _, err := EnsureGatewayOIDCToken(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("JWT-expiry token should cache; minted %d times", calls)
	}
}

func TestEnsureGatewayOIDCToken_AuthCodeNeedsLoginWhenNoCache(t *testing.T) {
	oauth.SetCredentialsDir(t.TempDir())
	t.Cleanup(func() { oauth.SetCredentialsDir("") })
	o := &settings.ModelGatewayOIDC{Grant: "auth_code", AuthorizeURL: "https://idp/authorize", TokenURL: "https://idp/token", ClientID: "c1"}
	// No cached token, and the overlay must not open a browser.
	if _, err := EnsureGatewayOIDCToken(context.Background(), o); err == nil {
		t.Fatal("auth_code with no cached token must error (prompt to run forge auth login), not open a browser")
	}
}

func TestOIDCAnthropicGuard(t *testing.T) {
	// Refuses the anthropic public API host.
	if err := oidcAnthropicGuard("", "", "https://api.anthropic.com/v1/token"); err == nil {
		t.Error("must refuse an OIDC flow against api.anthropic.com")
	}
	// An IdP-fronted anthropic gateway (different host) is allowed.
	if err := oidcAnthropicGuard("https://idp.corp/issuer", "", "https://idp.corp/token"); err != nil {
		t.Errorf("a non-anthropic IdP must be allowed, got %v", err)
	}
}

func TestEnsureGatewayOIDCToken_ClientCredentialsRefusesAnthropicPublic(t *testing.T) {
	oauth.SetCredentialsDir(t.TempDir())
	t.Cleanup(func() { oauth.SetCredentialsDir("") })
	t.Setenv("GW_SECRET", "s")
	o := &settings.ModelGatewayOIDC{Grant: "client_credentials", TokenURL: "https://api.anthropic.com/oauth/token", ClientID: "c1", ClientSecretEnv: "GW_SECRET"}
	if _, err := EnsureGatewayOIDCToken(context.Background(), o); err == nil {
		t.Fatal("must refuse a client_credentials mint against the anthropic public URL")
	}
}

func TestOIDCValidate(t *testing.T) {
	for _, bad := range []*settings.ModelGatewayOIDC{
		{Grant: "", ClientID: "c", TokenURL: "u"},                   // missing grant
		{Grant: "basic", ClientID: "c", TokenURL: "u"},              // unknown grant
		{Grant: "client_credentials", TokenURL: "u"},                // missing client_id
		{Grant: "client_credentials", ClientID: "c"},                // missing token_url/issuer
		{Grant: "client_credentials", ClientID: "c", TokenURL: "u"}, // missing client_secret_env
		{Grant: "auth_code", ClientID: "c", TokenURL: "u"},          // missing authorize_url/issuer
	} {
		if err := bad.Validate(); err == nil {
			t.Errorf("expected validation error for %+v", bad)
		}
	}
	ok := []*settings.ModelGatewayOIDC{
		{Grant: "client_credentials", ClientID: "c", TokenURL: "u", ClientSecretEnv: "E"},
		{Grant: "client_credentials", ClientID: "c", Issuer: "https://idp", ClientSecretEnv: "E"},
		{Grant: "auth_code", ClientID: "c", AuthorizeURL: "a", TokenURL: "u"},
		{Grant: "auth_code", ClientID: "c", Issuer: "https://idp"},
	}
	for _, o := range ok {
		if err := o.Validate(); err != nil {
			t.Errorf("expected valid, got %v for %+v", err, o)
		}
	}
}

func TestDiscoverOIDC(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]string{
			"authorization_endpoint": "https://idp/authorize",
			"token_endpoint":         "https://idp/token",
		})
	}))
	defer srv.Close()

	a, tk, err := discoverOIDC(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if gotPath != "/.well-known/openid-configuration" {
		t.Errorf("discovery path = %q, want /.well-known/openid-configuration", gotPath)
	}
	if a != "https://idp/authorize" || tk != "https://idp/token" {
		t.Errorf("endpoints = %q, %q", a, tk)
	}
}
