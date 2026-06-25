package runtime

import (
	"os"
	"strconv"
)

// AuditPayloadCapture controls whether the audit pipeline emits raw
// LLM prompt / completion text and raw tool args / results in audit
// events. Every capture flag defaults to false; the default audit
// posture is "metadata only" (size + type + token counts + duration).
// This matches the security commitment baked into the audit emission
// sites today and codified by FWS-8 (issue #91).
//
// Customers who need raw payloads in audit (debugging, replay,
// supervised-learning corpora) opt in field by field, NEVER globally.
// The cap fields bound per-event byte size so a 1MB prompt doesn't
// turn one audit event into a memory-hostile record — the captured
// substring is the first CapXxxBytes bytes followed by a truncation
// marker `…[truncated:N]`.
//
// Capture flags + caps are read by the runner's hook-registered audit
// emitters (registerAuditHooks). The Sink layer is unaware of capture
// settings — it just emits what the AuditEvent says.
//
// THIS IS A SECURITY-RELEVANT CONFIGURATION. Operators who enable
// any capture flag should keep Redact on (the default) so vendor
// secrets that an LLM glues into a `cli_execute` command (a real
// failure mode — the model remembers a token from the prompt and
// inlines it into a shell invocation) are scrubbed before the event
// lands on the audit stream. Set Redact=false ONLY when a downstream
// sink runs its own scrubber. See PrepareCapturedContent in
// content_redact.go.
//
// Beyond redact: operators must also ensure the audit transport (the
// FWS-7 sink or the stderr safety net) lands in a store that respects
// the captured payloads' sensitivity (PII, prompts, etc.).
type AuditPayloadCapture struct {
	// LLMMessages controls whether each `llm_call` event carries the
	// list of inbound chat messages (role + content) the agent sent
	// to the model. Off by default.
	LLMMessages bool
	// LLMResponse controls whether `llm_call` carries the model's
	// completion text. Off by default.
	LLMResponse bool
	// ToolArgs controls whether `tool_exec` carries the raw input
	// the agent passed to the tool. Off by default.
	ToolArgs bool
	// ToolResult controls whether `tool_exec` carries the raw
	// output the tool returned. Off by default.
	ToolResult bool

	// Redact runs the vendor-secret regex scrub
	// (content_redact.go:RedactSecrets) on each captured field
	// before truncation. The struct's zero value has Redact=false
	// purely as a Go-zero-value artifact — operators MUST go through
	// AuditPayloadCaptureFromEnv (which defaults Redact=true) or
	// explicitly set the field. The runner's resolver enforces this
	// by always passing through FromEnv before applying any
	// forge.yaml overrides.
	//
	// Disabling redact is a deliberate escape hatch: operators whose
	// downstream sink has its own scrubber and who explicitly want to
	// see the raw bytes for tuning. See issue #163.
	Redact bool

	// CapLLMMessagesBytes is the max bytes serialized for the
	// captured chat messages array. 0 = use the package default
	// (DefaultPayloadCaptureCapBytes).
	CapLLMMessagesBytes int
	// CapLLMResponseBytes — same shape, for completion text.
	CapLLMResponseBytes int
	// CapToolArgsBytes — same shape, for tool input.
	CapToolArgsBytes int
	// CapToolResultBytes — same shape, for tool output.
	CapToolResultBytes int
}

// DefaultPayloadCaptureCapBytes is the per-field byte cap when the
// caller doesn't override. 16 KiB matches the runtime's
// "long-tool-output" threshold (the same threshold the chat-side path
// uses to switch to a file part), so audit captures roughly align
// with what's visible in the chat UI.
const DefaultPayloadCaptureCapBytes = 16 << 10

// AnyEnabled reports whether at least one capture flag is on. The
// runner skips the hook overhead entirely when nothing is enabled.
func (c AuditPayloadCapture) AnyEnabled() bool {
	return c.LLMMessages || c.LLMResponse || c.ToolArgs || c.ToolResult
}

// CapOrDefault picks the configured cap for the field, falling back
// to the package default when zero. Negative values are clamped to
// the default; "0 means no capture" is what AnyEnabled / the per-
// field flag covers — once a flag is on, some cap applies. Exported
// so the runner's hook layer can pick the right cap per field
// without duplicating the fallback logic.
func CapOrDefault(configured int) int {
	if configured <= 0 {
		return DefaultPayloadCaptureCapBytes
	}
	return configured
}

