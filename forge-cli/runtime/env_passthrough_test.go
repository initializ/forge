package runtime

import (
	"testing"

	clitools "github.com/initializ/forge/forge-cli/tools"
	"github.com/initializ/forge/forge-core/types"
)

// Governed api/mcp bearer tokens must be collected so they can be withheld from
// the script env passthrough (a script with the token could bypass the PDP).
func TestGovernedToolTokenEnvs(t *testing.T) {
	cfg := &types.ForgeConfig{
		APIs: types.APIConfig{Servers: []types.APIServer{
			{Name: "member-service", Auth: &types.APIAuth{TokenEnv: "API_MEMBER_SERVICE_TOKEN"}},
			{Name: "noauth"}, // nil Auth → contributes nothing
		}},
		MCP: types.MCPConfig{Servers: []types.MCPServer{
			{Name: "jira", Auth: &types.MCPAuth{Type: "bearer", TokenEnv: "MCP_JIRA_TOKEN"}},
			{Name: "oauth-mcp", Auth: &types.MCPAuth{Type: "oauth"}}, // no static token env
		}},
	}
	got := governedToolTokenEnvs(cfg)
	if len(got) != 2 || !got["API_MEMBER_SERVICE_TOKEN"] || !got["MCP_JIRA_TOKEN"] {
		t.Fatalf("governed token env set = %v, want exactly {API_MEMBER_SERVICE_TOKEN, MCP_JIRA_TOKEN}", got)
	}
	if governedToolTokenEnvs(nil) != nil {
		t.Error("nil config should yield nil set")
	}
}

func TestWithoutEnvNames(t *testing.T) {
	in := []string{"HOME_LIKE", "API_MEMBER_SERVICE_TOKEN", "TAVILY_API_KEY"}
	out := withoutEnvNames(in, map[string]bool{"API_MEMBER_SERVICE_TOKEN": true})
	// Governed token removed; the skill's own script secret survives; order kept.
	if len(out) != 2 || out[0] != "HOME_LIKE" || out[1] != "TAVILY_API_KEY" {
		t.Fatalf("out = %v, want [HOME_LIKE TAVILY_API_KEY]", out)
	}
	// Empty/nil exclude → passthrough unchanged (and input not mutated).
	if got := withoutEnvNames(in, nil); len(got) != 3 {
		t.Errorf("nil exclude should pass all through, got %v", got)
	}
	if in[1] != "API_MEMBER_SERVICE_TOKEN" {
		t.Errorf("input slice was mutated: %v", in)
	}
}

// The EXPLICIT cli_execute path must also strip governed tokens from its
// env_passthrough (mirrors the runner's explicit-cli_execute registration).
func TestExplicitCLIExecuteStripsGovernedToken(t *testing.T) {
	cfg := clitools.ParseCLIExecuteConfig(map[string]any{
		"allowed_binaries": []any{"curl"},
		"env_passthrough":  []any{"API_MEMBER_SERVICE_TOKEN", "MY_SCRIPT_KEY"},
	})
	cfg.EnvPassthrough = withoutEnvNames(cfg.EnvPassthrough, map[string]bool{"API_MEMBER_SERVICE_TOKEN": true})
	if len(cfg.EnvPassthrough) != 1 || cfg.EnvPassthrough[0] != "MY_SCRIPT_KEY" {
		t.Fatalf("env_passthrough = %v, want [MY_SCRIPT_KEY] (governed token stripped, script secret kept)", cfg.EnvPassthrough)
	}
}
