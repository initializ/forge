package builtins

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/runtime"
)

func TestSuggestActionsTool_RecordsToCollector(t *testing.T) {
	tool := &suggestActionsTool{}
	c := runtime.NewActionsCollector()
	ctx := runtime.WithActionsCollector(context.Background(), c)

	out, err := tool.Execute(ctx, json.RawMessage(`{"actions":[
		{"label":"Deploy to staging","prompt":"deploy to staging"},
		{"label":"Show the diff","prompt":"show me the diff"}
	]}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := c.Actions()
	if len(got) != 2 || got[0].Label != "Deploy to staging" || got[1].Prompt != "show me the diff" {
		t.Fatalf("actions not recorded: %+v", got)
	}
	if !strings.Contains(out, "Deploy to staging") {
		t.Errorf("result should name the offered actions, got %q", out)
	}
}

func TestSuggestActionsTool_Validation(t *testing.T) {
	tool := &suggestActionsTool{}
	ctx := runtime.WithActionsCollector(context.Background(), runtime.NewActionsCollector())

	if _, err := tool.Execute(ctx, json.RawMessage(`{"actions":[]}`)); err == nil {
		t.Error("empty actions should error")
	}
	// All entries invalid (no label) → error.
	if _, err := tool.Execute(ctx, json.RawMessage(`{"actions":[{"label":"","prompt":"x"}]}`)); err == nil {
		t.Error("all-invalid actions should error")
	}
	if _, err := tool.Execute(ctx, json.RawMessage(`not json`)); err == nil {
		t.Error("malformed input should error")
	}
}

func TestSuggestActionsTool_NoCollectorGraceful(t *testing.T) {
	tool := &suggestActionsTool{}
	// No collector in context (ran outside an Execute turn): must not error, so
	// the agent loop isn't broken — just reports the actions weren't surfaced.
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"actions":[{"label":"x","prompt":"y"}]}`))
	if err != nil {
		t.Fatalf("should not error without a collector: %v", err)
	}
	if !strings.Contains(out, "not attached") {
		t.Errorf("expected a 'not attached' note, got %q", out)
	}
}
