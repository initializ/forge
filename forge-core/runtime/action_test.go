package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/llm"
)

// finalizeResponse must attach collected actions to the response metadata, and
// leave metadata nil when none were offered (the common case).
func TestFinalizeResponse_AttachesActions(t *testing.T) {
	e := NewLLMExecutor(LLMExecutorConfig{})

	// No actions → nil metadata.
	plain := e.finalizeResponse(WithActionsCollector(context.Background(), NewActionsCollector()), llm.ChatMessage{Role: llm.RoleAssistant, Content: "hi"})
	if plain.Metadata != nil {
		t.Errorf("no actions should leave metadata nil, got %+v", plain.Metadata)
	}

	// Actions offered → attached under MetadataKeyActions.
	c := NewActionsCollector()
	c.Set([]Action{{Label: "Deploy", Prompt: "deploy"}})
	ctx := WithActionsCollector(context.Background(), c)
	out := e.finalizeResponse(ctx, llm.ChatMessage{Role: llm.RoleAssistant, Content: "done"})
	got := ActionsFromMetadata(out.Metadata)
	if len(got) != 1 || got[0].Label != "Deploy" || got[0].Prompt != "deploy" {
		t.Errorf("actions not attached to response metadata: %+v", out.Metadata)
	}
}

func TestActionsCollector_SetValidatesAndCaps(t *testing.T) {
	c := NewActionsCollector()
	c.Set([]Action{
		{Label: " Deploy ", Prompt: " deploy to staging "}, // trimmed
		{Label: "", Prompt: "x"},                           // dropped (no label)
		{Label: "y", Prompt: ""},                           // dropped (no prompt)
	})
	got := c.Actions()
	if len(got) != 1 {
		t.Fatalf("got %d actions, want 1 (empties dropped): %+v", len(got), got)
	}
	if got[0].Label != "Deploy" || got[0].Prompt != "deploy to staging" {
		t.Errorf("not trimmed: %+v", got[0])
	}

	// Over-long label/prompt are truncated to the Slack bounds.
	c.Set([]Action{{Label: strings.Repeat("L", 200), Prompt: strings.Repeat("P", 5000)}})
	got = c.Actions()
	if len(got[0].Label) != MaxActionLabelLen || len(got[0].Prompt) != MaxActionPromptLen {
		t.Errorf("not capped: label=%d prompt=%d", len(got[0].Label), len(got[0].Prompt))
	}

	// More than MaxActions are capped.
	many := make([]Action, MaxActions+5)
	for i := range many {
		many[i] = Action{Label: "l", Prompt: "p"}
	}
	c.Set(many)
	if len(c.Actions()) != MaxActions {
		t.Errorf("got %d, want cap of %d", len(c.Actions()), MaxActions)
	}

	// Latest Set wins (replace, not append).
	c.Set([]Action{{Label: "only", Prompt: "one"}})
	if a := c.Actions(); len(a) != 1 || a[0].Label != "only" {
		t.Errorf("Set should replace, got %+v", a)
	}
}

func TestActionsCollector_NilSafe(t *testing.T) {
	var c *ActionsCollector
	c.Set([]Action{{Label: "x", Prompt: "y"}}) // no panic
	if c.Actions() != nil {
		t.Error("nil collector Actions() must be nil")
	}
	if ActionsCollectorFromContext(context.Background()) != nil {
		t.Error("no collector in ctx must return nil")
	}
}

func TestWithActionsCollector_RoundTrip(t *testing.T) {
	c := NewActionsCollector()
	ctx := WithActionsCollector(context.Background(), c)
	got := ActionsCollectorFromContext(ctx)
	if got != c {
		t.Fatal("collector not round-tripped through context")
	}
}

func TestActionsFromMetadata(t *testing.T) {
	// Native []Action (in-process delivery).
	meta := map[string]any{MetadataKeyActions: []Action{{Label: "A", Prompt: "a"}}}
	if got := ActionsFromMetadata(meta); len(got) != 1 || got[0].Label != "A" {
		t.Errorf("native []Action not decoded: %+v", got)
	}

	// []any of maps (JSON round-trip shape).
	meta = map[string]any{MetadataKeyActions: []any{
		map[string]any{"label": "B", "prompt": "b"},
		map[string]any{"label": "", "prompt": "skip"}, // dropped
		"not-a-map", // dropped
	}}
	got := ActionsFromMetadata(meta)
	if len(got) != 1 || got[0].Label != "B" || got[0].Prompt != "b" {
		t.Errorf("[]any shape not decoded: %+v", got)
	}

	// Absent / nil / wrong type → nil.
	if ActionsFromMetadata(nil) != nil {
		t.Error("nil metadata must yield nil")
	}
	if ActionsFromMetadata(map[string]any{"other": 1}) != nil {
		t.Error("absent key must yield nil")
	}
	if ActionsFromMetadata(map[string]any{MetadataKeyActions: 42}) != nil {
		t.Error("wrong-typed value must yield nil")
	}
}
