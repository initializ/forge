package surface

import "testing"

// TestMCPToolsetExcludesShell is a security regression guard: the unsandboxed
// shell tool must NEVER be exposed over MCP (what Claude Code connects to) — only
// the argv-based, no-shell forge-ops + forge_docs. If this fails, an unsandboxed
// host-exec tool has leaked into the Claude Code / deployed surface.
func TestMCPToolsetExcludesShell(t *testing.T) {
	names := map[string]bool{}
	for _, tool := range MCPToolset(t.TempDir()) {
		names[tool.Name()] = true
	}
	if names["shell"] {
		t.Fatal("SECURITY: shell tool must not be in MCPToolset (Claude Code surface)")
	}
	// Sanity: the intended tools ARE present.
	for _, want := range []string{"forge_docs", "forge_scaffold", "forge_cli"} {
		if !names[want] {
			t.Errorf("MCPToolset missing %q", want)
		}
	}
}
