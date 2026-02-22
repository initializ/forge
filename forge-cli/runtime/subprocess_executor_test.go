package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/a2a"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

func TestSubprocessExecutor_Execute(t *testing.T) {
	// Create a mock A2A server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req a2a.JSONRPCRequest
		json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck

		task := &a2a.Task{
			ID: "t-1",
			Status: a2a.TaskStatus{
				State: a2a.TaskStateCompleted,
				Message: &a2a.Message{
					Role:  a2a.MessageRoleAgent,
					Parts: []a2a.Part{a2a.NewTextPart("subprocess response")},
				},
			},
		}

		resp := a2a.NewResponse(req.ID, task)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
	}))
	defer ts.Close()

	// Parse port from test server URL (http://127.0.0.1:PORT)
	parts := strings.Split(ts.URL, ":")
	port, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}

	rt := &SubprocessRuntime{
		internalPort: port,
		logger:       coreruntime.NewJSONLogger(nil, false),
	}

	exec := NewSubprocessExecutor(rt)
	task := &a2a.Task{ID: "t-1"}
	msg := &a2a.Message{
		Role:  a2a.MessageRoleUser,
		Parts: []a2a.Part{a2a.NewTextPart("hello")},
	}

	resp, err := exec.Execute(context.Background(), task, msg)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.Parts[0].Text != "subprocess response" {
		t.Errorf("text: got %q", resp.Parts[0].Text)
	}
}

func TestSubprocessExecutor_ExecuteStream(t *testing.T) {
	// Create a mock A2A server that returns a JSON response (non-SSE)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req a2a.JSONRPCRequest
		json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck

		task := &a2a.Task{
			ID: "t-2",
			Status: a2a.TaskStatus{
				State: a2a.TaskStateCompleted,
				Message: &a2a.Message{
					Role:  a2a.MessageRoleAgent,
					Parts: []a2a.Part{a2a.NewTextPart("stream response")},
				},
			},
		}

		resp := a2a.NewResponse(req.ID, task)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
	}))
	defer ts.Close()

	parts := strings.Split(ts.URL, ":")
	port, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}

	rt := &SubprocessRuntime{
		internalPort: port,
		logger:       coreruntime.NewJSONLogger(nil, false),
	}

	exec := NewSubprocessExecutor(rt)
	task := &a2a.Task{ID: "t-2"}
	msg := &a2a.Message{
		Role:  a2a.MessageRoleUser,
		Parts: []a2a.Part{a2a.NewTextPart("hello")},
	}

	ch, err := exec.ExecuteStream(context.Background(), task, msg)
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	count := 0
	for resp := range ch {
		count++
		if resp.Parts[0].Text != "stream response" {
			t.Errorf("text: got %q", resp.Parts[0].Text)
		}
	}
	if count != 1 {
		t.Errorf("expected 1 message, got %d", count)
	}
}

func TestSubprocessExecutor_Close(t *testing.T) {
	rt := &SubprocessRuntime{
		logger: coreruntime.NewJSONLogger(nil, false),
	}
	exec := NewSubprocessExecutor(rt)
	if err := exec.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}
