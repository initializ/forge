package security

import (
	"testing"
)

func TestResolve_DenyAll(t *testing.T) {
	cfg, err := Resolve("strict", "deny-all", nil, nil, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Profile != ProfileStrict {
		t.Errorf("Profile = %q, want %q", cfg.Profile, ProfileStrict)
	}
	if cfg.Mode != ModeDenyAll {
		t.Errorf("Mode = %q, want %q", cfg.Mode, ModeDenyAll)
	}
	if len(cfg.AllDomains) != 0 {
		t.Errorf("AllDomains should be empty, got %v", cfg.AllDomains)
	}
}

func TestResolve_DevOpen(t *testing.T) {
	cfg, err := Resolve("permissive", "dev-open", nil, nil, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Mode != ModeDevOpen {
		t.Errorf("Mode = %q, want %q", cfg.Mode, ModeDevOpen)
	}
}

func TestResolve_Allowlist(t *testing.T) {
	explicit := []string{"api.example.com", "data.example.com"}
	tools := []string{"web_search", "github_api"}

	cfg, err := Resolve("standard", "allowlist", explicit, tools, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Mode != ModeAllowlist {
		t.Errorf("Mode = %q, want %q", cfg.Mode, ModeAllowlist)
	}
	if len(cfg.AllowedDomains) != 2 {
		t.Errorf("AllowedDomains count = %d, want 2", len(cfg.AllowedDomains))
	}
	if len(cfg.ToolDomains) == 0 {
		t.Error("ToolDomains should not be empty for web_search + github_api")
	}
	if len(cfg.AllDomains) == 0 {
		t.Error("AllDomains should not be empty")
	}

	// Check deduplication
	seen := make(map[string]bool)
	for _, d := range cfg.AllDomains {
		if seen[d] {
			t.Errorf("duplicate domain in AllDomains: %s", d)
		}
		seen[d] = true
	}
}

func TestResolve_Defaults(t *testing.T) {
	cfg, err := Resolve("", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Profile != ProfileStrict {
		t.Errorf("Profile = %q, want default %q", cfg.Profile, ProfileStrict)
	}
	if cfg.Mode != ModeDenyAll {
		t.Errorf("Mode = %q, want default %q", cfg.Mode, ModeDenyAll)
	}
}

func TestResolve_InvalidProfile(t *testing.T) {
	_, err := Resolve("invalid", "deny-all", nil, nil, nil)
	if err == nil {
		t.Fatal("expected error for invalid profile")
	}
}

func TestResolve_InvalidMode(t *testing.T) {
	_, err := Resolve("strict", "invalid", nil, nil, nil)
	if err == nil {
		t.Fatal("expected error for invalid mode")
	}
}

func TestInferToolDomains(t *testing.T) {
	domains := InferToolDomains([]string{"web_search", "github_api"})
	if len(domains) == 0 {
		t.Fatal("expected inferred domains")
	}
	// Check web_search domains included
	found := false
	for _, d := range domains {
		if d == "api.perplexity.ai" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected api.perplexity.ai in inferred domains for web_search")
	}
}

func TestInferToolDomains_Unknown(t *testing.T) {
	domains := InferToolDomains([]string{"unknown_tool"})
	if len(domains) != 0 {
		t.Errorf("expected no domains for unknown tool, got %v", domains)
	}
}

func TestResolve_AllowlistWithCapabilities(t *testing.T) {
	explicit := []string{"api.example.com"}
	cfg, err := Resolve("standard", "allowlist", explicit, nil, []string{"slack"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// Check that slack domains are in AllDomains
	want := map[string]bool{
		"api.example.com": true,
		"slack.com":       true,
		"hooks.slack.com": true,
		"api.slack.com":   true,
	}
	for d := range want {
		found := false
		for _, got := range cfg.AllDomains {
			if got == d {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected %q in AllDomains, got %v", d, cfg.AllDomains)
		}
	}
}

func TestResolve_AllowlistTelegramCapability(t *testing.T) {
	cfg, err := Resolve("standard", "allowlist", nil, nil, []string{"telegram"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	found := false
	for _, d := range cfg.AllDomains {
		if d == "api.telegram.org" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected api.telegram.org in AllDomains, got %v", cfg.AllDomains)
	}
}

func TestResolve_CapabilitiesIgnoredForDenyAll(t *testing.T) {
	cfg, err := Resolve("strict", "deny-all", nil, nil, []string{"slack"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(cfg.AllDomains) != 0 {
		t.Errorf("AllDomains should be empty for deny-all, got %v", cfg.AllDomains)
	}
}
