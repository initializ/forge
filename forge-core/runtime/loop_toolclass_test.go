package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/initializ/forge/forge-core/llm"
	"github.com/initializ/forge/forge-core/tools"
)

// classStubTool is a Tool with a settable Category (and optional MCP marker via
// the embedding below) for classifyTool tests.
type classStubTool struct {
	name string
	cat  tools.Category
}

func (s classStubTool) Name() string                                             { return s.name }
func (s classStubTool) Description() string                                      { return "" }
func (s classStubTool) Category() tools.Category                                 { return s.cat }
func (s classStubTool) InputSchema() json.RawMessage                             { return json.RawMessage(`{}`) }
func (s classStubTool) Execute(context.Context, json.RawMessage) (string, error) { return "", nil }

type classStubMCPTool struct{ classStubTool }

func (classStubMCPTool) MCPSource() {}

// TestClassifyTool pins #484: the engine resolves tool_class from Category and a
// finer tool_kind for adapters (mcp vs api). Unknown tools classify to empty
// (so the audit stamping is simply omitted).
func TestClassifyTool(t *testing.T) {
	reg := tools.NewRegistry()
	if err := reg.Register(classStubTool{name: "math_calculate", cat: tools.CategoryBuiltin}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(classStubTool{name: "memberservice_reverse", cat: tools.CategoryAdapter}); err != nil {
		t.Fatal(err) // adapter, non-MCP → api
	}
	if err := reg.Register(classStubMCPTool{classStubTool{name: "jira_search", cat: tools.CategoryAdapter}}); err != nil {
		t.Fatal(err) // adapter + MCPSource → mcp
	}

	e := &LLMExecutor{tools: reg}

	cases := []struct {
		name, wantClass, wantKind string
	}{
		{"math_calculate", "builtin", ""},
		{"memberservice_reverse", "adapter", "api"},
		{"jira_search", "adapter", "mcp"},
		{"nonexistent", "", ""},
	}
	for _, c := range cases {
		gotClass, gotKind := e.classifyTool(c.name)
		if gotClass != c.wantClass || gotKind != c.wantKind {
			t.Errorf("classifyTool(%q) = (%q, %q), want (%q, %q)", c.name, gotClass, gotKind, c.wantClass, c.wantKind)
		}
	}
}

// A classifier that can't expose ClassOf (a bare stub executor) must not panic
// and yields an empty class — the audit stamping then omits tool_class.
func TestClassifyTool_NoClassOf(t *testing.T) {
	e := &LLMExecutor{tools: noClassExecutor{}}
	if class, kind := e.classifyTool("anything"); class != "" || kind != "" {
		t.Errorf("classifyTool without ClassOf = (%q, %q), want empty", class, kind)
	}
}

// noClassExecutor satisfies ToolExecutor but has no ClassOf method.
type noClassExecutor struct{}

func (noClassExecutor) Execute(context.Context, string, json.RawMessage) (string, error) {
	return "", nil
}
func (noClassExecutor) ToolDefinitions() []llm.ToolDefinition { return nil }
func (noClassExecutor) IsMCPTool(string) bool                 { return false }
