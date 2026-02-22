package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/agentspec"
)

func TestMockRuntime_Invoke(t *testing.T) {
	tools := []agentspec.ToolSpec{
		{Name: "search", Description: "Search the web"},
		{Name: "calculator", Description: "Do math"},
	}

	rt := NewMockRuntime(tools)

	msg := &a2a.Message{
		Role:  a2a.MessageRoleUser,
		Parts: []a2a.Part{a2a.NewTextPart("hello world")},
	}

	task, err := rt.Invoke(context.Background(), "t-1", msg)
	if err != nil {
		t.Fatalf("Invoke error: %v", err)
	}

	if task.ID != "t-1" {
		t.Errorf("id: got %q", task.ID)
	}
	if task.Status.State != a2a.TaskStateCompleted {
		t.Errorf("state: got %q", task.Status.State)
	}

	// Check mock text contains input
	if task.Status.Message == nil {
		t.Fatal("expected status message")
	}
	text := task.Status.Message.Parts[0].Text
	if !strings.Contains(text, "hello world") {
		t.Errorf("response should contain input, got: %q", text)
	}
	if !strings.Contains(text, "search") {
		t.Errorf("response should mention tools, got: %q", text)
	}
}

func TestMockRuntime_Stream(t *testing.T) {
	rt := NewMockRuntime(nil)

	msg := &a2a.Message{
		Role:  a2a.MessageRoleUser,
		Parts: []a2a.Part{a2a.NewTextPart("test")},
	}

	ch, err := rt.Stream(context.Background(), "t-2", msg)
	if err != nil {
		t.Fatalf("Stream error: %v", err)
	}

	count := 0
	for task := range ch {
		count++
		if task.ID != "t-2" {
			t.Errorf("id: got %q", task.ID)
		}
	}
	if count != 1 {
		t.Errorf("expected 1 event, got %d", count)
	}
}

func TestMockRuntime_NoOps(t *testing.T) {
	rt := NewMockRuntime(nil)

	if err := rt.Start(context.Background()); err != nil {
		t.Errorf("Start: %v", err)
	}
	if err := rt.Stop(); err != nil {
		t.Errorf("Stop: %v", err)
	}
	if err := rt.Restart(context.Background()); err != nil {
		t.Errorf("Restart: %v", err)
	}
	if !rt.Healthy(context.Background()) {
		t.Error("expected healthy=true")
	}
}

func TestMockRuntime_NoTextInput(t *testing.T) {
	rt := NewMockRuntime(nil)

	msg := &a2a.Message{
		Role:  a2a.MessageRoleUser,
		Parts: []a2a.Part{a2a.NewDataPart(map[string]string{"key": "val"})},
	}

	task, err := rt.Invoke(context.Background(), "t-3", msg)
	if err != nil {
		t.Fatalf("Invoke error: %v", err)
	}

	text := task.Status.Message.Parts[0].Text
	if !strings.Contains(text, "<no text>") {
		t.Errorf("expected '<no text>' for non-text input, got: %q", text)
	}
}
