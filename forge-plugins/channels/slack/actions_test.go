package slack

import (
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/channels"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

func TestBuildActionsBlocks(t *testing.T) {
	actions := []coreruntime.Action{
		{Label: "Deploy", Prompt: "deploy to staging"},
		{Label: "Diff", Prompt: "show the diff"},
	}
	blocks := buildActionsBlocks(actions, "1699.0001")
	if len(blocks) != 1 || blocks[0]["type"] != "actions" {
		t.Fatalf("expected one actions block, got %+v", blocks)
	}
	if blocks[0]["block_id"] != "forge_action:1699.0001" {
		t.Errorf("block_id = %v, want forge_action:1699.0001 (carries the thread)", blocks[0]["block_id"])
	}
	elems, _ := blocks[0]["elements"].([]map[string]any)
	if len(elems) != 2 {
		t.Fatalf("expected 2 buttons, got %d", len(elems))
	}
	if elems[0]["action_id"] != "forge_action_0" || elems[0]["value"] != "deploy to staging" {
		t.Errorf("button 0 wrong: %+v", elems[0])
	}
	if elems[1]["action_id"] != "forge_action_1" {
		t.Errorf("button 1 action_id = %v, want forge_action_1", elems[1]["action_id"])
	}
}

func TestParseActionClick(t *testing.T) {
	payload := []byte(`{
		"type":"block_actions",
		"user":{"id":"UCLICKER"},
		"channel":{"id":"C123"},
		"message":{"ts":"1699.9999"},
		"actions":[{"action_id":"forge_action_0","block_id":"forge_action:1699.0001","value":"deploy to staging","text":{"text":"Deploy"}}]
	}`)
	click, ok := parseActionClick(payload)
	if !ok {
		t.Fatal("expected a parsed action click")
	}
	if click.prompt != "deploy to staging" || click.label != "Deploy" {
		t.Errorf("prompt/label wrong: %+v", click)
	}
	if click.threadTS != "1699.0001" {
		t.Errorf("threadTS = %q, want 1699.0001 (from block_id)", click.threadTS)
	}
	if click.channelID != "C123" || click.msgTS != "1699.9999" || click.userID != "UCLICKER" {
		t.Errorf("container fields wrong: %+v", click)
	}
}

func TestParseActionClick_IgnoresNonForge(t *testing.T) {
	// An approval button (different action_id / no forge_action block_id) must
	// not parse as an action click.
	cases := []string{
		`{"type":"block_actions","actions":[{"action_id":"forge_approve","value":"task-1","block_id":"forge_defer:task-1"}]}`,
		`{"type":"block_actions","actions":[{"action_id":"forge_action_0","block_id":"other","value":"x"}]}`,         // wrong block_id
		`{"type":"block_actions","actions":[{"action_id":"forge_action_0","block_id":"forge_action:t","value":""}]}`, // empty value
		`{"type":"view_submission"}`,
	}
	for _, c := range cases {
		if _, ok := parseActionClick([]byte(c)); ok {
			t.Errorf("should NOT parse as action click: %s", c)
		}
	}
}

// SendResponse posts the reply, then a separate actions message when the
// response carries actions.
func TestSendResponse_PostsActionsMessage(t *testing.T) {
	srv, calls := recordingServer(t)
	defer srv.Close()

	p := New()
	p.botToken = "xoxb-test"
	p.apiBase = srv.URL

	event := &channels.ChannelEvent{WorkspaceID: "C123", MessageID: "1699.0001"}
	resp := &a2a.Message{
		Role:  a2a.MessageRoleAgent,
		Parts: []a2a.Part{a2a.NewTextPart("Here's the plan.")},
		Metadata: map[string]any{
			coreruntime.MetadataKeyActions: []coreruntime.Action{{Label: "Deploy", Prompt: "deploy to staging"}},
		},
	}
	if err := p.SendResponse(event, resp); err != nil {
		t.Fatalf("SendResponse error: %v", err)
	}

	// Expect at least two chat.postMessage calls: the reply, then the actions.
	var sawActions bool
	for _, c := range *calls {
		if blocks, ok := c["blocks"].([]any); ok && len(blocks) > 0 {
			if b, ok := blocks[0].(map[string]any); ok && b["type"] == "actions" {
				sawActions = true
				if b["block_id"] != "forge_action:1699.0001" {
					t.Errorf("actions block_id = %v", b["block_id"])
				}
			}
		}
	}
	if !sawActions {
		t.Errorf("expected an actions message with an actions block; calls=%d", len(*calls))
	}
}

// A response with no actions posts no actions message (common case).
func TestSendResponse_NoActionsNoExtraMessage(t *testing.T) {
	srv, calls := recordingServer(t)
	defer srv.Close()
	p := New()
	p.botToken = "xoxb-test"
	p.apiBase = srv.URL

	event := &channels.ChannelEvent{WorkspaceID: "C123", MessageID: "1699.0001"}
	resp := &a2a.Message{Role: a2a.MessageRoleAgent, Parts: []a2a.Part{a2a.NewTextPart("plain reply")}}
	if err := p.SendResponse(event, resp); err != nil {
		t.Fatalf("SendResponse error: %v", err)
	}
	for _, c := range *calls {
		if _, ok := c["blocks"]; ok {
			t.Errorf("no-actions reply must not post a blocks message: %+v", c)
		}
	}
}

func TestActionsFallbackText(t *testing.T) {
	got := actionsFallbackText([]coreruntime.Action{{Label: "A", Prompt: "a"}, {Label: "B", Prompt: "b"}})
	if !strings.Contains(got, "A") || !strings.Contains(got, "B") {
		t.Errorf("fallback text should list labels: %q", got)
	}
}
