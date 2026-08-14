package tools

import "testing"

// fakeNamespacedTool implements the NamespacedSource marker (for per-op API
// tools) — the non-MCP opt-in to the "__" separator.
type fakeNamespacedTool struct {
	fakeTool
}

func (f *fakeNamespacedTool) NamespacedSource() {}

func TestRegistry_AcceptsDoubleUnderscoreFromNamespacedSource(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	if err := r.Register(&fakeNamespacedTool{fakeTool{name: "memberservice__reverse_fee"}}); err != nil {
		t.Fatalf("NamespacedSource tool with '__' should register: %v", err)
	}
	// A plain tool with '__' is still rejected.
	if err := r.Register(&fakeTool{name: "bad__name"}); err == nil {
		t.Error("plain tool with '__' should be rejected")
	}
}
