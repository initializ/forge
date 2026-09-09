package whatsapp

import "sync"

// dedup is a sliding-window deduplicator for WhatsApp message IDs.
//
// Two inbound paths can deliver the same message twice: a history sync after
// reconnect replays recent messages, and an unacknowledged delivery is retried
// by the server. The ring filters both so the agent isn't invoked twice for
// one prompt.
//
// It also carries the outbound echo guard. Every message this adapter sends is
// marked before the send returns, so if it comes back through history sync it
// is dropped before admission. That is belt-and-braces alongside the IsFromMe
// check in admit() — IsFromMe covers the live path, the ring covers replay,
// where a message sent by the human operator on their own phone and one sent
// by the agent are otherwise indistinguishable.
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

// markSeen records id and reports whether it had already been marked. It is
// the single-call form of seen-then-mark, so two goroutines racing on the same
// redelivered message cannot both observe it as new.
func (d *dedup) markSeen(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.set[id] {
		return true
	}

	if len(d.order) < d.cap {
		d.order = append(d.order, id)
	} else {
		delete(d.set, d.order[d.head])
		d.order[d.head] = id
		d.head = (d.head + 1) % d.cap
	}
	d.set[id] = true
	return false
}

// size returns the current number of tracked IDs (for tests).
func (d *dedup) size() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.order)
}
