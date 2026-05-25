package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/initializ/forge/forge-core/llm/oauth"
)

// setupCredsHome points oauth.LoadCredentials/SaveCredentials at a
// temp dir for the duration of the test. Returns the temp HOME.
func setupCredsHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	// On macOS the OAuth store also looks at FORGE_PASSPHRASE for the
	// encrypted backend; clear it so tests use the plaintext path.
	t.Setenv("FORGE_PASSPHRASE", "")
	return dir
}

func TestOAuthFlow_BearerToken_NoStoredToken_ReturnsRevoked(t *testing.T) {
	setupCredsHome(t)
	f := NewOAuthFlow()
	_, err := f.BearerToken(context.Background(), "nonexistent",
		OAuthServerConfig{ClientID: "x", AuthorizeURL: "https://a", TokenURL: "https://t"})
	if !errors.Is(err, ErrTokenRevoked) {
		t.Fatalf("err = %v, want ErrTokenRevoked", err)
	}
	if !strings.Contains(err.Error(), "forge mcp login") {
		t.Errorf("err should hint at login command, got: %v", err)
	}
}

func TestOAuthFlow_BearerToken_FreshToken_NoRefresh(t *testing.T) {
	setupCredsHome(t)
	if err := oauth.SaveCredentials("mcp_fresh", &oauth.Token{
		AccessToken:  "AAA",
		RefreshToken: "RRR",
		ExpiresAt:    time.Now().Add(10 * time.Minute),
	}); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}
	// Token endpoint must NOT be called.
	var calls atomic.Int32
	tokSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer tokSrv.Close()

	f := NewOAuthFlow()
	tok, err := f.BearerToken(context.Background(), "fresh",
		OAuthServerConfig{ClientID: "x", AuthorizeURL: "https://a", TokenURL: tokSrv.URL})
	if err != nil {
		t.Fatalf("BearerToken: %v", err)
	}
	if tok != "AAA" {
		t.Errorf("token = %q, want AAA", tok)
	}
	if c := calls.Load(); c != 0 {
		t.Errorf("/token called %d times — should be 0 for fresh token", c)
	}
}

func TestOAuthFlow_BearerToken_NearExpiry_TriggersRefresh(t *testing.T) {
	setupCredsHome(t)
	// 30s left — inside the 60s refresh window → must refresh.
	if err := oauth.SaveCredentials("mcp_near", &oauth.Token{
		AccessToken:  "OLD",
		RefreshToken: "REFRESH1",
		ExpiresAt:    time.Now().Add(30 * time.Second),
	}); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}
	tokSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "REFRESH1" {
			http.Error(w, "wrong form", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"NEW","token_type":"Bearer","expires_in":3600}`))
	}))
	defer tokSrv.Close()

	var audited atomic.Value
	f := NewOAuthFlow()
	f.AuditFn = func(server string, ok bool, reason string) {
		audited.Store([3]any{server, ok, reason})
	}
	tok, err := f.BearerToken(context.Background(), "near",
		OAuthServerConfig{ClientID: "x", AuthorizeURL: "https://a", TokenURL: tokSrv.URL})
	if err != nil {
		t.Fatalf("BearerToken: %v", err)
	}
	if tok != "NEW" {
		t.Errorf("token = %q, want NEW", tok)
	}
	got := audited.Load()
	if got == nil {
		t.Fatalf("expected audit event, got none")
	}
	arr := got.([3]any)
	if arr[0] != "near" || arr[1] != true || arr[2] != "refreshed" {
		t.Errorf("audit = %v, want [near, true, refreshed]", arr)
	}
}

func TestOAuthFlow_RefreshFail_InvalidGrant_MapsToRevoked(t *testing.T) {
	setupCredsHome(t)
	if err := oauth.SaveCredentials("mcp_revoked", &oauth.Token{
		AccessToken:  "OLD",
		RefreshToken: "RR",
		ExpiresAt:    time.Now().Add(-1 * time.Minute), // already expired
	}); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}
	tokSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"token revoked by user"}`))
	}))
	defer tokSrv.Close()

	f := NewOAuthFlow()
	_, err := f.BearerToken(context.Background(), "revoked",
		OAuthServerConfig{ClientID: "x", AuthorizeURL: "https://a", TokenURL: tokSrv.URL})
	if !errors.Is(err, ErrTokenRevoked) {
		t.Fatalf("err = %v, want ErrTokenRevoked", err)
	}
}

