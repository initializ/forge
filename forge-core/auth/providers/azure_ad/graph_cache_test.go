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
