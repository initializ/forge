package slack

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/channels"
)

func TestInit_RequiresAppToken(t *testing.T) {
	p := New()
	err := p.Init(channels.ChannelConfig{
		Adapter: "slack",
		Settings: map[string]string{
			"bot_token": "xoxb-test",
		},
	})
	if err == nil {
		t.Fatal("expected error when app_token is missing")
	}
	if !strings.Contains(err.Error(), "app_token") {
		t.Errorf("error = %q, want mention of app_token", err)
	}
}

func TestInit_RequiresBotToken(t *testing.T) {
	p := New()
	err := p.Init(channels.ChannelConfig{
		Adapter: "slack",
		Settings: map[string]string{
			"app_token": "xapp-test",
		},
	})
	if err == nil {
		t.Fatal("expected error when bot_token is missing")
	}
	if !strings.Contains(err.Error(), "bot_token") {
		t.Errorf("error = %q, want mention of bot_token", err)
	}
}

func TestInit_Success(t *testing.T) {
	p := New()
	err := p.Init(channels.ChannelConfig{
		Adapter: "slack",
		Settings: map[string]string{
			"app_token": "xapp-test",
			"bot_token": "xoxb-test",
		},
	})
	if err != nil {
		t.Fatalf("Init() unexpected error: %v", err)
	}
	if p.appToken != "xapp-test" {
		t.Errorf("appToken = %q, want xapp-test", p.appToken)
	}
	if p.botToken != "xoxb-test" {
		t.Errorf("botToken = %q, want xoxb-test", p.botToken)
	}
}

func TestOpenConnection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apps.connections.open" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer xapp-test-token" {
			t.Errorf("Authorization = %q, want 'Bearer xapp-test-token'", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"url":"wss://example.com/ws"}`)) //nolint:errcheck
	}))
	defer srv.Close()

	p := New()
	p.appToken = "xapp-test-token"
	p.apiBase = srv.URL

	wsURL, err := p.openConnection()
	if err != nil {
		t.Fatalf("openConnection() error: %v", err)
	}
	if wsURL != "wss://example.com/ws" {
		t.Errorf("wsURL = %q, want wss://example.com/ws", wsURL)
	}
}

func TestOpenConnection_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":false,"error":"invalid_auth"}`)) //nolint:errcheck
	}))
	defer srv.Close()

	p := New()
	p.appToken = "xapp-bad"
	p.apiBase = srv.URL

	_, err := p.openConnection()
	if err == nil {
		t.Fatal("expected error for invalid auth")
	}
	if !strings.Contains(err.Error(), "invalid_auth") {
		t.Errorf("error = %q, want mention of invalid_auth", err)
	}
}

func TestAddReaction(t *testing.T) {
	var gotPath, gotChannel, gotTS, gotEmoji string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		var payload map[string]string
		json.Unmarshal(body, &payload) //nolint:errcheck
		gotChannel = payload["channel"]
		gotTS = payload["timestamp"]
		gotEmoji = payload["name"]
		w.Write([]byte(`{"ok":true}`)) //nolint:errcheck
	}))
	defer srv.Close()

	p := New()
	p.botToken = "xoxb-test"
	p.apiBase = srv.URL

	err := p.addReaction("C123", "1234.5678", "eyes")
	if err != nil {
		t.Fatalf("addReaction() error: %v", err)
	}
	if gotPath != "/reactions.add" {
		t.Errorf("path = %q, want /reactions.add", gotPath)
	}
	if gotChannel != "C123" {
		t.Errorf("channel = %q, want C123", gotChannel)
	}
	if gotTS != "1234.5678" {
		t.Errorf("timestamp = %q, want 1234.5678", gotTS)
	}
	if gotEmoji != "eyes" {
		t.Errorf("name = %q, want eyes", gotEmoji)
	}
}

func TestRemoveReaction(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte(`{"ok":true}`)) //nolint:errcheck
	}))
	defer srv.Close()

	p := New()
	p.botToken = "xoxb-test"
	p.apiBase = srv.URL

	err := p.removeReaction("C123", "1234.5678", "eyes")
	if err != nil {
		t.Fatalf("removeReaction() error: %v", err)
	}
	if gotPath != "/reactions.remove" {
		t.Errorf("path = %q, want /reactions.remove", gotPath)
	}
}

