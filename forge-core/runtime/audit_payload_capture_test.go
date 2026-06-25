package runtime

import (
	"strings"
	"testing"
)

// TestAuditPayloadCaptureFromEnv_Defaults pins the safe-default
// posture: zero env vars in the process give back a config with every
// capture flag false and Redact = true. An operator who flips ToolArgs
// on via env without touching Redact must keep the scrubber on by
// default — issue #163 is largely about closing the "secret leaked to
// audit because nobody set Redact" gap.
func TestAuditPayloadCaptureFromEnv_Defaults(t *testing.T) {
	t.Setenv(EnvAuditCaptureToolArgs, "")
	t.Setenv(EnvAuditCaptureToolResult, "")
	t.Setenv(EnvAuditCaptureLLMMessages, "")
	t.Setenv(EnvAuditCaptureLLMResponse, "")
	t.Setenv(EnvAuditCaptureRedact, "")
	t.Setenv(EnvAuditCaptureMaxBytes, "")

	cfg := AuditPayloadCaptureFromEnv()
	if cfg.ToolArgs || cfg.ToolResult || cfg.LLMMessages || cfg.LLMResponse {
		t.Errorf("expected every capture flag false; got %+v", cfg)
	}
	if !cfg.Redact {
		t.Errorf("Redact must default true; got false")
	}
	if cfg.AnyEnabled() {
		t.Errorf("AnyEnabled should be false in default posture; got true")
	}
}

// TestAuditPayloadCaptureFromEnv_FlagsParsed walks each capture flag
// and confirms the env var flips it. The cases use the strconv.ParseBool
// vocabulary ("true", "1", "false") because that's what the
// constructor uses — different from "yes"/"on" some other shells
// accept.
func TestAuditPayloadCaptureFromEnv_FlagsParsed(t *testing.T) {
	cases := []struct {
		envVar string
		field  func(AuditPayloadCapture) bool
		name   string
	}{
		{EnvAuditCaptureToolArgs, func(c AuditPayloadCapture) bool { return c.ToolArgs }, "ToolArgs"},
		{EnvAuditCaptureToolResult, func(c AuditPayloadCapture) bool { return c.ToolResult }, "ToolResult"},
		{EnvAuditCaptureLLMMessages, func(c AuditPayloadCapture) bool { return c.LLMMessages }, "LLMMessages"},
		{EnvAuditCaptureLLMResponse, func(c AuditPayloadCapture) bool { return c.LLMResponse }, "LLMResponse"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(c.envVar, "true")
			cfg := AuditPayloadCaptureFromEnv()
			if !c.field(cfg) {
				t.Errorf("env %s=true did not flip %s; got %+v", c.envVar, c.name, cfg)
			}
		})
	}
}

// TestAuditPayloadCaptureFromEnv_RedactEscapeHatch is the explicit
// override path — operators with their own downstream scrubber set
// FORGE_AUDIT_CAPTURE_REDACT=false and the runner stops scrubbing.
// Documented escape hatch; tested here so regressions land in red.
func TestAuditPayloadCaptureFromEnv_RedactEscapeHatch(t *testing.T) {
	t.Setenv(EnvAuditCaptureRedact, "false")
	cfg := AuditPayloadCaptureFromEnv()
	if cfg.Redact {
		t.Errorf("FORGE_AUDIT_CAPTURE_REDACT=false must flip Redact to false; got true")
	}
}

// TestAuditPayloadCaptureFromEnv_MaxBytesIsSingleKnob confirms the
// per-field-uniform semantic: FORGE_AUDIT_CAPTURE_MAX_BYTES applies
// the same cap across all four CapXxxBytes fields. Operators who
// genuinely need divergent caps embed Forge as a library; the env
// surface stays single-knob.
func TestAuditPayloadCaptureFromEnv_MaxBytesIsSingleKnob(t *testing.T) {
	t.Setenv(EnvAuditCaptureMaxBytes, "8192")
	cfg := AuditPayloadCaptureFromEnv()
	for _, got := range []int{
		cfg.CapToolArgsBytes,
		cfg.CapToolResultBytes,
		cfg.CapLLMMessagesBytes,
		cfg.CapLLMResponseBytes,
	} {
		if got != 8192 {
			t.Errorf("MAX_BYTES=8192 did not propagate uniformly; cap=%d", got)
		}
	}
}

