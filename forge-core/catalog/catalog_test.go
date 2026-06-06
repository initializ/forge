package catalog_test

import (
	"testing"

	"github.com/initializ/forge/forge-core/auth"
	"github.com/initializ/forge/forge-core/catalog"

	// Blank-import the auth providers so their init() registers them, letting
	// TestAuthModesMatchRegistry cross-check the catalog against the registry.
	_ "github.com/initializ/forge/forge-core/auth/providers/aws_sigv4"
	_ "github.com/initializ/forge/forge-core/auth/providers/azure_ad"
	_ "github.com/initializ/forge/forge-core/auth/providers/gcp_iap"
	_ "github.com/initializ/forge/forge-core/auth/providers/httpverifier"
	_ "github.com/initializ/forge/forge-core/auth/providers/oidc"
)

func TestProvidersWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range catalog.AllProviders() {
		if p.ID == "" || p.Label == "" {
			t.Errorf("provider has empty id/label: %+v", p)
		}
		if seen[p.ID] {
			t.Errorf("duplicate provider id %q", p.ID)
		}
		seen[p.ID] = true
		if p.NeedsAPIKey && p.APIKeyEnvVar == "" {
			t.Errorf("provider %q needs an API key but has no APIKeyEnvVar", p.ID)
		}
		if !p.IsCustom && p.DefaultModel == "" {
			t.Errorf("non-custom provider %q has no DefaultModel", p.ID)
		}
		for _, m := range p.Models {
			if m.ModelID == "" || m.Label == "" {
				t.Errorf("provider %q has a malformed model: %+v", p.ID, m)
			}
		}
	}
	if _, ok := catalog.ProviderByID("openai"); !ok {
		t.Error("expected openai provider in catalog")
	}
	if _, ok := catalog.ProviderByID("nope"); ok {
		t.Error("ProviderByID returned ok for a missing id")
	}
}

func TestChannelsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range catalog.AllChannels() {
		if c.ID == "" || c.Label == "" {
			t.Errorf("channel has empty id/label: %+v", c)
		}
		if seen[c.ID] {
			t.Errorf("duplicate channel id %q", c.ID)
		}
		seen[c.ID] = true
		for _, cr := range c.Credentials {
			if cr.EnvVar == "" || cr.Prompt == "" {
				t.Errorf("channel %q has a malformed credential: %+v", c.ID, cr)
			}
		}
	}
	none, ok := catalog.ChannelByID("none")
	if !ok || len(none.Credentials) != 0 {
		t.Error("expected a 'none' channel with no credentials")
	}
}

func TestAuthModesWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, m := range catalog.AllAuthModes() {
		if m.ID == "" || m.Label == "" {
			t.Errorf("auth mode has empty id/label: %+v", m)
		}
		if seen[m.ID] {
			t.Errorf("duplicate auth mode id %q", m.ID)
		}
		seen[m.ID] = true
		for _, f := range m.Fields {
			if f.Key == "" || f.Prompt == "" {
				t.Errorf("auth mode %q has a malformed field: %+v", m.ID, f)
			}
		}
	}
	if m, ok := catalog.AuthModeByID("none"); !ok || len(m.Fields) != 0 {
		t.Error("expected a 'none' auth mode with no fields")
	}
}

// TestAuthModesMatchRegistry ensures every provider-backed auth mode in the
// catalog corresponds to a real registered auth provider, so the wizard can
// never offer a mode that the runtime cannot build. "none" and "custom" are
// wizard-only and exempt.
func TestAuthModesMatchRegistry(t *testing.T) {
	registered := map[string]bool{}
	for _, name := range auth.RegisteredTypes() {
		registered[name] = true
	}
	for _, m := range catalog.AllAuthModes() {
		if m.ID == "none" || m.ID == "custom" {
			continue
		}
		if !registered[m.ID] {
			t.Errorf("catalog auth mode %q is not a registered auth provider (registered: %v)", m.ID, auth.RegisteredTypes())
		}
	}
}
