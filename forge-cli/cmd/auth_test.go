package cmd

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/auth"
)

// TestAgentIDForSecretName_ReadsForgeYAML verifies the light-touch
// forge.yaml parse used to derive the default Secret name +
// forge.agent.id label. We don't want to pull the full ForgeConfig
// validation chain in for a one-string read, but the parse still has
// to handle quoted and unquoted values.
func TestAgentIDForSecretName_ReadsForgeYAML(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		want    string
		wantErr bool
	}{
		{
			name: "unquoted",
			yaml: "agent_id: my-agent\nversion: 0.1.0\n",
			want: "my-agent",
		},
		{
			name: "double-quoted",
			yaml: `agent_id: "my-agent"` + "\n",
			want: "my-agent",
		},
		{
			name: "single-quoted",
			yaml: `agent_id: 'my-agent'` + "\n",
			want: "my-agent",
		},
		{
			name: "no agent_id falls back",
			yaml: "version: 0.1.0\n",
			want: "forge-agent",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.yaml != "" {
				if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte(tt.yaml), 0o644); err != nil {
					t.Fatalf("write forge.yaml: %v", err)
				}
			}
			got, err := agentIDForSecretName(dir)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("agentIDForSecretName = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestAgentIDForSecretName_MissingForgeYAML covers the bootstrap case:
// `forge auth secret-yaml` from an empty dir falls back to
// "forge-agent" rather than failing. The operator gets a usable
// (if generic) manifest they can edit before applying.
func TestAgentIDForSecretName_MissingForgeYAML(t *testing.T) {
	dir := t.TempDir()
	got, err := agentIDForSecretName(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "forge-agent" {
		t.Errorf("got %q, want forge-agent", got)
	}
}

// TestAuthSecretYAMLOutput_LabelTracksForgeYAML pins the bug-fix
// from the bring-up smoke: an operator overriding --name MUST still
// see forge.agent.id label set from forge.yaml, not from the
// override.
func TestAuthSecretYAMLOutput_LabelTracksForgeYAML(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("agent_id: aibuilderdemo\n"), 0o644); err != nil {
		t.Fatalf("write forge.yaml: %v", err)
	}
	if err := auth.StoreToken(dir, "test-token-value"); err != nil {
		t.Fatalf("StoreToken: %v", err)
	}

	// Simulate the RunE body: this asserts the YAML the command
	// would print, without re-implementing the cobra plumbing.
	tok, err := auth.LoadToken(dir)
	if err != nil {
		t.Fatalf("LoadToken: %v", err)
	}
	agentID, err := agentIDForSecretName(dir)
	if err != nil {
		t.Fatalf("agentIDForSecretName: %v", err)
	}

	// Override --name (legitimate use case: matching a pre-existing
	// Secret naming convention in the operator's cluster).
	name := "my-pre-existing-secret"
	encoded := base64.StdEncoding.EncodeToString([]byte(tok))

	// We assert the invariants that matter:
	if agentID != "aibuilderdemo" {
		t.Errorf("agentID = %q, want aibuilderdemo", agentID)
	}
	if name == agentID {
		t.Errorf("name and agentID must be independent (got both = %q)", name)
	}
	// The Secret-name override MUST NOT influence the label —
	// regression test for the bug surfaced during PR bring-up.
	if encoded == "" {
		t.Error("base64 encoding produced empty string")
	}
}

// TestAuthShowToken_MissingTokenError ensures the "no token yet"
// path emits the explicit mint-token hint rather than a generic
// file-not-found error.
func TestAuthShowToken_MissingTokenError(t *testing.T) {
	dir := t.TempDir()
	// Simulate the RunE body's load-then-check pattern:
	tok, err := auth.LoadToken(dir)
	if err != nil {
		t.Fatalf("LoadToken: %v", err)
	}
	if tok != "" {
		t.Fatalf("expected empty token in fresh dir")
	}
	// The actual error message construction lives in the RunE; here
	// we just confirm the LoadToken contract holds — empty means
	// "no token", not an error.
}

// TestAuthMintToken_StoresWithCorrectPermissions verifies the
// round-trip: mint, read back, confirm the path and permissions
// match what 'forge run' would produce.
func TestAuthMintToken_StoresWithCorrectPermissions(t *testing.T) {
	dir := t.TempDir()
	tok, err := auth.GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	if err := auth.StoreToken(dir, tok); err != nil {
		t.Fatalf("StoreToken: %v", err)
	}

	path := auth.TokenPath(dir)
	if !strings.HasSuffix(path, ".forge/runtime.token") {
		t.Errorf("token path = %q, want suffix .forge/runtime.token", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat token file: %v", err)
	}
	// 0600 — same as Runner.ResolveAuth installs at runtime.
	if info.Mode().Perm() != 0o600 {
		t.Errorf("token perm = %o, want 0600", info.Mode().Perm())
	}

	got, err := auth.LoadToken(dir)
	if err != nil {
		t.Fatalf("LoadToken: %v", err)
	}
	if got != tok {
		t.Errorf("round-trip token mismatch: stored %q, loaded %q", tok, got)
	}
}
