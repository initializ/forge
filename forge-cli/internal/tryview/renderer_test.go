package tryview

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// feed writes a single synthetic audit event (as NDJSON) to the renderer.
func feed(r *Renderer, ndjson string) {
	_ = r.Write(context.Background(), []byte(ndjson+"\n"))
}

func TestRenderer_EventLines(t *testing.T) {
	cases := []struct {
		name  string
		event string
		want  string
	}{
		{
			"tool start with args",
			`{"event":"tool_exec","fields":{"tool":"weather_current","phase":"start","args":"{\"location\":\"Tokyo\"}"}}`,
			`weather_current(location=Tokyo)`,
		},
		{
			"tool end with result",
			`{"event":"tool_exec","fields":{"tool":"weather_current","phase":"end","result":"18C light rain"}}`,
			`18C light rain`,
		},
		{
			"egress allowed",
			`{"event":"egress_allowed","fields":{"domain":"wttr.in"}}`,
			`allowed`,
		},
		{
			"egress blocked",
			`{"event":"egress_blocked","fields":{"domain":"evil.example"}}`,
			`blocked`,
		},
		{
			"guardrail blocked",
			`{"event":"guardrail_check","fields":{"name":"no-secrets","decision":"blocked"}}`,
			`blocked`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			r := New(&buf, false, false, false) // plain text
			feed(r, tc.event)
			if got := buf.String(); !strings.Contains(got, tc.want) {
				t.Errorf("line = %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

func TestRenderer_GuardrailPassRendersNothing(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, false, false, false)
	feed(r, `{"event":"guardrail_check","fields":{"name":"ok","decision":"allow"}}`)
	if buf.Len() != 0 {
		t.Errorf("a passing guardrail must render nothing, got %q", buf.String())
	}
}

func TestRenderer_Quiet(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, true /*quiet*/, false, false)
	feed(r, `{"event":"tool_exec","fields":{"tool":"x","phase":"start"}}`)
	feed(r, `{"event":"egress_allowed","fields":{"domain":"wttr.in"}}`)
	r.FlushSummary()
	if buf.Len() != 0 {
		t.Errorf("--quiet must render nothing, got %q", buf.String())
	}
}

func TestRenderer_AuditEchoesRawNDJSON(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, false, true /*audit*/, false)
	raw := `{"event":"tool_exec","fields":{"tool":"x","phase":"start"}}`
	feed(r, raw)
	if got := strings.TrimSpace(buf.String()); got != raw {
		t.Errorf("--audit should echo raw NDJSON; got %q want %q", got, raw)
	}
}

func TestRenderer_NoColorHasNoEscapes(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, false, false, false) // color=false
	feed(r, `{"event":"egress_allowed","fields":{"domain":"wttr.in"}}`)
	if strings.Contains(buf.String(), "\x1b[") {
		t.Errorf("plain-text mode must not emit ANSI escapes, got %q", buf.String())
	}
}

func TestRenderer_FlushSummary(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, false, false, false)
	feed(r, `{"event":"tool_exec","fields":{"tool":"weather_current","phase":"start"}}`)
	feed(r, `{"event":"egress_allowed","fields":{"domain":"wttr.in"}}`)
	buf.Reset() // isolate the summary
	r.FlushSummary()
	got := buf.String()
	if !strings.Contains(got, "audit") || !strings.Contains(got, "weather_current") || !strings.Contains(got, "wttr.in:allow") {
		t.Errorf("summary = %q, want audit + tool + egress", got)
	}
	// A second flush with no new events prints nothing (turn was reset).
	buf.Reset()
	r.FlushSummary()
	if buf.Len() != 0 {
		t.Errorf("summary after reset should be empty, got %q", buf.String())
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("hello world", 5); got != "hell…" {
		t.Errorf("truncate = %q, want %q", got, "hell…")
	}
	if got := truncate("short", 80); got != "short" {
		t.Errorf("truncate should pass short strings, got %q", got)
	}
}
