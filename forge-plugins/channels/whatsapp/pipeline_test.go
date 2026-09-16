package whatsapp

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/initializ/forge/forge-core/channels"
)

// End-to-end coverage of onMessage — the dedup → stale → admit → dispatch
// pipeline. This ordering is the loop guard and the admission boundary, so it
// is the path most worth pinning against regression (PR review, item 3).

const (
	pipeOwner  = "14155550999@s.whatsapp.net"
	pipeSender = "14155550100@s.whatsapp.net"
	pipeGroup  = "120363000000000000@g.us"
)

// pipeHarness runs onMessage with dispatch intercepted, so the pipeline can be
// exercised without a socket.
type pipeHarness struct {
	plugin     *Plugin
	dispatched chan *channels.ChannelEvent
}

func newPipeHarness(t *testing.T, settings map[string]string) *pipeHarness {
	t.Helper()

	p := New()
	if err := p.Init(cfgWith(settings)); err != nil {
		t.Fatalf("Init: %v", err)
	}
	// Start would need a paired session and a live socket; set by hand the
	// two things it derives.
	p.cfg.Admission.OwnJIDs = []string{pipeOwner}
	p.startedAt = time.Now()

	h := &pipeHarness{plugin: p, dispatched: make(chan *channels.ChannelEvent, 4)}
	p.dispatchFn = func(event *channels.ChannelEvent, _ types.JID, _ channels.EventHandler) {
		h.dispatched <- event
	}
	return h
}

// send feeds one message through the pipeline and reports whether it reached
// dispatch.
func (h *pipeHarness) send(t *testing.T, msg *events.Message) *channels.ChannelEvent {
	t.Helper()
	h.plugin.onMessage(context.Background(), msg, nil)
	select {
	case ev := <-h.dispatched:
		return ev
	case <-time.After(300 * time.Millisecond):
		return nil
	}
}

func textMessage(id, chat, sender, text string, fromMe bool) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			ID:        id,
			Timestamp: time.Now(),
			PushName:  "Tester",
			MessageSource: types.MessageSource{
				Chat:     mustJID(chat),
				Sender:   mustJID(sender),
				IsFromMe: fromMe,
				IsGroup:  IsGroupJID(chat),
			},
		},
		Message: &waE2E.Message{Conversation: proto.String(text)},
	}
}

func mentionMessage(id, chat, sender, text string, mentioned []string) *events.Message {
	m := textMessage(id, chat, sender, text, false)
	m.Message = &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text:        proto.String(text),
			ContextInfo: &waE2E.ContextInfo{MentionedJID: mentioned},
		},
	}
	return m
}

func mustJID(s string) types.JID {
	j, err := types.ParseJID(s)
	if err != nil {
		panic(err)
	}
	return j
}

func TestPipeline_DMFromOwnerIsDispatched(t *testing.T) {
	h := newPipeHarness(t, nil)

	ev := h.send(t, textMessage("m1", pipeSender, pipeOwner, "hello", false))
	if ev == nil {
		t.Fatal("expected the message to reach dispatch")
	}
	if ev.Channel != "whatsapp" {
		t.Errorf("Channel = %q", ev.Channel)
	}
	if ev.Message != "hello" {
		t.Errorf("Message = %q", ev.Message)
	}
	if ev.MessageID != "m1" {
		t.Errorf("MessageID = %q", ev.MessageID)
	}
	// One durable session per chat.
	if ev.ThreadID != ev.WorkspaceID {
		t.Errorf("ThreadID %q should equal WorkspaceID %q", ev.ThreadID, ev.WorkspaceID)
	}
}

// The default is owner-only, so an unlisted stranger must not reach dispatch.
func TestPipeline_StrangerIsDropped(t *testing.T) {
	h := newPipeHarness(t, nil)
	if ev := h.send(t, textMessage("m1", pipeSender, pipeSender, "hello", false)); ev != nil {
		t.Errorf("expected a stranger dropped under the owner-only default, dispatched %+v", ev)
	}
}

// Dedup runs before everything and covers ALL inbound, so a redelivered
// message is evaluated once.
func TestPipeline_DuplicateIDDispatchedOnce(t *testing.T) {
	h := newPipeHarness(t, nil)

	if ev := h.send(t, textMessage("dup", pipeSender, pipeOwner, "first", false)); ev == nil {
		t.Fatal("first delivery should dispatch")
	}
	if ev := h.send(t, textMessage("dup", pipeSender, pipeOwner, "second", false)); ev != nil {
		t.Errorf("redelivery of the same id must not dispatch again, got %+v", ev)
	}
}

// Outbound ids are pre-marked, so an echo of our own reply drops at dedup —
// before admission ever sees it.
func TestPipeline_OutboundEchoDroppedAtDedup(t *testing.T) {
	h := newPipeHarness(t, nil)
	h.plugin.markSent("reply-1")

	msg := textMessage("reply-1", pipeOwner, pipeOwner, "⚒ Forge: 4", true)
	if ev := h.send(t, msg); ev != nil {
		t.Errorf("our own reply must drop at dedup, dispatched %+v", ev)
	}
}

// The stale guard is an independent second replay defence: a message that
// would otherwise be admitted is still dropped when it predates the run.
func TestPipeline_StaleMessageDroppedDespiteBeingAdmissible(t *testing.T) {
	h := newPipeHarness(t, nil)

	msg := textMessage("old", pipeSender, pipeOwner, "hello", false)
	msg.Info.Timestamp = time.Now().Add(-time.Hour)

	if ev := h.send(t, msg); ev != nil {
		t.Errorf("a replayed message must not dispatch, got %+v", ev)
	}
}