// TruncateForAudit returns s truncated to at most max bytes; if s
// exceeded the cap, the returned string ends with the suffix
// `…[truncated:N]` where N is the original byte length. Use for
// every captured field so a runaway prompt can't bloat one event.
//
// The function operates on bytes, not runes — UTF-8 sequences may be
// split mid-codepoint at the truncation boundary. Audit consumers
// must treat captured strings as opaque bytes, not as user-renderable
// text. The size info in the field name (`prompt_messages_size_bytes`
// vs `prompt_messages`) is the contract.
func TruncateForAudit(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	// Reserve room for the truncation marker.
	const marker = "…[truncated:"
	const tail = "]"
	// We need len(prefix) + len(marker) + len(decimal(orig)) + len(tail) <= max.
	// In practice the marker overhead is < 32 bytes; clamp the prefix
	// generously to keep the math simple.
	prefixCap := max - len(marker) - len(tail) - 12 // 12 digits is enough for any int64
	if prefixCap < 0 {
		prefixCap = 0
	}
	if prefixCap > len(s) {
		prefixCap = len(s)
	}
	return s[:prefixCap] + marker + itoa(len(s)) + tail
}

// Environment variable names for AuditPayloadCaptureFromEnv. Mirrors
// the AuditExportConfig / GuardrailAuditConfig env naming so operators
// see one consistent `FORGE_AUDIT_*` namespace across the audit
// subsystem. See issue #163.
const (
	EnvAuditCaptureToolArgs    = "FORGE_AUDIT_CAPTURE_TOOL_ARGS"
	EnvAuditCaptureToolResult  = "FORGE_AUDIT_CAPTURE_TOOL_RESULT"
	EnvAuditCaptureLLMMessages = "FORGE_AUDIT_CAPTURE_LLM_MESSAGES"
	EnvAuditCaptureLLMResponse = "FORGE_AUDIT_CAPTURE_LLM_RESPONSE"
	EnvAuditCaptureRedact      = "FORGE_AUDIT_CAPTURE_REDACT"
	EnvAuditCaptureMaxBytes    = "FORGE_AUDIT_CAPTURE_MAX_BYTES"
)

// AuditPayloadCaptureFromEnv reads the FORGE_AUDIT_CAPTURE_* env vars
// and returns a populated config. Redact defaults to TRUE so flipping
// any capture flag on without touching Redact keeps the safer posture
// — the same default the GuardrailAuditConfig.Redact uses for
// guardrail evidence and the OTel tracing CaptureContent path uses
// for span content. See issue #163 for the consolidation rationale.
//
// FORGE_AUDIT_CAPTURE_MAX_BYTES is the single-knob per-field cap: when
// set it populates all four CapXxxBytes fields uniformly. Operators
// who need different caps per field set the struct directly (Forge
// embedded as a library) — env-var coverage is intentionally
// single-knob to keep the operator surface small.
//
// Parse failures (non-boolean strings for the flags, non-integer for
// the cap) fall through to defaults. Same forgiving posture as the
// other Audit*FromEnv constructors.
func AuditPayloadCaptureFromEnv() AuditPayloadCapture {
	cfg := AuditPayloadCapture{Redact: true}
	if v := os.Getenv(EnvAuditCaptureToolArgs); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.ToolArgs = b
		}
	}
	if v := os.Getenv(EnvAuditCaptureToolResult); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.ToolResult = b
		}
	}
	if v := os.Getenv(EnvAuditCaptureLLMMessages); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.LLMMessages = b
		}
	}
	if v := os.Getenv(EnvAuditCaptureLLMResponse); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.LLMResponse = b
		}
	}
	if v := os.Getenv(EnvAuditCaptureRedact); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.Redact = b
		}
	}
	if v := os.Getenv(EnvAuditCaptureMaxBytes); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.CapLLMMessagesBytes = n
			cfg.CapLLMResponseBytes = n
			cfg.CapToolArgsBytes = n
			cfg.CapToolResultBytes = n
		}
	}
	return cfg
}

// itoa avoids fmt.Sprintf("%d", n) so TruncateForAudit stays tight.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
