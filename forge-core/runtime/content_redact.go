package runtime

import (
	"encoding/json"
	"regexp"

	"github.com/initializ/forge/forge-core/llm"
)

// Span-attribute content capture (issue #130 / Phase 3.5).
//
// Phase 3 of the OTel Tracing v1 initiative (#108, PR #125) shipped
// span instrumentation across the executor loop and tool calls but
// kept it metadata-only — span attributes carried provider, model,
// usage tokens, finish reasons, but no prompt / completion / tool I/O
// text. Phase 2 (#103, PR #124) plumbed two operator-facing knobs
// (`capture_content`, `redact`) through the config schema but the
// runtime never read them. This file is the redact-and-cap pipeline
// that Phase 3 sites call into when `CaptureContent=true` so the same
// PII / secret scrub passes both the OTel attribute path and (in the
// future) the audit payload-capture path.
//
// Pattern parity: RedactSecrets's regex list mirrors the runtime
// guardrails CustomRule defaults in forge-cli/runtime/guardrails_loader.go's
// DefaultStructuredGuardrails. The two should evolve together — when
// a new secret token shape is added to the guardrails list, add it
// here. The parity test in content_redact_parity_test.go inside
// forge-cli/runtime/ enforces this at CI time.
//
// Order matters: redact runs BEFORE truncate so the truncation
// boundary can never split a `[REDACTED]` marker mid-string.
//
// The functions are designed to be called on hot paths
// (every LLM call, every tool call) so the regex set is pre-compiled
// at package init and the empty-input fast path skips the pattern
// loop entirely.

// RedactionMarker is the placeholder substituted for any matched
// secret. Operators grepping audit logs and traces for "[REDACTED]"
// can correlate scrub events across both pipelines.
const RedactionMarker = "[REDACTED]"

// DefaultSpanContentCapBytes is the per-attribute byte cap for span
// content. 4 KiB stays comfortably under common observability backend
// limits (Datadog caps attributes around 5 KiB; Tempo's default attr
// length limit is 4 KiB) so a long prompt doesn't get re-truncated by
// the backend with a different marker shape, breaking the
// correlate-by-marker grep flow.
const DefaultSpanContentCapBytes = 4 << 10

// redactPattern is a single regex applied to span / audit content
// before storage. Each entry's regex is pre-compiled at init.
type redactPattern struct {
	name string
	re   *regexp.Regexp
}

