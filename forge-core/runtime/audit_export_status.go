package runtime

import (
	"context"
	"os"
	"time"
)

// Audit-export status heartbeat cadence (issue #280). The event proves the
// audit pipeline is alive and reports per-sink health. Emitting it once a
// minute produced ~1,440 rows/agent/day of near-constant "still fine" noise
// that dominated the audit collection at fleet scale. The hybrid below keeps
// the integrity signal while collapsing steady-state volume to a handful/day:
//
//   - poll sink health frequently and emit IMMEDIATELY on a state change
//     (a sink's `connected` flag flips, or a sink is added/removed), so a
//     failure surfaces within one poll interval;
//   - otherwise emit a keepalive on a slow interval, so liveness stays
//     provable (a missing keepalive past the interval is itself alertable).
//
// The edge signal is `connected` — a LEVEL the sink maintains — and NOT the
// cumulative `drops_*` counters. This is deliberate and load-bearing: the
// status event itself flows through every sink, including a failing one, so
// its own write bumps that sink's drop counter. On an idle agent the status
// emits are the *only* writes, so a drop-delta edge would be self-referential
// — every poll would see "drops increased since last time" and emit again,
// one event per poll for the whole outage (4× the old fixed cadence, and
// worst precisely during an incident). `connected` has no such feedback: a
// dial failure holds it at 0 and a write timeout disconnects the sink (so it
// also reads 0 on the next poll), so both failure modes converge on a single
// 1→0 transition that settles until recovery flips it back. Drop counters
// still ride in every emit's `sinks[]` payload for anyone tracking totals;
// they just don't drive the edge.
//
// Every emit carries fields.reason ("state_change" | "keepalive") so
// consumers can distinguish "something changed" from "still alive".
var (
	// AuditExportStatusPollInterval is how often sink health is checked for a
	// state change. This is the failure-detection latency. Package var so
	// tests can shorten it.
	AuditExportStatusPollInterval = 15 * time.Second

	// AuditExportStatusKeepaliveInterval is the maximum time between emitted
	// status events when nothing changes. Overridable at process startup via
	// the AUDIT_STATUS_KEEPALIVE_INTERVAL env var (a Go duration, e.g. "15m",
	// "1h"); it is read once when the heartbeat starts, so a change takes a
	// restart — there is no live reload. Package var so tests can shorten it.
	AuditExportStatusKeepaliveInterval = 15 * time.Minute
)

// EnvAuditStatusKeepaliveInterval overrides AuditExportStatusKeepaliveInterval
// when set to a valid Go duration. Read once at startup (deploy-time knob, not
// live-tunable).
const EnvAuditStatusKeepaliveInterval = "AUDIT_STATUS_KEEPALIVE_INTERVAL"

// AuditExportStatus is the event type for the per-sink health report.
const AuditExportStatus = "audit_export_status"

// Emit reasons (fields.reason).
const (
	auditStatusReasonKeepalive   = "keepalive"
	auditStatusReasonStateChange = "state_change"
)

// StartAuditExportStatus spawns a background goroutine that emits
// audit_export_status events using the hybrid cadence described above, until
// the returned stop function is called or ctx is cancelled. The goroutine
// uses the same AuditLogger it reports on — the status event flows through
// every sink (including the export ones) so operators can see "is my export
// healthy?" by inspecting the export stream.
//
// An initial keepalive is emitted at startup so liveness is provable from
// t=0. The stop func blocks until the goroutine exits (deterministic shutdown
// ordering) and is idempotent.
//
// See issue #95 / FWS-7 §6 (original) and issue #280 (hybrid cadence).
func StartAuditExportStatus(ctx context.Context, audit *AuditLogger) (stop func()) {
	if audit == nil {
		return func() {}
	}
	keepalive := resolveKeepaliveInterval()
	done := make(chan struct{})
	stopCh := make(chan struct{})
	go func() {
		defer close(done)

		// Baseline emit so a healthy pipeline is visible immediately, and a
		// reference snapshot to diff subsequent polls against.
		last := sinkStateSnapshot(audit)
		emitAuditExportStatus(audit, auditStatusReasonKeepalive)
		lastEmit := time.Now()

		poll := time.NewTicker(AuditExportStatusPollInterval)
		defer poll.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stopCh:
				return
			case <-poll.C:
				cur := sinkStateSnapshot(audit)
				switch {
				case sinkStateChanged(last, cur):
					emitAuditExportStatus(audit, auditStatusReasonStateChange)
					last, lastEmit = cur, time.Now()
				case time.Since(lastEmit) >= keepalive:
					emitAuditExportStatus(audit, auditStatusReasonKeepalive)
					last, lastEmit = cur, time.Now()
				}
			}
		}
	}()
	var stopped bool
	return func() {
		if stopped {
			return
		}
		stopped = true
		close(stopCh)
		<-done
	}
}

// resolveKeepaliveInterval reads AUDIT_STATUS_KEEPALIVE_INTERVAL when set to a
// positive Go duration; otherwise returns AuditExportStatusKeepaliveInterval.
func resolveKeepaliveInterval() time.Duration {
	if v := os.Getenv(EnvAuditStatusKeepaliveInterval); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return AuditExportStatusKeepaliveInterval
}

// sinkState is the edge-trigger-relevant health of a sink: whether it
// currently holds a working connection. The cumulative `drops_*` counters are
// deliberately EXCLUDED (see the package comment and sinkStateChanged) — they
// increment on the status event's own writes, so diffing them would make the
// signal self-referential and re-fire every poll during an outage.
type sinkState struct {
	connected int64
}

// sinkStateSnapshot captures the health-relevant state for every sink, keyed
// by sink name.
func sinkStateSnapshot(audit *AuditLogger) map[string]sinkState {
	sinks := audit.Sinks()
	out := make(map[string]sinkState, len(sinks))
	for _, s := range sinks {
		out[s.Name()] = sinkState{connected: s.Stats()["connected"]}
	}
	return out
}

// sinkStateChanged reports whether any sink's health changed between two
// snapshots: a `connected` flip (either direction — 1→0 is failure, 0→1 is
// recovery) or a sink added/removed. Drop counters are intentionally not part
// of the diff (see the package comment).
func sinkStateChanged(prev, cur map[string]sinkState) bool {
	if len(prev) != len(cur) {
		return true
	}
	for name, c := range cur {
		p, ok := prev[name]
		if !ok || p.connected != c.connected {
			return true
		}
	}
	return false
}

// emitAuditExportStatus snapshots every registered sink's full stats and
// emits a single status event tagged with the given reason. Sinks are
// reported in registration order so the stderr safety-net always lands at
// sinks[0] — operators reading the event have a consistent shape. The full
// stats (including the cumulative drop counters) ride in the payload even
// though only `connected` drives the edge, so anyone tracking totals still
// sees them on every emit.
//
// The event flows through the AuditLogger like any other; there's no feedback
// loop that triggers another status emit (a sink write does not call back into
// this — it's just bytes on the wire).
func emitAuditExportStatus(audit *AuditLogger, reason string) {
	sinks := audit.Sinks()
	report := make([]map[string]any, 0, len(sinks))
	for _, s := range sinks {
		entry := map[string]any{"name": s.Name()}
		for k, v := range s.Stats() {
			entry[k] = v
		}
		report = append(report, entry)
	}
	audit.Emit(AuditEvent{
		Event:  AuditExportStatus,
		Fields: map[string]any{"reason": reason, "sinks": report},
	})
}
