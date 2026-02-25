package llm

import (
	"sync"
	"time"
)

// cooldownEntry tracks failure state for a single provider.
type cooldownEntry struct {
	count    int
	reason   FailoverReason
	lastFail time.Time
}

// CooldownTracker manages per-provider cooldown state with exponential backoff.
type CooldownTracker struct {
	mu      sync.RWMutex
	entries map[string]*cooldownEntry
	nowFunc func() time.Time
}

// NewCooldownTracker creates a new cooldown tracker.
func NewCooldownTracker() *CooldownTracker {
	return &CooldownTracker{
		entries: make(map[string]*cooldownEntry),
		nowFunc: time.Now,
	}
}

// MarkFailure records a failure for the given provider.
func (ct *CooldownTracker) MarkFailure(provider string, reason FailoverReason) {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	e, ok := ct.entries[provider]
	if !ok {
		e = &cooldownEntry{}
		ct.entries[provider] = e
	}
	e.count++
	e.reason = reason
	e.lastFail = ct.nowFunc()
}

// MarkSuccess resets all cooldown state for a provider.
func (ct *CooldownTracker) MarkSuccess(provider string) {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	delete(ct.entries, provider)
}

// IsAvailable returns true if the provider is not currently in cooldown.
func (ct *CooldownTracker) IsAvailable(provider string) bool {
	ct.mu.RLock()
	defer ct.mu.RUnlock()

	e, ok := ct.entries[provider]
	if !ok {
		return true
	}

	dur := cooldownDuration(e.reason, e.count)
	return ct.nowFunc().After(e.lastFail.Add(dur))
}

// cooldownDuration returns the cooldown period based on reason and failure count.
//
// Standard errors (rate_limit, overloaded, timeout, unknown):
//
//	count 1: 1 min, count 2: 5 min, count 3: 25 min, count 4+: 1 hour (cap)
//
// Billing errors:
//
//	count 1: 5 hours, count 2: 10 hours, count 3: 20 hours, count 4+: 24 hours (cap)
//
// Auth errors:
//
//	Always 24 hours (credentials won't fix themselves mid-session)
func cooldownDuration(reason FailoverReason, count int) time.Duration {
	if count <= 0 {
		return 0
	}

	switch reason {
	case FailoverAuth:
		return 24 * time.Hour

	case FailoverBilling:
		// 5h * 2^(count-1), capped at 24h
		base := 5 * time.Hour
		d := base
		for i := 1; i < count; i++ {
			d *= 2
		}
		if d > 24*time.Hour {
			d = 24 * time.Hour
		}
		return d

	default:
		// Standard: 1min * 5^(count-1), capped at 1h
		base := time.Minute
		d := base
		for i := 1; i < count; i++ {
			d *= 5
		}
		if d > time.Hour {
			d = time.Hour
		}
		return d
	}
}
