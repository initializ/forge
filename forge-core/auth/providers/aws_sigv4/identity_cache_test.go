package aws_sigv4

import (
	"testing"
	"time"

	"github.com/initializ/forge/forge-core/auth"
)

func TestIdentityCache_HitMiss(t *testing.T) {
	c := NewIdentityCache(time.Minute)
	if _, ok := c.Get("k1"); ok {
		t.Error("empty cache returned hit")
	}
	id := &auth.Identity{UserID: "arn1"}
	c.Put("k1", id)
	got, ok := c.Get("k1")
	if !ok || got != id {
		t.Errorf("Get hit = (%v, %v), want id, true", got, ok)
	}
}

func TestIdentityCache_Expiry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	c := NewIdentityCache(60 * time.Second)
	c.setNow(func() time.Time { return now })

	c.Put("k", &auth.Identity{UserID: "arn"})

	if _, ok := c.Get("k"); !ok {
		t.Fatal("expected hit before expiry")
	}

	now = now.Add(61 * time.Second)
	if _, ok := c.Get("k"); ok {
		t.Error("expected miss after TTL expiry")
	}
}

func TestIdentityCache_PutDoesNotExtendExpiry(t *testing.T) {
	// Defense against refresh-just-before-expiry holding a stolen
	// credential alive indefinitely. Put MUST replace the entry's
	// expireAt, not extend it.
	now := time.Unix(1_700_000_000, 0)
	c := NewIdentityCache(60 * time.Second)
	c.setNow(func() time.Time { return now })

	c.Put("k", &auth.Identity{UserID: "arn"})
	// Refresh at t+50s — expireAt becomes t+110s.
	now = now.Add(50 * time.Second)
	c.Put("k", &auth.Identity{UserID: "arn"})

	// At t+120s, original-expiry+TTL would still be valid; replacement
	// makes the cap t+110s. Verify it's expired.
	now = now.Add(70 * time.Second) // t+120s
	if _, ok := c.Get("k"); ok {
		t.Error("Put extended TTL beyond the bound — refresh-extends-stolen-key bug")
	}
}

func TestIdentityCache_OpportunisticEviction(t *testing.T) {
	// Force the map past the 10_000 threshold with expired entries; one
	// more Put should trigger a sweep that drops them.
	now := time.Unix(1_700_000_000, 0)
	c := NewIdentityCache(time.Second)
	c.setNow(func() time.Time { return now })

	id := &auth.Identity{UserID: "arn"}
	for i := range 10_001 {
		c.Put(string(rune(i)), id)
	}
	now = now.Add(2 * time.Second) // expire everything

	c.Put("fresh", id) // triggers sweep

	if got, ok := c.Get("fresh"); !ok || got != id {
		t.Fatal("fresh entry lost during sweep")
	}
	// Map should have been pruned — at most a few entries remain
	c.mu.RLock()
	size := len(c.data)
	c.mu.RUnlock()
	if size > 10 {
		t.Errorf("eviction did not run; cache size = %d", size)
	}
}
