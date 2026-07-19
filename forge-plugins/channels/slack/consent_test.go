package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/initializ/forge/forge-core/channels"
)

// mockSlack serves the Slack API calls the consent flow makes, recording the
// last chat.postMessage body. lookupByEmail → user id, conversations.open → DM.
func mockSlack(t *testing.T, postBody *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/users.lookupByEmail"):
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "user": map[string]any{"id": "U123"}})
		case strings.HasSuffix(r.URL.Path, "/conversations.open"):
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "channel": map[string]any{"id": "D999"}})
		case strings.HasSuffix(r.URL.Path, "/chat.postMessage"):
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			if postBody != nil {
				*postBody = b
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		}
	}))
}

func consentPrompt() channels.ConsentPrompt {
	return channels.ConsentPrompt{
		Subject: "alice@corp.com", Server: "atlassian",
		AuthorizeURL: "https://agent.example/mcp/oauth/start?state=xyz",
		Deadline:     time.Now().Add(time.Hour),
	}
}

func TestBuildConsentPayload(t *testing.T) {
	t.Run("connect URL button, no cancel", func(t *testing.T) {
		p := buildConsentPayload(consentPrompt(), false)
		blocks := p["blocks"].([]any)
		actions := blocks[1].(map[string]any)["elements"].([]any)
		if len(actions) != 1 {
			t.Fatalf("want 1 action (Connect), got %d", len(actions))
		}
		btn := actions[0].(map[string]any)
		if btn["url"] != "https://agent.example/mcp/oauth/start?state=xyz" {
			t.Errorf("connect button url = %v", btn["url"])
		}
		if btn["action_id"] != consentConnectActionID {
			t.Errorf("connect action_id = %v", btn["action_id"])
		}
	})
	t.Run("cancel button present when canceler wired", func(t *testing.T) {
		p := buildConsentPayload(consentPrompt(), true)
		actions := p["blocks"].([]any)[1].(map[string]any)["elements"].([]any)
		if len(actions) != 2 {
			t.Fatalf("want Connect + Cancel, got %d", len(actions))
		}
		cancel := actions[1].(map[string]any)
		if cancel["action_id"] != consentCancelActionID {
			t.Fatalf("cancel action_id = %v", cancel["action_id"])
		}
		var v consentCancelValue
		if err := json.Unmarshal([]byte(cancel["value"].(string)), &v); err != nil {
			t.Fatalf("cancel value not JSON: %v", err)
		}
		if v.Subject != "alice@corp.com" || v.Server != "atlassian" {
			t.Errorf("cancel value = %+v", v)
		}
	})
}

func TestDeliverConsent_DMByEmail(t *testing.T) {
	var body map[string]any
	srv := mockSlack(t, &body)
	defer srv.Close()

	p := New()
	p.apiBase = srv.URL
	p.botToken = "xoxb-test"

	if err := p.DeliverConsent(context.Background(), consentPrompt()); err != nil {
		t.Fatalf("DeliverConsent: %v", err)
	}
	if body["channel"] != "D999" {
		t.Errorf("posted to channel %v, want the opened DM D999", body["channel"])
	}
	if _, hasThread := body["thread_ts"]; hasThread {
		t.Error("a cold DM must not set thread_ts")
	}
	// Caches: a second delivery reuses the resolved id + DM (still works).
	if err := p.DeliverConsent(context.Background(), consentPrompt()); err != nil {
		t.Fatalf("second DeliverConsent: %v", err)
	}
}

func TestDeliverConsent_OriginThread(t *testing.T) {
	var body map[string]any
	lookupHit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/users.lookupByEmail") {
			lookupHit = true
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/chat.postMessage") {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	p := New()
	p.apiBase = srv.URL
	p.botToken = "xoxb-test"

	req := consentPrompt()
	req.Origin = &channels.ChannelOrigin{Adapter: "slack", Channel: "C555", ThreadTS: "1699.0001"}
	if err := p.DeliverConsent(context.Background(), req); err != nil {
		t.Fatalf("DeliverConsent: %v", err)
	}
	if body["channel"] != "C555" || body["thread_ts"] != "1699.0001" {
		t.Errorf("origin delivery posted to %v thread %v, want C555/1699.0001", body["channel"], body["thread_ts"])
	}
	if lookupHit {
		t.Error("origin delivery must not resolve the email")
	}
}

// cancelSlack serves users.info returning clickerEmail, so the clicker-identity
// guard can be exercised.
func cancelSlack(t *testing.T, clickerEmail string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/users.info") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true, "user": map[string]any{"profile": map[string]any{"email": clickerEmail}},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
}

func cancelPayload(t *testing.T) []byte {
	t.Helper()
	val, _ := json.Marshal(consentCancelValue{Subject: "alice@corp.com", Server: "atlassian"})
	payload, _ := json.Marshal(map[string]any{
		"type":    "block_actions",
		"user":    map[string]any{"id": "U1"},
		"channel": map[string]any{"id": "D999"},
		"message": map[string]any{"ts": "1699.1"},
		"actions": []any{map[string]any{"action_id": consentCancelActionID, "value": string(val)}},
	})
	return payload
}

func TestHandleConsentCancel_InvokesCanceler(t *testing.T) {
	srv := cancelSlack(t, "alice@corp.com") // clicker IS the subject
	defer srv.Close()

	var gotSubject, gotServer string
	p := New()
	p.apiBase = srv.URL
	p.botToken = "xoxb-test"
	p.SetConsentCanceler(func(_ context.Context, subject, server string) error {
		gotSubject, gotServer = subject, server
		return nil
	})

	// Routed through the shared block-action dispatch.
	if err := p.handleBlockAction(context.Background(), cancelPayload(t)); err != nil {
		t.Fatalf("handleBlockAction: %v", err)
	}
	if gotSubject != "alice@corp.com" || gotServer != "atlassian" {
		t.Errorf("canceler got %q/%q", gotSubject, gotServer)
	}
}

// A different user clicking Cancel (only reachable once origin-thread delivery
// lands) must NOT cancel the subject's parked call.
func TestHandleConsentCancel_RejectsNonSubject(t *testing.T) {
	srv := cancelSlack(t, "mallory@corp.com") // clicker is NOT the subject
	defer srv.Close()

	called := false
	p := New()
	p.apiBase = srv.URL
	p.botToken = "xoxb-test"
	p.SetConsentCanceler(func(_ context.Context, _, _ string) error {
		called = true
		return nil
	})

	if err := p.handleBlockAction(context.Background(), cancelPayload(t)); err != nil {
		t.Fatalf("handleBlockAction: %v", err)
	}
	if called {
		t.Error("a non-subject Cancel click must not cancel the parked call")
	}
}

// A non-consent block action (e.g. an approval button) must not be swallowed by
// the consent-cancel handler.
func TestHandleConsentCancel_IgnoresOthers(t *testing.T) {
	if _, _, _, _, _, ok := parseConsentCancel([]byte(`{"type":"block_actions","actions":[{"action_id":"forge_defer_approve","value":"task-1"}]}`)); ok {
		t.Error("an approval action must not parse as a consent cancel")
	}
}
