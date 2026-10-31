package slack

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// repliesServer fakes conversations.replies, returning the given messages JSON
// (the value of the "messages" array). It records the query so tests can assert
// the channel/ts were passed.
func repliesServer(t *testing.T, messagesJSON string, gotQuery *url.Values) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gotQuery != nil {
			*gotQuery = r.URL.Query()
		}
		if r.URL.Path != "/conversations.replies" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"ok":true,"messages":` + messagesJSON + `}`))
	}))
}

func newParticipationPlugin(t *testing.T, apiBase string) *Plugin {
	t.Helper()
	p := New()
	p.botToken = "xoxb-test"
	p.apiBase = apiBase
	p.botUserID = "UBOT"
	return p
}

func TestBotParticipatesInThread(t *testing.T) {
	tests := []struct {
		name     string
		messages string
		want     bool
	}{
		{
			name:     "bot authored a reply",
			messages: `[{"user":"UALICE","text":"hi"},{"user":"UBOT","text":"on it"}]`,
			want:     true,
		},
		{
			name:     "bot mentioned in a thread message",
			messages: `[{"user":"UALICE","text":"hey <@UBOT> look"}]`,
			want:     true,
		},
		{
			name:     "bot neither posted nor mentioned",
			messages: `[{"user":"UALICE","text":"hi"},{"user":"UBOB","text":"<@UCAROL> ping"}]`,
			want:     false,
		},
		{
			name:     "empty thread",
			messages: `[]`,
			want:     false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var q url.Values
			srv := repliesServer(t, tt.messages, &q)
			defer srv.Close()
			p := newParticipationPlugin(t, srv.URL)

			got := p.botParticipatesInThread(context.Background(), "C123", "1699.0001")
			if got != tt.want {
				t.Errorf("botParticipatesInThread = %v, want %v", got, tt.want)
			}
			if q.Get("channel") != "C123" || q.Get("ts") != "1699.0001" {
				t.Errorf("conversations.replies query = %v, want channel=C123 ts=1699.0001", q)
			}
		})
	}
}

// Fail-closed: a transport/API failure or missing inputs must return false so a
// transient error can't reintroduce the over-answering bug (#541).
func TestBotParticipatesInThread_FailsClosed(t *testing.T) {
	// API returns an error payload.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":false,"error":"channel_not_found"}`))
	}))
	defer srv.Close()
	p := newParticipationPlugin(t, srv.URL)
	if p.botParticipatesInThread(context.Background(), "C123", "1699.0001") {
		t.Error("API error must fail closed (false)")
	}

	// Unreachable endpoint → transport error → false.
	p2 := newParticipationPlugin(t, "http://127.0.0.1:0")
	if p2.botParticipatesInThread(context.Background(), "C123", "1699.0001") {
		t.Error("transport error must fail closed (false)")
	}

	// Missing inputs → false without any call.
	p3 := newParticipationPlugin(t, "http://invalid")
	if p3.botParticipatesInThread(context.Background(), "", "1699") {
		t.Error("empty channel must return false")
	}
	if p3.botParticipatesInThread(context.Background(), "C1", "") {
		t.Error("empty threadTS must return false")
	}
	p3.botUserID = ""
	if p3.botParticipatesInThread(context.Background(), "C1", "1699") {
		t.Error("unknown bot user id must return false")
	}
}
