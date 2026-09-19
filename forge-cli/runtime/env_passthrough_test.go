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

// governedEnvCollisions surfaces the #480 misconfiguration: a skill declaring a
// governed API/MCP token in requires.env (which is then withheld from scripts,
// yielding a confusing "missing <TOKEN>" downstream). It returns the sorted,
// de-duplicated intersection so the diagnostic is stable.
func TestGovernedEnvCollisions(t *testing.T) {
	governed := map[string]bool{"API_SPORTRADAR_NFL_TOKEN": true, "MCP_JIRA_TOKEN": true}

	// A skill that declares the governed token (the field repro) → flagged.
	declared := []string{"WEATHER_API_KEY", "API_SPORTRADAR_NFL_TOKEN"}
	got := governedEnvCollisions(declared, governed)
	if len(got) != 1 || got[0] != "API_SPORTRADAR_NFL_TOKEN" {
		t.Fatalf("collisions = %v, want [API_SPORTRADAR_NFL_TOKEN]", got)
	}

	// Multiple + duplicate declarations → sorted, de-duplicated.
	multi := governedEnvCollisions([]string{"MCP_JIRA_TOKEN", "API_SPORTRADAR_NFL_TOKEN", "MCP_JIRA_TOKEN"}, governed)
	if len(multi) != 2 || multi[0] != "API_SPORTRADAR_NFL_TOKEN" || multi[1] != "MCP_JIRA_TOKEN" {
		t.Fatalf("collisions = %v, want [API_SPORTRADAR_NFL_TOKEN MCP_JIRA_TOKEN]", multi)
	}

	// A well-formed skill (only its own script secret) → no collision.
	if got := governedEnvCollisions([]string{"WEATHER_API_KEY"}, governed); got != nil {
		t.Errorf("expected no collision, got %v", got)
	}
	// No governed tokens configured → nothing to flag.
	if got := governedEnvCollisions([]string{"API_SPORTRADAR_NFL_TOKEN"}, nil); got != nil {
		t.Errorf("empty governed set should yield nil, got %v", got)
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
