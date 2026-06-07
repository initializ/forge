package runtime

import (
	"context"
	"time"
)

// AuditExportStatusInterval is how often StartAuditExportStatus emits
// an audit_export_status event. The issue calls for 60s; exposed as a
// package var so tests can shorten it.
var AuditExportStatusInterval = 60 * time.Second

// AuditExportStatus is the event type for the periodic per-sink health
// report. Single event per tick; carries one entry in fields.sinks per
// registered sink with that sink's counters (writes_ok, drops_timeout,
// drops_dial, connected).
const AuditExportStatus = "audit_export_status"

// StartAuditExportStatus spawns a background goroutine that emits one
// audit_export_status event every AuditExportStatusInterval until the
// returned stop function is called or ctx is cancelled. The goroutine
// uses the same AuditLogger it reports on — the status event itself
// flows through every sink, including the export ones, so operators
// can see "is my export healthy?" by inspecting the export stream.
//
// Returns a stop func the caller invokes during shutdown. The stop
// func blocks until the goroutine exits so shutdown ordering is
// deterministic (no chance of a final status event landing after the
// runtime has torn down its writers).
//
// Idempotent: calling stop twice is safe.
//
// See issue #95 / FWS-7 acceptance criterion §6.
func StartAuditExportStatus(ctx context.Context, audit *AuditLogger) (stop func()) {
	if audit == nil {
		return func() {}
	}
	done := make(chan struct{})
	stopCh := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(AuditExportStatusInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stopCh:
				return
			case <-t.C:
				emitAuditExportStatus(audit)
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

// emitAuditExportStatus snapshots every registered sink's stats and
// emits a single status event. Sinks are reported in registration
// order so the stderr safety-net always lands at sinks[0] — operators
// reading the event have a consistent shape.
//
// The event flows through the AuditLogger like any other, so the
// status itself reaches every sink. There's no risk of a feedback
// loop: the status event doesn't trigger sink-write callbacks; it's
// just bytes on the wire.
func emitAuditExportStatus(audit *AuditLogger) {
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
		Fields: map[string]any{"sinks": report},
	})
}
