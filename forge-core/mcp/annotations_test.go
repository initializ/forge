package mcp

import (
	"encoding/json"
	"testing"
)

// tools/list responses may carry the MCP annotations object; decode must
// surface it (platform discovery seeds side-effect classes from these hints)
// and its absence must stay distinguishable from explicit false.
func TestMCPToolDescriptor_AnnotationsDecode(t *testing.T) {
	raw := []byte(`[
	  {"name":"list_issues","inputSchema":{},"annotations":{"readOnlyHint":true}},
	  {"name":"delete_issue","inputSchema":{},"annotations":{"destructiveHint":true,"readOnlyHint":false}},
	  {"name":"create_issue","inputSchema":{}}
	]`)
	var descs []MCPToolDescriptor
	if err := json.Unmarshal(raw, &descs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if descs[0].Annotations == nil || descs[0].Annotations.ReadOnlyHint == nil || !*descs[0].Annotations.ReadOnlyHint {
		t.Fatalf("readOnlyHint lost: %+v", descs[0].Annotations)
	}
	if descs[1].Annotations == nil || descs[1].Annotations.DestructiveHint == nil || !*descs[1].Annotations.DestructiveHint {
		t.Fatalf("destructiveHint lost: %+v", descs[1].Annotations)
	}
	if descs[2].Annotations != nil {
		t.Fatalf("absent annotations must stay nil, got %+v", descs[2].Annotations)
	}
}