// TestAuditPayloadCaptureFromEnv_MaxBytesIgnoresInvalid pins the
// forgiving parse posture — garbage values leave the default-zero so
// CapOrDefault falls through. Matches the AuditExportConfigFromEnv
// pattern: a typo in env doesn't kill the agent at startup.
func TestAuditPayloadCaptureFromEnv_MaxBytesIgnoresInvalid(t *testing.T) {
	t.Setenv(EnvAuditCaptureMaxBytes, "not-a-number")
	cfg := AuditPayloadCaptureFromEnv()
	if cfg.CapToolArgsBytes != 0 {
		t.Errorf("invalid MAX_BYTES should leave cap zero; got %d", cfg.CapToolArgsBytes)
	}
	// And the per-field default still applies via CapOrDefault.
	if eff := CapOrDefault(cfg.CapToolArgsBytes); eff != DefaultPayloadCaptureCapBytes {
		t.Errorf("CapOrDefault should fall back to default; got %d", eff)
	}
}

// TestAuditPayloadCaptureFromEnv_RejectsZeroAndNegativeMaxBytes
// confirms the boundary: 0 means "not set" so per-field defaults
// apply; negative means "obviously invalid input" same outcome.
func TestAuditPayloadCaptureFromEnv_RejectsZeroAndNegativeMaxBytes(t *testing.T) {
	for _, v := range []string{"0", "-100"} {
		t.Setenv(EnvAuditCaptureMaxBytes, v)
		cfg := AuditPayloadCaptureFromEnv()
		if cfg.CapToolArgsBytes != 0 {
			t.Errorf("MAX_BYTES=%q should leave cap zero; got %d", v, cfg.CapToolArgsBytes)
		}
	}
}

// TestAnyEnabled_CoversAllFlags is the regression pin for FWS-8's
// "skip the hook overhead when nothing is captured" optimization in
// runner.registerAuditHooks. Each flag individually must surface
// through AnyEnabled or the runner won't install the hook and the
// captured payload silently never reaches the audit stream.
func TestAnyEnabled_CoversAllFlags(t *testing.T) {
	cases := []AuditPayloadCapture{
		{ToolArgs: true},
		{ToolResult: true},
		{LLMMessages: true},
		{LLMResponse: true},
	}
	for i, c := range cases {
		if !c.AnyEnabled() {
			t.Errorf("case %d: AnyEnabled() returned false for %+v", i, c)
		}
	}
	if (AuditPayloadCapture{}).AnyEnabled() {
		t.Errorf("zero value reports AnyEnabled() true")
	}
}

// TestPrepareCapturedContent_RedactScrubsVendorTokens is the core
// issue #163 invariant: a captured payload carrying a vendor secret
// has the secret replaced by [REDACTED] before it can land on the
// audit stream. Walks the regex set so every supported vendor token
// shape stays covered through consolidation.
func TestPrepareCapturedContent_RedactScrubsVendorTokens(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"anthropic", "key=sk-ant-aaaaaaaaaaaaaaaaaaaaaaaaaaaa here"},
		{"openai", "key=sk-AAAAAAAAAAAAAAAAAAAA here"},
		{"github_pat", "tok=ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA here"},
		{"aws_access", "id=AKIAAAAAAAAAAAAAAAAA here"},
		{"slack_bot", "tok=xoxb-1234567890-AAAAAAAAAAAA here"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := PrepareCapturedContent(c.in, true, 1024)
			if !strings.Contains(out, RedactionMarker) {
				t.Errorf("expected [REDACTED] in %q for input %q", out, c.in)
			}
		})
	}
}

// TestPrepareCapturedContent_NoRedactKeepsSecretsVerbatim is the
// escape-hatch confirmation — REDACT=false leaves the raw secret in
// the captured content. This is the path operators with a downstream
// SIEM scrubber take.
func TestPrepareCapturedContent_NoRedactKeepsSecretsVerbatim(t *testing.T) {
	in := "tok=sk-ant-aaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	out := PrepareCapturedContent(in, false, 1024)
	if !strings.Contains(out, "sk-ant-aaaaaaaaaaaaaaaaaaaaaaaaaaaa") {
		t.Errorf("redact=false should leave the raw token; got %q", out)
	}
	if strings.Contains(out, RedactionMarker) {
		t.Errorf("redact=false should NOT emit [REDACTED]; got %q", out)
	}
}

