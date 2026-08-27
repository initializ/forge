package a2a

import (
	"strings"
	"testing"
)

func TestMessagePromptText(t *testing.T) {
	tests := []struct {
		name  string
		parts []Part
		want  string
	}{
		{
			name:  "text only",
			parts: []Part{NewTextPart("hello world")},
			want:  "hello world",
		},
		{
			name:  "multiple text parts joined",
			parts: []Part{NewTextPart("a"), NewTextPart("b")},
			want:  "a\nb",
		},
		{
			// The #410 field-hit: a data-part-only message must NOT read as empty.
			name:  "data only",
			parts: []Part{NewDataPart(map[string]any{"input": "do the thing"})},
			want:  "```json\n{\n  \"input\": \"do the thing\"\n}\n```",
		},
		{
			name: "text then data",
			parts: []Part{
				NewTextPart("summarize this:"),
				NewDataPart(map[string]any{"n": float64(2)}),
			},
			want: "summarize this:\n```json\n{\n  \"n\": 2\n}\n```",
		},
		{
			name:  "nil data skipped",
			parts: []Part{{Kind: PartKindData, Data: nil}},
			want:  "",
		},
		{
			name: "file part ignored, data kept",
			parts: []Part{
				{Kind: PartKindFile, File: &FileContent{Name: "x.bin"}},
				NewDataPart([]any{"a", "b"}),
			},
			want: "```json\n[\n  \"a\",\n  \"b\"\n]\n```",
		},
		{
			name:  "empty message",
			parts: nil,
			want:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Message{Role: MessageRoleUser, Parts: tt.parts}.PromptText()
			if got != tt.want {
				t.Errorf("PromptText() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestMessagePromptText_DataPartNeverEmpty is the core regression: a message
// carrying only a data part produces non-empty prompt content.
func TestMessagePromptText_DataPartNeverEmpty(t *testing.T) {
	m := Message{Role: MessageRoleUser, Parts: []Part{
		NewDataPart(map[string]any{"input": "<upstream step output>"}),
	}}
	got := m.PromptText()
	if strings.TrimSpace(got) == "" {
		t.Fatal("data-part-only message produced an empty prompt (#410 regression)")
	}
	if !strings.Contains(got, "upstream step output") {
		t.Errorf("data content not projected into prompt: %q", got)
	}
}

// TestMessagePromptText_DataPartCap: a hostile/huge data part must not become
// an unbounded prompt block (review #411). The cap is rune-safe and applies
// identically to the scanners and the prompt builder (same projection), so
// truncation cannot open a scan/model divergence.
func TestMessagePromptText_DataPartCap(t *testing.T) {
	big := strings.Repeat("世", 20<<10) // ~60KB of 3-byte runes once JSON-encoded
	m := Message{Role: MessageRoleUser, Parts: []Part{
		NewDataPart(map[string]any{"payload": big}),
	}}
	got := m.PromptText()
	if len(got) > dataPartProjectionCap+256 {
		t.Fatalf("projection not capped: %d bytes", len(got))
	}
	if !strings.Contains(got, "(data truncated)") {
		t.Errorf("truncation marker missing")
	}
	for _, r := range got {
		if r == '�' {
			t.Fatal("replacement char — cap sliced mid-rune")
		}
	}
	// Small data parts are untouched.
	small := Message{Role: MessageRoleUser, Parts: []Part{
		NewDataPart(map[string]any{"input": "fine"}),
	}}
	if s := small.PromptText(); strings.Contains(s, "truncated") {
		t.Errorf("small part must not truncate: %q", s)
	}
}