func TestUploadFile(t *testing.T) {
	var step int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/files.getUploadURLExternal":
			step++
			if step != 1 {
				t.Errorf("getUploadURLExternal called at step %d, want 1", step)
			}
			if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
				t.Errorf("Content-Type = %q, want application/x-www-form-urlencoded", ct)
			}
			if err := r.ParseForm(); err != nil {
				t.Fatalf("ParseForm: %v", err)
			}
			if r.FormValue("filename") != "report.md" {
				t.Errorf("filename = %q, want report.md", r.FormValue("filename"))
			}
			if r.FormValue("length") != "17" {
				t.Errorf("length = %q, want 17", r.FormValue("length"))
			}
			w.Write([]byte(`{"ok":true,"upload_url":"` + "http://" + r.Host + `/upload","file_id":"F123"}`)) //nolint:errcheck
		case "/upload":
			step++
			if step != 2 {
				t.Errorf("upload called at step %d, want 2", step)
			}
			body, _ := io.ReadAll(r.Body)
			if string(body) != "file content here" {
				t.Errorf("upload body = %q, want 'file content here'", string(body))
			}
			w.WriteHeader(http.StatusOK)
		case "/files.completeUploadExternal":
			step++
			if step != 3 {
				t.Errorf("completeUploadExternal called at step %d, want 3", step)
			}
			body, _ := io.ReadAll(r.Body)
			var payload map[string]any
			json.Unmarshal(body, &payload) //nolint:errcheck
			if payload["channel_id"] != "C123" {
				t.Errorf("channel_id = %v, want C123", payload["channel_id"])
			}
			w.Write([]byte(`{"ok":true}`)) //nolint:errcheck
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	p := New()
	p.botToken = "xoxb-test"
	p.apiBase = srv.URL

	event := &channels.ChannelEvent{
		WorkspaceID: "C123",
		MessageID:   "1234.5678",
	}

	err := p.uploadFile(event, "report.md", "file content here")
	if err != nil {
		t.Fatalf("uploadFile() error: %v", err)
	}
	if step != 3 {
		t.Errorf("expected 3 API calls, got %d", step)
	}
}

func TestNormalizeEvent(t *testing.T) {
	raw := `{
		"team_id": "T1234",
		"event": {
			"type": "message",
			"channel": "C0123456",
			"user": "U789",
			"text": "hello world",
			"ts": "1234567890.123456",
			"thread_ts": "1234567890.000001"
		}
	}`

	p := New()
	event, err := p.NormalizeEvent([]byte(raw))
	if err != nil {
		t.Fatalf("NormalizeEvent() error: %v", err)
	}

	if event.Channel != "slack" {
		t.Errorf("Channel = %q, want slack", event.Channel)
	}
	if event.WorkspaceID != "C0123456" {
		t.Errorf("WorkspaceID = %q, want C0123456", event.WorkspaceID)
	}
	if event.UserID != "U789" {
		t.Errorf("UserID = %q, want U789", event.UserID)
	}
	if event.ThreadID != "1234567890.000001" {
		t.Errorf("ThreadID = %q, want 1234567890.000001", event.ThreadID)
	}
	if event.Message != "hello world" {
		t.Errorf("Message = %q, want 'hello world'", event.Message)
	}
}

func TestNormalizeEvent_NoThread(t *testing.T) {
	raw := `{
		"team_id": "T1234",
		"event": {
			"type": "message",
			"channel": "C0123456",
			"user": "U789",
			"text": "top-level message",
			"ts": "1234567890.123456"
		}
	}`

	p := New()
	event, err := p.NormalizeEvent([]byte(raw))
	if err != nil {
		t.Fatalf("NormalizeEvent() error: %v", err)
	}

	// ThreadID should equal the message TS so thread replies find the same session.
	if event.ThreadID != "1234567890.123456" {
		t.Errorf("ThreadID = %q, want 1234567890.123456", event.ThreadID)
	}
	// MessageID should hold the message timestamp for reply targeting.
	if event.MessageID != "1234567890.123456" {
		t.Errorf("MessageID = %q, want 1234567890.123456", event.MessageID)
	}
}

