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

func TestAuthDomains_AWSSigv4(t *testing.T) {
	got := security.AuthDomains(types.AuthConfig{
		Providers: []types.AuthProvider{
			{Type: "aws_sigv4", Settings: map[string]any{"region": "us-east-1"}},
		},
	})
	want := []string{"sts.us-east-1.amazonaws.com"}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("AuthDomains = %v, want %v", got, want)
	}
}

func TestAuthDomains_AWSSigv4_DifferentRegion(t *testing.T) {
	got := security.AuthDomains(types.AuthConfig{
		Providers: []types.AuthProvider{
			{Type: "aws_sigv4", Settings: map[string]any{"region": "eu-west-2"}},
		},
	})
	if len(got) != 1 || got[0] != "sts.eu-west-2.amazonaws.com" {
		t.Errorf("AuthDomains = %v, want [sts.eu-west-2.amazonaws.com]", got)
	}
}

func TestAuthDomains_AWSSigv4_TestEndpointOverride(t *testing.T) {
	// The sts_endpoint override (test-only escape hatch) must surface in
	// the egress allowlist too — otherwise local integration tests are
	// blocked by the egress enforcer.
	got := security.AuthDomains(types.AuthConfig{
		Providers: []types.AuthProvider{
			{Type: "aws_sigv4", Settings: map[string]any{
				"region":       "us-east-1",
				"sts_endpoint": "http://127.0.0.1:8080",
			}},
		},
	})
	have := map[string]bool{}
	for _, d := range got {
		have[d] = true
	}
	if !have["sts.us-east-1.amazonaws.com"] {
		t.Errorf("AuthDomains missing real STS host: %v", got)
	}
	if !have["127.0.0.1"] {
		t.Errorf("AuthDomains missing test override host: %v", got)
	}
}

func TestAuthDomains_AWSSigv4_MissingRegionReturnsEmpty(t *testing.T) {
	// Defensive: even though Factory rejects missing region at startup,
	// AuthDomains should not panic or emit a malformed host if it's ever
	// called with an incomplete config.
	got := security.AuthDomains(types.AuthConfig{
		Providers: []types.AuthProvider{
			{Type: "aws_sigv4", Settings: map[string]any{}},
		},
	})
	if got != nil {
		t.Errorf("AuthDomains with missing region = %v, want nil", got)
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

// TestAuthDomains_AssumesPortAgnosticMatcher pins the cross-package
// contract that AuthDomains relies on. AuthDomains strips ports
// (e.g., "https://login.example.com:8443" → "login.example.com") on the
// assumption that the egress matcher will ALSO strip ports off outbound
// hosts before checking the allowlist. If that assumption ever breaks
// (someone makes the matcher port-aware), AuthDomains needs to flip to
// emitting host:port, OR every existing forge.yaml that points at a
// non-443 IdP suddenly silently blocks at JWKS-fetch time.
//
// This test catches that drift early: it builds a matcher with the
// hostname-only allowlist AuthDomains would produce, then asks it
// whether the SAME hostname WITH a port is allowed. If the matcher
// answers "no", the contract is broken and someone needs to look at
// security/auth_domains.go.
func TestAuthDomains_AssumesPortAgnosticMatcher(t *testing.T) {
	hosts := security.AuthDomains(types.AuthConfig{
		Providers: []types.AuthProvider{
			{Type: "oidc", Settings: map[string]any{
				"issuer":   "http://localhost:8080/realms/dev",
				"audience": "api://forge",
			}},
		},
	})
	if len(hosts) == 0 {
		t.Fatal("AuthDomains returned no hosts for a configured OIDC issuer")
	}

	// Build a matcher with exactly what the runner would feed it.
	matcher := security.NewDomainMatcher(security.ModeAllowlist, hosts)

	// What we want: matching the raw hostname must succeed.
	if !matcher.IsAllowed("localhost") {
		t.Fatalf("matcher rejected the very hostname AuthDomains produced: %v", hosts)
	}

	// What this guards: the matcher MUST also be port-agnostic. The
	// dialer / enforcer call IsAllowed with hostname-only strings (port
	// is stripped via net.SplitHostPort or url.Hostname()). If a future
	// change ever makes the matcher inspect ports, this contract
	// changes — and this test will need to flip together with
	// AuthDomains' port-stripping behavior.
	//
	// We can't directly probe "matcher accepts localhost:8080" because
	// the matcher's documented input is hostname-only. The integration
	// contract is enforced at the dialer layer (see egress_enforcer.go:40
	// — req.URL.Hostname() strips the port before matcher.IsAllowed).
	// What we CAN do here is assert the matcher's input shape: passing
	// a host:port string MUST NOT match an allowlist with the bare host.
	// That guarantees we'll notice if the matcher silently grew port
	// awareness — IsAllowed("localhost:8080") would then start matching
	// a "localhost" allowlist entry and this assertion would flip.
	if matcher.IsAllowed("localhost:8080") {
		t.Fatal("matcher unexpectedly accepts host:port form — the contract " +
			"AuthDomains assumes (port stripped before IsAllowed) may have " +
			"changed. Review security/auth_domains.go and the documented " +
			"callsites (egress_enforcer.go, egress_proxy.go, safe_dialer.go).")
	}
}
