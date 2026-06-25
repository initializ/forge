package runtime

import (
	"context"
	"os"
	"strconv"

	"github.com/initializ/guardrails"

	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

// GuardrailAuditConfig controls how the LibraryGuardrailEngine emits
// guardrail_check audit events. The default zero value preserves the
// pre-#155 metadata-only posture: an emitted event carries direction,
// decision, guardrail type, and violation count, but never the raw
// content that triggered the rule.
//
// Operators who need the offending text (to tune patterns, debug
// false positives, or satisfy compliance evidence requirements) opt
// in by flipping CaptureEvidence to true. The Redact knob is on by
// default and runs an obvious-secret scrub even on the captured
// evidence, so a leaked API key in a prompt does not get re-published
// into the audit stream verbatim. MaxBytes bounds the captured
// substring per event; zero falls back to DefaultGuardrailEvidenceCapBytes.
//
// Same posture as the #130 OTel content-capture work: default off,
// opt-in per-deployment, redact-then-truncate when on.
type GuardrailAuditConfig struct {
	// CaptureEvidence includes the raw triggering content in the
	// emitted guardrail_check event's `fields.evidence`. OFF by default.
	CaptureEvidence bool

	// Redact runs a known-secret regex pass on the captured evidence
	// before truncation. ON by default. Disable only when consuming
	// in an environment that has its own scrubbing layer (e.g. a
	// platform-side SIEM normalizer).
	Redact bool

	// MaxBytes is the soft cap on the captured evidence string. Zero
	// uses DefaultGuardrailEvidenceCapBytes (4 KiB).
	MaxBytes int
}

// DefaultGuardrailEvidenceCapBytes is the per-event cap for captured
// evidence when GuardrailAuditConfig.MaxBytes is unset. 4 KiB matches
// the OTel span attribute soft cap so the same content travels through
// both pipelines under the same size envelope.
const DefaultGuardrailEvidenceCapBytes = 4 << 10

// Environment variable names mirror the existing audit/export pattern.
// The CLI surfaces these via run/serve flags or operators can set them
// directly on the agent process.
const (
	EnvGuardrailCaptureEvidence = "FORGE_GUARDRAIL_CAPTURE_EVIDENCE"
	EnvGuardrailRedact          = "FORGE_GUARDRAIL_REDACT"
	EnvGuardrailMaxBytes        = "FORGE_GUARDRAIL_MAX_BYTES"
)

// GuardrailAuditConfigFromEnv reads the env vars and returns a populated
// config. Redact defaults to true so flipping CaptureEvidence on without
// touching Redact preserves the safer posture.
func GuardrailAuditConfigFromEnv() GuardrailAuditConfig {
	cfg := GuardrailAuditConfig{Redact: true}
	if v := os.Getenv(EnvGuardrailCaptureEvidence); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.CaptureEvidence = b
		}
	}
	if v := os.Getenv(EnvGuardrailRedact); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.Redact = b
		}
	}
	if v := os.Getenv(EnvGuardrailMaxBytes); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.MaxBytes = n
		}
	}
	return cfg
}

// prepareEvidence applies redact (if on) then byte-truncates to the
// configured cap, delegating to the shared content-capture pipeline
// (coreruntime.PrepareCapturedContent) so the vendor-secret regex set
// and the truncation marker stay in lockstep with the FWS-8 payload-
// capture path and the #130 OTel content path. Pre-#163 this function
// carried its own copy of the redact regex set — see the issue for
// the consolidation rationale.
//
// Returns "" when input is "" so callers can drop the field cleanly.
func prepareEvidence(s string, cfg GuardrailAuditConfig) string {
	if s == "" {
		return ""
	}
	cap := cfg.MaxBytes
	if cap <= 0 {
		cap = DefaultGuardrailEvidenceCapBytes
	}
	return coreruntime.PrepareCapturedContent(s, cfg.Redact, cap)
}

// emitGuardrailEvent builds and emits a guardrail_check audit event
// for one mask/block/warn decision. Routed through EmitFromContext so
// the per-invocation correlation_id, task_id, sequence number,
// tenancy, and workflow tags auto-attach from the request context.
//
// The fields.gate value comes directly from res.Gate — the library's
// own classification (input / context / tool_call / output / stream).
// This replaces the older direction field (issue #155 / #156) per
// issue #159's unified-gate decision. Operators consuming audit
// streams from agents pre-#159 should map the old `direction` field
// to `gate` via the documented fallback table.
//
// Behavior matrix:
//
//   - audit logger nil → no-op (DB mode with platform-side audit
//     only, or unit tests with no logger wired)
//   - res nil          → no-op (defensive)
//   - CaptureEvidence on AND content non-empty → fields.evidence is
//     set (redacted + truncated per cfg)
//   - CaptureEvidence off → fields.evidence omitted entirely
//
// `tool` is set on the event when present (tool_call + tool_output
// paths), so SIEM consumers can distinguish output-gate fires on a
// tool result from output-gate fires on the model's response to the
// user (same gate value, different `tool` cardinality).
func (e *LibraryGuardrailEngine) emitGuardrailEvent(
	ctx context.Context,
	tool, content string,
	decision string,
	res *guardrails.Result,
) {
	if e.auditLogger == nil || res == nil {
		return
	}
	fields := map[string]any{
		"gate":            string(res.Gate),
		"decision":        decision,
		"violation_count": len(res.Violations),
	}
	if len(res.Violations) > 0 {
		fields["guardrail"] = res.Violations[0].Type
		if cat := res.Violations[0].Category; cat != "" {
			fields["category"] = cat
		}
	} else {
		fields["guardrail"] = "none"
	}
	if tool != "" {
		fields["tool"] = tool
	}
	if e.auditCfg.CaptureEvidence {
		if ev := prepareEvidence(content, e.auditCfg); ev != "" {
			fields["evidence"] = ev
		}
	}
	e.auditLogger.EmitFromContext(ctx, coreruntime.AuditEvent{
		Event:  coreruntime.AuditGuardrail,
		Fields: fields,
	})
}
