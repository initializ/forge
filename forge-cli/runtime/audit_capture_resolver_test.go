package runtime

import (
	"testing"

	coreruntime "github.com/initializ/forge/forge-core/runtime"
	"github.com/initializ/forge/forge-core/types"
)

func boolPtr(b bool) *bool { return &b }

// TestResolveAuditPayloadCapture_EnvOnly is the baseline: with no
// forge.yaml block, the env-derived config flows through unchanged.
// The default Redact=true from AuditPayloadCaptureFromEnv survives.
func TestResolveAuditPayloadCapture_EnvOnly(t *testing.T) {
	env := coreruntime.AuditPayloadCapture{
		ToolArgs: true,
		Redact:   true,
	}
	got := ResolveAuditPayloadCapture(env, types.AuditCaptureConfig{})
	if !got.ToolArgs || !got.Redact {
		t.Errorf("env-only path lost flags: %+v", got)
	}
}

// TestResolveAuditPayloadCapture_YAMLWinsOverEnv pins the
// precedence direction. forge.yaml is the highest layer; an operator
// who writes `tool_args: false` MUST be able to override an env var
// that says true (e.g. a corporate-wide debug toggle a per-agent
// deployment wants to disable).
func TestResolveAuditPayloadCapture_YAMLWinsOverEnv(t *testing.T) {
	env := coreruntime.AuditPayloadCapture{
		ToolArgs:   true,
		ToolResult: true,
		Redact:     true,
	}
	yaml := types.AuditCaptureConfig{
		ToolArgs: boolPtr(false),
		Redact:   boolPtr(false),
	}
	got := ResolveAuditPayloadCapture(env, yaml)
	if got.ToolArgs {
		t.Errorf("yaml tool_args=false did not override env=true")
	}
	if got.Redact {
		t.Errorf("yaml redact=false did not override env=true")
	}
	// Fields the yaml DIDN'T touch retain env values.
	if !got.ToolResult {
		t.Errorf("yaml omission must not clobber env: ToolResult lost")
	}
}

// TestResolveAuditPayloadCapture_YAMLNilDoesNotClobberEnv is the
// `*bool` nullable-field invariant. An operator who writes
// `audit.capture: {}` (or just doesn't write the block at all) leaves
// every yaml field nil. Nil MUST mean "fall through to env", never
// "false."
func TestResolveAuditPayloadCapture_YAMLNilDoesNotClobberEnv(t *testing.T) {
	env := coreruntime.AuditPayloadCapture{
		ToolArgs:    true,
		ToolResult:  true,
		LLMMessages: true,
		LLMResponse: true,
		Redact:      true,
	}
	got := ResolveAuditPayloadCapture(env, types.AuditCaptureConfig{})
	if !got.ToolArgs || !got.ToolResult || !got.LLMMessages || !got.LLMResponse {
		t.Errorf("nil yaml fields clobbered env capture flags: %+v", got)
	}
	if !got.Redact {
		t.Errorf("nil yaml.Redact clobbered env Redact=true")
	}
}

// TestResolveAuditPayloadCapture_MaxBytesUniform pins the single-knob
// semantic on the yaml layer too: max_bytes when set propagates
// uniformly across all four CapXxxBytes fields, matching the env-
// layer behavior. Operators don't reason about four caps; they
// reason about one.
func TestResolveAuditPayloadCapture_MaxBytesUniform(t *testing.T) {
	env := coreruntime.AuditPayloadCapture{}
	yaml := types.AuditCaptureConfig{MaxBytes: 8192}
	got := ResolveAuditPayloadCapture(env, yaml)
	for name, v := range map[string]int{
		"CapToolArgsBytes":    got.CapToolArgsBytes,
		"CapToolResultBytes":  got.CapToolResultBytes,
		"CapLLMMessagesBytes": got.CapLLMMessagesBytes,
		"CapLLMResponseBytes": got.CapLLMResponseBytes,
	} {
		if v != 8192 {
			t.Errorf("%s = %d, want 8192 (yaml max_bytes should propagate uniformly)", name, v)
		}
	}
}

// TestResolveAuditPayloadCapture_AllSurfacesOff is the default-deploy
// canary: no env, no yaml → every capture flag stays false, but
// Redact stays at whatever the env layer set (zero value from
// FromEnv()) so an operator who later flips a flag on doesn't get a
// surprise unscrubbed event.
func TestResolveAuditPayloadCapture_AllSurfacesOff(t *testing.T) {
	got := ResolveAuditPayloadCapture(coreruntime.AuditPayloadCapture{}, types.AuditCaptureConfig{})
	if got.AnyEnabled() {
		t.Errorf("default deploy must have AnyEnabled()=false; got %+v", got)
	}
}
