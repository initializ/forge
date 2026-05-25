package validate

import (
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/types"
)

// TestB9_Validate_RejectsDoubleUnderscoreInToolNames — validate
// now refuses operator-supplied tool names containing "__"
// (review B9). An entry like `tools: { allow: ["foo__bar"] }`
// would silently turn into "<server>__foo__bar" at runtime —
// ambiguous to log parsers, conflict-prone with other tools.
func TestB9_Validate_RejectsDoubleUnderscoreInToolNames(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cfg  types.MCPConfig
		want string
	}{
		{
			name: "allow contains __",
			cfg: types.MCPConfig{Servers: []types.MCPServer{{
				Name: "x", Transport: "http", URL: "http://x",
				Tools: types.MCPToolFilter{Allow: []string{"foo__bar"}},
			}}},
			want: "allow",
		},
		{
			name: "deny contains __",
			cfg: types.MCPConfig{Servers: []types.MCPServer{{
				Name: "x", Transport: "http", URL: "http://x",
				Tools: types.MCPToolFilter{Allow: []string{"ok"}, Deny: []string{"bad__name"}},
			}}},
			want: "deny",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &ValidationResult{}
			ValidateMCPConfig(tc.cfg, r)
			if len(r.Errors) == 0 {
				t.Fatalf("expected an error for %s", tc.name)
			}
			joined := strings.Join(r.Errors, " | ")
			if !strings.Contains(joined, `"__"`) {
				t.Errorf("err lacks \"__\" hint: %v", r.Errors)
			}
			if !strings.Contains(joined, tc.want) {
				t.Errorf("err lacks %q hint: %v", tc.want, r.Errors)
			}
		})
	}
}

// TestB9_Validate_AllowsSingleUnderscore confirms the new check
// is precise: a single underscore in a tool name is still legal.
func TestB9_Validate_AllowsSingleUnderscore(t *testing.T) {
	t.Parallel()
	cfg := types.MCPConfig{Servers: []types.MCPServer{{
		Name: "x", Transport: "http", URL: "http://x",
		Tools: types.MCPToolFilter{Allow: []string{"create_issue", "list_pulls"}},
	}}}
	r := &ValidationResult{}
	ValidateMCPConfig(cfg, r)
	if len(r.Errors) != 0 {
		t.Errorf("expected no errors, got: %v", r.Errors)
	}
}
