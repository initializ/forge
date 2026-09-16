package whatsapp

import (
	"strconv"
	"sync"
	"testing"
)

func TestDedup_MarkAndSeen(t *testing.T) {
	d := newDedup(10)
	if d.seen("a") {
		t.Error("expected unseen id")
	}
	d.mark("a")
	if !d.seen("a") {
		t.Error("expected id seen after mark")
	}
}

func TestDedup_MarkIsIdempotent(t *testing.T) {
	d := newDedup(10)
	d.mark("a")
	d.mark("a")
	if d.size() != 1 {
		t.Errorf("expected size 1 after duplicate mark, got %d", d.size())
	}
}

func TestDedup_EvictsOldestWhenFull(t *testing.T) {
	d := newDedup(3)
	d.mark("a")
	d.mark("b")
	d.mark("c")
	d.mark("d") // evicts "a"

	if d.seen("a") {
		t.Error("expected oldest entry evicted")
	}
	for _, id := range []string{"b", "c", "d"} {
		if !d.seen(id) {
			t.Errorf("expected %q retained", id)
		}
	}
	if d.size() != 3 {
		t.Errorf("expected size capped at 3, got %d", d.size())
	}
}

func TestDedup_DefaultCapacity(t *testing.T) {
	for _, c := range []int{0, -1} {
		d := newDedup(c)
		for i := range 1000 {
			d.mark(strconv.Itoa(i))
		}
		if d.size() != 1000 {
			t.Errorf("newDedup(%d): expected default capacity 1000, got size %d", c, d.size())
		}
	}
}

func TestDedup_MarkSeenReportsPriorState(t *testing.T) {
	d := newDedup(10)
	if d.markSeen("a") {
		t.Error("expected first markSeen to report unseen")
	}
	if !d.markSeen("a") {
		t.Error("expected second markSeen to report seen")
	}
	if d.size() != 1 {
		t.Errorf("expected size 1, got %d", d.size())
	}
}

// Two goroutines racing on the same redelivered message must not both treat it
// as new — exactly one may win.
func TestDedup_MarkSeenIsAtomicUnderRace(t *testing.T) {
	d := newDedup(100)
	const goroutines = 50

	var wg sync.WaitGroup
	var mu sync.Mutex
	newCount := 0

	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !d.markSeen("same-id") {
				mu.Lock()
				newCount++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if newCount != 1 {
		t.Errorf("expected exactly one goroutine to observe the id as new, got %d", newCount)
	}
}

func TestDedup_ConcurrentMarkAndSeen(t *testing.T) {
	d := newDedup(500)
	var wg sync.WaitGroup

	for i := range 200 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			d.mark(strconv.Itoa(i))
		}()
		go func() {
			defer wg.Done()
			_ = d.seen(strconv.Itoa(i))
		}()
	}
	wg.Wait()

	if d.size() != 200 {
		t.Errorf("expected 200 distinct ids, got %d", d.size())
	}
}
