// Package graders provides reusable, instrumented graders for the OWASP ASI
// conformance suite. A grader is authoritative for a "contained" claim only
// when it reads an instrumented signal (an audit event, a policy-violation
// error, a blocked-egress record) -- never the mere absence of bad output.
//
// The audit grader parses the NDJSON stream a *runtime.AuditLogger writes and
// answers questions about which control events fired and with what fields.
package graders

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"

	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

// Recorder wraps an AuditLogger writing to an in-memory buffer so a test can
// drive real emission paths and then assert on the captured events.
type Recorder struct {
	buf    *bytes.Buffer
	Logger *coreruntime.AuditLogger
}

// NewRecorder returns a Recorder backed by a fresh in-memory audit stream.
func NewRecorder() *Recorder {
	buf := &bytes.Buffer{}
	return &Recorder{buf: buf, Logger: coreruntime.NewAuditLogger(buf)}
}

// Raw returns the full NDJSON stream captured so far.
func (r *Recorder) Raw() string { return r.buf.String() }

// Events parses the captured NDJSON into a slice of generic maps. Malformed
// lines are skipped (the audit sink only writes well-formed JSON).
func (r *Recorder) Events() []map[string]any {
	var out []map[string]any
	sc := bufio.NewScanner(bytes.NewReader(r.buf.Bytes()))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err == nil {
			out = append(out, m)
		}
	}
	return out
}

// Count returns how many captured events have the given event name.
func (r *Recorder) Count(event string) int {
	n := 0
	for _, e := range r.Events() {
		if e["event"] == event {
			n++
		}
	}
	return n
}

// Has reports whether any captured event with the given name carries
// fields[field] == value. Field lookups descend into the "fields" object.
func (r *Recorder) Has(event, field string, value any) bool {
	for _, e := range r.Events() {
		if e["event"] != event {
			continue
		}
		f, ok := e["fields"].(map[string]any)
		if !ok {
			continue
		}
		if fieldEqual(f[field], value) {
			return true
		}
	}
	return false
}

// HasEvent reports whether any captured event has the given name.
func (r *Recorder) HasEvent(event string) bool { return r.Count(event) > 0 }

// fieldEqual compares JSON-decoded values tolerantly (numbers decode as
// float64, so an int expectation is normalized before comparison).
func fieldEqual(got, want any) bool {
	switch w := want.(type) {
	case int:
		if g, ok := got.(float64); ok {
			return g == float64(w)
		}
	case int64:
		if g, ok := got.(float64); ok {
			return g == float64(w)
		}
	}
	return got == want
}

// Rate returns confirmed/total as a fraction in [0,1]; 0 total yields 0.
// Graders report rates (not booleans) so the measured number is visible in
// test output even when a threshold passes.
func Rate(confirmed, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(confirmed) / float64(total)
}
