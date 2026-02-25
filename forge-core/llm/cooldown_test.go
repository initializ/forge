package llm

import (
	"sync"
	"testing"
	"time"
)

func TestCooldownTracker_NewProviderAvailable(t *testing.T) {
	ct := NewCooldownTracker()
	if !ct.IsAvailable("openai") {
		t.Error("new provider should be available")
	}
}

func TestCooldownTracker_MarkFailurePutsCooldown(t *testing.T) {
	ct := NewCooldownTracker()
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	ct.nowFunc = func() time.Time { return now }

	ct.MarkFailure("openai", FailoverRateLimit)

	// Immediately after failure, provider should be unavailable
	if ct.IsAvailable("openai") {
		t.Error("provider should be in cooldown immediately after failure")
	}

	// After 30 seconds, still unavailable (1 min cooldown)
	ct.nowFunc = func() time.Time { return now.Add(30 * time.Second) }
	if ct.IsAvailable("openai") {
		t.Error("provider should still be in cooldown after 30s")
	}

	// After 61 seconds, available again
	ct.nowFunc = func() time.Time { return now.Add(61 * time.Second) }
	if !ct.IsAvailable("openai") {
		t.Error("provider should be available after cooldown expires")
	}
}

func TestCooldownTracker_ExponentialBackoff(t *testing.T) {
	ct := NewCooldownTracker()
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	ct.nowFunc = func() time.Time { return now }

	// First failure: 1 min cooldown
	ct.MarkFailure("openai", FailoverRateLimit)
	ct.nowFunc = func() time.Time { return now.Add(61 * time.Second) }
	if !ct.IsAvailable("openai") {
		t.Error("should be available after 1 min cooldown")
	}

	// Second failure: 5 min cooldown
	now = now.Add(2 * time.Minute)
	ct.nowFunc = func() time.Time { return now }
	ct.MarkFailure("openai", FailoverRateLimit)

	ct.nowFunc = func() time.Time { return now.Add(4 * time.Minute) }
	if ct.IsAvailable("openai") {
		t.Error("should still be in cooldown (5 min required, 4 min elapsed)")
	}

	ct.nowFunc = func() time.Time { return now.Add(6 * time.Minute) }
	if !ct.IsAvailable("openai") {
		t.Error("should be available after 5 min cooldown")
	}

	// Third failure: 25 min cooldown
	now = now.Add(10 * time.Minute)
	ct.nowFunc = func() time.Time { return now }
	ct.MarkFailure("openai", FailoverRateLimit)

	ct.nowFunc = func() time.Time { return now.Add(24 * time.Minute) }
	if ct.IsAvailable("openai") {
		t.Error("should still be in cooldown (25 min required)")
	}

	ct.nowFunc = func() time.Time { return now.Add(26 * time.Minute) }
	if !ct.IsAvailable("openai") {
		t.Error("should be available after 25 min cooldown")
	}

	// Fourth failure: capped at 1 hour
	now = now.Add(30 * time.Minute)
	ct.nowFunc = func() time.Time { return now }
	ct.MarkFailure("openai", FailoverRateLimit)

	ct.nowFunc = func() time.Time { return now.Add(59 * time.Minute) }
	if ct.IsAvailable("openai") {
		t.Error("should still be in cooldown (1 hour cap)")
	}

	ct.nowFunc = func() time.Time { return now.Add(61 * time.Minute) }
	if !ct.IsAvailable("openai") {
		t.Error("should be available after 1 hour cap")
	}
}

func TestCooldownTracker_AuthAlways24Hours(t *testing.T) {
	ct := NewCooldownTracker()
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	ct.nowFunc = func() time.Time { return now }

	ct.MarkFailure("openai", FailoverAuth)

	// 23 hours later: still unavailable
	ct.nowFunc = func() time.Time { return now.Add(23 * time.Hour) }
	if ct.IsAvailable("openai") {
		t.Error("auth failure should have 24h cooldown")
	}

	// 25 hours later: available
	ct.nowFunc = func() time.Time { return now.Add(25 * time.Hour) }
	if !ct.IsAvailable("openai") {
		t.Error("should be available after 24h auth cooldown")
	}
}

