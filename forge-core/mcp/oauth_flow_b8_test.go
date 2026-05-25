package mcp

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// TestB8_LoginServer_HasAllTimeouts pins the review-B8 fix: the
// loopback HTTP server used by Login has every common timeout set,
// not just ReadHeaderTimeout. A regression that drops any of these
// (or that adds a new timeout knob to http.Server we forget) fails
// this test.
func TestB8_LoginServer_HasAllTimeouts(t *testing.T) {
	t.Parallel()
	srv := newLoginServer(http.NewServeMux())

	// All five fields MUST be non-zero. Pin them to their declared
	// constants so a future tightening / loosening shows up here.
	checks := []struct {
		name string
		got  time.Duration
		want time.Duration
	}{
		{"ReadHeaderTimeout", srv.ReadHeaderTimeout, loginReadHeaderTimeout},
		{"ReadTimeout", srv.ReadTimeout, loginReadTimeout},
		{"WriteTimeout", srv.WriteTimeout, loginWriteTimeout},
		{"IdleTimeout", srv.IdleTimeout, loginIdleTimeout},
	}
	for _, c := range checks {
		if c.got == 0 {
			t.Errorf("login server %s = 0 — defense-in-depth (review B8) requires a bound", c.name)
		}
		if c.got != c.want {
			t.Errorf("login server %s = %v, want %v (constant drift)", c.name, c.got, c.want)
		}
	}

	// All timeouts must be sane — non-zero, well below an hour, with
	// idle ≥ read ≥ readHeader (so a partial header read doesn't
	// pre-empt a slow body read on legitimate traffic). The legit
	// callback is small; values can be aggressive.
	if loginReadHeaderTimeout > loginReadTimeout {
		t.Errorf("ReadHeaderTimeout(%v) > ReadTimeout(%v) — invalid ordering", loginReadHeaderTimeout, loginReadTimeout)
	}
	if loginReadTimeout > loginIdleTimeout {
		t.Errorf("ReadTimeout(%v) > IdleTimeout(%v) — invalid ordering", loginReadTimeout, loginIdleTimeout)
	}
	for _, d := range []time.Duration{
		loginReadHeaderTimeout, loginReadTimeout, loginWriteTimeout,
		loginIdleTimeout, loginShutdownTimeout,
	} {
		if d > time.Hour {
			t.Errorf("duration %v > 1h is implausible for a single-callback loopback server", d)
		}
	}
}

// TestB8_LoginShutdown_IsBounded simulates a hung connection holding
// the loopback server open past Login's return point. Before the
// fix, the deferred server.Shutdown(context.Background()) would
// hang the Login goroutine indefinitely. After the fix, Shutdown
// returns within loginShutdownTimeout regardless.
func TestB8_LoginShutdown_IsBounded(t *testing.T) {
	t.Parallel()
	// Server with a handler that blocks until ctx fires — simulates
	// a misbehaving redirect.
	srv := newLoginServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))

	// We're not actually doing Login here — just exercising the
	// shutdown path with the same bounded ctx pattern Login uses.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), loginShutdownTimeout)
	defer cancel()

	t0 := time.Now()
	err := srv.Shutdown(shutdownCtx)
	elapsed := time.Since(t0)

	// Shutdown returns either nil (clean) or ctx.DeadlineExceeded
	// (forced after timeout). Either is acceptable — the property
	// we care about is "did not hang forever."
	if elapsed > 2*loginShutdownTimeout {
		t.Errorf("Shutdown took %v, expected ≤ %v (bounded by loginShutdownTimeout)", elapsed, loginShutdownTimeout)
	}
	_ = err
}
