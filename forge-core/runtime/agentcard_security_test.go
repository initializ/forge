package runtime

import (
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/types"
)

// Regression tests for SecuritySchemes derivation from the configured
// auth chain (issue #85 / FWS-1 deliverable: "authentication schemes
// match A2A standard").

func TestPopulateSecuritySchemes_NoOpWhenAuthChainEmpty(t *testing.T) {
	card := &a2a.AgentCard{Name: "agent", URL: "http://x", Version: "0.1.0", ProtocolVersion: "0.3.0"}
	PopulateSecuritySchemes(card, &types.ForgeConfig{})
	if len(card.SecuritySchemes) != 0 {
		t.Errorf("no auth chain should emit no schemes, got %v", card.SecuritySchemes)
	}
	if len(card.Security) != 0 {
		t.Errorf("no auth chain should emit no Security entries, got %v", card.Security)
	}
}

func TestPopulateSecuritySchemes_NoOpWhenCfgNil(t *testing.T) {
	card := &a2a.AgentCard{}
	PopulateSecuritySchemes(card, nil) // must not panic
	if len(card.SecuritySchemes) != 0 {
		t.Errorf("nil cfg should emit no schemes")
	}
}

func TestPopulateSecuritySchemes_StaticTokenMapsToHTTPBearer(t *testing.T) {
	card := &a2a.AgentCard{}
	cfg := &types.ForgeConfig{
		Auth: types.AuthConfig{Providers: []types.AuthProvider{
			{Type: "static_token"},
		}},
	}
	PopulateSecuritySchemes(card, cfg)

	s, ok := card.SecuritySchemes["static_token"]
	if !ok {
		t.Fatalf("expected scheme keyed by provider type")
	}
	if s.Type != "http" || s.Scheme != "bearer" {
		t.Errorf("static_token = %+v, want http + bearer", s)
	}
}

func TestPopulateSecuritySchemes_OIDCEmitsDiscoveryURL(t *testing.T) {
	card := &a2a.AgentCard{}
	cfg := &types.ForgeConfig{
		Auth: types.AuthConfig{Providers: []types.AuthProvider{
			{Type: "oidc", Name: "okta", Settings: map[string]any{
				"issuer": "https://acme.okta.com/",
			}},
		}},
	}
	PopulateSecuritySchemes(card, cfg)

	s := card.SecuritySchemes["okta"]
	if s == nil {
		t.Fatalf("expected scheme keyed by Name=okta")
	}
	if s.Type != "openIdConnect" {
		t.Errorf("Type = %q, want openIdConnect", s.Type)
	}
	if want := "https://acme.okta.com/.well-known/openid-configuration"; s.OpenIDConnectURL != want {
		t.Errorf("OpenIDConnectURL = %q, want %q (trailing slash trimmed)", s.OpenIDConnectURL, want)
	}
}

func TestPopulateSecuritySchemes_AzureADUsesTenantInDiscovery(t *testing.T) {
	card := &a2a.AgentCard{}
	cfg := &types.ForgeConfig{
		Auth: types.AuthConfig{Providers: []types.AuthProvider{
			{Type: "azure_ad", Settings: map[string]any{
				"tenant_id": "11111111-2222-3333-4444-555555555555",
			}},
		}},
	}
	PopulateSecuritySchemes(card, cfg)
	s := card.SecuritySchemes["azure_ad"]
	if s == nil || s.OpenIDConnectURL == "" {
		t.Fatalf("expected azure_ad openIdConnect scheme with discovery URL, got %+v", s)
	}
	if !strings.Contains(s.OpenIDConnectURL, "11111111-2222-3333-4444-555555555555") {
		t.Errorf("OpenIDConnectURL %q should embed tenant id", s.OpenIDConnectURL)
	}
}

func TestPopulateSecuritySchemes_GCPIAPMapsToAPIKeyHeader(t *testing.T) {
	card := &a2a.AgentCard{}
	cfg := &types.ForgeConfig{
		Auth: types.AuthConfig{Providers: []types.AuthProvider{
			{Type: "gcp_iap"},
		}},
	}
	PopulateSecuritySchemes(card, cfg)
	s := card.SecuritySchemes["gcp_iap"]
	if s == nil {
		t.Fatal("expected gcp_iap scheme")
	}
	if s.Type != "apiKey" || s.In != "header" || s.Name != "X-Goog-Iap-Jwt-Assertion" {
		t.Errorf("gcp_iap mapping = %+v, want apiKey in header X-Goog-Iap-Jwt-Assertion", s)
	}
}

func TestPopulateSecuritySchemes_AWSSigv4MapsToBearerWithCustomFormat(t *testing.T) {
	card := &a2a.AgentCard{}
	cfg := &types.ForgeConfig{
		Auth: types.AuthConfig{Providers: []types.AuthProvider{
			{Type: "aws_sigv4"},
		}},
	}
	PopulateSecuritySchemes(card, cfg)
	s := card.SecuritySchemes["aws_sigv4"]
	if s == nil {
		t.Fatal("expected aws_sigv4 scheme")
	}
	if s.Type != "http" || s.Scheme != "bearer" || s.BearerFormat != "forge-aws-v1" {
		t.Errorf("aws_sigv4 mapping = %+v, want http+bearer+forge-aws-v1", s)
	}
}

func TestPopulateSecuritySchemes_ChainEmitsSecurityArrayInOrder(t *testing.T) {
	card := &a2a.AgentCard{}
	cfg := &types.ForgeConfig{
		Auth: types.AuthConfig{Providers: []types.AuthProvider{
			{Type: "static_token"},
			{Type: "oidc", Name: "okta", Settings: map[string]any{"issuer": "https://i.example.com"}},
		}},
	}
	PopulateSecuritySchemes(card, cfg)

	if len(card.Security) != 2 {
		t.Fatalf("expected one Security entry per provider, got %d", len(card.Security))
	}
	// First-match-wins → OR semantics on the outer list per A2A 0.3.0.
	first := card.Security[0]
	if _, ok := first["static_token"]; !ok {
		t.Errorf("Security[0] should reference static_token, got %v", first)
	}
	second := card.Security[1]
	if _, ok := second["okta"]; !ok {
		t.Errorf("Security[1] should reference okta, got %v", second)
	}
}

func TestPopulateSecuritySchemes_PreservesPreviouslyConfiguredScheme(t *testing.T) {
	// Callers may have hand-wired a scheme before invoking the deriver.
	// PopulateSecuritySchemes must be additive and not clobber.
	card := &a2a.AgentCard{
		SecuritySchemes: map[string]*a2a.SecurityScheme{
			"static_token": {Type: "http", Scheme: "bearer", Description: "operator-supplied description"},
		},
	}
	cfg := &types.ForgeConfig{
		Auth: types.AuthConfig{Providers: []types.AuthProvider{
			{Type: "static_token"},
		}},
	}
	PopulateSecuritySchemes(card, cfg)

	if d := card.SecuritySchemes["static_token"].Description; d != "operator-supplied description" {
		t.Errorf("scheme clobbered: Description = %q", d)
	}
}
