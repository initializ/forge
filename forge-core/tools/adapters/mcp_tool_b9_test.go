package adapters

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/mcp"
)

// TestB9_NewMCPTool_RejectsBadDescriptorName pins the review-B9
// guard. The registry's strings.Contains("__") admission check
// accepts ambiguous names like "<server>__" (empty descriptor
// name) or "<server>____foo__bar" (descriptor name containing
// "__"). NewMCPTool now rejects both at construction so the
// adapter never enters the registry.
func TestB9_NewMCPTool_RejectsBadDescriptorName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		descN   string
		wantSub string
	}{
		{"empty", "", "is empty"},
		{"contains __", "foo__bar", `"__"`},
		{"all underscores", "__", `"__"`},
		{"leading __", "__foo", `"__"`},
		{"trailing __", "foo__", `"__"`},
		{"triple underscore", "a___b", `"__"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewMCPTool(MCPToolOpts{
				Server: "srv",
				Descriptor: mcp.MCPToolDescriptor{
					Name:        tc.descN,
					InputSchema: json.RawMessage(`{}`),
				},
			})
			if err == nil {
				t.Fatalf("expected error for descriptor name %q", tc.descN)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("err lacks %q: %v", tc.wantSub, err)
			}
		})
	}
}

// TestB9_NewMCPTool_AcceptsValidDescriptorName sanity check —
// every name that should pass actually does.
func TestB9_NewMCPTool_AcceptsValidDescriptorName(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"echo", "create_issue", "x", "foo_bar_baz", "a1b2c3"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := NewMCPTool(MCPToolOpts{
				Server: "srv",
				Descriptor: mcp.MCPToolDescriptor{
					Name:        name,
					InputSchema: json.RawMessage(`{}`),
				},
			})
			if err != nil {
				t.Errorf("valid name %q rejected: %v", name, err)
			}
		})
	}
}

// TestB9_RegisteredName_HasExactlyOneSeparator covers the property
// the namespacing scheme depends on. For every valid descriptor
// name, the registered name "<server>__<tool>" contains EXACTLY
// one "__" substring — the separator. Log parsers and the registry
// conflict detector can rely on this.
func TestB9_RegisteredName_HasExactlyOneSeparator(t *testing.T) {
	t.Parallel()
	for _, descN := range []string{"echo", "create_issue", "x", "foo_bar"} {
		tool, err := NewMCPTool(MCPToolOpts{
			Server: "linear",
			Descriptor: mcp.MCPToolDescriptor{
				Name:        descN,
				InputSchema: json.RawMessage(`{}`),
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		name := tool.Name()
		count := strings.Count(name, "__")
		if count != 1 {
			t.Errorf("Name() = %q has %d \"__\" separators, want exactly 1", name, count)
		}
	}
}
