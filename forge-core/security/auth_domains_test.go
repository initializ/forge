package security_test

import (
	"reflect"
	"testing"

	"github.com/initializ/forge/forge-core/security"
	"github.com/initializ/forge/forge-core/types"
)

func TestAuthDomains_Empty(t *testing.T) {
	if got := security.AuthDomains(types.AuthConfig{}); got != nil {
		t.Errorf("AuthDomains(empty) = %v, want nil", got)
	}
}

func TestAuthDomains_OIDCIssuer(t *testing.T) {
	got := security.AuthDomains(types.AuthConfig{
		Providers: []types.AuthProvider{
			{Type: "oidc", Settings: map[string]any{
				"issuer":   "https://login.example.com",
				"audience": "api://forge",
			}},
		},
	})
	want := []string{"login.example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("AuthDomains = %v, want %v", got, want)
	}
}

func TestAuthDomains_HTTPVerifier(t *testing.T) {
	got := security.AuthDomains(types.AuthConfig{
		Providers: []types.AuthProvider{
			{Type: "http_verifier", Settings: map[string]any{
				"url": "https://verify.example.com/verify",
			}},
		},
	})
	want := []string{"verify.example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("AuthDomains = %v, want %v", got, want)
	}
}

func TestAuthDomains_OIDCWithExplicitJWKSURL(t *testing.T) {
	got := security.AuthDomains(types.AuthConfig{
		Providers: []types.AuthProvider{
			{Type: "oidc", Settings: map[string]any{
				"issuer":   "https://login.example.com",
				"audience": "api://forge",
				"jwks_url": "https://keys.example.com/.well-known/jwks.json",
			}},
		},
	})
	want := []string{"keys.example.com", "login.example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("AuthDomains = %v, want %v", got, want)
	}
}

func TestAuthDomains_MultiProviderDedup(t *testing.T) {
	got := security.AuthDomains(types.AuthConfig{
		Providers: []types.AuthProvider{
			{Type: "oidc", Settings: map[string]any{
				"issuer":   "https://login.example.com",
				"audience": "api://forge",
			}},
			{Type: "http_verifier", Settings: map[string]any{
				"url": "https://login.example.com/verify",
			}},
			{Type: "static_token", Settings: map[string]any{"token_env": "X"}},
		},
	})
	want := []string{"login.example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("AuthDomains = %v, want %v (must dedup across providers)", got, want)
	}
}

func TestAuthDomains_StaticTokenContributesNothing(t *testing.T) {
	got := security.AuthDomains(types.AuthConfig{
		Providers: []types.AuthProvider{
			{Type: "static_token", Settings: map[string]any{"token_env": "X"}},
		},
	})
	if got != nil {
		t.Errorf("static_token-only config should contribute no domains, got %v", got)
	}
}

func TestAuthDomains_MalformedURLsSkipped(t *testing.T) {
	got := security.AuthDomains(types.AuthConfig{
		Providers: []types.AuthProvider{
			{Type: "oidc", Settings: map[string]any{
				"issuer":   "://not a url",
				"audience": "api://forge",
			}},
			{Type: "oidc", Settings: map[string]any{
				"issuer":   "https://valid.example.com",
				"audience": "api://forge",
			}},
		},
	})
	// Malformed URL silently skipped (validate package handles surface errors).
	want := []string{"valid.example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("AuthDomains = %v, want %v", got, want)
	}
}

func TestAuthDomains_PortStripped(t *testing.T) {
	got := security.AuthDomains(types.AuthConfig{
		Providers: []types.AuthProvider{
			{Type: "oidc", Settings: map[string]any{
				"issuer":   "http://localhost:8080/realms/dev",
				"audience": "api://forge",
			}},
		},
	})
	want := []string{"localhost"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("AuthDomains = %v, want %v (port must be stripped)", got, want)
	}
}

func TestAuthDomains_UnknownProviderTypeReturnsEmpty(t *testing.T) {
	got := security.AuthDomains(types.AuthConfig{
		Providers: []types.AuthProvider{
			{Type: "future_provider", Settings: map[string]any{"url": "https://x.example.com"}},
		},
	})
	if got != nil {
		t.Errorf("unknown provider type returned domains: %v (must be nil — extractor not registered)", got)
	}
}