func TestOAuthFlow_Singleflight_CollapsesConcurrentRefreshes(t *testing.T) {
	setupCredsHome(t)
	if err := oauth.SaveCredentials("mcp_sf", &oauth.Token{
		AccessToken:  "OLD",
		RefreshToken: "RR",
		ExpiresAt:    time.Now().Add(10 * time.Second),
	}); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}
	var calls atomic.Int32
	tokSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		time.Sleep(80 * time.Millisecond) // hold the singleflight slot
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"NEW","token_type":"Bearer","expires_in":3600}`))
	}))
	defer tokSrv.Close()

	f := NewOAuthFlow()
	const n = 25
	errs := make(chan error, n)
	for range n {
		go func() {
			_, err := f.BearerToken(context.Background(), "sf",
				OAuthServerConfig{ClientID: "x", AuthorizeURL: "https://a", TokenURL: tokSrv.URL})
			errs <- err
		}()
	}
	for i := range n {
		if err := <-errs; err != nil {
			t.Errorf("BearerToken[%d]: %v", i, err)
		}
	}
	if c := calls.Load(); c != 1 {
		t.Errorf("/token called %d times — singleflight should collapse to 1", c)
	}
}

func TestOAuthFlow_Login_HappyPath(t *testing.T) {
	setupCredsHome(t)

	// Mock token endpoint returns a fresh token.
	tokSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "authorization_code" {
			http.Error(w, "wrong grant", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"NEW","refresh_token":"RR","token_type":"Bearer","expires_in":3600}`))
	}))
	defer tokSrv.Close()

	// Authorize endpoint — but we won't actually hit it; instead we
	// inject a BrowserOpener that immediately POSTs the callback.
	f := NewOAuthFlow()
	var redirectURI string
	f.BrowserOpener = func(target string) error {
		// Parse target, extract redirect_uri, and POST a fake callback.
		u, err := url.Parse(target)
		if err != nil {
			return err
		}
		redirectURI = u.Query().Get("redirect_uri")
		state := u.Query().Get("state")
		go func() {
			// Wait briefly for the listener to be ready.
			time.Sleep(20 * time.Millisecond)
			cb, _ := url.Parse(redirectURI)
			q := cb.Query()
			q.Set("code", "AUTHCODE")
			q.Set("state", state)
			cb.RawQuery = q.Encode()
			_, _ = http.Get(cb.String())
		}()
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := f.Login(ctx, "test", OAuthServerConfig{
		ClientID:     "client-1",
		AuthorizeURL: "https://example.com/authorize",
		TokenURL:     tokSrv.URL,
		Scopes:       []string{"read"},
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	// Verify token was persisted.
	loaded, err := oauth.LoadCredentials("mcp_test")
	if err != nil || loaded == nil {
		t.Fatalf("LoadCredentials: %v, %v", loaded, err)
	}
	if loaded.AccessToken != "NEW" {
		t.Errorf("AccessToken = %q, want NEW", loaded.AccessToken)
	}
}

func TestOAuthFlow_Login_StateMismatch(t *testing.T) {
	setupCredsHome(t)
	tokSrv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	defer tokSrv.Close()
	f := NewOAuthFlow()
	f.BrowserOpener = func(target string) error {
		u, _ := url.Parse(target)
		redirectURI := u.Query().Get("redirect_uri")
		go func() {
			time.Sleep(20 * time.Millisecond)
			cb, _ := url.Parse(redirectURI)
			q := cb.Query()
			q.Set("code", "x")
			q.Set("state", "WRONG-STATE")
			cb.RawQuery = q.Encode()
			_, _ = http.Get(cb.String())
		}()
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := f.Login(ctx, "test", OAuthServerConfig{
		ClientID: "c", AuthorizeURL: "https://example.com/auth", TokenURL: tokSrv.URL,
	})
	if !errors.Is(err, ErrProtocolError) {
		t.Fatalf("state mismatch should yield ErrProtocolError, got %v", err)
	}
}

func TestOAuthFlow_Logout_DeletesTokens(t *testing.T) {
	dir := setupCredsHome(t)
	if err := oauth.SaveCredentials("mcp_doomed", &oauth.Token{
		AccessToken: "X", ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}
	// File exists somewhere under HOME/.forge/credentials
	credsPath := filepath.Join(dir, ".forge", "credentials", "mcp_doomed.json")
	if _, err := os.Stat(credsPath); err != nil {
		t.Fatalf("credentials file expected at %s: %v", credsPath, err)
	}

	f := NewOAuthFlow()
	if err := f.Logout("doomed"); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, err := os.Stat(credsPath); !os.IsNotExist(err) {
		t.Fatalf("expected credentials file gone, stat err = %v", err)
	}
}

func TestBuildAuthorizeURL_HasPKCEParams(t *testing.T) {
	t.Parallel()
	got, err := buildAuthorizeURL(
		"https://example.com/authorize",
		"client-1",
		"http://127.0.0.1:8080/callback",
		"STATE",
		"CHALLENGE",
		[]string{"read", "write"},
	)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(got)
	q := u.Query()
	checks := map[string]string{
		"response_type":         "code",
		"client_id":             "client-1",
		"redirect_uri":          "http://127.0.0.1:8080/callback",
		"state":                 "STATE",
		"code_challenge":        "CHALLENGE",
		"code_challenge_method": "S256",
		"scope":                 "read write",
	}
	for k, want := range checks {
		if got := q.Get(k); got != want {
			t.Errorf("query[%s] = %q, want %q", k, got, want)
		}
	}
}