// redactPatterns covers token shapes operators have asked us to scrub
// from prompts / completions / tool I/O. The shapes are drawn from
// runtime-observed secrets in vendor SDKs — same list as the
// guardrails CustomRules defaults. See the package-doc note above on
// parity with forge-cli/runtime/guardrails_loader.go.
var redactPatterns = []redactPattern{
	{name: "anthropic_key", re: regexp.MustCompile(`sk-ant-[A-Za-z0-9\-]{20,}`)},
	{name: "openai_key", re: regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`)},
	{name: "github_pat", re: regexp.MustCompile(`ghp_[A-Za-z0-9]{36}`)},
	{name: "github_oauth", re: regexp.MustCompile(`gho_[A-Za-z0-9]{36}`)},
	{name: "github_server", re: regexp.MustCompile(`ghs_[A-Za-z0-9]{36}`)},
	{name: "github_fine", re: regexp.MustCompile(`github_pat_[A-Za-z0-9_]{22,}`)},
	{name: "aws_access", re: regexp.MustCompile(`AKIA[0-9A-Z]{16}`)},
	{name: "slack_bot", re: regexp.MustCompile(`xoxb-[0-9]{10,}-[A-Za-z0-9-]+`)},
	{name: "slack_user", re: regexp.MustCompile(`xoxp-[0-9]{10,}-[A-Za-z0-9-]+`)},
	// Private-key block: anchored to both BEGIN and END markers so we
	// scrub the entire payload at once. (?s) makes . match newlines.
	{name: "private_key", re: regexp.MustCompile(`(?s)-----BEGIN (RSA|EC|OPENSSH|PRIVATE) [^-]*KEY-----.*?-----END (RSA|EC|OPENSSH|PRIVATE) [^-]*KEY-----`)},
	{name: "telegram_bot", re: regexp.MustCompile(`[0-9]{8,10}:[A-Za-z0-9_-]{35,}`)},
}

// RedactSecrets returns s with every known secret token shape replaced
// by RedactionMarker. Empty input is returned unchanged (fast path).
//
// Applied in pattern-list order; overlap is fine because
// ReplaceAllString rewrites the string left-to-right and subsequent
// patterns operate on the post-replacement output. A run that matches
// multiple shapes (e.g. an `sk-` prefix that also starts a longer
// vendor key) is scrubbed once — RedactionMarker doesn't satisfy any
// other pattern, so re-applying patterns is idempotent.
func RedactSecrets(s string) string {
	if s == "" {
		return s
	}
	for _, p := range redactPatterns {
		s = p.re.ReplaceAllString(s, RedactionMarker)
	}
	return s
}

// serializeChatMessages JSON-encodes the inbound chat messages list
// for use as the gen_ai.prompt span attribute (OTel GenAI semantic
// conventions). Returns the empty string for nil / empty input or on
// marshal failure — an empty return signals the caller to skip
// stamping the attribute, preserving the "absent attribute = no
// opt-in" contract.
//
// Lives next to PrepareSpanContent because both are pure
// content-shaping helpers for the span-capture pipeline; the audit
// pipeline uses the same input but emits it as native event fields,
// not a JSON blob.
func serializeChatMessages(messages []llm.ChatMessage) string {
	if len(messages) == 0 {
		return ""
	}
	b, err := json.Marshal(messages)
	if err != nil {
		return ""
	}
	return string(b)
}

// PrepareCapturedContent is the shared redact-then-truncate pipeline
// for any content the runtime captures into a long-lived artifact —
// audit events (FWS-8 payload capture), OTel span attributes (#130
// content capture), guardrail evidence (#155 / #156). All three call
// sites previously open-coded a near-identical redact + truncate flow
// with three independent copies of the vendor-secret regex set; this
// helper consolidates them onto one implementation so a fix to the
// regex set propagates to every capture path (issue #163).
//
// Pipeline:
//
//  1. Empty input is returned unchanged (fast path; callers can use
//     the empty return as the signal to drop the field cleanly).
//  2. If redact=true, RedactSecrets scrubs known vendor token shapes.
//  3. maxBytes <= 0 falls back to DefaultPayloadCaptureCapBytes
//     (16 KiB). Each call site that wants a different default (the
//     span path uses DefaultSpanContentCapBytes=4 KiB; the guardrail
//     evidence path uses DefaultGuardrailEvidenceCapBytes=4 KiB) MUST
//     pass its own explicit cap.
//  4. TruncateForAudit applies the byte cap and writes the
//     `…[truncated:N]` marker the audit and OTel paths share.
//
// Order matters: redact runs BEFORE truncate so the truncation
// boundary can never split a `[REDACTED]` marker mid-string.
func PrepareCapturedContent(s string, redact bool, maxBytes int) string {
	if s == "" {
		return s
	}
	if redact {
		s = RedactSecrets(s)
	}
	if maxBytes <= 0 {
		maxBytes = DefaultPayloadCaptureCapBytes
	}
	return TruncateForAudit(s, maxBytes)
}

// PrepareSpanContent is the OTel-span-attribute-specific adapter over
// PrepareCapturedContent. It applies the span-attribute default cap
// (DefaultSpanContentCapBytes, 4 KiB — comfortably under common
// observability backend per-attribute limits) when the caller passes
// maxBytes <= 0. Behavior is otherwise identical to
// PrepareCapturedContent and the truncation marker is shared so an
// operator correlating an audit `…[truncated:N]` substring with the
// linked span attribute sees the same suffix on both.
//
// Issue #130 shipped this helper; issue #163 collapses it onto
// PrepareCapturedContent so all three content-capture paths share
// one implementation.
func PrepareSpanContent(s string, redact bool, maxBytes int) string {
	if maxBytes <= 0 {
		maxBytes = DefaultSpanContentCapBytes
	}
	return PrepareCapturedContent(s, redact, maxBytes)
}
