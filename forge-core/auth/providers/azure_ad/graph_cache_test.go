package azure_ad

import (
	"testing"
	"time"
)

func TestGraphCache_HitMiss(t *testing.T) {
	c := NewGraphCache(time.Minute)
	if _, ok := c.Get("user-1"); ok {
		t.Error("empty cache returned hit")
	}
	c.Put("user-1", []string{"g1", "g2"})
	groups, ok := c.Get("user-1")
	if !ok || len(groups) != 2 {
		t.Errorf("Get = (%v, %v)", groups, ok)
	}
}

func TestGraphCache_GetReturnsDefensiveCopy(t *testing.T) {
	// Caller mutating Identity.Groups must NOT corrupt the cache
	// (Review NIT).
	c := NewGraphCache(time.Minute)
	c.Put("u", []string{"g1", "g2", "g3"})

	got, ok := c.Get("u")
	if !ok {
		t.Fatal("expected hit")
	}
	got[0] = "tampered"
	//nolint:staticcheck // intentional caller-side append; the result is
	// discarded because we're testing that mutations of `got` don't
	// reach back into the cache.
	_ = append(got, "extra")

	again, _ := c.Get("u")
	if again[0] != "g1" {
		t.Errorf("cache was mutated by caller: %v", again)
	}
	if len(again) != 3 {
		t.Errorf("cache slice length changed: %d", len(again))
	}
}

func TestGraphCache_PutStoresDefensiveCopy(t *testing.T) {
	// Caller mutating the input slice after Put must NOT bleed
	// through into future Gets.
	c := NewGraphCache(time.Minute)
	src := []string{"g1", "g2"}
	c.Put("u", src)
	src[0] = "tampered"

	got, _ := c.Get("u")
	if got[0] != "g1" {
		t.Errorf("Put didn't copy: %v", got)
	}
}

func TestGraphCache_Expiry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	c := NewGraphCache(60 * time.Second)
	c.setNow(func() time.Time { return now })

	c.Put("user-1", []string{"g1"})

	if _, ok := c.Get("user-1"); !ok {
		t.Fatal("expected hit before expiry")
	}
	now = now.Add(61 * time.Second)
	if _, ok := c.Get("user-1"); ok {
		t.Error("expected miss after expiry")
	}
}
