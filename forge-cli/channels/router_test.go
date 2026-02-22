package channels

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/channels"
)

func TestRouter_ForwardToA2A_Success(t *testing.T) {
	// Mock A2A server
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req a2a.JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decoding request: %v", err)
		}

		if req.Method != "tasks/send" {
			t.Errorf("expected method tasks/send, got %s", req.Method)
		}

		var params a2a.SendTaskParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			t.Fatalf("decoding params: %v", err)
		}

		if params.Message.Parts[0].Text != "hello agent" {
			t.Errorf("unexpected message text: %s", params.Message.Parts[0].Text)
		}

		task := a2a.Task{
			ID: params.ID,
			Status: a2a.TaskStatus{
				State: a2a.TaskStateCompleted,
				Message: &a2a.Message{
					Role:  a2a.MessageRoleAgent,
					Parts: []a2a.Part{a2a.NewTextPart("hello user")},
				},
			},
		}

		resp := a2a.NewResponse(req.ID, task)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
	}))
	defer srv.Close()

	router := NewRouter(srv.URL)
	event := &channels.ChannelEvent{
		Channel:     "test",
		WorkspaceID: "W123",
		UserID:      "U456",
		Message:     "hello agent",
	}

	msg, err := router.forwardToA2A(context.Background(), event)
	if err != nil {
		t.Fatalf("forwardToA2A() error: %v", err)
	}

	if msg.Role != a2a.MessageRoleAgent {
		t.Errorf("expected agent role, got %s", msg.Role)
	}

	if len(msg.Parts) != 1 || msg.Parts[0].Text != "hello user" {
		t.Errorf("unexpected response text: %v", msg.Parts)
	}
}

func TestRouter_ForwardToA2A_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req a2a.JSONRPCRequest
		json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck

		resp := a2a.NewErrorResponse(req.ID, a2a.ErrCodeInternal, "agent unavailable")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
	}))
	defer srv.Close()

	router := NewRouter(srv.URL)
	event := &channels.ChannelEvent{
		Channel:     "test",
		WorkspaceID: "W123",
		Message:     "hello",
	}

	_, err := router.forwardToA2A(context.Background(), event)
	if err == nil {
		t.Fatal("expected error for A2A error response")
	}
}

func TestRouter_ForwardToA2A_NoMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req a2a.JSONRPCRequest
		json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck

		// Task with no status message
		task := a2a.Task{
			ID:     "t1",
			Status: a2a.TaskStatus{State: a2a.TaskStateCompleted},
		}

		resp := a2a.NewResponse(req.ID, task)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
	}))
	defer srv.Close()

	router := NewRouter(srv.URL)
	event := &channels.ChannelEvent{
		Channel:     "test",
		WorkspaceID: "W123",
		Message:     "hello",
	}

	msg, err := router.forwardToA2A(context.Background(), event)
	if err != nil {
		t.Fatalf("forwardToA2A() error: %v", err)
	}

	if msg.Parts[0].Text != "(no response)" {
		t.Errorf("expected fallback message, got %q", msg.Parts[0].Text)
	}
}

func TestRouter_Handler(t *testing.T) {
	router := NewRouter("http://localhost:9999")
	handler := router.Handler()
	if handler == nil {
		t.Fatal("Handler() returned nil")
	}
}
