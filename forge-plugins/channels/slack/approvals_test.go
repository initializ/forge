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

	if _, present := payload["channel"]; present {
		t.Error("channel must be set by DeliverApproval (from the resolved id), not the builder")
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
		"type":       "block_actions",
		"trigger_id": "trig-1",
		"user":       map[string]any{"id": "U1", "username": username, "name": "Alice N"},
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
		d, uid, ch, ts, _, ok := parseApprovalInteraction(interactionJSON(approveActionID, "task-7", "alice"))
		if !ok || d.Decision != "approve" || d.TaskID != "task-7" || d.Approver != "alice" {
			t.Fatalf("got %+v ok=%v", d, ok)
		}
		if uid != "U1" {
			t.Errorf("user id = %q, want U1 (needed for email resolution)", uid)
		}
		if ch != "C1" || ts != "1700000000.000100" {
			t.Errorf("message locator wrong: ch=%q ts=%q", ch, ts)
		}
	})
	t.Run("reject carries trigger id for the modal", func(t *testing.T) {
		d, _, _, _, trigger, ok := parseApprovalInteraction(interactionJSON(rejectActionID, "task-7", "bob"))
		if !ok || d.Decision != "reject" {
			t.Fatalf("got %+v ok=%v", d, ok)
		}
		if trigger != "trig-1" {
			t.Errorf("trigger_id = %q, want trig-1 (needed to open the reason modal)", trigger)
		}
	})
	t.Run("approver falls back to name then id", func(t *testing.T) {
		d, _, _, _, _, _ := parseApprovalInteraction(interactionJSON(approveActionID, "t", "")) // no username
		if d.Approver != "Alice N" {
			t.Errorf("approver fallback = %q, want name", d.Approver)
		}
	})
	t.Run("not our button ignored", func(t *testing.T) {
		if _, _, _, _, _, ok := parseApprovalInteraction(interactionJSON("some_other_app_action", "t", "x")); ok {
			t.Error("a non-forge action_id must be ignored")
		}
	})
	t.Run("wrong type ignored", func(t *testing.T) {
		if _, _, _, _, _, ok := parseApprovalInteraction([]byte(`{"type":"view_submission"}`)); ok {
			t.Error("non-block_actions must be ignored")
		}
	})
	t.Run("empty task id ignored", func(t *testing.T) {
		if _, _, _, _, _, ok := parseApprovalInteraction(interactionJSON(approveActionID, "", "x")); ok {
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

// slackTestServer stands in for the Slack API: it resolves a fixed channel
// (#oncall → C999, private) via conversations.list and captures the
// chat.postMessage body. listCalls counts conversations.list hits (for the
// cache test).
func slackTestServer(t *testing.T, postBody *[]byte, listCalls *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/conversations.list"):
			if listCalls != nil {
				*listCalls++
			}
			_, _ = w.Write([]byte(`{"ok":true,"channels":[{"id":"C999","name":"oncall"}],"response_metadata":{"next_cursor":""}}`))
		case strings.HasSuffix(r.URL.Path, "/chat.postMessage"):
			if postBody != nil {
				*postBody, _ = io.ReadAll(r.Body)
			}
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
}

// TestDeliverApproval_PostsBlockKit: the send path resolves the target name to
// an id and posts a Block Kit message with the buttons to chat.postMessage.
func TestDeliverApproval_PostsBlockKit(t *testing.T) {
	var body []byte
	srv := slackTestServer(t, &body, nil)
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
	// #oncall resolved to its id C999 before posting.
	for _, want := range []string{`"channel":"C999"`, `"action_id":"` + approveActionID + `"`, `"value":"task-1"`, "kubectl delete pod x"} {
		if !strings.Contains(blob, want) {
			t.Errorf("posted message missing %q\n%s", want, blob)
		}
	}
}

// TestResolveChannelID covers id passthrough (no API call), name → id
// resolution (public + private), the cache, and fail-closed on not-found.
func TestResolveChannelID(t *testing.T) {
	t.Run("encoded id passes through without an API call", func(t *testing.T) {
		p := New()
		p.apiBase = "http://127.0.0.1:0" // any call would fail
		for _, id := range []string{"C0123ABC5", "G0456DEF7", "D0789GHI9"} {
			got, err := p.resolveChannelID(context.Background(), id)
			if err != nil || got != id {
				t.Errorf("resolveChannelID(%q) = (%q,%v), want passthrough", id, got, err)
			}
		}
	})

	t.Run("name resolves and caches", func(t *testing.T) {
		var calls int
		srv := slackTestServer(t, nil, &calls)
		defer srv.Close()
		p := New()
		p.apiBase = srv.URL
		p.botToken = "xoxb-test"

		for _, in := range []string{"#oncall", "oncall", "#OnCall"} { // #, bare, mixed-case
			got, err := p.resolveChannelID(context.Background(), in)
			if err != nil || got != "C999" {
				t.Fatalf("resolveChannelID(%q) = (%q,%v), want C999", in, got, err)
			}
		}
		if calls != 1 {
			t.Errorf("conversations.list called %d times, want 1 (cached)", calls)
		}
	})

	t.Run("not found fails closed", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"ok":true,"channels":[],"response_metadata":{"next_cursor":""}}`))
		}))
		defer srv.Close()
		p := New()
		p.apiBase = srv.URL
		p.botToken = "xoxb-test"
		if _, err := p.resolveChannelID(context.Background(), "#ghost"); err == nil {
			t.Fatal("expected an error for an unresolvable channel (fail closed)")
		}
	})

	t.Run("slack API error surfaces", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"ok":false,"error":"missing_scope"}`))
		}))
		defer srv.Close()
		p := New()
		p.apiBase = srv.URL
		p.botToken = "xoxb-test"
		_, err := p.resolveChannelID(context.Background(), "#oncall")
		if err == nil || !strings.Contains(err.Error(), "missing_scope") {
			t.Fatalf("expected the slack error to surface, got %v", err)
		}
	})
}