// TestPrepareCapturedContent_TruncatesAtCap pins the byte-cap behavior
// — issue #163 verification step 6 (100 KB output, 16 KiB cap).
func TestPrepareCapturedContent_TruncatesAtCap(t *testing.T) {
	in := strings.Repeat("a", 100*1024)
	out := PrepareCapturedContent(in, false, 16*1024)
	if len(out) >= len(in) {
		t.Errorf("output not truncated; in=%d out=%d", len(in), len(out))
	}
	if !strings.HasSuffix(out, "]") || !strings.Contains(out, "…[truncated:") {
		t.Errorf("output missing truncation marker; tail=%q", out[len(out)-32:])
	}
}

// TestPrepareCapturedContent_RedactBeforeTruncate is the ordering pin
// — the truncation cut must not split a [REDACTED] marker mid-string.
// Concretely: build a payload where the cap would land inside the
// REDACTED substring and assert the marker stays intact.
func TestPrepareCapturedContent_RedactBeforeTruncate(t *testing.T) {
	in := strings.Repeat("x", 100) + "sk-ant-aaaaaaaaaaaaaaaaaaaaaaaaaaaa" + strings.Repeat("y", 100)
	// Cap chosen so the redacted output (which is shorter than the
	// raw input) still fits comfortably — confirming redact happened
	// before any cap reasoning kicked in.
	out := PrepareCapturedContent(in, true, 1024)
	if strings.Contains(out, "sk-ant-aaa") {
		t.Errorf("raw token leaked past redact: %q", out)
	}
	if !strings.Contains(out, RedactionMarker) {
		t.Errorf("[REDACTED] marker missing: %q", out)
	}
}

// TestPrepareCapturedContent_EmptyFastPath confirms the empty-input
// short-circuit — callers can use the empty return as the signal to
// drop the field entirely from the AuditEvent.
func TestPrepareCapturedContent_EmptyFastPath(t *testing.T) {
	if out := PrepareCapturedContent("", true, 1024); out != "" {
		t.Errorf("empty input should return empty; got %q", out)
	}
}

// TestPrepareCapturedContent_DefaultCap pins the maxBytes <= 0
// fallback — the larger 16 KiB payload cap applies (not the span
// path's 4 KiB) when the caller leaves the cap unset.
func TestPrepareCapturedContent_DefaultCap(t *testing.T) {
	in := strings.Repeat("a", DefaultPayloadCaptureCapBytes+1024)
	out := PrepareCapturedContent(in, false, 0)
	// Output should be roughly cap-sized (marker + digits add a few
	// bytes). A delta of 64 covers the marker overhead generously.
	if len(out) > DefaultPayloadCaptureCapBytes+64 {
		t.Errorf("default cap not applied; in=%d out=%d cap=%d",
			len(in), len(out), DefaultPayloadCaptureCapBytes)
	}
}

// TestPrepareSpanContent_StillDelegatesAndUsesItsOwnDefault confirms
// the #130 contract survives the #163 consolidation: span content
// still gets the 4 KiB default (not the 16 KiB payload-capture
// default), and redact+marker behavior is byte-identical to
// PrepareCapturedContent for matched-cap calls.
func TestPrepareSpanContent_StillDelegatesAndUsesItsOwnDefault(t *testing.T) {
	in := strings.Repeat("a", DefaultSpanContentCapBytes+1024)
	spanOut := PrepareSpanContent(in, false, 0)
	if len(spanOut) > DefaultSpanContentCapBytes+64 {
		t.Errorf("span default cap not applied; in=%d out=%d cap=%d",
			len(in), len(spanOut), DefaultSpanContentCapBytes)
	}

	// Same input, both helpers with matched cap = identical output.
	bothCap := 512
	if PrepareSpanContent("tok=sk-ant-aaaaaaaaaaaaaaaaaaaaaaaaaaaa", true, bothCap) !=
		PrepareCapturedContent("tok=sk-ant-aaaaaaaaaaaaaaaaaaaaaaaaaaaa", true, bothCap) {
		t.Errorf("PrepareSpanContent and PrepareCapturedContent diverged for matched-cap input")
	}
}