func TestSendResponse(t *testing.T) {
	// Mock Slack API
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer xoxb-test-token" {
			t.Errorf("Authorization = %q, want 'Bearer xoxb-test-token'", r.Header.Get("Authorization"))
		}

		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		json.Unmarshal(body, &payload) //nolint:errcheck

		if payload["channel"] != "C0123456" {
			t.Errorf("channel = %v, want C0123456", payload["channel"])
		}
		if payload["thread_ts"] != "1234567890.000001" {
			t.Errorf("thread_ts = %v, want 1234567890.000001", payload["thread_ts"])
		}
		if payload["mrkdwn"] != true {
			t.Errorf("mrkdwn = %v, want true", payload["mrkdwn"])
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`)) //nolint:errcheck
	}))
	defer srv.Close()

	p := New()
	p.botToken = "xoxb-test-token"
	p.apiBase = srv.URL

	event := &channels.ChannelEvent{
		WorkspaceID: "C0123456",
		ThreadID:    "1234567890.000001",
	}

	msg := &a2a.Message{
		Role:  a2a.MessageRoleAgent,
		Parts: []a2a.Part{a2a.NewTextPart("hello from agent")},
	}

	err := p.SendResponse(event, msg)
	if err != nil {
		t.Fatalf("SendResponse() error: %v", err)
	}
}

func TestSendResponse_MarkdownConversion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		json.Unmarshal(body, &payload) //nolint:errcheck

		text, _ := payload["text"].(string)
		if !strings.Contains(text, "*bold*") {
			t.Errorf("expected *bold* in text, got %q", text)
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`)) //nolint:errcheck
	}))
	defer srv.Close()

	p := New()
	p.botToken = "xoxb-test-token"
	p.apiBase = srv.URL

	event := &channels.ChannelEvent{
		WorkspaceID: "C0123456",
		ThreadID:    "1234567890.000001",
	}

	msg := &a2a.Message{
		Role:  a2a.MessageRoleAgent,
		Parts: []a2a.Part{a2a.NewTextPart("this is **bold** text")},
	}

	err := p.SendResponse(event, msg)
	if err != nil {
		t.Fatalf("SendResponse() error: %v", err)
	}
}

func TestExtractText(t *testing.T) {
	tests := []struct {
		name string
		msg  *a2a.Message
		want string
	}{
		{"nil message", nil, "(no response)"},
		{"single text", &a2a.Message{Parts: []a2a.Part{a2a.NewTextPart("hello")}}, "hello"},
		{"multiple text", &a2a.Message{Parts: []a2a.Part{a2a.NewTextPart("a"), a2a.NewTextPart("b")}}, "a\nb"},
		{"no text parts", &a2a.Message{Parts: []a2a.Part{a2a.NewDataPart(42)}}, "(no text response)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractText(tt.msg)
			if got != tt.want {
				t.Errorf("extractText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractLargestFile(t *testing.T) {
	t.Run("nil message", func(t *testing.T) {
		content, name := extractLargestFile(nil)
		if content != "" || name != "" {
			t.Errorf("expected empty, got content=%q name=%q", content, name)
		}
	})

	t.Run("no file parts", func(t *testing.T) {
		msg := &a2a.Message{Parts: []a2a.Part{a2a.NewTextPart("hello")}}
		content, name := extractLargestFile(msg)
		if content != "" || name != "" {
			t.Errorf("expected empty, got content=%q name=%q", content, name)
		}
	})

	t.Run("single file part", func(t *testing.T) {
		msg := &a2a.Message{Parts: []a2a.Part{
			a2a.NewTextPart("summary"),
			a2a.NewFilePart(a2a.FileContent{
				Name:     "report.md",
				MimeType: "text/markdown",
				Bytes:    []byte("full report content"),
			}),
		}}
		content, name := extractLargestFile(msg)
		if content != "full report content" {
			t.Errorf("content = %q, want 'full report content'", content)
		}
		if name != "report.md" {
			t.Errorf("name = %q, want 'report.md'", name)
		}
	})

	t.Run("picks largest file", func(t *testing.T) {
		msg := &a2a.Message{Parts: []a2a.Part{
			a2a.NewFilePart(a2a.FileContent{Name: "small.md", Bytes: []byte("short")}),
			a2a.NewFilePart(a2a.FileContent{Name: "big.md", Bytes: []byte("this is much longer content")}),
		}}
		content, name := extractLargestFile(msg)
		if content != "this is much longer content" {
			t.Errorf("content = %q, want largest file content", content)
		}
		if name != "big.md" {
			t.Errorf("name = %q, want 'big.md'", name)
		}
	})
}

func TestResolveBotID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth.test" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer xoxb-test-token" {
			t.Errorf("Authorization = %q, want 'Bearer xoxb-test-token'", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"user_id":"U123BOT"}`)) //nolint:errcheck
	}))
	defer srv.Close()

	p := New()
	p.botToken = "xoxb-test-token"
	p.apiBase = srv.URL

	err := p.resolveBotID()
	if err != nil {
		t.Fatalf("resolveBotID() error: %v", err)
	}
	if p.botUserID != "U123BOT" {
		t.Errorf("botUserID = %q, want U123BOT", p.botUserID)
	}
}

func TestResolveBotID_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":false,"error":"invalid_auth"}`)) //nolint:errcheck
	}))
	defer srv.Close()

	p := New()
	p.botToken = "xoxb-bad"
	p.apiBase = srv.URL

	err := p.resolveBotID()
	if err == nil {
		t.Fatal("expected error for invalid auth")
	}
	if !strings.Contains(err.Error(), "invalid_auth") {
		t.Errorf("error = %q, want mention of invalid_auth", err)
	}
}