// TestDeliverApproval_EmptyTarget guards the obvious misconfig.
func TestDeliverApproval_EmptyTarget(t *testing.T) {
	if err := New().DeliverApproval(context.Background(), channels.ApprovalRequest{TaskID: "t"}); err == nil {
		t.Fatal("expected an error for an empty target channel")
	}
}

// TestResolveUserEmail covers users.info resolution (#313): success + cache,
// no-email, and missing-scope error.
func TestResolveUserEmail(t *testing.T) {
	t.Run("resolves lowercased + caches", func(t *testing.T) {
		var calls int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "/users.info") {
				calls++
				_, _ = w.Write([]byte(`{"ok":true,"user":{"profile":{"email":"Alice@Corp.com"}}}`))
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()
		p := New()
		p.apiBase = srv.URL
		p.botToken = "xoxb-test"
		for range 2 {
			email, err := p.resolveUserEmail(context.Background(), "U1")
			if err != nil || email != "alice@corp.com" {
				t.Fatalf("resolveUserEmail = (%q,%v), want alice@corp.com", email, err)
			}
		}
		if calls != 1 {
			t.Errorf("users.info called %d times, want 1 (cached)", calls)
		}
	})

	t.Run("no email fails", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"ok":true,"user":{"profile":{"email":""}}}`))
		}))
		defer srv.Close()
		p := New()
		p.apiBase = srv.URL
		p.botToken = "x"
		if _, err := p.resolveUserEmail(context.Background(), "U2"); err == nil {
			t.Error("expected an error when no email is on the profile")
		}
	})

	t.Run("missing scope surfaces", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"ok":false,"error":"missing_scope"}`))
		}))
		defer srv.Close()
		p := New()
		p.apiBase = srv.URL
		p.botToken = "x"
		if _, err := p.resolveUserEmail(context.Background(), "U3"); err == nil || !strings.Contains(err.Error(), "missing_scope") {
			t.Errorf("expected the missing_scope error, got %v", err)
		}
	})
}

// TestHandleInteractive_AttachesEmail: a click resolves the approver's email
// and attaches it to the decision passed to the runtime (#313).
func TestHandleInteractive_AttachesEmail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/users.info") {
			_, _ = w.Write([]byte(`{"ok":true,"user":{"profile":{"email":"carol@corp.com"}}}`))
			return
		}
		w.WriteHeader(http.StatusOK) // chat.update
	}))
	defer srv.Close()

	var got channels.ApprovalDecision
	p := New()
	p.apiBase = srv.URL
	p.botToken = "xoxb-test"
	p.SetApprovalResolver(func(_ context.Context, d channels.ApprovalDecision) error { got = d; return nil })

	if err := p.handleInteractive(context.Background(), interactionJSON(approveActionID, "task-9", "carol")); err != nil {
		t.Fatalf("handleInteractive: %v", err)
	}
	if got.ApproverEmail != "carol@corp.com" {
		t.Errorf("ApproverEmail = %q, want carol@corp.com", got.ApproverEmail)
	}
}

// rejectSubmissionJSON builds a view_submission payload for the reject modal.
func rejectSubmissionJSON(callbackID, meta, reason string) []byte {
	m := map[string]any{
		"type": "view_submission",
		"user": map[string]any{"id": "U1", "username": "dave"},
		"view": map[string]any{
			"callback_id":      callbackID,
			"private_metadata": meta,
			"state": map[string]any{
				"values": map[string]any{
					reasonBlockID: map[string]any{
						reasonActionID: map[string]any{"type": "plain_text_input", "value": reason},
					},
				},
			},
		},
	}
	b, _ := json.Marshal(m)
	return b
}

