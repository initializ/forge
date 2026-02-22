package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/agentspec"
)

func TestMockExecutor_Execute(t *testing.T) {
	tools := []agentspec.ToolSpec{
		{Name: "search", Description: "Search the web"},
		{Name: "calculator", Description: "Do math"},
	}

	exec := NewMockExecutor(tools)
	task := &a2a.Task{ID: "t-1"}
	msg := &a2a.Message{
		Role:  a2a.MessageRoleUser,
		Parts: []a2a.Part{a2a.NewTextPart("hello world")},
	}

	resp, err := exec.Execute(context.Background(), task, msg)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if resp.Role != a2a.MessageRoleAgent {
		t.Errorf("role: got %q, want %q", resp.Role, a2a.MessageRoleAgent)
	}

	text := resp.Parts[0].Text
	if !strings.Contains(text, "hello world") {
		t.Errorf("response should contain input, got: %q", text)
	}
	if !strings.Contains(text, "search") {
		t.Errorf("response should mention tools, got: %q", text)
	}
	if !strings.Contains(text, "calculator") {
		t.Errorf("response should mention all tools, got: %q", text)
	}
}

func TestMockExecutor_ExecuteNoTools(t *testing.T) {
	exec := NewMockExecutor(nil)
	task := &a2a.Task{ID: "t-2"}
	msg := &a2a.Message{
		Role:  a2a.MessageRoleUser,
		Parts: []a2a.Part{a2a.NewTextPart("test")},
	}

	resp, err := exec.Execute(context.Background(), task, msg)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	text := resp.Parts[0].Text
	if strings.Contains(text, "available tools") {
		t.Errorf("should not mention tools when none, got: %q", text)
	}
}

func TestMockExecutor_ExecuteStream(t *testing.T) {
	exec := NewMockExecutor(nil)
	task := &a2a.Task{ID: "t-3"}
	msg := &a2a.Message{
		Role:  a2a.MessageRoleUser,
		Parts: []a2a.Part{a2a.NewTextPart("test")},
	}

	ch, err := exec.ExecuteStream(context.Background(), task, msg)
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	count := 0
	for resp := range ch {
		count++
		if resp.Role != a2a.MessageRoleAgent {
			t.Errorf("role: got %q", resp.Role)
		}
	}
	if count != 1 {
		t.Errorf("expected 1 message, got %d", count)
	}
}

func TestMockExecutor_Close(t *testing.T) {
	exec := NewMockExecutor(nil)
	if err := exec.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}
