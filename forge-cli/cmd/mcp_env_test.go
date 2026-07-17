package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadForgeConfig_ExpandsMCPEnv verifies the mcp-subcommand loader
// (used by `forge mcp login`/`list`/`test`) routes through ParseForgeConfig
// so ${VAR} placeholders in MCP fields expand — #323 review finding 1. The
// bug was that `mcp.go:loadForgeConfig` did a raw yaml.Unmarshal, so a
// managed `client_id: ${…}` reached the OAuth flow as a literal string.
func TestLoadForgeConfig_ExpandsMCPEnv(t *testing.T) {
	const y = `
agent_id: a
version: 0.1.0
framework: custom
entrypoint: /bin/true
mcp:
  servers:
    - name: linear
      transport: http
      url: https://mcp.linear.app/mcp
      auth:
        type: oauth
        client_id: ${MCP_LOGIN_CID}
        authorize_url: https://as/authorize
        token_url: https://as/token
      tools: { allow: ["*"] }
`
	writeCfg := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte(y), 0o644); err != nil {
			t.Fatal(err)
		}
		return filepath.Join(dir, "forge.yaml")
	}
	withCfgFile := func(path string) func() {
		old := cfgFile
		cfgFile = path
		return func() { cfgFile = old }
	}

	t.Run("from process env", func(t *testing.T) {
		p := writeCfg(t)
		t.Setenv("MCP_LOGIN_CID", "cid-from-env")
		defer withCfgFile(p)()
		cfg, err := loadForgeConfig(nil)
		if err != nil {
			t.Fatalf("loadForgeConfig: %v", err)
		}
		if got := cfg.MCP.Servers[0].Auth.ClientID; got != "cid-from-env" {
			t.Errorf("client_id = %q, want expanded from the process env", got)
		}
	})

	t.Run("from .env file — login must resolve .env too", func(t *testing.T) {
		p := writeCfg(t)
		if err := os.WriteFile(filepath.Join(filepath.Dir(p), ".env"), []byte("MCP_LOGIN_CID=cid-from-dotenv\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("MCP_LOGIN_CID", "") // present-but-empty → .env value fills it
		defer withCfgFile(p)()
		cfg, err := loadForgeConfig(nil)
		if err != nil {
			t.Fatalf("loadForgeConfig: %v", err)
		}
		if got := cfg.MCP.Servers[0].Auth.ClientID; got != "cid-from-dotenv" {
			t.Errorf("client_id = %q, want expanded from the .env file", got)
		}
	})
}
