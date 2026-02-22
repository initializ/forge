package security

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGenerateAllowlistJSON_DenyAll(t *testing.T) {
	cfg := &EgressConfig{
		Profile: ProfileStrict,
		Mode:    ModeDenyAll,
	}
	data, err := GenerateAllowlistJSON(cfg)
	if err != nil {
		t.Fatalf("GenerateAllowlistJSON: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["profile"] != "strict" {
		t.Errorf("profile = %v, want strict", out["profile"])
	}
	if out["mode"] != "deny-all" {
		t.Errorf("mode = %v, want deny-all", out["mode"])
	}
	// Should have empty arrays, not null
	if domains, ok := out["all_domains"].([]any); !ok || len(domains) != 0 {
		t.Errorf("all_domains should be empty array, got %v", out["all_domains"])
	}
}

func TestGenerateAllowlistJSON_Allowlist(t *testing.T) {
	cfg := &EgressConfig{
		Profile:        ProfileStandard,
		Mode:           ModeAllowlist,
		AllowedDomains: []string{"api.example.com"},
		ToolDomains:    []string{"googleapis.com"},
		AllDomains:     []string{"api.example.com", "googleapis.com"},
	}
	data, err := GenerateAllowlistJSON(cfg)
	if err != nil {
		t.Fatalf("GenerateAllowlistJSON: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	domains := out["all_domains"].([]any)
	if len(domains) != 2 {
		t.Errorf("all_domains count = %d, want 2", len(domains))
	}
}

func TestGenerateK8sNetworkPolicy_DenyAll(t *testing.T) {
	cfg := &EgressConfig{
		Profile: ProfileStrict,
		Mode:    ModeDenyAll,
	}
	data, err := GenerateK8sNetworkPolicy("test-agent", cfg)
	if err != nil {
		t.Fatalf("GenerateK8sNetworkPolicy: %v", err)
	}
	s := string(data)
	if len(s) == 0 {
		t.Fatal("expected non-empty output")
	}
}

func TestGenerateK8sNetworkPolicy_Nil(t *testing.T) {
	_, err := GenerateK8sNetworkPolicy("test-agent", nil)
	if err == nil {
		t.Fatal("expected error for nil config")
	}
}

func TestGenerateK8sNetworkPolicy_Allowlist(t *testing.T) {
	cfg := &EgressConfig{
		Profile:    ProfileStandard,
		Mode:       ModeAllowlist,
		AllDomains: []string{"api.example.com", "hooks.slack.com"},
	}
	data, err := GenerateK8sNetworkPolicy("my-agent", cfg)
	if err != nil {
		t.Fatalf("GenerateK8sNetworkPolicy: %v", err)
	}

	s := string(data)
	// Should contain pod selector
	if !strings.Contains(s, "app: my-agent") {
		t.Error("expected pod selector with agent ID")
	}
	// Should contain port 443
	if !strings.Contains(s, "443") {
		t.Error("expected port 443 in egress rules")
	}
	// Should contain domain annotation
	if !strings.Contains(s, "api.example.com") {
		t.Error("expected domain annotation")
	}
}

func TestGenerateK8sNetworkPolicy_DevOpen(t *testing.T) {
	cfg := &EgressConfig{
		Profile: ProfilePermissive,
		Mode:    ModeDevOpen,
	}
	data, err := GenerateK8sNetworkPolicy("dev-agent", cfg)
	if err != nil {
		t.Fatalf("GenerateK8sNetworkPolicy: %v", err)
	}

	s := string(data)
	// Dev-open should allow egress (not deny)
	if strings.Contains(s, "egress: []") {
		t.Error("dev-open should not deny all egress")
	}
	// Should have ports 80 and 443
	if !strings.Contains(s, "80") || !strings.Contains(s, "443") {
		t.Error("dev-open should allow ports 80 and 443")
	}
}

func TestGenerateAllowlistJSON_NilArraysSafeJSON(t *testing.T) {
	cfg := &EgressConfig{
		Profile: ProfileStandard,
		Mode:    ModeAllowlist,
		// All slices intentionally nil
	}
	data, err := GenerateAllowlistJSON(cfg)
	if err != nil {
		t.Fatalf("GenerateAllowlistJSON: %v", err)
	}

	s := string(data)
	// Should have empty arrays, not null
	if strings.Contains(s, "null") {
		t.Errorf("JSON should not contain null for empty slices: %s", s)
	}
	// Verify JSON parses correctly
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Each domain field should be an empty array
	for _, field := range []string{"allowed_domains", "tool_domains", "all_domains"} {
		arr, ok := out[field].([]any)
		if !ok {
			t.Errorf("%s should be an array, got %T", field, out[field])
		} else if len(arr) != 0 {
			t.Errorf("%s should be empty, got %d items", field, len(arr))
		}
	}
}
