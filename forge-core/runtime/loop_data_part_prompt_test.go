package runtime

import (
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/llm"
)

// TestA2aMessageToLLM_IncludesDataParts is the #410 regression at the actual
// prompt-building seam: a data-part-only inbound message must convert to a
// non-empty LLM turn, not an empty prompt (which made the agent reply
// "Hello! How can I help?").
func TestA2aMessageToLLM_IncludesDataParts(t *testing.T) {
	msg := a2a.Message{
		Role:  a2a.MessageRoleUser,
		Parts: []a2a.Part{a2a.NewDataPart(map[string]any{"input": "process me"})},
	}
	got := a2aMessageToLLM(msg)

	if got.Role != llm.RoleUser {
		t.Errorf("role = %v, want user", got.Role)
	}
	if strings.TrimSpace(got.Content) == "" {
		t.Fatal("data-part-only message built an empty LLM prompt (#410)")
	}
	if !strings.Contains(got.Content, "process me") {
		t.Errorf("data content missing from prompt: %q", got.Content)
	}
}

// TestA2aMessagesEqual_DataParts confirms the #143 history-dedup comparison
// still works now that the projection includes data parts: two data-only
// messages with the same data are equal; different data are not.
func TestA2aMessagesEqual_DataParts(t *testing.T) {
	a := a2a.Message{Role: a2a.MessageRoleUser, Parts: []a2a.Part{a2a.NewDataPart(map[string]any{"x": "1"})}}
	b := a2a.Message{Role: a2a.MessageRoleUser, Parts: []a2a.Part{a2a.NewDataPart(map[string]any{"x": "1"})}}
	c := a2a.Message{Role: a2a.MessageRoleUser, Parts: []a2a.Part{a2a.NewDataPart(map[string]any{"x": "2"})}}

	if !a2aMessagesEqual(a, b) {
		t.Error("identical data-part messages should be equal")
	}
	if a2aMessagesEqual(a, c) {
		t.Error("different data-part messages should not be equal")
	}
}
