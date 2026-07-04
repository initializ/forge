package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/initializ/forge/forge-core/compress"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

// PR #241 review: invocation_complete is emitted from THREE sites
// (executeTask + both sendSubscribe streaming handlers), and every one must
// pop the per-correlation compression bucket via appendCompressionFields —
// a missed site leaks the bucket and drops the metrics. This pins the
// helper's contract: fields populated, bucket popped exactly once, nil-safe.
func TestAppendCompressionFields_PopsAndPopulates(t *testing.T) {
	comp, err := compress.New(compress.Config{
		StorePath: filepath.Join(t.TempDir(), "ctxzip.db"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = comp.Close() })
	r := &Runner{compression: comp}

	// Simulate a streaming invocation that compressed one big tool output.
	ctx := coreruntime.WithCorrelationID(context.Background(), "sse-task-1")
	items := make([]map[string]any, 80)
	for i := range items {
		items[i] = map[string]any{"id": fmt.Sprintf("r-%03d", i), "state": "nominal", "zone": "us-east-1"}
	}
	blob, _ := json.Marshal(items)
	hctx := &coreruntime.HookContext{ToolName: "list", ToolOutput: string(blob)}
	if err := comp.AfterToolExecHook()(ctx, hctx); err != nil {
		t.Fatal(err)
	}
	if hctx.ToolOutput == string(blob) {
		t.Fatal("fixture did not compress — test cannot exercise the bucket")
	}

	// First call: fields populated, bucket popped.
	fields := map[string]any{}
	r.appendCompressionFields(ctx, fields)
	if fields["compression_count"].(int64) != 1 {
		t.Fatalf("compression_count = %v, want 1", fields["compression_count"])
	}
	if fields["compression_saved_tokens_total"].(int64) <= 0 {
		t.Fatalf("compression_saved_tokens_total = %v, want > 0", fields["compression_saved_tokens_total"])
	}

	// Second call for the same invocation: bucket already popped → zeros
	// (proves no double-count if two sites ever fire for one invocation).
	fields2 := map[string]any{}
	r.appendCompressionFields(ctx, fields2)
	if fields2["compression_count"].(int64) != 0 {
		t.Fatalf("second pop should be empty, got %v", fields2["compression_count"])
	}

	// Nil compression runtime: no fields, no panic.
	fields3 := map[string]any{}
	(&Runner{}).appendCompressionFields(ctx, fields3)
	if len(fields3) != 0 {
		t.Fatalf("nil runtime should add no fields: %v", fields3)
	}
}
