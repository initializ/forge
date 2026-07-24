package runtime

import (
	"context"
	"testing"

	coreruntime "github.com/initializ/forge/forge-core/runtime"
	"github.com/initializ/forge/forge-core/types"
)

// TestRegisterInvocationSeq_SharesCounter is the #341/#366 wiring: after
// registerInvocationSeq, an out-of-band emitter (the egress proxy / consent
// resume) that looks up the counter by (correlation_id, task_id) advances the
// SAME counter the in-context request goroutine uses — one gap-free sequence —
// and eviction drops the registration.
func TestRegisterInvocationSeq_SharesCounter(t *testing.T) {
	r, err := NewRunner(RunnerConfig{Config: &types.ForgeConfig{AgentID: "agent", Version: "0.1.0", Framework: "forge"}})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	ctx := context.Background()
	ctx = coreruntime.WithCorrelationID(ctx, "corr-1")
	ctx = coreruntime.WithTaskID(ctx, "task-1")
	ctx = coreruntime.EnsureSequenceCounter(ctx)

	if n := coreruntime.NextSequence(ctx); n != 1 { // in-context (e.g. session_start)
		t.Fatalf("in-context NextSequence = %d, want 1", n)
	}

	evict := r.registerInvocationSeq(ctx)
	if n := r.seqRegistry.NextSequenceFor("corr-1", "task-1"); n != 2 { // out-of-band (proxy)
		t.Fatalf("out-of-band NextSequenceFor = %d, want 2 (shared counter)", n)
	}
	if n := coreruntime.NextSequence(ctx); n != 3 { // back in-context, no gap
		t.Fatalf("in-context NextSequence = %d, want 3", n)
	}

	evict()
	if n := r.seqRegistry.NextSequenceFor("corr-1", "task-1"); n != 0 {
		t.Errorf("after evict NextSequenceFor = %d, want 0 (unregistered)", n)
	}
}
