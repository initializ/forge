package slack

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/initializ/forge/forge-core/channels"
)

// TestBuildApprovalPayload pins the Block Kit shape (#310): the target channel,
// an accessible text fallback, and two buttons whose action_ids + value (task
// id) drive resolution.
func TestBuildApprovalPayload(t *testing.T) {
	payload := buildApprovalPayload(channels.ApprovalRequest{
		TaskID:  "task-42",
		Tool:    "atlassian__jira_create_issue",
		Context: "create issue in PROJ",
		Timeout: 15 * time.Minute,
		Target:  "#oncall",
	})

	if payload["channel"] != "#oncall" {
		t.Errorf("channel = %v, want #oncall", payload["channel"])
	}
	if txt, _ := payload["text"].(string); !strings.Contains(txt, "atlassian__jira_create_issue") {
		t.Errorf("fallback text missing tool: %q", txt)
	}

	// Re-marshal + walk to find the two buttons (avoids brittle type assertions).
	raw, _ := json.Marshal(payload)
	blob := string(raw)
	for _, want := range []string{
		`"block_id":"forge_defer:task-42"`,
		`"action_id":"` + approveActionID + `"`,
		`"action_id":"` + rejectActionID + `"`,
		`"value":"task-42"`,
		`"style":"primary"`,
		`"style":"danger"`,
		"create issue in PROJ",
	} {
		if !strings.Contains(blob, want) {
			t.Errorf("block kit payload missing %q\n%s", want, blob)
		}
	}
}

func interactionJSON(actionID, taskID, username string) []byte {
	m := map[string]any{
		"type": "block_actions",
		"user": map[string]any{"id": "U1", "username": username, "name": "Alice N"},
		"actions": []any{map[string]any{
			"action_id": actionID, "value": taskID, "type": "button",
		}},
		"channel": map[string]any{"id": "C1"},
		"message": map[string]any{"ts": "1700000000.000100"},
	}
	b, _ := json.Marshal(m)
	return b
}

// TestParseApprovalInteraction covers the block_actions → decision mapping and
// the ignore paths (not our buttons / wrong shape).
func TestParseApprovalInteraction(t *testing.T) {
	t.Run("approve", func(t *testing.T) {
		d, ch, ts, ok := parseApprovalInteraction(interactionJSON(approveActionID, "task-7", "alice"))
		if !ok || d.Decision != "approve" || d.TaskID != "task-7" || d.Approver != "alice" {
			t.Fatalf("got %+v ok=%v", d, ok)
		}
		if ch != "C1" || ts != "1700000000.000100" {
			t.Errorf("message locator wrong: ch=%q ts=%q", ch, ts)
		}
	})
	t.Run("reject", func(t *testing.T) {
		d, _, _, ok := parseApprovalInteraction(interactionJSON(rejectActionID, "task-7", "bob"))
		if !ok || d.Decision != "reject" {
			t.Fatalf("got %+v ok=%v", d, ok)
		}
	})
	t.Run("approver falls back to name then id", func(t *testing.T) {
		d, _, _, _ := parseApprovalInteraction(interactionJSON(approveActionID, "t", "")) // no username
		if d.Approver != "Alice N" {
			t.Errorf("approver fallback = %q, want name", d.Approver)
		}
	})
	t.Run("not our button ignored", func(t *testing.T) {
		if _, _, _, ok := parseApprovalInteraction(interactionJSON("some_other_app_action", "t", "x")); ok {
			t.Error("a non-forge action_id must be ignored")
		}
	})
	t.Run("wrong type ignored", func(t *testing.T) {
		if _, _, _, ok := parseApprovalInteraction([]byte(`{"type":"view_submission"}`)); ok {
			t.Error("non-block_actions must be ignored")
		}
	})
	t.Run("empty task id ignored", func(t *testing.T) {
		if _, _, _, ok := parseApprovalInteraction(interactionJSON(approveActionID, "", "x")); ok {
			t.Error("empty value (task id) must be ignored")
		}
	})
}

// TestHandleInteractive_ResolvesAndUpdates: a button click invokes the wired
// resolver with the right decision, then best-effort updates the message.
func TestHandleInteractive_ResolvesAndUpdates(t *testing.T) {
	var updated bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/chat.update") {
			updated = true
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	var got channels.ApprovalDecision
	p := New()
	p.apiBase = srv.URL
	p.botToken = "xoxb-test"
	p.SetApprovalResolver(func(_ context.Context, d channels.ApprovalDecision) error {
		got = d
		return nil
	})

	if err := p.handleInteractive(context.Background(), interactionJSON(approveActionID, "task-9", "carol")); err != nil {
		t.Fatalf("handleInteractive: %v", err)
	}
	if got.TaskID != "task-9" || got.Decision != "approve" || got.Approver != "carol" {
		t.Errorf("resolver got %+v", got)
	}
	if !updated {
		t.Error("expected a chat.update to replace the buttons with the outcome")
	}
}

// TestHandleInteractive_NoResolver errors (surfaced/logged by the caller) so a
// misconfiguration is visible rather than silently dropping the approval.
func TestHandleInteractive_NoResolver(t *testing.T) {
	p := New()
	if err := p.handleInteractive(context.Background(), interactionJSON(approveActionID, "t", "x")); err == nil {
		t.Fatal("expected an error when no resolver is wired")
	}
}

// TestHandleInteractive_IgnoresForeign returns nil (and doesn't call the
// resolver) for interactions that aren't ours.
func TestHandleInteractive_IgnoresForeign(t *testing.T) {
	called := false
	p := New()
	p.SetApprovalResolver(func(context.Context, channels.ApprovalDecision) error { called = true; return nil })
	if err := p.handleInteractive(context.Background(), []byte(`{"type":"shortcut"}`)); err != nil {
		t.Fatalf("foreign interaction should be a no-op, got %v", err)
	}
	if called {
		t.Error("resolver must not fire for a foreign interaction")
	}
}

// TestDeliverApproval_PostsBlockKit: the send path posts a Block Kit message
// with the buttons to chat.postMessage.
func TestDeliverApproval_PostsBlockKit(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/chat.postMessage") {
			body, _ = io.ReadAll(r.Body)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := New()
	p.apiBase = srv.URL
	p.botToken = "xoxb-test"

	err := p.DeliverApproval(context.Background(), channels.ApprovalRequest{
		TaskID: "task-1", Tool: "cli_execute", Context: "kubectl delete pod x", Target: "#oncall",
	})
	if err != nil {
		t.Fatalf("DeliverApproval: %v", err)
	}
	blob := string(body)
	for _, want := range []string{`"channel":"#oncall"`, `"action_id":"` + approveActionID + `"`, `"value":"task-1"`, "kubectl delete pod x"} {
		if !strings.Contains(blob, want) {
			t.Errorf("posted message missing %q\n%s", want, blob)
		}
	}
}

// TestDeliverApproval_EmptyTarget guards the obvious misconfig.
func TestDeliverApproval_EmptyTarget(t *testing.T) {
	if err := New().DeliverApproval(context.Background(), channels.ApprovalRequest{TaskID: "t"}); err == nil {
		t.Fatal("expected an error for an empty target channel")
	}
}
