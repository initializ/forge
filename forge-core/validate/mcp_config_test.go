package validate

import (
	"strings"
	"testing"
	"time"

	"github.com/initializ/forge/forge-core/types"
)

// TestValidateMCPConfig exercises the closed-set rules. Each case
// pins one rule; substring matches on Errors keep the assertion
// resilient to format tweaks.
func TestValidateMCPConfig(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		cfg       types.MCPConfig
		wantErrs  []string // substring match — at least one error must contain each
		wantNoErr bool
	}{
		{
			name:      "empty config is valid (non-MCP agents unaffected)",
			cfg:       types.MCPConfig{},
			wantNoErr: true,
		},
		{
			name: "stdio is rejected with roadmap hint",
			cfg: types.MCPConfig{Servers: []types.MCPServer{{
				Name: "x", Transport: "stdio", URL: "http://x",
				Tools: types.MCPToolFilter{Allow: []string{"y"}},
			}}},
			wantErrs: []string{"stdio is on the roadmap", "Phase 1 supports HTTP"},
		},
		{
			name: "missing name",
			cfg: types.MCPConfig{Servers: []types.MCPServer{{
				Transport: "http", URL: "http://x",
				Tools: types.MCPToolFilter{Allow: []string{"y"}},
			}}},
			wantErrs: []string{"name is required"},
		},
		{
			name: "name not slug",
			cfg: types.MCPConfig{Servers: []types.MCPServer{{
				Name: "Has-Capitals", Transport: "http", URL: "http://x",
				Tools: types.MCPToolFilter{Allow: []string{"y"}},
			}}},
			wantErrs: []string{"must match"},
		},
		{
			name: "missing transport",
			cfg: types.MCPConfig{Servers: []types.MCPServer{{
				Name: "x", URL: "http://x",
				Tools: types.MCPToolFilter{Allow: []string{"y"}},
			}}},
			wantErrs: []string{"transport is required"},
		},
		{
			name: "unknown transport",
			cfg: types.MCPConfig{Servers: []types.MCPServer{{
				Name: "x", Transport: "grpc", URL: "http://x",
				Tools: types.MCPToolFilter{Allow: []string{"y"}},
			}}},
			wantErrs: []string{"unknown transport"},
		},
		{
			name: "missing url for http",
			cfg: types.MCPConfig{Servers: []types.MCPServer{{
				Name: "x", Transport: "http",
				Tools: types.MCPToolFilter{Allow: []string{"y"}},
			}}},
			wantErrs: []string{"url is required"},
		},
		{
			name: "malformed url",
			cfg: types.MCPConfig{Servers: []types.MCPServer{{
				Name: "x", Transport: "http", URL: "://nope",
				Tools: types.MCPToolFilter{Allow: []string{"y"}},
			}}},
			wantErrs: []string{"malformed"},
		},
		{
			name: "wrong url scheme",
			cfg: types.MCPConfig{Servers: []types.MCPServer{{
				Name: "x", Transport: "http", URL: "ftp://example.com",
				Tools: types.MCPToolFilter{Allow: []string{"y"}},
			}}},
			wantErrs: []string{"http or https"},
		},
		{
			name: "duplicate names",
			cfg: types.MCPConfig{Servers: []types.MCPServer{
				{Name: "dup", Transport: "http", URL: "http://a", Tools: types.MCPToolFilter{Allow: []string{"y"}}},
				{Name: "dup", Transport: "http", URL: "http://b", Tools: types.MCPToolFilter{Allow: []string{"y"}}},
			}},
			wantErrs: []string{"duplicate name"},
		},
		{
			// #316: client_id may be omitted (dynamic client registration),
			// with both endpoints explicit — valid, no error.
			name: "auth oauth without client_id (DCR) is allowed",
			cfg: types.MCPConfig{Servers: []types.MCPServer{{
				Name: "x", Transport: "http", URL: "http://x",
				Auth: &types.MCPAuth{
					Type:         "oauth",
					AuthorizeURL: "https://example.com/authorize",
					TokenURL:     "https://example.com/token",
				},
				Tools: types.MCPToolFilter{Allow: []string{"y"}},
			}}},
			wantNoErr: true,
		},
		{
			// #316: both endpoints omitted ⇒ discover from the server url. Valid.
			name: "auth oauth full discovery (no endpoints, no client_id) is allowed",
			cfg: types.MCPConfig{Servers: []types.MCPServer{{
				Name: "x", Transport: "http", URL: "https://mcp.example.com/mcp",
				Auth:  &types.MCPAuth{Type: "oauth", Scopes: []string{"read"}},
				Tools: types.MCPToolFilter{Allow: []string{"y"}},
			}}},
			wantNoErr: true,
		},
		{
			// #324: agent-principal client_credentials — full config valid.
			name: "auth oauth client_credentials (full) is allowed",
			cfg: types.MCPConfig{Servers: []types.MCPServer{{
				Name: "x", Transport: "http", URL: "https://x",
				Auth: &types.MCPAuth{
					Type: "oauth", Grant: "client_credentials",
					ClientID: "a", ClientSecretEnv: "MCP_SECRET", TokenURL: "https://as/token",
				},
				Tools: types.MCPToolFilter{Allow: []string{"y"}},
			}}},
			wantNoErr: true,
		},
		{
			name: "auth oauth client_credentials missing secret_env is an error",
			cfg: types.MCPConfig{Servers: []types.MCPServer{{
				Name: "x", Transport: "http", URL: "https://x",
				Auth: &types.MCPAuth{
					Type: "oauth", Grant: "client_credentials",
					ClientID: "a", TokenURL: "https://as/token",
				},
				Tools: types.MCPToolFilter{Allow: []string{"y"}},
			}}},
			wantErrs: []string{"client_secret_env is required for grant client_credentials"},
		},
		{
			name: "auth oauth unknown grant is an error",
			cfg: types.MCPConfig{Servers: []types.MCPServer{{
				Name: "x", Transport: "http", URL: "https://x",
				Auth:  &types.MCPAuth{Type: "oauth", Grant: "password"},
				Tools: types.MCPToolFilter{Allow: []string{"y"}},
			}}},
			wantErrs: []string{"must be one of: authorization_code, client_credentials"},
		},
		{
			// Partial endpoint config is still an error — must be paired.
			name: "auth oauth with only token_url (partial) is an error",
			cfg: types.MCPConfig{Servers: []types.MCPServer{{
				Name: "x", Transport: "http", URL: "http://x",
				Auth: &types.MCPAuth{
					Type:     "oauth",
					ClientID: "abc",
					TokenURL: "https://example.com/token",
				},
				Tools: types.MCPToolFilter{Allow: []string{"y"}},
			}}},
			wantErrs: []string{"must be set together"},
		},
		{
			name: "auth oauth with only authorize_url (partial) is an error",
			cfg: types.MCPConfig{Servers: []types.MCPServer{{
				Name: "x", Transport: "http", URL: "http://x",
				Auth: &types.MCPAuth{
					Type:         "oauth",
					ClientID:     "abc",
					AuthorizeURL: "https://example.com/authorize",
				},
				Tools: types.MCPToolFilter{Allow: []string{"y"}},
			}}},
			wantErrs: []string{"must be set together"},
		},
		{
			name: "auth bearer without token_env",
			cfg: types.MCPConfig{Servers: []types.MCPServer{{
				Name: "x", Transport: "http", URL: "http://x",
				Auth:  &types.MCPAuth{Type: "bearer"},
				Tools: types.MCPToolFilter{Allow: []string{"y"}},
			}}},
			wantErrs: []string{"token_env is required"},
		},
		{
			name: "auth static without token_env",
			cfg: types.MCPConfig{Servers: []types.MCPServer{{
				Name: "x", Transport: "http", URL: "http://x",
				Auth:  &types.MCPAuth{Type: "static"},
				Tools: types.MCPToolFilter{Allow: []string{"y"}},
			}}},
			wantErrs: []string{"token_env is required"},
		},
		{
			name: "auth unknown type",
			cfg: types.MCPConfig{Servers: []types.MCPServer{{
				Name: "x", Transport: "http", URL: "http://x",
				Auth:  &types.MCPAuth{Type: "magic"},
				Tools: types.MCPToolFilter{Allow: []string{"y"}},
			}}},
			wantErrs: []string{"must be one of: oauth, bearer, static"},
		},
		{
			name: "auth type missing",
			cfg: types.MCPConfig{Servers: []types.MCPServer{{
				Name: "x", Transport: "http", URL: "http://x",
				Auth:  &types.MCPAuth{},
				Tools: types.MCPToolFilter{Allow: []string{"y"}},
			}}},
			wantErrs: []string{"auth: type is required"},
		},
		{
			name: "tools allow+deny both empty (default-deny)",
			cfg: types.MCPConfig{Servers: []types.MCPServer{{
				Name: "x", Transport: "http", URL: "http://x",
			}}},
			wantErrs: []string{"at least one of allow or deny"},
		},
		{
			name: "tool name with spaces rejected",
			cfg: types.MCPConfig{Servers: []types.MCPServer{{
				Name: "x", Transport: "http", URL: "http://x",
				Tools: types.MCPToolFilter{Allow: []string{"bad name"}},
			}}},
			wantErrs: []string{"invalid tool name"},
		},
		{
			name: "deny tool name with hyphen rejected",
			cfg: types.MCPConfig{Servers: []types.MCPServer{{
				Name: "x", Transport: "http", URL: "http://x",
				Tools: types.MCPToolFilter{Allow: []string{"good"}, Deny: []string{"bad-tool"}},
			}}},
			wantErrs: []string{"invalid tool name"},
		},
		{
			name: "tool in both allow and deny",
			cfg: types.MCPConfig{Servers: []types.MCPServer{{
				Name: "x", Transport: "http", URL: "http://x",
				Tools: types.MCPToolFilter{Allow: []string{"foo"}, Deny: []string{"foo"}},
			}}},
			wantErrs: []string{"appears in both allow and deny"},
		},
		{
			name: "timeout below minimum",
			cfg: types.MCPConfig{Servers: []types.MCPServer{{
				Name: "x", Transport: "http", URL: "http://x",
				Tools:   types.MCPToolFilter{Allow: []string{"y"}},
				Timeout: 500 * time.Millisecond,
			}}},
			wantErrs: []string{"below minimum"},
		},
		{
			name: "valid happy path — oauth",
			cfg: types.MCPConfig{Servers: []types.MCPServer{{
				Name: "linear", Transport: "http",
				URL: "https://mcp.linear.app/sse",
				Auth: &types.MCPAuth{
					Type:         "oauth",
					ClientID:     "abc",
					Scopes:       []string{"read"},
					AuthorizeURL: "https://linear.app/oauth/authorize",
					TokenURL:     "https://api.linear.app/oauth/token",
				},
				Tools: types.MCPToolFilter{Allow: []string{"create_issue", "list_issues"}},
			}}},
			wantNoErr: true,
		},
		{
			name: "valid happy path — bearer + deny-list",
			cfg: types.MCPConfig{Servers: []types.MCPServer{{
				Name: "internal", Transport: "http",
				URL:   "http://internal-mcp.default.svc.cluster.local:8080/mcp",
				Auth:  &types.MCPAuth{Type: "bearer", TokenEnv: "INTERNAL_MCP_TOKEN"},
				Tools: types.MCPToolFilter{Allow: []string{"*"}, Deny: []string{"drop_table"}},
			}}},
			wantNoErr: true,
		},
		{
			name: "wildcard allow is accepted",
			cfg: types.MCPConfig{Servers: []types.MCPServer{{
				Name: "x", Transport: "http", URL: "http://x",
				Tools: types.MCPToolFilter{Allow: []string{"*"}},
			}}},
			wantNoErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &ValidationResult{}
			ValidateMCPConfig(tc.cfg, r)

			if tc.wantNoErr {
				if len(r.Errors) != 0 {
					t.Fatalf("expected no errors, got: %v", r.Errors)
				}
				return
			}

			for _, want := range tc.wantErrs {
				found := false
				for _, got := range r.Errors {
					if strings.Contains(got, want) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected an error containing %q, got: %v", want, r.Errors)
				}
			}
		})
	}
}

