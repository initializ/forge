package owaspasi

import "testing"

// ASI06 — Memory & Context Poisoning. Grade: Partial.
//
// Enforced property (covered by forge-core/memory unit tests, not duplicated
// here): MEMORY.md is canonical, the vector index is derived/rebuildable and
// never enters LLM context directly (retrieval returns markdown Chunk.Content).
// See forge-core/memory/*_test.go and the conformance matrix (ASI06).
//
// The gaps below are the buildable backlog. These xfail until issue #225 lands.

// TestASI06_SelfAuthoredMemoryNotReingested is the failing target for issue
// #225: the compaction path (memory_compactor.go AppendDailyLog) writes the
// agent's own observations, which are then indexed and retrievable — a
// bootstrap-poisoning loop with no self-reingestion guard.
func TestASI06_SelfAuthoredMemoryNotReingested(t *testing.T) {
	t.Skip("xfail: GAP-MEM / issue #225 — no self-reingestion guard (ASI06 #6); " +
		"agent-authored memory is currently indexed and retrievable.")
}

// TestASI06_MemoryWriteScanned is the failing target for issue #225: memory
// writes are not scanned/validated for provenance before commit (ASI06 #2/#5).
func TestASI06_MemoryWriteScanned(t *testing.T) {
	t.Skip("xfail: GAP-MEM / issue #225 — memory writes are not scanned or " +
		"provenance-attributed before commit (ASI06 #2/#5).")
}
