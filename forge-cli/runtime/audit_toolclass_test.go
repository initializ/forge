package runtime

import (
	"testing"

	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

// TestStampToolClass pins the #484 audit stamping: tool_class / tool_kind are
// added when the engine classified the tool, and OMITTED when empty so existing
// tool_exec consumers are unaffected.
func TestStampToolClass(t *testing.T) {
	// Adapter + kind → both stamped.
	f := map[string]any{}
	stampToolClass(f, &coreruntime.HookContext{ToolClass: "adapter", ToolKind: "mcp"})
	if f["tool_class"] != "adapter" || f["tool_kind"] != "mcp" {
		t.Errorf("adapter/mcp: got %v", f)
	}

	// Builtin → class only, kind omitted.
	f = map[string]any{}
	stampToolClass(f, &coreruntime.HookContext{ToolClass: "builtin"})
	if f["tool_class"] != "builtin" {
		t.Errorf("builtin class not stamped: %v", f)
	}
	if _, ok := f["tool_kind"]; ok {
		t.Error("tool_kind must be omitted when empty")
	}

	// Unclassified → both omitted.
	f = map[string]any{}
	stampToolClass(f, &coreruntime.HookContext{})
	if _, ok := f["tool_class"]; ok {
		t.Error("tool_class must be omitted when empty")
	}
	if _, ok := f["tool_kind"]; ok {
		t.Error("tool_kind must be omitted when empty")
	}
}
