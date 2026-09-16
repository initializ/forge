package optimizer

import (
	"math"
	"testing"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestPricing_DefaultsAndDerivedCacheRates(t *testing.T) {
	p := NewPricing(nil)

	mp := p.For("claude-opus-4-8")
	if mp.InputPerMTok != 5 || mp.OutputPerMTok != 25 {
		t.Fatalf("opus base rates wrong: %+v", mp)
	}
	// Cache rates derive from input: read 0.1×, write 1.25×.
	if !approx(mp.CacheReadPerMTok, 0.5) || !approx(mp.CacheWritePerMTok, 6.25) {
		t.Errorf("derived cache rates wrong: read=%v write=%v", mp.CacheReadPerMTok, mp.CacheWritePerMTok)
	}
}

func TestPricing_LongestPrefixAndUnknownFallback(t *testing.T) {
	p := NewPricing(nil)
	// The [1m] long-context suffix must resolve to the base opus entry.
	if got := p.For("claude-opus-4-8[1m]").InputPerMTok; got != 5 {
		t.Errorf("[1m] variant input rate = %v, want 5", got)
	}
	// Unknown model → mid-tier fallback, never $0.
	if got := p.For("some-other-model").InputPerMTok; got != 3 {
		t.Errorf("unknown model input rate = %v, want 3 (fallback)", got)
	}
	if got := p.For("").InputPerMTok; got != 3 {
		t.Errorf("empty model input rate = %v, want 3 (fallback)", got)
	}
}

func TestPricing_OverridesWin(t *testing.T) {
	p := NewPricing(map[string]ModelPrice{
		"claude-opus-4-8": {InputPerMTok: 4, OutputPerMTok: 20},
	})
	mp := p.For("claude-opus-4-8")
	if mp.InputPerMTok != 4 {
		t.Errorf("override not applied: %+v", mp)
	}
	// Derived cache rates follow the overridden input.
	if !approx(mp.CacheReadPerMTok, 0.4) || !approx(mp.CacheWritePerMTok, 5) {
		t.Errorf("override cache derivation wrong: %+v", mp)
	}
}

func TestPricing_AvoidedTiers(t *testing.T) {
	p := NewPricing(nil)
	// 1M tokens in each tier for opus: input $5, cache-write $6.25, cache-read $0.5.
	in, wr, rd := p.AvoidedTiers("claude-opus-4-8", 1_000_000, 1_000_000, 1_000_000)
	if !approx(in, 5) || !approx(wr, 6.25) || !approx(rd, 0.5) {
		t.Errorf("AvoidedTiers = (%v, %v, %v), want (5, 6.25, 0.5)", in, wr, rd)
	}
	// Zero tokens → zero dollars.
	if i, w, r := p.AvoidedTiers("claude-opus-4-8", 0, 0, 0); i+w+r != 0 {
		t.Errorf("zero tokens should be $0, got %v %v %v", i, w, r)
	}
}

func TestPricing_SpendUSD(t *testing.T) {
	p := NewPricing(nil)
	// opus: input 1M ($5) + cache-write 1M ($6.25) + cache-read 1M ($0.5) + output 1M ($25) = $36.75.
	got := p.SpendUSD("claude-opus-4-8", 1_000_000, 1_000_000, 1_000_000, 1_000_000)
	if !approx(got, 36.75) {
		t.Errorf("SpendUSD = %v, want 36.75", got)
	}
}
