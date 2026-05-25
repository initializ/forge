package runtime

import (
	"os"
	"testing"
)

// TestNit_TokenStorePath_PrecedenceYAMLBeatsEnv pins the
// wiring contract from review B11: yaml mcp.token_store_path wins
// over MCP_TOKEN_STORE_PATH env var, which wins over the default.
func TestNit_TokenStorePath_PrecedenceYAMLBeatsEnv(t *testing.T) {
	t.Setenv("MCP_TOKEN_STORE_PATH", "/from/env")
	if got := mcpTokenStorePath("/from/yaml"); got != "/from/yaml" {
		t.Errorf("yaml-set: got %q, want /from/yaml", got)
	}
	if got := mcpTokenStorePath(""); got != "/from/env" {
		t.Errorf("yaml-empty + env-set: got %q, want /from/env", got)
	}
}

func TestNit_TokenStorePath_DefaultsToEmpty(t *testing.T) {
	// Use t.Setenv with an explicit empty value (Go test harness
	// restores after) — Unsetenv directly would survive past the
	// test.
	t.Setenv("MCP_TOKEN_STORE_PATH", "")
	// On macOS some shells inject other env; isolate the var.
	_ = os.Unsetenv("MCP_TOKEN_STORE_PATH")
	if got := mcpTokenStorePath(""); got != "" {
		t.Errorf("both empty: got %q, want \"\"", got)
	}
}
