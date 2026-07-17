package mcp

import (
	"testing"
	"time"
)

func TestMemSubjectTokenStore_GetPutEvict(t *testing.T) {
	s := newMemSubjectTokenStore(0)
	if _, ok := s.Get("alice@corp.com"); ok {
		t.Fatal("empty store must miss")
	}
	s.Put("alice@corp.com", "tok-a", time.Hour)
	s.Put("bob@corp.com", "tok-b", time.Hour)

	if tok, ok := s.Get("alice@corp.com"); !ok || tok != "tok-a" {
		t.Fatalf("alice = (%q, %v), want tok-a/true", tok, ok)
	}
	// Distinct users never share a token.
	if tok, _ := s.Get("bob@corp.com"); tok != "tok-b" {
		t.Fatalf("bob = %q, want tok-b (per-subject isolation)", tok)
	}

	s.Evict("alice@corp.com")
	if _, ok := s.Get("alice@corp.com"); ok {
		t.Fatal("evicted token must be gone")
	}
	if _, ok := s.Get("bob@corp.com"); !ok {
		t.Fatal("evicting alice must not touch bob")
	}
}

// A token within the skew window of expiry is treated as already stale so it
// never expires mid-flight downstream — and is evicted on that read.
func TestMemSubjectTokenStore_SkewAndEvictOnStale(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	s := newMemSubjectTokenStore(30 * time.Second)
	s.now = func() time.Time { return now }

	s.Put("alice@corp.com", "tok-a", 20*time.Second) // expires in 20s, < 30s skew
	if _, ok := s.Get("alice@corp.com"); ok {
		t.Fatal("a token inside the skew window must be treated as stale")
	}
	// The stale read must have evicted it (no sensitive token held past use).
	s.mu.Lock()
	_, present := s.m["alice@corp.com"]
	s.mu.Unlock()
	if present {
		t.Fatal("stale token must be evicted on read")
	}
}

func TestMemSubjectTokenStore_ExpiredMiss(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	s := newMemSubjectTokenStore(0)
	s.now = func() time.Time { return now }
	s.Put("alice@corp.com", "tok-a", time.Minute)
	s.now = func() time.Time { return now.Add(2 * time.Minute) }
	if _, ok := s.Get("alice@corp.com"); ok {
		t.Fatal("expired token must miss")
	}
}

// Put opportunistically sweeps expired entries so one-shot users don't leak.
func TestMemSubjectTokenStore_PutSweepsExpired(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	s := newMemSubjectTokenStore(0)
	s.now = func() time.Time { return now }
	s.Put("alice@corp.com", "tok-a", time.Minute)
	s.Put("bob@corp.com", "tok-b", time.Minute)

	// Advance past expiry, then Put a third — the sweep drops alice & bob.
	s.now = func() time.Time { return now.Add(2 * time.Minute) }
	s.Put("carol@corp.com", "tok-c", time.Minute)

	s.mu.Lock()
	n := len(s.m)
	s.mu.Unlock()
	if n != 1 {
		t.Fatalf("after sweep %d entries remain, want 1 (only carol)", n)
	}
}

// The default store satisfies the interface (compile-time guard).
var _ SubjectTokenStore = (*memSubjectTokenStore)(nil)
