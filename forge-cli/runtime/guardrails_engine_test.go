package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/initializ/guardrails/models"

	"github.com/initializ/forge/forge-core/a2a"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

// testLogger is a no-op logger for tests.
type grTestLogger struct{}

func (l *grTestLogger) Info(_ string, _ map[string]any)  {}
func (l *grTestLogger) Debug(_ string, _ map[string]any) {}
func (l *grTestLogger) Warn(_ string, _ map[string]any)  {}
func (l *grTestLogger) Error(_ string, _ map[string]any) {}

// TestLibraryGuardrailEngine_ImplementsInterface verifies the engine
// satisfies the GuardrailChecker interface.
func TestLibraryGuardrailEngine_ImplementsInterface(t *testing.T) {
	sg := DefaultStructuredGuardrails()
	engine, err := NewFileGuardrailEngine(sg, false, &grTestLogger{})
	if err != nil {
		t.Fatalf("NewFileGuardrailEngine() error: %v", err)
	}
	var _ coreruntime.GuardrailChecker = engine
}

// TestFileGuardrailEngine_CheckInbound tests basic inbound checking.
func TestFileGuardrailEngine_CheckInbound(t *testing.T) {
	sg := DefaultStructuredGuardrails()
	engine, err := NewFileGuardrailEngine(sg, true, &grTestLogger{})
	if err != nil {
		t.Fatalf("NewFileGuardrailEngine() error: %v", err)
	}

	// Normal message should pass
	msg := &a2a.Message{
		Role:  "user",
		Parts: []a2a.Part{{Kind: a2a.PartKindText, Text: "Hello, how are you?"}},
	}
	if err := engine.CheckInbound(msg); err != nil {
		t.Errorf("normal message should pass inbound check: %v", err)
	}

	// Empty message should pass
	emptyMsg := &a2a.Message{Role: "user"}
	if err := engine.CheckInbound(emptyMsg); err != nil {
		t.Errorf("empty message should pass inbound check: %v", err)
	}
}

// TestFileGuardrailEngine_CheckOutbound tests outbound content handling.
func TestFileGuardrailEngine_CheckOutbound(t *testing.T) {
	sg := DefaultStructuredGuardrails()
	engine, err := NewFileGuardrailEngine(sg, false, &grTestLogger{})
	if err != nil {
		t.Fatalf("NewFileGuardrailEngine() error: %v", err)
	}

	// Normal message should pass through unchanged
	msg := &a2a.Message{
		Role:  "agent",
		Parts: []a2a.Part{{Kind: a2a.PartKindText, Text: "Here is the result."}},
	}
	if err := engine.CheckOutbound(msg); err != nil {
		t.Errorf("normal message should pass outbound check: %v", err)
	}
}

// TestFileGuardrailEngine_CheckToolOutput tests tool output scanning.
func TestFileGuardrailEngine_CheckToolOutput(t *testing.T) {
	sg := DefaultStructuredGuardrails()
	engine, err := NewFileGuardrailEngine(sg, false, &grTestLogger{})
	if err != nil {
		t.Fatalf("NewFileGuardrailEngine() error: %v", err)
	}

	// Normal text should pass through
	out, err := engine.CheckToolOutput("some_tool", "some normal output")
	if err != nil {
		t.Errorf("normal output should pass: %v", err)
	}
	if out != "some normal output" {
		t.Errorf("normal output should not be modified, got %q", out)
	}

	// Empty text should pass through
	out, err = engine.CheckToolOutput("some_tool", "")
	if err != nil {
		t.Errorf("empty output should pass: %v", err)
	}
	if out != "" {
		t.Errorf("empty output should remain empty, got %q", out)
	}
}

// TestBuildGuardrailChecker_FileMode tests the builder with file-based config.
func TestBuildGuardrailChecker_FileMode(t *testing.T) {
	logger := &grTestLogger{}
	checker := BuildGuardrailChecker(nil, "/nonexistent", false, logger)
	if checker == nil {
		t.Fatal("BuildGuardrailChecker should return a non-nil checker")
	}

	// Should still work (uses defaults)
	msg := &a2a.Message{
		Role:  "user",
		Parts: []a2a.Part{{Kind: a2a.PartKindText, Text: "hello"}},
	}
	if err := checker.CheckInbound(msg); err != nil {
		t.Errorf("default checker should pass normal message: %v", err)
	}
}

// TestLoadGuardrailsJSON tests parsing a guardrails.json file.
func TestLoadGuardrailsJSON(t *testing.T) {
	dir := t.TempDir()
	sg := &models.StructuredGuardrails{
		PII: &models.PIIConfig{
			Enabled: true,
			Action:  "mask",
			Categories: map[string]models.PIICategoryConfig{
				"email": {Enabled: true, Action: "mask"},
			},
		},
		GateConfig: &models.GateConfig{
			InputGate:  true,
			OutputGate: true,
		},
	}

	data, err := json.MarshalIndent(sg, "", "  ")
	if err != nil {
		t.Fatalf("marshaling test config: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "guardrails.json"), data, 0o644); err != nil {
		t.Fatalf("writing test guardrails.json: %v", err)
	}

	loaded := LoadGuardrailsJSON(nil, dir)
	if loaded == nil {
		t.Fatal("LoadGuardrailsJSON returned nil for existing file")
	}
	if loaded.PII == nil || !loaded.PII.Enabled {
		t.Error("expected PII to be enabled in loaded config")
	}
	if loaded.GateConfig == nil || !loaded.GateConfig.InputGate {
		t.Error("expected InputGate to be enabled in loaded config")
	}
}

// TestLoadGuardrailsJSON_Missing tests loading when file doesn't exist.
func TestLoadGuardrailsJSON_Missing(t *testing.T) {
	loaded := LoadGuardrailsJSON(nil, "/nonexistent")
	if loaded != nil {
		t.Error("LoadGuardrailsJSON should return nil for missing file")
	}
}

// TestDefaultStructuredGuardrails tests the default config has expected sections.
func TestDefaultStructuredGuardrails(t *testing.T) {
	sg := DefaultStructuredGuardrails()

	if sg.PII == nil || !sg.PII.Enabled {
		t.Error("default should have PII enabled")
	}
	if len(sg.PII.Categories) != 4 {
		t.Errorf("default PII should have 4 categories, got %d", len(sg.PII.Categories))
	}
	if sg.Security == nil || sg.Security.JailbreakDetection == nil {
		t.Error("default should have jailbreak detection")
	}
	if sg.CustomRules == nil || len(sg.CustomRules.Rules) != 11 {
		t.Errorf("default should have 11 secret rules, got %d", len(sg.CustomRules.Rules))
	}
	if sg.GateConfig == nil || !sg.GateConfig.InputGate || !sg.GateConfig.OutputGate {
		t.Error("default should have input and output gates enabled")
	}
}
