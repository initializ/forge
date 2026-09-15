package optimizer

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDeriveSessionID(t *testing.T) {
	// Same model + system → same id (stable across a conversation / resume).
	a := deriveSessionID(nil, []byte(`{"model":"claude-opus-4-8","system":"You are helpful","messages":[{"role":"user","content":"hi"}]}`))
	b := deriveSessionID(nil, []byte(`{"model":"claude-opus-4-8","system":"You are helpful","messages":[{"role":"user","content":"different turn, growing history"}]}`))
	if a == "" || a != b {
		t.Errorf("session id not stable across turns: %q vs %q", a, b)
	}
	// Different system prompt → different id.
	c := deriveSessionID(nil, []byte(`{"model":"claude-opus-4-8","system":"You are a poet","messages":[]}`))
	if c == a {
		t.Errorf("different system prompt should yield a different session id")
	}
	// System as block array is supported.
	d := deriveSessionID(nil, []byte(`{"model":"claude-opus-4-8","system":[{"type":"text","text":"You are helpful"}],"messages":[]}`))
	if d == "" {
		t.Errorf("array-form system should still derive an id")
	}
	// Explicit header overrides.
	h := http.Header{}
	h.Set(SessionHeader, "sess_custom")
	if got := deriveSessionID(h, []byte(`{"model":"x","system":"y"}`)); got != "sess_custom" {
		t.Errorf("header override not honored: %q", got)
	}
}

// TestStats_KeyedBySession sends two requests with different system prompts
// through the proxy and asserts /stats breaks usage down per session.
func TestStats_KeyedBySession(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"claude-opus-4-8","usage":{"input_tokens":40,"output_tokens":10}}`)
	}))
	defer upstream.Close()

	s, _ := newServerTo(t, upstream)
	front := httptest.NewServer(s.Handler())
	defer front.Close()

	post := func(system string) {
		body := `{"model":"claude-opus-4-8","system":"` + system + `","messages":[{"role":"user","content":"hi"}]}`
		resp, err := http.Post(front.URL+"/v1/messages", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		_, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
	}
	post("agent A")
	post("agent A") // same session
	post("agent B") // different session

	// Fetch /stats and assert two sessions, with A having 2 requests, B having 1.
	resp, err := http.Get(front.URL + "/stats")
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	defer resp.Body.Close()
	var snap Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if len(snap.Sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d: %+v", len(snap.Sessions), snap.Sessions)
	}
	idA := deriveSessionID(nil, []byte(`{"model":"claude-opus-4-8","system":"agent A"}`))
	idB := deriveSessionID(nil, []byte(`{"model":"claude-opus-4-8","system":"agent B"}`))
	if snap.Sessions[idA].Requests != 2 {
		t.Errorf("session A requests = %d, want 2", snap.Sessions[idA].Requests)
	}
	if snap.Sessions[idB].Requests != 1 {
		t.Errorf("session B requests = %d, want 1", snap.Sessions[idB].Requests)
	}
	if snap.Sessions[idA].InputTokens != 80 {
		t.Errorf("session A input tokens = %d, want 80", snap.Sessions[idA].InputTokens)
	}

	// The ?session= filter returns just that session.
	fresp, _ := http.Get(front.URL + "/stats?session=" + idB)
	defer fresp.Body.Close()
	var filtered struct {
		Session string       `json:"session"`
		Stats   SessionStats `json:"stats"`
		Recent  []Report     `json:"recent"`
	}
	_ = json.NewDecoder(fresp.Body).Decode(&filtered)
	if filtered.Session != idB || filtered.Stats.Requests != 1 || len(filtered.Recent) != 1 {
		t.Errorf("filtered stats wrong: %+v", filtered)
	}
}
