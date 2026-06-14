package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
	ctx := context.Background()
	if err := engine.CheckInbound(ctx, msg); err != nil {
		t.Errorf("normal message should pass inbound check: %v", err)
	}

	// Empty message should pass
	emptyMsg := &a2a.Message{Role: "user"}
	if err := engine.CheckInbound(ctx, emptyMsg); err != nil {
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
	if err := engine.CheckOutbound(context.Background(), msg); err != nil {
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

	ctx := context.Background()
	// Normal text should pass through
	out, err := engine.CheckToolOutput(ctx, "some_tool", "some normal output")
	if err != nil {
		t.Errorf("normal output should pass: %v", err)
	}
	if out != "some normal output" {
		t.Errorf("normal output should not be modified, got %q", out)
	}

	// Empty text should pass through
	out, err = engine.CheckToolOutput(ctx, "some_tool", "")
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
	checker := BuildGuardrailChecker(nil, "/nonexistent", false, logger, nil, GuardrailAuditConfig{})
	if checker == nil {
		t.Fatal("BuildGuardrailChecker should return a non-nil checker")
	}

	// Should still work (uses defaults)
	msg := &a2a.Message{
		Role:  "user",
		Parts: []a2a.Part{{Kind: a2a.PartKindText, Text: "hello"}},
	}
	if err := checker.CheckInbound(context.Background(), msg); err != nil {
		t.Errorf("default checker should pass normal message: %v", err)
	}
}

// TestLibraryGuardrailEngine_EmitsAuditOnInboundMask verifies the engine
// emits a guardrail_check event on the configured audit logger when an
// inbound message triggers a mask decision, and that capturing evidence
// surfaces the offending text (redacted + truncated) in fields.evidence.
func TestLibraryGuardrailEngine_EmitsAuditOnInboundMask(t *testing.T) {
	sg := DefaultStructuredGuardrails()
	engine, err := NewFileGuardrailEngine(sg, false, &grTestLogger{})
	if err != nil {
		t.Fatalf("NewFileGuardrailEngine: %v", err)
	}

	var buf bytes.Buffer
	al := coreruntime.NewAuditLogger(&buf)
	engine.WithAuditLogger(al, GuardrailAuditConfig{CaptureEvidence: true, Redact: true})

	msg := &a2a.Message{
		Role:  "user",
		Parts: []a2a.Part{{Kind: a2a.PartKindText, Text: "my email is foo@example.com please verify"}},
	}
	if err := engine.CheckInbound(context.Background(), msg); err != nil {
		t.Fatalf("CheckInbound: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, `"event":"guardrail_check"`) {
		t.Errorf("expected guardrail_check event, got: %s", out)
	}
	if !strings.Contains(out, `"direction":"inbound"`) {
		t.Errorf("expected direction=inbound, got: %s", out)
	}
	if !strings.Contains(out, `"decision":"masked"`) {
		t.Errorf("expected decision=masked, got: %s", out)
	}
	if !strings.Contains(out, `"evidence"`) {
		t.Errorf("expected evidence field with CaptureEvidence=true, got: %s", out)
	}
	if !strings.Contains(out, "foo@example.com") {
		t.Errorf("expected raw email in evidence, got: %s", out)
	}
}

// TestLibraryGuardrailEngine_OmitsEvidenceByDefault verifies the
// metadata-only posture: CaptureEvidence=false (the zero value) means
// fields.evidence is absent even when a mask fires.
func TestLibraryGuardrailEngine_OmitsEvidenceByDefault(t *testing.T) {
	sg := DefaultStructuredGuardrails()
	engine, err := NewFileGuardrailEngine(sg, false, &grTestLogger{})
	if err != nil {
		t.Fatalf("NewFileGuardrailEngine: %v", err)
	}

	var buf bytes.Buffer
	al := coreruntime.NewAuditLogger(&buf)
	engine.WithAuditLogger(al, GuardrailAuditConfig{}) // CaptureEvidence off

	msg := &a2a.Message{
		Role:  "user",
		Parts: []a2a.Part{{Kind: a2a.PartKindText, Text: "my email is foo@example.com"}},
	}
	if err := engine.CheckInbound(context.Background(), msg); err != nil {
		t.Fatalf("CheckInbound: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, `"event":"guardrail_check"`) {
		t.Errorf("expected guardrail_check event, got: %s", out)
	}
	if strings.Contains(out, `"evidence"`) {
		t.Errorf("evidence MUST be omitted when CaptureEvidence=false, got: %s", out)
	}
	if strings.Contains(out, "foo@example.com") {
		t.Errorf("raw content MUST NOT leak when CaptureEvidence=false, got: %s", out)
	}
}

// TestPrepareEvidence verifies the redact + truncate pipeline that
// runs over captured evidence before it lands in fields.evidence.
// Exercises both knobs independently of the guardrails library decision
// path — that path is covered by the EmitsAuditOnInboundMask test.
func TestPrepareEvidence(t *testing.T) {
	tests := []struct {
		name string
		in   string
		cfg  GuardrailAuditConfig
		want string
	}{
		{
			name: "empty input returns empty",
			in:   "",
			cfg:  GuardrailAuditConfig{Redact: true},
			want: "",
		},
		{
			name: "redact-off leaves anthropic token intact",
			in:   "leak: sk-ant-abcdefghijklmnopqrstuvwxyz123",
			cfg:  GuardrailAuditConfig{Redact: false},
			want: "leak: sk-ant-abcdefghijklmnopqrstuvwxyz123",
		},
		{
			name: "redact-on scrubs anthropic token to marker",
			in:   "leak: sk-ant-abcdefghijklmnopqrstuvwxyz123",
			cfg:  GuardrailAuditConfig{Redact: true},
			want: "leak: [REDACTED]",
		},
		{
			name: "redact-on scrubs github pat",
			in:   "leak: ghp_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			cfg:  GuardrailAuditConfig{Redact: true},
			want: "leak: [REDACTED]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := prepareEvidence(tt.in, tt.cfg)
			if got != tt.want {
				t.Errorf("prepareEvidence(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestPrepareEvidence_TruncatesAtCap verifies the byte cap activates
// when input exceeds MaxBytes; the truncation marker is appended.
func TestPrepareEvidence_TruncatesAtCap(t *testing.T) {
	in := strings.Repeat("x", 200)
	got := prepareEvidence(in, GuardrailAuditConfig{Redact: false, MaxBytes: 50})
	if len(got) >= 200 {
		t.Errorf("expected truncated output, got length %d", len(got))
	}
	if !strings.Contains(got, "[truncated:") {
		t.Errorf("expected truncation marker, got: %q", got)
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
