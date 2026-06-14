package runtime

import (
	"context"
	"os"
	"regexp"
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

// secretRedactPatterns are the vendor token shapes scrubbed when
// GuardrailAuditConfig.Redact is on. Same set as the OTel content
// redaction pass (issue #130) so the audit and trace pipelines stay
// consistent. Defence-in-depth only: the guardrail library may already
// have masked these, but an unmasked input that hit a different rule
// (e.g. moderation) would otherwise carry secrets through verbatim.
var secretRedactPatterns = []*regexp.Regexp{
	regexp.MustCompile(`sk-ant-[A-Za-z0-9\-]{20,}`),
	regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`ghp_[A-Za-z0-9]{36}`),
	regexp.MustCompile(`gho_[A-Za-z0-9]{36}`),
	regexp.MustCompile(`ghs_[A-Za-z0-9]{36}`),
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{22,}`),
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
	regexp.MustCompile(`xox[bp]-[0-9]{10,}-[A-Za-z0-9-]+`),
	regexp.MustCompile(`-----BEGIN (RSA|EC|OPENSSH|PRIVATE) .*KEY-----`),
	regexp.MustCompile(`[0-9]{8,10}:[A-Za-z0-9_-]{35,}`),
}

// redactSecrets replaces any known secret-token shape with [REDACTED].
// Mirrors the marker used by the FWS-8 capture path so audit consumers
// see one consistent token across both pipelines.
func redactSecrets(s string) string {
	for _, re := range secretRedactPatterns {
		s = re.ReplaceAllString(s, "[REDACTED]")
	}
	return s
}

// prepareEvidence applies redact (if on) then byte-truncates to the
// configured cap. Returns "" when input is "" so callers can drop the
// field cleanly.
func prepareEvidence(s string, cfg GuardrailAuditConfig) string {
	if s == "" {
		return ""
	}
	if cfg.Redact {
		s = redactSecrets(s)
	}
	cap := cfg.MaxBytes
	if cap <= 0 {
		cap = DefaultGuardrailEvidenceCapBytes
	}
	return coreruntime.TruncateForAudit(s, cap)
}

// emitGuardrailEvent builds and emits a guardrail_check audit event for
// one mask/block/warn decision. Routed through EmitFromContext so the
// per-invocation correlation_id, task_id, sequence number, and workflow
// tags auto-attach from the request context.
//
// Behavior matrix:
//
//   - audit logger nil → no-op (DB mode with platform-side audit only,
//     or unit tests with no logger wired)
//   - res nil          → no-op (defensive; emit only when we have a
//     guardrail Result to summarize)
//   - CaptureEvidence on AND content non-empty → fields.evidence is
//     set (redacted + truncated per cfg)
//   - CaptureEvidence off → fields.evidence omitted entirely
func (e *LibraryGuardrailEngine) emitGuardrailEvent(
	ctx context.Context,
	direction, tool, content string,
	decision string,
	res *guardrails.Result,
) {
	if e.auditLogger == nil || res == nil {
		return
	}
	fields := map[string]any{
		"direction":       direction,
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