// A brief restart should still pick up what arrived while the agent was down.
func TestPipeline_RecentMessageWithinGraceIsDispatched(t *testing.T) {
	h := newPipeHarness(t, nil)

	msg := textMessage("recent", pipeSender, pipeOwner, "hello", false)
	msg.Info.Timestamp = time.Now().Add(-time.Minute)

	if ev := h.send(t, msg); ev == nil {
		t.Error("a message from just before startup should still be answered")
	}
}

func TestPipeline_GroupWithoutMentionIsDropped(t *testing.T) {
	h := newPipeHarness(t, map[string]string{"allowed_senders": "anyone"})
	if ev := h.send(t, textMessage("g1", pipeGroup, pipeSender, "chatter", false)); ev != nil {
		t.Errorf("unmentioned group chatter must not dispatch, got %+v", ev)
	}
}

func TestPipeline_GroupWithMentionIsDispatched(t *testing.T) {
	h := newPipeHarness(t, map[string]string{"allowed_senders": "anyone"})
	msg := mentionMessage("g2", pipeGroup, pipeSender, "@agent summarise", []string{pipeOwner})
	if ev := h.send(t, msg); ev == nil {
		t.Error("a mentioned group message should dispatch")
	}
}

// The personal-agent flow: own message, own chat.
func TestPipeline_SelfChatIsDispatched(t *testing.T) {
	h := newPipeHarness(t, nil)
	if ev := h.send(t, textMessage("s1", pipeOwner, pipeOwner, "what is 2+2?", true)); ev == nil {
		t.Error("a self-chat message should dispatch")
	}
}

func TestPipeline_SelfChatDisabledDropsOwnMessage(t *testing.T) {
	h := newPipeHarness(t, map[string]string{"self_chat": "false"})
	if ev := h.send(t, textMessage("s2", pipeOwner, pipeOwner, "hello", true)); ev != nil {
		t.Errorf("self_chat off should drop own messages, got %+v", ev)
	}
}

// Outside the self-chat an own message is our echo, even if not in the ring.
func TestPipeline_OwnMessageInGroupIsDropped(t *testing.T) {
	h := newPipeHarness(t, map[string]string{"allowed_senders": "anyone"})
	msg := mentionMessage("g3", pipeGroup, pipeOwner, "@agent hi", []string{pipeOwner})
	msg.Info.IsFromMe = true
	if ev := h.send(t, msg); ev != nil {
		t.Errorf("own group message must not dispatch, got %+v", ev)
	}
}

func TestPipeline_NonConversationJIDsDropped(t *testing.T) {
	h := newPipeHarness(t, map[string]string{"allowed_senders": "anyone"})
	for _, chat := range []string{"123@newsletter", "status@broadcast"} {
		if ev := h.send(t, textMessage("n-"+chat, chat, pipeSender, "x", false)); ev != nil {
			t.Errorf("%s must not dispatch, got %+v", chat, ev)
		}
	}
}

// Media with no caption carries no prompt; not an error, just nothing to do.
func TestPipeline_MessageWithoutTextIsDropped(t *testing.T) {
	h := newPipeHarness(t, nil)
	msg := textMessage("empty", pipeSender, pipeOwner, "", false)
	msg.Message = &waE2E.Message{}
	if ev := h.send(t, msg); ev != nil {
		t.Errorf("a message with no text must not dispatch, got %+v", ev)
	}
}

// A captioned photo mentioning the agent is a prompt — the review's item 1,
// exercised through the full pipeline rather than the helper alone.
func TestPipeline_GroupMediaCaptionMentionIsDispatched(t *testing.T) {
	h := newPipeHarness(t, map[string]string{"allowed_senders": "anyone"})

	msg := textMessage("img", pipeGroup, pipeSender, "", false)
	msg.Message = &waE2E.Message{
		ImageMessage: &waE2E.ImageMessage{
			Caption:     proto.String("@agent what is this?"),
			ContextInfo: &waE2E.ContextInfo{MentionedJID: []string{pipeOwner}},
		},
	}

	ev := h.send(t, msg)
	if ev == nil {
		t.Fatal("a captioned group photo mentioning the agent should dispatch")
	}
	if ev.Message == "" {
		t.Error("the caption should carry through as the prompt")
	}
}

// Surrounding group chatter is recorded for context even though it is not
// itself admitted — that is what makes a later "summarise this" answerable.
func TestPipeline_DroppedGroupChatterStillRecordedAsContext(t *testing.T) {
	h := newPipeHarness(t, map[string]string{"allowed_senders": "anyone"})

	if ev := h.send(t, textMessage("c1", pipeGroup, pipeSender, "deploy failed", false)); ev != nil {
		t.Fatal("unmentioned chatter should not dispatch")
	}
	if got := h.plugin.history.recent(pipeGroup, 10); len(got) != 1 || got[0].Text != "deploy failed" {
		t.Errorf("dropped chatter should still be recorded, got %v", got)
	}
}

// And it must reach the prompt when the agent is finally addressed.
func TestPipeline_HistoryIsInjectedIntoPrompt(t *testing.T) {
	h := newPipeHarness(t, map[string]string{"allowed_senders": "anyone"})

	h.send(t, textMessage("c1", pipeGroup, pipeSender, "deploy failed", false))
	ev := h.send(t, mentionMessage("c2", pipeGroup, pipeSender, "@agent summarise", []string{pipeOwner}))
	if ev == nil {
		t.Fatal("the mention should dispatch")
	}
	if !strings.Contains(ev.Message, "deploy failed") {
		t.Errorf("prior chatter should be injected as context, got %q", ev.Message)
	}
}
