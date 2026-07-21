// Package tryview renders the `forge try` agent's audit stream as inline,
// human-readable "loop" lines — tool calls, egress checks, guardrail blocks —
// so the first-run experience shows the agent working, not just its reply.
//
// The renderer implements the coreruntime audit Sink contract, so it attaches
// alongside (or instead of) the NDJSON sink. It reads existing audit events
// only; it never derives fields that aren't on the event. Styling degrades to
// plain text when color is unavailable.
package tryview

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Renderer maps audit events to inline loop lines. Zero value is not usable;
// construct with New.
type Renderer struct {
	out   io.Writer
	style styles
	quiet bool // hide every loop line and the summary
	audit bool // echo full NDJSON instead of the mapped lines
	turn  turnSummary
}

// New builds a Renderer writing to out. quiet hides the loop; audit echoes the
// full NDJSON stream; color enables the orange/grey styling (pass false for a
// non-TTY or NO_COLOR).
func New(out io.Writer, quiet, audit, color bool) *Renderer {
	return &Renderer{out: out, style: newStyles(color), quiet: quiet, audit: audit}
}

// turnSummary accumulates the salient facts of one turn for the compact audit
// line printed after the agent reply.
type turnSummary struct {
	Tools  []string `json:"tools,omitempty"`
	Egress []string `json:"egress,omitempty"` // "domain:allow" / "domain:block"
}

// auditLine is the subset of the audit event the renderer reads.
type auditLine struct {
	Event  string         `json:"event"`
	Fields map[string]any `json:"fields"`
}

// Write implements the audit Sink interface. eventBytes is one NDJSON line
// (newline included).
func (r *Renderer) Write(_ context.Context, eventBytes []byte) error {
	if r.audit {
		_, _ = r.out.Write(eventBytes)
		return nil
	}
	if r.quiet {
		return nil
	}
	var e auditLine
	if err := json.Unmarshal(eventBytes, &e); err != nil {
		return nil // never fail the emit path over a render hiccup
	}
	if line := r.lineFor(&e); line != "" {
		_, _ = fmt.Fprintln(r.out, line)
	}
	return nil
}

// Close implements the audit Sink interface.
func (r *Renderer) Close(context.Context) error { return nil }

// Name implements the audit Sink interface.
func (r *Renderer) Name() string { return "tryview" }

// Stats implements the audit Sink interface.
func (r *Renderer) Stats() map[string]int64 { return nil }

// FlushSummary prints the one-line compact audit summary for the turn (dim),
// then resets it. Call it after printing the agent reply. No-op under --quiet /
// --audit or when the turn touched no tools or egress.
func (r *Renderer) FlushSummary() {
	defer func() { r.turn = turnSummary{} }()
	if r.quiet || r.audit {
		return
	}
	if len(r.turn.Tools) == 0 && len(r.turn.Egress) == 0 {
		return
	}
	b, _ := json.Marshal(r.turn)
	_, _ = fmt.Fprintln(r.out, "  "+r.style.dim("audit  "+string(b)))
}

// lineFor maps one audit event to its rendered line, or "" to render nothing.
func (r *Renderer) lineFor(e *auditLine) string {
	switch e.Event {
	case "tool_exec":
		return r.toolLine(e)
	case "egress_allowed":
		domain := fieldStr(e.Fields, "domain")
		r.turn.Egress = append(r.turn.Egress, domain+":allow")
		return r.prefix("egress") + " " + domain + "   " + r.style.ok("✓ allowed")
	case "egress_blocked":
		domain := fieldStr(e.Fields, "domain")
		r.turn.Egress = append(r.turn.Egress, domain+":block")
		return r.prefix("egress") + " " + domain + "   " + r.style.bad("✗ blocked")
	case "guardrail_check":
		if !guardBlocked(e.Fields) {
			return ""
		}
		name := firstStr(e.Fields, "name", "guardrail", "rule")
		return r.prefix("guard") + " " + name + "   " + r.style.bad("✗ blocked")
	}
	return ""
}

// toolLine renders a tool_exec start (the call) or end (the result / error).
func (r *Renderer) toolLine(e *auditLine) string {
	tool := fieldStr(e.Fields, "tool")
	switch fieldStr(e.Fields, "phase") {
	case "start":
		if tool != "" {
			r.turn.Tools = append(r.turn.Tools, tool)
		}
		return r.prefix("tool") + " " + tool + "(" + argPreview(e.Fields["args"]) + ")"
	case "end":
		if msg := fieldStr(e.Fields, "error"); msg != "" {
			return "  " + r.style.label("◂") + " " + r.style.bad("error: "+truncate(oneLine(msg), 80))
		}
		if res := fieldStr(e.Fields, "result"); res != "" {
			return "  " + r.style.label("◂") + " " + truncate(oneLine(res), 80)
		}
	}
	return ""
}

// prefix builds the styled "  ▸ <label>" lead, label padded to align columns.
func (r *Renderer) prefix(label string) string {
	return "  " + r.style.label(fmt.Sprintf("▸ %-6s", label))
}

// --- field helpers -------------------------------------------------------

func fieldStr(fields map[string]any, key string) string {
	if fields == nil {
		return ""
	}
	s, _ := fields[key].(string)
	return s
}

func firstStr(fields map[string]any, keys ...string) string {
	for _, k := range keys {
		if s := fieldStr(fields, k); s != "" {
			return s
		}
	}
	return ""
}

func guardBlocked(fields map[string]any) bool {
	switch d := fieldStr(fields, "decision"); d {
	case "block", "blocked", "deny", "denied":
		return true
	}
	if b, ok := fields["blocked"].(bool); ok {
		return b
	}
	return false
}

// argPreview renders a tool's JSON-object args as key="value", ... — falling
// back to the raw (truncated) string when it isn't a flat object.
func argPreview(v any) string {
	s, _ := v.(string)
	if s == "" {
		return ""
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(s), &obj); err != nil {
		return truncate(oneLine(s), 60)
	}
	var parts []string
	for k, val := range obj {
		parts = append(parts, fmt.Sprintf("%s=%v", k, val))
	}
	return truncate(strings.Join(parts, ", "), 60)
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

// --- styling -------------------------------------------------------------

type styles struct {
	label func(string) string // orange accent for ▸ / ◂ / labels
	dim   func(string) string // muted grey for the audit summary
	ok    func(string) string // success (allowed)
	bad   func(string) string // failure (blocked / error)
}

func newStyles(color bool) styles {
	if !color {
		id := func(s string) string { return s }
		return styles{label: id, dim: id, ok: id, bad: id}
	}
	orange := lipgloss.NewStyle().Foreground(lipgloss.Color("#f97316"))
	grey := lipgloss.NewStyle().Foreground(lipgloss.Color("#888888"))
	green := lipgloss.NewStyle().Foreground(lipgloss.Color("#22c55e"))
	red := lipgloss.NewStyle().Foreground(lipgloss.Color("#ef4444"))
	return styles{
		label: func(s string) string { return orange.Render(s) },
		dim:   func(s string) string { return grey.Render(s) },
		ok:    func(s string) string { return green.Render(s) },
		bad:   func(s string) string { return red.Render(s) },
	}
}
