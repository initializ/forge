package runtime

import (
	"context"
	"testing"

	"github.com/initializ/forge/forge-core/a2a"
)

// testLogger is a no-op logger for tests. Shared across test files in this package.
type testLogger struct {
	warnings []string
}

func (l *testLogger) Info(_ string, _ map[string]any)  {}
func (l *testLogger) Debug(_ string, _ map[string]any) {}
func (l *testLogger) Warn(msg string, _ map[string]any) {
	l.warnings = append(l.warnings, msg)
}
func (l *testLogger) Error(_ string, _ map[string]any) {}

// TestNoopGuardrailChecker_ImplementsInterface verifies NoopGuardrailChecker
// satisfies the GuardrailChecker interface.
func TestNoopGuardrailChecker_ImplementsInterface(t *testing.T) {
	var checker GuardrailChecker = &NoopGuardrailChecker{}

	msg := &a2a.Message{
		Role: "user",
		Parts: []a2a.Part{
			{Kind: a2a.PartKindText, Text: "hello world"},
		},
	}

	ctx := context.Background()
	if err := checker.CheckInbound(ctx, msg); err != nil {
		t.Errorf("NoopGuardrailChecker.CheckInbound() unexpected error: %v", err)
	}

	if err := checker.CheckOutbound(ctx, msg); err != nil {
		t.Errorf("NoopGuardrailChecker.CheckOutbound() unexpected error: %v", err)
	}

	out, err := checker.CheckToolOutput(ctx, "some_tool", "some text")
	if err != nil {
		t.Errorf("NoopGuardrailChecker.CheckToolOutput() unexpected error: %v", err)
	}
	if out != "some text" {
		t.Errorf("NoopGuardrailChecker.CheckToolOutput() = %q, want %q", out, "some text")
	}
}

// TestExtractText verifies the text extraction helper.
func TestExtractText(t *testing.T) {
	tests := []struct {
		name string
		msg  *a2a.Message
		want string
	}{
		{
			name: "single text part",
			msg: &a2a.Message{
				Parts: []a2a.Part{{Kind: a2a.PartKindText, Text: "hello"}},
			},
			want: "hello",
		},
		{
			name: "multiple text parts",
			msg: &a2a.Message{
				Parts: []a2a.Part{
					{Kind: a2a.PartKindText, Text: "hello"},
					{Kind: a2a.PartKindText, Text: "world"},
				},
			},
			want: "hello world",
		},
		{
			name: "empty parts",
			msg:  &a2a.Message{},
			want: "",
		},
		{
			name: "non-text parts ignored",
			msg: &a2a.Message{
				Parts: []a2a.Part{
					{Kind: a2a.PartKindText, Text: "text"},
					{Kind: "data", Text: ""},
				},
			},
			want: "text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractText(tt.msg)
			if got != tt.want {
				t.Errorf("ExtractText() = %q, want %q", got, tt.want)
			}
		})
	}
}
