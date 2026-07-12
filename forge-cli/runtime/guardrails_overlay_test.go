package runtime

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/initializ/guardrails/models"

	"github.com/initializ/forge/forge-core/security"
)

// TestOverlayFromRawYAML_Bridge verifies the YAML→JSON→struct bridge maps
// the camelCase guardrails schema authored in policy.yaml onto the typed
// StructuredGuardrails correctly (the core YAML-vs-JSON concern).
func TestOverlayFromRawYAML_Bridge(t *testing.T) {
	policyYAML := []byte(`
denied_tools:
  - http_request
guardrails:
  gateConfig:
    outputGate: true
  security:
    commandInjection:
      enabled: true
      confidenceThreshold: 20
      action: block
  customRules:
    rules:
      - id: platform_secret
        name: Internal token
        type: regex
        constraint: hard
        pattern: "intz-[a-f0-9]{16}"
        action: block
`)
	pol, err := security.ParsePlatformPolicy(policyYAML)
	if err != nil {
		t.Fatalf("policy parse failed: %v", err)
	}
	if len(pol.Guardrails) == 0 {
		t.Fatal("guardrails block did not parse into PlatformPolicy.Guardrails")
	}

	ov, err := overlayFromRawYAML(pol.Guardrails)
	if err != nil {
		t.Fatalf("bridge failed: %v", err)
	}
	if ov.GateConfig == nil || !ov.GateConfig.OutputGate {
		t.Error("gateConfig.outputGate did not bridge")
	}
	if ov.Security == nil || ov.Security.CommandInjection == nil {
		t.Fatal("security.commandInjection did not bridge")
	}
	if ov.Security.CommandInjection.ConfidenceThreshold != 20 {
		t.Errorf("confidenceThreshold bridged wrong: %g", ov.Security.CommandInjection.ConfidenceThreshold)
	}
	if ov.Security.CommandInjection.Action != "block" {
		t.Errorf("action bridged wrong: %q", ov.Security.CommandInjection.Action)
	}
	if ov.CustomRules == nil || len(ov.CustomRules.Rules) != 1 || ov.CustomRules.Rules[0].ID != "platform_secret" {
		t.Errorf("customRules did not bridge: %+v", ov.CustomRules)
	}
}

// TestOverlayFromRawYAML_StrictUnknownField ensures an operator typo in the
// guardrails block fails loudly (parity with the strict policy loader).
func TestOverlayFromRawYAML_StrictUnknownField(t *testing.T) {
	pol, err := security.ParsePlatformPolicy([]byte(`
guardrails:
  gateConfig:
    outptGate: true
`)) // typo: outptGate
	if err != nil {
		t.Fatalf("policy parse failed: %v", err)
	}
	if _, err := overlayFromRawYAML(pol.Guardrails); err == nil {
		t.Error("expected strict bridge to reject the unknown field 'outptGate'")
	}
}

// TestLoadPlatformGuardrailsOverlay_Workspace loads a workspace-layer
// policy.yaml (via FORGE_PLATFORM_POLICY) carrying a guardrails overlay and
// checks it merges over an agent config. HOME + FORGE_SYSTEM_POLICY are
// redirected so the system/user layers don't leak from the host.
func TestLoadPlatformGuardrailsOverlay_Workspace(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)                                                 // isolate user layer (~/.forge)
	t.Setenv("FORGE_SYSTEM_POLICY", filepath.Join(dir, "no-system.yaml")) // isolate system layer

	policyPath := filepath.Join(dir, "policy.yaml")
	if err := os.WriteFile(policyPath, []byte(`
guardrails:
  gateConfig:
    outputGate: true
  security:
    commandInjection:
      enabled: true
      confidenceThreshold: 15
      action: block
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FORGE_PLATFORM_POLICY", policyPath)

	overlay, sources, err := LoadPlatformGuardrailsOverlay()
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if overlay == nil {
		t.Fatal("expected an overlay from the workspace policy")
	}
	if len(sources) != 1 || sources[0] != security.LayerWorkspace {
		t.Errorf("expected workspace source, got %v", sources)
	}

	// Agent that is weaker than the overlay: gate off, command injection warn.
	agent := &models.StructuredGuardrails{
		GateConfig: &models.GateConfig{OutputGate: false},
		Security:   &models.SecurityConfig{CommandInjection: thr(true, 50, "warn")},
	}
	effective, tt := MergeGuardrails(agent, overlay)
	if !effective.GateConfig.OutputGate {
		t.Error("overlay should have forced outputGate on")
	}
	if effective.Security.CommandInjection.Action != "block" {
		t.Error("overlay should have raised commandInjection action to block")
	}
	if effective.Security.CommandInjection.ConfidenceThreshold != 15 {
		t.Error("overlay should have lowered the threshold to 15")
	}
	if len(tt) == 0 {
		t.Error("expected recorded tightenings")
	}
}

// TestLoadPlatformGuardrailsOverlay_None returns nil when no layer declares
// a guardrails overlay.
func TestLoadPlatformGuardrailsOverlay_None(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("FORGE_SYSTEM_POLICY", filepath.Join(dir, "no-system.yaml"))
	t.Setenv("FORGE_PLATFORM_POLICY", "") // no workspace policy

	overlay, _, err := LoadPlatformGuardrailsOverlay()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if overlay != nil {
		t.Errorf("expected nil overlay when no layer declares one, got %+v", overlay)
	}
}
