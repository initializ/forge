package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// fakeTool is a non-MCP Tool used to test the "__ reserved" rule.
type fakeTool struct {
	name string
}

func (f *fakeTool) Name() string                                             { return f.name }
func (f *fakeTool) Description() string                                      { return "fake" }
func (f *fakeTool) Category() Category                                       { return CategoryBuiltin }
func (f *fakeTool) InputSchema() json.RawMessage                             { return json.RawMessage(`{}`) }
func (f *fakeTool) Execute(context.Context, json.RawMessage) (string, error) { return "", nil }

// fakeMCPTool is an MCP-source tool used to verify the rule's
// exception for tools that implement MCPSource.
type fakeMCPTool struct {
	fakeTool
}

func (f *fakeMCPTool) MCPSource() {}

func TestRegistry_RejectsDoubleUnderscoreFromNonMCP(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	err := r.Register(&fakeTool{name: "github__create_issue"})
	if err == nil {
		t.Fatalf("expected error for non-MCP tool with '__'")
	}
	if !strings.Contains(err.Error(), "reserved for namespacing") {
		t.Errorf("error message lacks hint: %v", err)
	}
}

func TestRegistry_AcceptsDoubleUnderscoreFromMCP(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	tool := &fakeMCPTool{fakeTool: fakeTool{name: "github__create_issue"}}
	if err := r.Register(tool); err != nil {
		t.Fatalf("MCP-sourced tool should be allowed: %v", err)
	}
}

func TestRegistry_AllowsSingleUnderscore(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	if err := r.Register(&fakeTool{name: "json_parse"}); err != nil {
		t.Fatalf("single underscore should be fine: %v", err)
	}
}

// fakeCatTool is a Tool with a settable Category, for ClassOf.
type fakeCatTool struct {
	fakeTool
	cat Category
}

func (f *fakeCatTool) Category() Category { return f.cat }

// TestRegistryClassOf pins the #484 lookup used to stamp tool_class on
// tool_exec: ClassOf returns the registered tool's Category as a string, and ""
// for an unknown tool.
func TestRegistryClassOf(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	_ = r.Register(&fakeTool{name: "json_parse"}) // builtin (fakeTool default)
	_ = r.Register(&fakeCatTool{fakeTool{name: "my_api"}, CategoryAdapter})
	_ = r.Register(&fakeCatTool{fakeTool{name: "code_agent"}, CategoryDev})
	_ = r.Register(&fakeCatTool{fakeTool{name: "custom_thing"}, CategoryCustom})

	for name, want := range map[string]string{
		"json_parse":   "builtin",
		"my_api":       "adapter",
		"code_agent":   "dev",
		"custom_thing": "custom",
		"nonexistent":  "",
	} {
		if got := r.ClassOf(name); got != want {
			t.Errorf("ClassOf(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestRegistry_DuplicateRejected(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	_ = r.Register(&fakeTool{name: "dup"})
	err := r.Register(&fakeTool{name: "dup"})
	if err == nil {
		t.Fatalf("duplicate registration should error")
	}
}
