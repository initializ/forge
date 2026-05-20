package msteams

import "sync"

// dedup is a sliding-window deduplicator for inbound Graph message IDs.
// Graph delta responses can return the same message twice across paginated
// or interrupted polls; the dedup ring filters them out so the agent doesn't
// receive duplicates.
//
// Capacity defaults to 1000 entries. Evicts the oldest entry when full.
// All operations are safe for concurrent use.
type dedup struct {
	mu    sync.Mutex
	cap   int
	order []string        // insertion order — order[head] is the oldest
	head  int             // index of the next slot to overwrite
	set   map[string]bool // membership lookup
}

func newDedup(capacity int) *dedup {
	if capacity <= 0 {
		capacity = 1000
	}
	return &dedup{
		cap:   capacity,
		order: make([]string, 0, capacity),
		set:   make(map[string]bool, capacity),
	}
}

// seen reports whether id was previously marked.
func (d *dedup) seen(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.set[id]
}

// mark records id and evicts the oldest entry if the ring is full.
func (d *dedup) mark(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.set[id] {
		return
	}

	if len(d.order) < d.cap {
		d.order = append(d.order, id)
	} else {
		// Evict the entry at head, then overwrite.
		delete(d.set, d.order[d.head])
		d.order[d.head] = id
		d.head = (d.head + 1) % d.cap
	}
	d.set[id] = true
}

// size returns the current number of tracked IDs (for tests).
func (d *dedup) size() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.order)
}