func TestCooldownTracker_BillingBackoff(t *testing.T) {
	ct := NewCooldownTracker()
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	ct.nowFunc = func() time.Time { return now }

	// First billing failure: 5 hours
	ct.MarkFailure("openai", FailoverBilling)

	ct.nowFunc = func() time.Time { return now.Add(4 * time.Hour) }
	if ct.IsAvailable("openai") {
		t.Error("should be in cooldown (5h required)")
	}

	ct.nowFunc = func() time.Time { return now.Add(6 * time.Hour) }
	if !ct.IsAvailable("openai") {
		t.Error("should be available after 5h billing cooldown")
	}
}

func TestCooldownTracker_MarkSuccessResets(t *testing.T) {
	ct := NewCooldownTracker()
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	ct.nowFunc = func() time.Time { return now }

	// Build up failures
	ct.MarkFailure("openai", FailoverRateLimit)
	ct.MarkFailure("openai", FailoverRateLimit)

	// Mark success resets everything
	ct.MarkSuccess("openai")

	if !ct.IsAvailable("openai") {
		t.Error("should be available after MarkSuccess")
	}

	// Next failure should be back to count=1 (1 min)
	ct.MarkFailure("openai", FailoverRateLimit)
	ct.nowFunc = func() time.Time { return now.Add(61 * time.Second) }
	if !ct.IsAvailable("openai") {
		t.Error("after reset, first failure should have 1 min cooldown")
	}
}

func TestCooldownTracker_IndependentProviders(t *testing.T) {
	ct := NewCooldownTracker()
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	ct.nowFunc = func() time.Time { return now }

	ct.MarkFailure("openai", FailoverRateLimit)

	if !ct.IsAvailable("anthropic") {
		t.Error("different provider should not be affected")
	}
	if ct.IsAvailable("openai") {
		t.Error("failed provider should be in cooldown")
	}
}

func TestCooldownTracker_ConcurrentAccess(t *testing.T) {
	ct := NewCooldownTracker()
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	ct.nowFunc = func() time.Time { return now }

	var wg sync.WaitGroup
	for range 100 {
		wg.Add(3)
		go func() {
			defer wg.Done()
			ct.MarkFailure("openai", FailoverRateLimit)
		}()
		go func() {
			defer wg.Done()
			ct.IsAvailable("openai")
		}()
		go func() {
			defer wg.Done()
			ct.MarkSuccess("openai")
		}()
	}
	wg.Wait()
}

func TestCooldownDuration(t *testing.T) {
	tests := []struct {
		reason FailoverReason
		count  int
		want   time.Duration
	}{
		// Standard
		{FailoverRateLimit, 1, time.Minute},
		{FailoverRateLimit, 2, 5 * time.Minute},
		{FailoverRateLimit, 3, 25 * time.Minute},
		{FailoverRateLimit, 4, time.Hour},
		{FailoverRateLimit, 10, time.Hour}, // capped
		{FailoverOverloaded, 1, time.Minute},
		{FailoverTimeout, 1, time.Minute},
		{FailoverUnknown, 1, time.Minute},

		// Billing
		{FailoverBilling, 1, 5 * time.Hour},
		{FailoverBilling, 2, 10 * time.Hour},
		{FailoverBilling, 3, 20 * time.Hour},
		{FailoverBilling, 4, 24 * time.Hour},
		{FailoverBilling, 10, 24 * time.Hour}, // capped

		// Auth
		{FailoverAuth, 1, 24 * time.Hour},
		{FailoverAuth, 5, 24 * time.Hour},

		// Zero count
		{FailoverRateLimit, 0, 0},
	}

	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			got := cooldownDuration(tt.reason, tt.count)
			if got != tt.want {
				t.Errorf("cooldownDuration(%s, %d) = %v, want %v", tt.reason, tt.count, got, tt.want)
			}
		})
	}
}