func TestExtractMentions(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []string
	}{
		{"no mentions", "hello world", nil},
		{"single mention", "<@U123> hello", []string{"U123"}},
		{"multiple mentions", "<@U123> and <@U456>", []string{"U123", "U456"}},
		{"mention with display name", "<@U123|bob> hello", []string{"U123"}},
		{"mixed mentions", "<@U123> and <@U456|alice>", []string{"U123", "U456"}},
		{"mention at end", "hey <@U789>", []string{"U789"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractMentions(tt.text)
			if len(got) != len(tt.want) {
				t.Fatalf("extractMentions(%q) = %v, want %v", tt.text, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("extractMentions(%q)[%d] = %q, want %q", tt.text, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestStripBotMention(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		botUserID string
		want      string
	}{
		{"removes mention", "<@UBOT> hello there", "UBOT", "hello there"},
		{"removes mention with display name", "<@UBOT|mybot> hello", "UBOT", "hello"},
		{"leaves other mentions", "<@U123> <@UBOT> hello", "UBOT", "<@U123> hello"},
		{"no mention present", "hello world", "UBOT", "hello world"},
		{"multiple bot mentions", "<@UBOT> hey <@UBOT>", "UBOT", "hey"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripBotMention(tt.text, tt.botUserID)
			if got != tt.want {
				t.Errorf("stripBotMention(%q, %q) = %q, want %q", tt.text, tt.botUserID, got, tt.want)
			}
		})
	}
}

func TestUnwrapJSONContent(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{
			name: "plain text unchanged",
			text: "hello world",
			want: "hello world",
		},
		{
			name: "JSON with content and sources",
			text: `{"content":"# Report\nSome findings.","sources":[{"url":"https://example.com","title":"Example"},{"url":"https://other.com","title":"Other"}],"status":"completed"}`,
			want: "# Report\nSome findings.\n\n**Sources:**\n- [Example](https://example.com)\n- [Other](https://other.com)",
		},
		{
			name: "JSON with content but empty sources",
			text: `{"content":"just content here","sources":[]}`,
			want: "just content here",
		},
		{
			name: "JSON without content field",
			text: `{"status":"completed","result":"some data"}`,
			want: `{"status":"completed","result":"some data"}`,
		},
		{
			name: "Tavily search with answer and results",
			text: `{"query":"test","answer":"The answer is 42.","results":[{"title":"Wikipedia","url":"https://en.wikipedia.org/wiki/42","content":"42 is the answer to everything.","score":0.95}]}`,
			want: "The answer is 42.\n\n**Sources:**\n- [Wikipedia](https://en.wikipedia.org/wiki/42): 42 is the answer to everything.",
		},
		{
			name: "Tavily search with answer only",
			text: `{"query":"test","answer":"Short answer.","results":[]}`,
			want: "Short answer.",
		},
		{
			name: "Tavily search with results only",
			text: `{"query":"test","results":[{"title":"Example","url":"https://example.com","content":"Some content.","score":0.9}]}`,
			want: "**Sources:**\n- [Example](https://example.com): Some content.",
		},
		{
			name: "JSON with source missing title",
			text: `{"content":"report","sources":[{"url":"https://bare.com","title":""}]}`,
			want: "report\n\n**Sources:**\n- https://bare.com",
		},
		{
			name: "JSON with source missing URL",
			text: `{"content":"report","sources":[{"url":"","title":"No URL"}]}`,
			want: "report",
		},
		{
			name: "empty string",
			text: "",
			want: "",
		},
		{
			name: "invalid JSON starting with brace",
			text: "{not valid json",
			want: "{not valid json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := unwrapJSONContent(tt.text)
			if got != tt.want {
				t.Errorf("unwrapJSONContent() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractText_UnwrapsJSON(t *testing.T) {
	msg := &a2a.Message{
		Parts: []a2a.Part{
			a2a.NewTextPart(`{"content":"# Research\nFindings here.","sources":[{"url":"https://example.com","title":"Example"}]}`),
		},
	}
	got := extractText(msg)
	want := "# Research\nFindings here.\n\n**Sources:**\n- [Example](https://example.com)"
	if got != want {
		t.Errorf("extractText() = %q, want %q", got, want)
	}
}

func TestExtractLargestFile_UnwrapsJSON(t *testing.T) {
	raw := `{"query":"test","answer":"The answer is 42.","results":[{"title":"Source","url":"https://example.com","content":"Details here.","score":0.9}]}`
	msg := &a2a.Message{
		Parts: []a2a.Part{
			{
				Kind: a2a.PartKindFile,
				File: &a2a.FileContent{
					Name:  "web_search-output.md",
					Bytes: []byte(raw),
				},
			},
		},
	}
	content, filename := extractLargestFile(msg)
	if filename != "web_search-output.md" {
		t.Errorf("filename = %q, want web_search-output.md", filename)
	}
	wantContent := "The answer is 42.\n\n**Sources:**\n- [Source](https://example.com): Details here."
	if content != wantContent {
		t.Errorf("extractLargestFile() content = %q, want %q", content, wantContent)
	}
}

func TestSendResponse_WithFilePart(t *testing.T) {
	var uploadCalls, postCalls int
	var lastPostText string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/files.getUploadURLExternal":
			uploadCalls++
			w.Write([]byte(`{"ok":true,"upload_url":"` + "http://" + r.Host + `/upload","file_id":"F456"}`)) //nolint:errcheck
		case "/upload":
			w.WriteHeader(http.StatusOK)
		case "/files.completeUploadExternal":
			w.Write([]byte(`{"ok":true}`)) //nolint:errcheck
		case "/chat.postMessage":
			postCalls++
			body, _ := io.ReadAll(r.Body)
			var payload map[string]any
			json.Unmarshal(body, &payload) //nolint:errcheck
			lastPostText, _ = payload["text"].(string)
			w.Write([]byte(`{"ok":true}`)) //nolint:errcheck
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	p := New()
	p.botToken = "xoxb-test"
	p.apiBase = srv.URL

	event := &channels.ChannelEvent{
		WorkspaceID: "C123",
		MessageID:   "1234.5678",
	}

	// Response with both a text summary and a file part (large tool output).
	msg := &a2a.Message{
		Role: a2a.MessageRoleAgent,
		Parts: []a2a.Part{
			a2a.NewTextPart("Here is a summary of the research."),
			a2a.NewFilePart(a2a.FileContent{
				Name:     "web_search-output.md",
				MimeType: "text/markdown",
				Bytes:    []byte("# Full Research Report\n\nThis is the complete untruncated content..."),
			}),
		},
	}

	err := p.SendResponse(event, msg)
	if err != nil {
		t.Fatalf("SendResponse() error: %v", err)
	}

	if uploadCalls != 1 {
		t.Errorf("expected 1 upload call, got %d", uploadCalls)
	}
	if postCalls != 1 {
		t.Errorf("expected 1 post call (summary), got %d", postCalls)
	}
	if !strings.Contains(lastPostText, "summary of the research") {
		t.Errorf("expected summary in post, got %q", lastPostText)
	}
	if !strings.Contains(lastPostText, "attached") {
		t.Errorf("expected 'attached' note in post, got %q", lastPostText)
	}
}
