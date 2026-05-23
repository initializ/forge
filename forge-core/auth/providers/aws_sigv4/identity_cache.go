package aws_sigv4

import (
	"sync"
	"time"

	"github.com/initializ/forge/forge-core/auth"
)

// IdentityCache holds verified Identities keyed by hash(AKID|YYYYMMDD).
// Bounds the stolen-key window: a leaked AKID is honored until either
// IdentityCacheTTL elapses OR midnight UTC (date bucket rolls over),
// whichever is sooner.
//
// Never caches rejections or errors — Phase 1 invariant: errors are
// not entities.
type IdentityCache struct {
	ttl  time.Duration
	mu   sync.RWMutex
	data map[string]cacheEntry
	now  func() time.Time
}

type cacheEntry struct {
	id       *auth.Identity
	expireAt time.Time
}

// NewIdentityCache returns an empty cache with the given TTL. time.Now is
// the default clock; tests can swap it via the exported NowFunc field.
func NewIdentityCache(ttl time.Duration) *IdentityCache {
	return &IdentityCache{
		ttl:  ttl,
		data: make(map[string]cacheEntry),
		now:  time.Now,
	}
}

// Get returns the cached identity if present and not expired.
func (c *IdentityCache) Get(key string) (*auth.Identity, bool) {
	c.mu.RLock()
	e, ok := c.data[key]
	c.mu.RUnlock()
	if !ok || c.now().After(e.expireAt) {
		return nil, false
	}
	return e.id, true
}

// Put stores the identity under key with a fresh TTL. Overwriting an
// existing entry does NOT extend the previous expiry — it replaces it.
// (Refusing to extend prevents a "refresh-just-before-expiry" attack
// from holding a stolen credential alive indefinitely.)
//
// Opportunistic eviction sweeps expired entries when the map grows past
// 10k. Bounds memory under sustained miss without needing a background
// goroutine.
func (c *IdentityCache) Put(key string, id *auth.Identity) {
	c.mu.Lock()
	c.data[key] = cacheEntry{id: id, expireAt: c.now().Add(c.ttl)}
	if len(c.data) > 10_000 {
		now := c.now()
		for k, e := range c.data {
			if now.After(e.expireAt) {
				delete(c.data, k)
			}
		}
	}
	c.mu.Unlock()
}

// setNow is a test-only hook for swapping the clock.
func (c *IdentityCache) setNow(fn func() time.Time) { c.now = fn }