// TestValidateMCPConfig_StdioErrorContainsRoadmapHint pins the exact
// substring that Phase 1 docs and acceptance scripts grep for. If this
// test fails, the doc-side checks in Commit 1 acceptance break too.
func TestValidateMCPConfig_StdioErrorContainsRoadmapHint(t *testing.T) {
	t.Parallel()
	cfg := types.MCPConfig{Servers: []types.MCPServer{{
		Name: "x", Transport: "stdio", URL: "http://x",
		Tools: types.MCPToolFilter{Allow: []string{"y"}},
	}}}
	r := &ValidationResult{}
	ValidateMCPConfig(cfg, r)

	joined := strings.Join(r.Errors, " | ")
	for _, want := range []string{"stdio is on the roadmap", "Phase 1 supports HTTP"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing required substring %q in errors: %v", want, r.Errors)
		}
	}
}

// §19 managed identities: platform/user are legal types; a Required
// user-server is rejected (delegated identity is inherently lazy).
func TestValidateMCPConfig_ManagedIdentityTypes(t *testing.T) {
	base := func(auth *types.MCPAuth, required bool) types.MCPConfig {
		return types.MCPConfig{Servers: []types.MCPServer{{
			Name: "x", Transport: "http", URL: "https://mcp.example.com/mcp",
			Auth: auth, Required: required,
			Tools: types.MCPToolFilter{Allow: []string{"y"}},
		}}}
	}

	var r ValidationResult
	ValidateMCPConfig(base(&types.MCPAuth{Type: "platform"}, true), &r)
	if len(r.Errors) != 0 {
		t.Fatalf("platform + required must validate (startup-viable): %v", r.Errors)
	}

	r = ValidationResult{}
	ValidateMCPConfig(base(&types.MCPAuth{Type: "user"}, false), &r)
	if len(r.Errors) != 0 {
		t.Fatalf("lazy user server must validate: %v", r.Errors)
	}

	r = ValidationResult{}
	ValidateMCPConfig(base(&types.MCPAuth{Type: "user"}, true), &r)
	if len(r.Errors) == 0 {
		t.Fatal("user + required:true must be a validation error")
	}
}
