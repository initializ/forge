package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/a2a"
)

func TestStubExecutor_Execute(t *testing.T) {
	exec := NewStubExecutor("custom")
	task := &a2a.Task{ID: "t-1"}
	msg := &a2a.Message{
		Role:  a2a.MessageRoleUser,
		Parts: []a2a.Part{a2a.NewTextPart("test")},
	}

	_, err := exec.Execute(context.Background(), task, msg)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "custom") {
		t.Errorf("error should contain framework name, got: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "not configured") {
		t.Errorf("error should mention not configured, got: %q", err.Error())
	}
}

func TestStubExecutor_ExecuteStream(t *testing.T) {
	exec := NewStubExecutor("langchain")
	task := &a2a.Task{ID: "t-1"}
	msg := &a2a.Message{
		Role:  a2a.MessageRoleUser,
		Parts: []a2a.Part{a2a.NewTextPart("test")},
	}

	_, err := exec.ExecuteStream(context.Background(), task, msg)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "langchain") {
		t.Errorf("error should contain framework name, got: %q", err.Error())
	}
}

func TestStubExecutor_Close(t *testing.T) {
	exec := NewStubExecutor("custom")
	if err := exec.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}
