package a2a

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestPart_Validate_KindMissingWithContent_NamesLegacyTypeMistake is
// the canonical regression test for issue #119: a client sending
// `"type": "text"` produces a Part with empty Kind + non-empty Text,
// and the validation error must say so in terms the operator can act
// on without reading source.
func TestPart_Validate_KindMissingWithContent_NamesLegacyTypeMistake(t *testing.T) {
	// Simulate the exact bug 2 repro: JSON-decode a payload using the
	// pre-0.3.0 `type` discriminator. encoding/json discards `type`
	// because the Part struct only knows `kind`.
	body := `{"type":"text","text":"Reply with a one-line hello."}`
	var p Part
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.Kind != "" {
		t.Fatalf("expected Kind to be empty after legacy-type decode; got %q", p.Kind)
	}
	if p.Text == "" {
		t.Fatalf("expected Text to be populated; got empty")
	}

	err := p.Validate()
	if err == nil {
		t.Fatal("expected Validate to fail on empty Kind")
	}
	if !errors.Is(err, ErrPartKindMissing) {
		t.Errorf("error should wrap ErrPartKindMissing; got %v", err)
	}
	msg := err.Error()
	// The error must name the likely mistake explicitly so an operator
	// reading it (or grepping their logs for it) understands what
	// happened without diving into source.
	for _, want := range []string{`"type"`, `"kind"`, "pre-0.3.0"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message must mention %q; got %q", want, msg)
		}
	}
}

func TestPart_Validate_KindMissingEmptyPart(t *testing.T) {
	var p Part // zero value: Kind == "", no content
	err := p.Validate()
	if err == nil {
		t.Fatal("zero-value Part should fail Validate")
	}
	if !errors.Is(err, ErrPartKindMissing) {
		t.Errorf("error should wrap ErrPartKindMissing; got %v", err)
	}
	// No content present → the error should NOT name the type-vs-kind
	// mistake (a zero-value part isn't the legacy-dialect case).
	if strings.Contains(err.Error(), `"type"`) {
		t.Errorf("empty-part error should not speculate about legacy `type` field; got %q", err.Error())
	}
}

func TestPart_Validate_KnownKindsPassThrough(t *testing.T) {
	for _, kind := range []PartKind{PartKindText, PartKindData, PartKindFile} {
		t.Run(string(kind), func(t *testing.T) {
			p := Part{Kind: kind}
			if err := p.Validate(); err != nil {
				t.Errorf("kind=%q with no content should be valid (empty parts allowed); got %v", kind, err)
			}
		})
	}
}

func TestPart_Validate_UnknownKindRejected(t *testing.T) {
	p := Part{Kind: "image", Text: "hi"}
	err := p.Validate()
	if err == nil {
		t.Fatal("expected unknown kind to fail Validate")
	}
	if !errors.Is(err, ErrPartKindUnknown) {
		t.Errorf("error should wrap ErrPartKindUnknown; got %v", err)
	}
	if !strings.Contains(err.Error(), `"image"`) {
		t.Errorf("error should name the offending kind; got %q", err.Error())
	}
}

func TestMessage_Validate_RoleRequired(t *testing.T) {
	m := Message{Parts: []Part{{Kind: PartKindText, Text: "hi"}}}
	err := m.Validate()
	if !errors.Is(err, ErrMessageRoleMissing) {
		t.Errorf("expected ErrMessageRoleMissing; got %v", err)
	}
}

func TestMessage_Validate_PartsNonEmpty(t *testing.T) {
	m := Message{Role: MessageRoleUser, Parts: nil}
	err := m.Validate()
	if !errors.Is(err, ErrMessagePartsEmpty) {
		t.Errorf("expected ErrMessagePartsEmpty; got %v", err)
	}
}

func TestMessage_Validate_NamesOffendingPartIndex(t *testing.T) {
	m := Message{
		Role: MessageRoleUser,
		Parts: []Part{
			{Kind: PartKindText, Text: "first"}, // valid
			{Text: "second-but-no-kind"},        // invalid — empty Kind
		},
	}
	err := m.Validate()
	if err == nil {
		t.Fatal("expected error from second part")
	}
	if !errors.Is(err, ErrPartKindMissing) {
		t.Errorf("error should wrap the part-level sentinel; got %v", err)
	}
	if !strings.Contains(err.Error(), "parts[1]") {
		t.Errorf("error should name the offending part index; got %q", err.Error())
	}
}

func TestMessage_Validate_HappyPath(t *testing.T) {
	m := Message{
		Role: MessageRoleUser,
		Parts: []Part{
			NewTextPart("Reply with a one-line hello."),
		},
	}
	if err := m.Validate(); err != nil {
		t.Errorf("well-formed message should validate; got %v", err)
	}
}
