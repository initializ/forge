package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

// fireToolExec replays the BeforeToolExec / AfterToolExec hook pair the
// runner fires for a single tool call and returns the parsed events.
// Shared by the issue #163 capture tests below so each test focuses on
// what it's asserting rather than wire-up boilerplate.
func fireToolExec(t *testing.T, capture coreruntime.AuditPayloadCapture, input, output string) []coreruntime.AuditEvent {
	t.Helper()
	var buf bytes.Buffer
	al := coreruntime.NewAuditLogger(&buf)
	r := &Runner{cfg: RunnerConfig{AuditPayloadCapture: capture}}
	hooks := coreruntime.NewHookRegistry()
	r.registerAuditHooks(hooks, al)

	ctx := coreruntime.WithCorrelationID(context.Background(), "corr")
	ctx = coreruntime.WithTaskID(ctx, "task")
	ctx = coreruntime.WithSequenceCounter(ctx, new(coreruntime.SequenceCounter))

	if err := hooks.Fire(ctx, coreruntime.BeforeToolExec, &coreruntime.HookContext{
		ToolName:  "cli_execute",
		ToolInput: input,
	}); err != nil {
		t.Fatalf("BeforeToolExec: %v", err)
	}
	if err := hooks.Fire(ctx, coreruntime.AfterToolExec, &coreruntime.HookContext{
		ToolName:         "cli_execute",
		ToolInput:        input,
		ToolOutput:       output,
		ToolExecDuration: 10 * time.Millisecond,
	}); err != nil {
		t.Fatalf("AfterToolExec: %v", err)
	}

	var events []coreruntime.AuditEvent
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var ev coreruntime.AuditEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("unmarshal %q: %v", line, err)
		}
		events = append(events, ev)
	}
	if got := len(events); got != 2 {
		t.Fatalf("got %d events, want 2 (BeforeToolExec + AfterToolExec)", got)
	}
	return events
}

// TestRegisterAuditHooks_DefaultPostureOmitsCaptureFields is the
// schema-golden pin for the default-deploy case: with capture flags
// off, neither BeforeToolExec nor AfterToolExec event carries an
// `args` or `result` key. The metadata `args_size` / `result_size`
// keys MUST still land — operators can graph payload volume without
// capturing the payloads themselves. Issue #163 verification step 1.
func TestRegisterAuditHooks_DefaultPostureOmitsCaptureFields(t *testing.T) {
	events := fireToolExec(t,
		coreruntime.AuditPayloadCapture{}, // every flag off
		`{"cmd":"ls -la /tmp"}`,
		`drwxr-xr-x  ...`,
	)
	before, after := events[0], events[1]

	if _, ok := before.Fields["args"]; ok {
		t.Errorf("default posture leaked args field: %+v", before.Fields)
	}
	if _, ok := after.Fields["result"]; ok {
		t.Errorf("default posture leaked result field: %+v", after.Fields)
	}
	// Size metadata MUST be present so observability dashboards
	// still work without capture.
	if before.Fields["args_size"] == nil {
		t.Errorf("args_size missing in default posture: %+v", before.Fields)
	}
	if after.Fields["result_size"] == nil {
		t.Errorf("result_size missing in default posture: %+v", after.Fields)
	}
}

// TestRegisterAuditHooks_CaptureToolArgs_OnlyStartEventCarries is
// the FWS-8 rule: ToolArgs capture lands on the start event, never
// the end event (the input doesn't change between hooks; double-
// emitting would inflate the audit footprint for no value).
func TestRegisterAuditHooks_CaptureToolArgs_OnlyStartEventCarries(t *testing.T) {
	events := fireToolExec(t,
		coreruntime.AuditPayloadCapture{ToolArgs: true},
		`{"cmd":"ls"}`,
		`fileA fileB`,
	)
	before, after := events[0], events[1]

	if before.Fields["args"] != `{"cmd":"ls"}` {
		t.Errorf("BeforeToolExec args = %q, want %q",
			before.Fields["args"], `{"cmd":"ls"}`)
	}
	if _, ok := after.Fields["args"]; ok {
		t.Errorf("AfterToolExec MUST NOT carry args; got %+v", after.Fields)
	}
}

// TestRegisterAuditHooks_CaptureRedactsVendorTokens is the core
// issue #163 invariant manifesting at the hook layer: an LLM that
// glues an API key into a `cli_execute` command produces a
// tool_exec event with `[REDACTED]` in place of the secret, not
// the raw token. Verification step 3 of the issue.
func TestRegisterAuditHooks_CaptureRedactsVendorTokens(t *testing.T) {
	input := `{"cmd":"curl -H 'Authorization: Bearer sk-ant-aaaaaaaaaaaaaaaaaaaaaaaaaaaa' https://api"}`
	events := fireToolExec(t,
		coreruntime.AuditPayloadCapture{ToolArgs: true, Redact: true},
		input, "",
	)
	args, ok := events[0].Fields["args"].(string)
	if !ok {
		t.Fatalf("args field missing or not a string: %+v", events[0].Fields)
	}
	if strings.Contains(args, "sk-ant-aaa") {
		t.Errorf("vendor secret leaked into captured args: %q", args)
	}
	if !strings.Contains(args, "[REDACTED]") {
		t.Errorf("expected [REDACTED] in captured args; got %q", args)
	}
}

// TestRegisterAuditHooks_CaptureRedactFalseLeavesSecretsVerbatim is
// the documented escape hatch: REDACT=false (typically because a
// downstream sink has its own scrubber) keeps the raw token in the
// captured args. Verification step 4 of the issue.
func TestRegisterAuditHooks_CaptureRedactFalseLeavesSecretsVerbatim(t *testing.T) {
	input := `{"cmd":"echo sk-ant-aaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`
	events := fireToolExec(t,
		coreruntime.AuditPayloadCapture{ToolArgs: true, Redact: false},
		input, "",
	)
	args, _ := events[0].Fields["args"].(string)
	if !strings.Contains(args, "sk-ant-aaaaaaaaaaaaaaaaaaaaaaaaaaaa") {
		t.Errorf("Redact=false should leave raw token; got %q", args)
	}
}

// TestRegisterAuditHooks_CaptureToolResult_Truncates pins the
// large-output truncation behavior. A 100 KiB output captured at the
// default 16 KiB cap MUST end with `…[truncated:N]` carrying the
// original size — the audit stream NEVER blocks streaming because of
// a runaway tool output. Verification step 6 of the issue.
func TestRegisterAuditHooks_CaptureToolResult_Truncates(t *testing.T) {
	big := strings.Repeat("a", 100*1024)
	events := fireToolExec(t,
		coreruntime.AuditPayloadCapture{ToolResult: true, Redact: false},
		`{"cmd":"cat big"}`,
		big,
	)
	result, ok := events[1].Fields["result"].(string)
	if !ok {
		t.Fatalf("result field missing: %+v", events[1].Fields)
	}
	if len(result) >= len(big) {
		t.Errorf("result not truncated; in=%d out=%d", len(big), len(result))
	}
	if !strings.Contains(result, "…[truncated:") {
		t.Errorf("truncation marker missing: tail=%q", result[max(0, len(result)-40):])
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
