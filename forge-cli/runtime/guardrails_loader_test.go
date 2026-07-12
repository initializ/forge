package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/observability"
)

// captureLogger records everything emitted through the ops logger so tests
// can assert what surfaced (e.g. the platform-overlay tightening line).
type captureLogger struct {
	infos  []string
	warns  []string
	errors []string
}

func (l *captureLogger) Debug(msg string, _ map[string]any) {}
func (l *captureLogger) Info(msg string, _ map[string]any)  { l.infos = append(l.infos, msg) }
func (l *captureLogger) Warn(msg string, _ map[string]any)  { l.warns = append(l.warns, msg) }
func (l *captureLogger) Error(msg string, _ map[string]any) { l.errors = append(l.errors, msg) }

func isolateLayers(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)                                                 // isolate the user policy layer
	t.Setenv("FORGE_SYSTEM_POLICY", filepath.Join(dir, "no-system.yaml")) // isolate the system layer
	t.Setenv("FORGE_PLATFORM_POLICY", "")                                 // no workspace policy unless a test sets it
	return dir
}

// TestBuildGuardrailChecker_AppliesPlatformOverlay wires a workspace
// policy.yaml with a guardrails overlay and confirms the build logs that the
// overlay tightened the agent guardrails (#284).
func TestBuildGuardrailChecker_AppliesPlatformOverlay(t *testing.T) {
	dir := isolateLayers(t)
	if err := os.WriteFile(filepath.Join(dir, "guardrails.json"), []byte(`{
		"gateConfig": {"outputGate": false}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(dir, "policy.yaml")
	if err := os.WriteFile(policyPath, []byte(`
guardrails:
  gateConfig:
    outputGate: true
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FORGE_PLATFORM_POLICY", policyPath)

	logger := &captureLogger{}
	checker, err := BuildGuardrailChecker(nil, dir, false, logger, nil,
		GuardrailAuditConfig{}, observability.TracingConfig{})
	if err != nil {
		t.Fatalf("overlay build errored: %v", err)
	}
	if checker == nil {
		t.Fatal("expected a non-nil checker")
	}
	if !anyContains(logger.infos, "platform overlay tightened") {
		t.Errorf("expected a tightening log line; got infos=%v", logger.infos)
	}
}

// TestBuildGuardrailChecker_MalformedOverlay_FailsClosed pins finding #1
// from the #285 review: a typo'd `guardrails:` block (rejected by strict
// decode) must ABORT startup, not silently drop the operator's mandate.
func TestBuildGuardrailChecker_MalformedOverlay_FailsClosed(t *testing.T) {
	dir := isolateLayers(t)
	policyPath := filepath.Join(dir, "policy.yaml")
	if err := os.WriteFile(policyPath, []byte(`
guardrails:
  gateConfig:
    outptGate: true
`), 0o600); err != nil { // typo: outptGate
		t.Fatal(err)
	}
	t.Setenv("FORGE_PLATFORM_POLICY", policyPath)

	logger := &captureLogger{}
	checker, err := BuildGuardrailChecker(nil, dir, false, logger, nil,
		GuardrailAuditConfig{}, observability.TracingConfig{})
	if err == nil {
		t.Fatalf("expected a startup error on malformed overlay; got checker=%v", checker)
	}
	if checker != nil {
		t.Errorf("fail-closed must return a nil checker; got %T", checker)
	}
	if len(logger.errors) == 0 {
		t.Errorf("expected an Error log line; got infos=%v warns=%v", logger.infos, logger.warns)
	}
}

// TestLoadPlatformGuardrailsOverlay_FoldsUserAndWorkspace confirms multiple
// layers combine (user ~/.forge/policy.yaml + workspace FORGE_PLATFORM_POLICY).
func TestLoadPlatformGuardrailsOverlay_FoldsUserAndWorkspace(t *testing.T) {
	dir := isolateLayers(t)

	userDir := filepath.Join(dir, ".forge")
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userDir, "policy.yaml"), []byte(`
guardrails:
  gateConfig:
    inputGate: true
`), 0o600); err != nil {
		t.Fatal(err)
	}
	wsPath := filepath.Join(dir, "ws-policy.yaml")
	if err := os.WriteFile(wsPath, []byte(`
guardrails:
  gateConfig:
    outputGate: true
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FORGE_PLATFORM_POLICY", wsPath)

	overlay, sources, err := LoadPlatformGuardrailsOverlay()
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if overlay == nil || overlay.GateConfig == nil {
		t.Fatal("expected a folded overlay with gateConfig")
	}
	if !overlay.GateConfig.InputGate || !overlay.GateConfig.OutputGate {
		t.Errorf("both layers should contribute gates; got %+v", overlay.GateConfig)
	}
	if len(sources) != 2 {
		t.Errorf("expected 2 contributing layers, got %v", sources)
	}
}

func anyContains(lines []string, substr string) bool {
	for _, l := range lines {
		if strings.Contains(l, substr) {
			return true
		}
	}
	return false
}
