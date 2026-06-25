package runtime

import (
	coreruntime "github.com/initializ/forge/forge-core/runtime"
	"github.com/initializ/forge/forge-core/types"
)

// ResolveAuditPayloadCapture merges a forge.yaml `audit.capture` block
// on top of an env-derived `AuditPayloadCapture` and returns the
// effective config the runner should hand to registerAuditHooks.
//
// Precedence (high → low):
//
//  1. forge.yaml `audit.capture.*` — any non-nil bool / non-zero int
//     wins over the env layer below.
//  2. Env vars `FORGE_AUDIT_CAPTURE_*` — already baked into the env
//     parameter via AuditPayloadCaptureFromEnv.
//  3. Zero / safe defaults — every capture flag false, Redact true.
//
// Why `*bool` in the yaml layer: an operator who writes
// `tool_args: false` in forge.yaml is making an explicit choice, not
// "fall through to env." Booleans need a nullable representation to
// preserve that distinction; ints get the same treatment via 0=unset
// for MaxBytes.
//
// MaxBytes when set populates all four CapXxxBytes fields uniformly
// (matching the env-layer single-knob semantic). Operators who need
// different caps per field embed Forge as a library and set
// AuditPayloadCapture programmatically; per-field env / yaml knobs
// would inflate the operator surface without clear demand. See
// issue #163.
func ResolveAuditPayloadCapture(env coreruntime.AuditPayloadCapture, yaml types.AuditCaptureConfig) coreruntime.AuditPayloadCapture {
	out := env
	if yaml.ToolArgs != nil {
		out.ToolArgs = *yaml.ToolArgs
	}
	if yaml.ToolResult != nil {
		out.ToolResult = *yaml.ToolResult
	}
	if yaml.LLMMessages != nil {
		out.LLMMessages = *yaml.LLMMessages
	}
	if yaml.LLMResponse != nil {
		out.LLMResponse = *yaml.LLMResponse
	}
	if yaml.Redact != nil {
		out.Redact = *yaml.Redact
	}
	if yaml.MaxBytes > 0 {
		out.CapLLMMessagesBytes = yaml.MaxBytes
		out.CapLLMResponseBytes = yaml.MaxBytes
		out.CapToolArgsBytes = yaml.MaxBytes
		out.CapToolResultBytes = yaml.MaxBytes
	}
	return out
}
