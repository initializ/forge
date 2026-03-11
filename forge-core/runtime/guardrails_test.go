package runtime

import (
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/agentspec"
)

// --- Validator unit tests ---

func TestValidateSSN(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"known test SSN with dashes", "123-45-6789", false}, // 123456789 is a known test SSN
		{"known test SSN no separators", "123456789", false},
		{"valid SSN dots", "456.78.9012", true},
		{"area 000", "000-12-3456", false},
		{"area 666", "666-12-3456", false},
		{"area 900+", "900-12-3456", false},
		{"area 999", "999-12-3456", false},
		{"group 00", "123-00-4567", false},
		{"serial 0000", "123-45-0000", false},
		{"all same digits", "111111111", false},
		{"all same digits 555", "555555555", false},
		{"known test SSN 078051120", "078051120", false},
		{"known test SSN 219099999", "219099999", false},
		{"too short", "12345678", false},
		{"too long", "1234567890", false},
		{"non-digit", "12a-45-6789", false},
		{"valid 456-78-9012", "456-78-9012", true},
		{"valid 321-54-9876", "321-54-9876", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validateSSN(tt.input); got != tt.want {
				t.Errorf("validateSSN(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestValidateLuhn(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		// Known valid test card numbers
		{"Visa test", "4111111111111111", true},
		{"Visa with spaces", "4111 1111 1111 1111", true},
		{"Visa with dashes", "4111-1111-1111-1111", true},
		{"Mastercard test", "5500000000000004", true},
		{"Amex test", "378282246310005", true},
		{"Discover test", "6011111111111117", true},
		// Invalid
		{"bad checksum", "4111111111111112", false},
		{"too short", "411111111111", false},
		{"too long", "41111111111111111111", false},
		{"non-digit", "4111abcd11111111", false},
		// Random numbers that happen to be 16 digits should usually fail
		{"random 16 digits", "1234567890123456", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validateLuhn(tt.input); got != tt.want {
				t.Errorf("validateLuhn(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// --- PII pattern matching tests ---

func TestCheckNoPII_Phone(t *testing.T) {
	noopLogger := &testLogger{}
	g := NewGuardrailEngine(&agentspec.PolicyScaffold{
		Guardrails: []agentspec.Guardrail{{Type: "no_pii"}},
	}, true, noopLogger)

	tests := []struct {
		name    string
		text    string
		wantErr bool
	}{
		{"US phone with dashes", "call 212-555-1234", true},
		{"US phone with dots", "call 212.555.1234", true},
		{"US phone with +1", "call +1-212-555-1234", true},
		{"US phone with parens", "call (212) 555-1234", true},
		// Area code must start with 2-9
		{"area code starts with 0", "call 012-555-1234", false},
		{"area code starts with 1", "call 112-555-1234", false},
		// K8s byte counts should NOT match
		{"k8s memory bytes 4Gi", "memory: 4294967296 bytes", false},
		{"k8s memory bytes 1Gi", "memory: 1073741824 bytes", false},
		{"k8s memory 10 digits", "allocatable: 3221225472 bytes", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := g.checkNoPII(tt.text)
			if (err != nil) != tt.wantErr {
				t.Errorf("checkNoPII(%q) error = %v, wantErr %v", tt.text, err, tt.wantErr)
			}
		})
	}
}

func TestCheckNoPII_SSN(t *testing.T) {
	noopLogger := &testLogger{}
	g := NewGuardrailEngine(&agentspec.PolicyScaffold{
		Guardrails: []agentspec.Guardrail{{Type: "no_pii"}},
	}, true, noopLogger)

	tests := []struct {
		name    string
		text    string
		wantErr bool
	}{
		{"valid SSN", "SSN: 456-78-9012", true},
		{"valid SSN no sep", "SSN: 456789012", true},
		{"invalid area 000", "SSN: 000-12-3456", false},
		{"invalid area 666", "SSN: 666-12-3456", false},
		{"invalid area 900+", "SSN: 900-12-3456", false},
		{"invalid group 00", "SSN: 123-00-4567", false},
		{"invalid serial 0000", "SSN: 123-45-0000", false},
		{"all same digits", "SSN: 111-11-1111", false},
		{"known test SSN", "SSN: 123-45-6789", false}, // 123456789 is a known test SSN
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := g.checkNoPII(tt.text)
			if (err != nil) != tt.wantErr {
				t.Errorf("checkNoPII(%q) error = %v, wantErr %v", tt.text, err, tt.wantErr)
			}
		})
	}
}

func TestCheckNoPII_CreditCard(t *testing.T) {
	noopLogger := &testLogger{}
	g := NewGuardrailEngine(&agentspec.PolicyScaffold{
		Guardrails: []agentspec.Guardrail{{Type: "no_pii"}},
	}, true, noopLogger)

	tests := []struct {
		name    string
		text    string
		wantErr bool
	}{
		{"Visa", "card: 4111111111111111", true},
		{"Visa with spaces", "card: 4111 1111 1111 1111", true},
		{"Mastercard", "card: 5500000000000004", true},
		{"Amex", "card: 378282246310005", true},
		{"bad Luhn", "card: 4111111111111112", false},
		{"random 16 digits", "card: 1234567890123456", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := g.checkNoPII(tt.text)
			if (err != nil) != tt.wantErr {
				t.Errorf("checkNoPII(%q) error = %v, wantErr %v", tt.text, err, tt.wantErr)
			}
		})
	}
}

func TestCheckNoPII_Email(t *testing.T) {
	noopLogger := &testLogger{}
	g := NewGuardrailEngine(&agentspec.PolicyScaffold{
		Guardrails: []agentspec.Guardrail{{Type: "no_pii"}},
	}, true, noopLogger)

	tests := []struct {
		name    string
		text    string
		wantErr bool
	}{
		{"simple email", "contact: user@example.com", true},
		{"email with plus", "contact: user+tag@example.com", true},
		{"not an email", "contact: user at example dot com", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := g.checkNoPII(tt.text)
			if (err != nil) != tt.wantErr {
				t.Errorf("checkNoPII(%q) error = %v, wantErr %v", tt.text, err, tt.wantErr)
			}
		})
	}
}

// --- CheckToolOutput tests ---

func TestCheckToolOutput_RedactsWithValidation(t *testing.T) {
	logger := &testLogger{}
	g := NewGuardrailEngine(&agentspec.PolicyScaffold{
		Guardrails: []agentspec.Guardrail{{Type: "no_pii"}},
	}, false, logger) // warn mode

	// Valid SSN should be redacted
	out, err := g.CheckToolOutput("SSN is 456-78-9012")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out == "SSN is 456-78-9012" {
		t.Error("expected valid SSN to be redacted")
	}

	// Invalid SSN (area 000) should NOT be redacted
	out, err = g.CheckToolOutput("code 000-12-3456 here")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "code 000-12-3456 here" {
		t.Errorf("expected invalid SSN to pass through, got %q", out)
	}
}

func TestCheckToolOutput_K8sBytesNotBlocked(t *testing.T) {
	logger := &testLogger{}
	g := NewGuardrailEngine(&agentspec.PolicyScaffold{
		Guardrails: []agentspec.Guardrail{{Type: "no_pii"}},
	}, true, logger) // enforce mode

	// K8s memory byte counts should not trigger PII detection
	k8sOutput := `{"memory": "4294967296", "cpu": "2000m", "pods": "110", "allocatable_memory": "3221225472"}`
	out, err := g.CheckToolOutput(k8sOutput)
	if err != nil {
		t.Fatalf("k8s output blocked as PII: %v", err)
	}
	if out != k8sOutput {
		t.Errorf("k8s output was modified: %q", out)
	}
}

func TestCheckToolOutput_EnforceBlocksValidPII(t *testing.T) {
	logger := &testLogger{}
	g := NewGuardrailEngine(&agentspec.PolicyScaffold{
		Guardrails: []agentspec.Guardrail{{Type: "no_pii"}},
	}, true, logger) // enforce mode

	_, err := g.CheckToolOutput("SSN: 456-78-9012")
	if err == nil {
		t.Error("expected enforce mode to block valid SSN")
	}
}

// --- CheckOutbound message tests ---

func TestCheckOutbound_PIIRedacted(t *testing.T) {
	logger := &testLogger{}
	g := NewGuardrailEngine(&agentspec.PolicyScaffold{
		Guardrails: []agentspec.Guardrail{{Type: "no_pii"}},
	}, true, logger)

	msg := &a2a.Message{
		Role: "agent",
		Parts: []a2a.Part{
			{Kind: a2a.PartKindText, Text: "Your SSN is 456-78-9012"},
		},
	}
	err := g.CheckOutbound(msg)
	if err != nil {
		t.Errorf("CheckOutbound should redact, not block: %v", err)
	}
	if !strings.Contains(msg.Parts[0].Text, "[REDACTED]") {
		t.Error("expected PII to be redacted in outbound message")
	}
}

func TestCheckOutbound_InvalidSSNPasses(t *testing.T) {
	logger := &testLogger{}
	g := NewGuardrailEngine(&agentspec.PolicyScaffold{
		Guardrails: []agentspec.Guardrail{{Type: "no_pii"}},
	}, true, logger)

	msg := &a2a.Message{
		Role: "agent",
		Parts: []a2a.Part{
			{Kind: a2a.PartKindText, Text: "code: 000-12-3456"},
		},
	}
	err := g.CheckOutbound(msg)
	if err != nil {
		t.Errorf("invalid SSN should pass through, got error: %v", err)
	}
}

// testLogger is a no-op logger for tests.
type testLogger struct {
	warnings []string
}

func (l *testLogger) Info(msg string, fields map[string]any)  {}
func (l *testLogger) Debug(msg string, fields map[string]any) {}
func (l *testLogger) Warn(msg string, fields map[string]any) {
	l.warnings = append(l.warnings, msg)
}
func (l *testLogger) Error(msg string, fields map[string]any) {}