// TestBuildRejectModal pins the modal shape: our callback_id, the round-tripped
// private_metadata, and the required reason input.
func TestBuildRejectModal(t *testing.T) {
	raw, _ := json.Marshal(buildRejectModal(`{"task_id":"task-5"}`))
	blob := string(raw)
	for _, want := range []string{
		`"type":"modal"`,
		`"callback_id":"` + rejectCallbackID + `"`,
		`"private_metadata":"{\"task_id\":\"task-5\"}"`,
		`"block_id":"` + reasonBlockID + `"`,
		`"action_id":"` + reasonActionID + `"`,
		`"multiline":true`,
	} {
		if !strings.Contains(blob, want) {
			t.Errorf("reject modal missing %q\n%s", want, blob)
		}
	}
}

// TestParseRejectSubmission covers the reason + metadata extraction and the
// ignore paths (wrong callback, unparseable/empty metadata).
func TestParseRejectSubmission(t *testing.T) {
	meta := `{"task_id":"task-5","channel_id":"C1","msg_ts":"111.222"}`
	t.Run("extracts reason and meta", func(t *testing.T) {
		d, uid, m, ok := parseRejectSubmission(rejectSubmissionJSON(rejectCallbackID, meta, "  too risky for prod  "))
		if !ok || d.Decision != "reject" || d.TaskID != "task-5" || d.Approver != "dave" {
			t.Fatalf("got %+v ok=%v", d, ok)
		}
		if d.Note != "too risky for prod" {
			t.Errorf("note = %q, want trimmed reason", d.Note)
		}
		if uid != "U1" || m.ChannelID != "C1" || m.MsgTS != "111.222" {
			t.Errorf("locator wrong: uid=%q meta=%+v", uid, m)
		}
	})
	t.Run("wrong callback ignored", func(t *testing.T) {
		if _, _, _, ok := parseRejectSubmission(rejectSubmissionJSON("someone_elses_modal", meta, "x")); ok {
			t.Error("a foreign callback_id must be ignored")
		}
	})
	t.Run("bad metadata ignored", func(t *testing.T) {
		if _, _, _, ok := parseRejectSubmission(rejectSubmissionJSON(rejectCallbackID, "not-json", "x")); ok {
			t.Error("unparseable private_metadata must be ignored")
		}
	})
}

// TestHandleBlockAction_RejectOpensModal: a Reject click opens the reason modal
// (views.open) and does NOT resolve the deferral yet.
func TestHandleBlockAction_RejectOpensModal(t *testing.T) {
	var openedModal bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/views.open") {
			openedModal = true
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"trigger_id":"trig-1"`) {
				t.Errorf("views.open missing trigger id: %s", body)
			}
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	var resolved bool
	p := New()
	p.apiBase = srv.URL
	p.botToken = "xoxb-test"
	p.SetApprovalResolver(func(context.Context, channels.ApprovalDecision) error { resolved = true; return nil })

	if err := p.handleInteractive(context.Background(), interactionJSON(rejectActionID, "task-5", "dave")); err != nil {
		t.Fatalf("handleInteractive: %v", err)
	}
	if !openedModal {
		t.Error("expected a views.open to prompt for the reject reason")
	}
	if resolved {
		t.Error("the deferral must NOT resolve until the modal is submitted")
	}
}

// TestHandleRejectSubmission: submitting the reason modal resolves the deferral
// as a reject carrying the typed reason and the resolved approver email.
func TestHandleRejectSubmission(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/users.info") {
			_, _ = w.Write([]byte(`{"ok":true,"user":{"profile":{"email":"dave@corp.com"}}}`))
			return
		}
		w.WriteHeader(http.StatusOK) // chat.update
	}))
	defer srv.Close()

	var got channels.ApprovalDecision
	p := New()
	p.apiBase = srv.URL
	p.botToken = "xoxb-test"
	p.SetApprovalResolver(func(_ context.Context, d channels.ApprovalDecision) error { got = d; return nil })

	meta := `{"task_id":"task-5","channel_id":"C1","msg_ts":"111.222"}`
	payload := rejectSubmissionJSON(rejectCallbackID, meta, "denied: touches prod data")
	if err := p.handleInteractive(context.Background(), payload); err != nil {
		t.Fatalf("handleInteractive: %v", err)
	}
	if got.Decision != "reject" || got.TaskID != "task-5" {
		t.Fatalf("resolver got %+v", got)
	}
	if got.Note != "denied: touches prod data" {
		t.Errorf("note = %q, want the typed reason", got.Note)
	}
	if got.ApproverEmail != "dave@corp.com" {
		t.Errorf("ApproverEmail = %q, want dave@corp.com", got.ApproverEmail)
	}
}
