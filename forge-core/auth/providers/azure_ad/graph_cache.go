package azure_ad

import (
	"sync"
	"time"
)

// GraphCache holds enriched group memberships keyed by user ID, with a
// short TTL. Bounds how long a stale "removed from group" state stays
// cached after AAD's reality changes.
type GraphCache struct {
	ttl  time.Duration
	mu   sync.RWMutex
	data map[string]graphEntry
	now  func() time.Time
}

type graphEntry struct {
	groups   []string
	expireAt time.Time
}

// NewGraphCache builds an empty cache.
func NewGraphCache(ttl time.Duration) *GraphCache {
	return &GraphCache{
		ttl:  ttl,
		data: make(map[string]graphEntry),
		now:  time.Now,
	}
}

// Get returns the cached groups for userID, or (nil, false) on miss/expiry.
//
// The returned slice is a defensive copy — callers that subsequently mutate
// their Identity.Groups (the auth.Identity layer treats Groups as a freely-
// mutable field) MUST NOT corrupt the cache. (Review NIT.)
func (c *GraphCache) Get(userID string) ([]string, bool) {
	c.mu.RLock()
	e, ok := c.data[userID]
	c.mu.RUnlock()
	if !ok || c.now().After(e.expireAt) {
		return nil, false
	}
	return append([]string(nil), e.groups...), true
}

// Put stores the groups under userID with a fresh TTL. Overwrites any
// prior entry (does not extend).
//
// Stores a defensive copy so subsequent caller mutations of the input
// slice don't reach back through cache hits.
func (c *GraphCache) Put(userID string, groups []string) {
	c.mu.Lock()
	c.data[userID] = graphEntry{
		groups:   append([]string(nil), groups...),
		expireAt: c.now().Add(c.ttl),
	}
	c.mu.Unlock()
}

func (c *GraphCache) setNow(fn func() time.Time) { c.now = fn }
