package runtime

import (
	"context"
	"sync"
	"testing"
)

func TestSequenceRegistry_RegisterGetEvict(t *testing.T) {
	reg := NewSequenceRegistry()
	c := new(SequenceCounter)
	reg.Register("corr-1", "task-1", c)

	if got := reg.Get("corr-1", "task-1"); got != c {
		t.Fatalf("Get returned %v, want the registered counter", got)
	}
	if got := reg.Get("corr-1", "other"); got != nil {
		t.Errorf("Get for an unregistered key = %v, want nil", got)
	}

	reg.Evict("corr-1", "task-1")
	if got := reg.Get("corr-1", "task-1"); got != nil {
		t.Errorf("Get after Evict = %v, want nil", got)
	}
}

// TestSequenceRegistry_NextSequenceForSharesCounter is the crux: the registry
// advances the SAME counter the in-context path (NextSequence) uses, so an
// out-of-band emitter and the request goroutine produce one gap-free sequence.
func TestSequenceRegistry_NextSequenceForSharesCounter(t *testing.T) {
	reg := NewSequenceRegistry()
	c := new(SequenceCounter)
	ctx := WithSequenceCounter(context.Background(), c)
	reg.Register("corr", "task", c)

	if n := NextSequence(ctx); n != 1 { // in-context (e.g. an in-process tool)
		t.Fatalf("NextSequence = %d, want 1", n)
	}
	if n := reg.NextSequenceFor("corr", "task"); n != 2 { // out-of-band (proxy)
		t.Fatalf("NextSequenceFor = %d, want 2 (shared counter)", n)
	}
	if n := NextSequence(ctx); n != 3 {
		t.Fatalf("NextSequence = %d, want 3", n)
	}
}

func TestSequenceRegistry_MissReturnsZero(t *testing.T) {
	reg := NewSequenceRegistry()
	if n := reg.NextSequenceFor("nope", "nope"); n != 0 {
		t.Errorf("NextSequenceFor on a miss = %d, want 0 (seq-less, not mis-seq'd)", n)
	}
}

func TestSequenceRegistry_NilSafe(t *testing.T) {
	var reg *SequenceRegistry // nil registry must be a no-op, not a panic
	reg.Register("c", "t", new(SequenceCounter))
	if reg.Get("c", "t") != nil || reg.NextSequenceFor("c", "t") != 0 {
		t.Error("nil registry should be inert")
	}
	reg.Evict("c", "t")
}

func TestSequenceRegistry_Concurrent(t *testing.T) {
	reg := NewSequenceRegistry()
	c := new(SequenceCounter)
	reg.Register("c", "t", c)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); reg.NextSequenceFor("c", "t") }()
	}
	wg.Wait()
	if got := c.Load(); got != 100 {
		t.Errorf("counter = %d after 100 concurrent advances, want 100", got)
	}
}
