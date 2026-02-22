package security

import (
	"testing"
)

func TestResolveCapabilities_Slack(t *testing.T) {
	domains := ResolveCapabilities([]string{"slack"})
	expected := map[string]bool{
		"slack.com":       true,
		"hooks.slack.com": true,
		"api.slack.com":   true,
	}
	if len(domains) != len(expected) {
		t.Fatalf("got %d domains, want %d: %v", len(domains), len(expected), domains)
	}
	for _, d := range domains {
		if !expected[d] {
			t.Errorf("unexpected domain %q", d)
		}
	}
}

func TestResolveCapabilities_Telegram(t *testing.T) {
	domains := ResolveCapabilities([]string{"telegram"})
	if len(domains) != 1 {
		t.Fatalf("got %d domains, want 1: %v", len(domains), domains)
	}
	if domains[0] != "api.telegram.org" {
		t.Errorf("got %q, want %q", domains[0], "api.telegram.org")
	}
}

func TestResolveCapabilities_Unknown(t *testing.T) {
	domains := ResolveCapabilities([]string{"discord"})
	if len(domains) != 0 {
		t.Errorf("expected empty for unknown capability, got %v", domains)
	}
}

func TestResolveCapabilities_Dedup(t *testing.T) {
	domains := ResolveCapabilities([]string{"slack", "slack"})
	// Should deduplicate: slack.com, hooks.slack.com, api.slack.com
	if len(domains) != 3 {
		t.Errorf("got %d domains after dedup, want 3: %v", len(domains), domains)
	}
}

func TestResolveCapabilities_Nil(t *testing.T) {
	domains := ResolveCapabilities(nil)
	if len(domains) != 0 {
		t.Errorf("expected empty for nil input, got %v", domains)
	}
}
