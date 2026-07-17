package types

import (
	"reflect"
	"testing"
)

// baseMCPYAML is a minimal valid forge.yaml with one oauth MCP server
// whose connection fields are ${VAR} placeholders (#321).
const baseMCPYAML = `
agent_id: a
version: 0.1.0
framework: custom
entrypoint: /bin/true
mcp:
  servers:
    - name: linear
      transport: http
      url: ${MCP_LINEAR_URL}
      auth:
        type: oauth
        client_id: ${MCP_LINEAR_CLIENT_ID}
        authorize_url: ${MCP_LINEAR_AUTHORIZE_URL}
        token_url: ${MCP_LINEAR_TOKEN_URL}
        scopes: ["${MCP_LINEAR_SCOPES}"]
      tools: { allow: [create_issue] }
`

// TestParseForgeConfig_ExpandsMCPEnv: with the vars set, the placeholders
// expand to their values and one whitespace-joined scopes var splits into
// multiple scopes — the managed-mode "explicit config" shape.
func TestParseForgeConfig_ExpandsMCPEnv(t *testing.T) {
	t.Setenv("MCP_LINEAR_URL", "https://mcp.linear.app/mcp")
	t.Setenv("MCP_LINEAR_CLIENT_ID", "dyn-abc")
	t.Setenv("MCP_LINEAR_AUTHORIZE_URL", "https://mcp.linear.app/authorize")
	t.Setenv("MCP_LINEAR_TOKEN_URL", "https://mcp.linear.app/token")
	t.Setenv("MCP_LINEAR_SCOPES", "read write")

	cfg, err := ParseForgeConfig([]byte(baseMCPYAML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	s := cfg.MCP.Servers[0]
	if s.URL != "https://mcp.linear.app/mcp" {
		t.Errorf("URL not expanded: %q", s.URL)
	}
	if s.Auth.ClientID != "dyn-abc" {
		t.Errorf("client_id not expanded: %q", s.Auth.ClientID)
	}
	if s.Auth.AuthorizeURL != "https://mcp.linear.app/authorize" {
		t.Errorf("authorize_url not expanded: %q", s.Auth.AuthorizeURL)
	}
	if s.Auth.TokenURL != "https://mcp.linear.app/token" {
		t.Errorf("token_url not expanded: %q", s.Auth.TokenURL)
	}
	if !reflect.DeepEqual(s.Auth.Scopes, []string{"read", "write"}) {
		t.Errorf("scopes = %v, want [read write] (single var split on whitespace)", s.Auth.Scopes)
	}
}

// TestParseForgeConfig_UnsetMCPEnv: with the vars unset, placeholders
// expand to "" — so the oauth block reads as unconfigured (discovery /
// fail-closed at login), never a literal `${…}` dial.
func TestParseForgeConfig_UnsetMCPEnv(t *testing.T) {
	// Ensure the vars are unset for this test.
	for _, k := range []string{"MCP_LINEAR_URL", "MCP_LINEAR_CLIENT_ID", "MCP_LINEAR_AUTHORIZE_URL", "MCP_LINEAR_TOKEN_URL", "MCP_LINEAR_SCOPES"} {
		t.Setenv(k, "") // set-then-empty; Setenv restores original after the test
	}
	cfg, err := ParseForgeConfig([]byte(baseMCPYAML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	s := cfg.MCP.Servers[0]
	for name, got := range map[string]string{
		"url": s.URL, "client_id": s.Auth.ClientID,
		"authorize_url": s.Auth.AuthorizeURL, "token_url": s.Auth.TokenURL,
	} {
		if got != "" {
			t.Errorf("%s = %q, want empty (unset var must not survive as a literal ${…})", name, got)
		}
	}
	if len(s.Auth.Scopes) != 0 {
		t.Errorf("scopes = %v, want empty (unset var drops)", s.Auth.Scopes)
	}
}

// TestParseForgeConfig_TokenEnvNotExpanded: TokenEnv is a NAME, not a
// value — it must survive verbatim even when it looks like a placeholder.
func TestParseForgeConfig_TokenEnvNotExpanded(t *testing.T) {
	t.Setenv("SOME_TOKEN", "should-not-be-substituted")
	const y = `
agent_id: a
version: 0.1.0
framework: custom
entrypoint: /bin/true
mcp:
  servers:
    - name: internal
      transport: http
      url: http://x
      auth:
        type: bearer
        token_env: SOME_TOKEN
      tools: { allow: ["*"] }
`
	cfg, err := ParseForgeConfig([]byte(y))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := cfg.MCP.Servers[0].Auth.TokenEnv; got != "SOME_TOKEN" {
		t.Errorf("token_env = %q, want the literal name SOME_TOKEN (must not be expanded)", got)
	}
}

// TestParseForgeConfig_LiteralMCPUnchanged: a config with no placeholders
// round-trips unchanged (no accidental splitting/rewriting).
func TestParseForgeConfig_LiteralMCPUnchanged(t *testing.T) {
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
        client_id: static-id
        authorize_url: https://as/authorize
        token_url: https://as/token
        scopes: [read, write]
      tools: { allow: ["*"] }
`
	cfg, err := ParseForgeConfig([]byte(y))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	s := cfg.MCP.Servers[0]
	if s.URL != "https://mcp.linear.app/mcp" || s.Auth.ClientID != "static-id" {
		t.Errorf("literal fields altered: %+v", s.Auth)
	}
	if !reflect.DeepEqual(s.Auth.Scopes, []string{"read", "write"}) {
		t.Errorf("literal scopes altered: %v", s.Auth.Scopes)
	}
}
