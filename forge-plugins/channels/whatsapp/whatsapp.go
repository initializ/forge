package whatsapp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/channels"
	"github.com/initializ/forge/forge-plugins/channels/markdown"
)

const (
	defaultSessionPath = ".forge/channels/whatsapp-session.db"
	// handlerTimeout bounds one agent turn. Matches the Telegram adapter.
	handlerTimeout = 10 * time.Minute
	// typingRefresh is how often the composing indicator is re-sent. WhatsApp
	// expires it after roughly 10s, so a slow turn needs a heartbeat or the
	// chat looks idle while the agent is working.
	typingRefresh = 8 * time.Second
	// historySoftCap bounds the injected context block, mirroring the Teams
	// adapter's budget guard.
	historySoftCap = 5000
	// defaultSelfChatPrefix marks the agent's replies in the owner's self-chat.
	//
	// WhatsApp decides which side of the thread a message renders on from its
	// sender, and in the self-chat the agent sends AS the owner — so its
	// replies are visually identical to the owner's own prompts. No wire-level
	// setting changes that; a text marker is the only way to tell them apart.
	defaultSelfChatPrefix = "⚒ Forge: "
	// staleGrace is how far before startup an inbound message may be dated and
	// still be acted on.
	//
	// Reconnecting delivers a backlog, and after a restart the dedup ring is
	// empty — so without this the agent answers a replayed conversation, and
	// in the self-chat it answers its OWN replayed replies, which loops. The
	// window is wide enough that a brief restart still picks up messages sent
	// while the agent was down, and narrow enough that a history sync of an
	// old conversation is ignored.
	staleGrace = 5 * time.Minute
)

// Plugin implements channels.ChannelPlugin for WhatsApp over the WhatsApp Web
// multidevice protocol.
//
// Unlike every other adapter in this repo, authentication is not a token from
// config: it is a QR pairing captured out-of-band by
// `forge channel whatsapp-login` and persisted to a local session store. Start
// therefore fails closed on an unpaired session rather than attempting to pair
// — pairing needs a human at a terminal, which `forge run` cannot assume.
type Plugin struct {
	cfg adapterConfig

	// Built at Start.
	container *sqlstore.Container
	client    *whatsmeow.Client
	dedup     *dedup
	history   *chatHistory

	// The paired account's identities, captured at Start. WhatsApp addresses
	// the same account by phone number (ownJID) and by hidden-number LID
	// (ownLID); a mention may arrive under either, so both are kept.
	ownJID string
	ownLID string
	// ownPushName is the display name other participants see, used to strip a
	// typed "@Name" prefix off the prompt.
	ownPushName string

	// logger is an optional structured ops logger (SetLogger). When set,
	// operational signals route through it; nil → log.Printf.
	logger channels.Logger

	// startedAt bounds how far back inbound messages are acted on. See
	// staleGrace.
	startedAt time.Time

	// Lifecycle.
	stopCh chan struct{}
	once   sync.Once
}

type adapterConfig struct {
	SessionPath          string
	SelfChatPrefix       string
	Admission            admissionConfig
	IncludeRecentHistory bool
	RecentHistoryCount   int
}

// New returns an uninitialised plugin. Init must be called before Start.
func New() *Plugin {
	return &Plugin{
		dedup:  newDedup(1000),
		stopCh: make(chan struct{}),
	}
}

func (p *Plugin) Name() string { return "whatsapp" }

// SetLogger wires a structured ops logger (channels.LoggerAware). Optional.
func (p *Plugin) SetLogger(l channels.Logger) { p.logger = l }

func (p *Plugin) Init(cfg channels.ChannelConfig) error {
	settings := channels.ResolveEnvVars(&cfg)

	ac := adapterConfig{
		SessionPath: strOrDefault(settings["session_path"], defaultSessionPath),
		// strOrDefault would swallow a deliberate "" (meaning "no prefix"), so
		// the presence of the key is what decides here.
		SelfChatPrefix: settingOrDefault(settings, "self_chat_prefix", defaultSelfChatPrefix),
		Admission: admissionConfig{
			Mode:           AdmitMode(strOrDefault(settings["admit"], string(AdmitDMOrGroupMention))),
			AllowedGroups:  parseJIDSet(settings["allowed_groups"], serverGroup),
			AllowedSenders: parseJIDSet(settings["allowed_senders"], serverUser),
			AllowAnySender: isAnySender(settings["allowed_senders"]),
			SelfChat:       parseBool(settings["self_chat"], true),
		},
		IncludeRecentHistory: parseBool(settings["include_recent_history"], true),
		RecentHistoryCount:   parseInt(settings["recent_history_count"], 20),
	}

	switch ac.Admission.Mode {
	case AdmitDM, AdmitGroupMention, AdmitDMOrGroupMention:
	default:
		return fmt.Errorf("whatsapp: admit must be one of dm, group_mention, dm_or_group_mention, got %q", ac.Admission.Mode)
	}
	if ac.RecentHistoryCount < 0 {
		return fmt.Errorf("whatsapp: recent_history_count must not be negative, got %d", ac.RecentHistoryCount)
	}

	p.cfg = ac
	if ac.IncludeRecentHistory {
		p.history = newChatHistory(ac.RecentHistoryCount)
	}
	return nil
}

// Start connects the paired session and dispatches inbound messages to
// handler. It blocks until ctx is cancelled or Stop is called.
func (p *Plugin) Start(ctx context.Context, handler channels.EventHandler) error {
	container, device, err := loadPairedDevice(ctx, p.cfg.SessionPath)
	if err != nil {
		return err
	}
	p.container = container

	p.ownJID = device.ID.ToNonAD().String()
	if lid := device.GetLID(); !lid.IsEmpty() {
		p.ownLID = lid.ToNonAD().String()
	}
	p.ownPushName = device.PushName

	// The paired account's identities are only known once the session is
	// loaded, so the owner-dependent parts of the gate are filled in here
	// rather than at Init.
	p.cfg.Admission.OwnJIDs = []string{p.ownJID, p.ownLID}
	p.startedAt = time.Now()

	p.client = whatsmeow.NewClient(device, nil)
	p.client.AddEventHandler(func(evt any) {
		switch v := evt.(type) {
		case *events.Message:
			p.onMessage(ctx, v, handler)
		case *events.LoggedOut:
			// The pairing was revoked from the phone. Reconnecting cannot
			// recover it — only a fresh QR scan can — so say so loudly rather
			// than looping on a dead session.
			p.logError("whatsapp: session logged out — the linked device was removed; re-pair with `forge channel whatsapp-login`", map[string]any{
				"reason": v.Reason.String(),
			})
		case *events.Connected:
			p.logInfo("whatsapp: connected", map[string]any{"jid": p.ownJID})
		case *events.Disconnected:
			p.logWarn("whatsapp: disconnected, whatsmeow will retry", nil)
		}
	})

	if err := p.client.Connect(); err != nil {
		return fmt.Errorf("whatsapp: connecting: %w", err)
	}

	select {
	case <-ctx.Done():
	case <-p.stopCh:
	}
	return nil
}

// Stop disconnects the client and closes the session store. Safe to call more
// than once.
func (p *Plugin) Stop() error {
	var err error
	p.once.Do(func() {
		close(p.stopCh)
		if p.client != nil {
			p.client.Disconnect()
		}
		if p.container != nil {
			err = p.container.Close()
		}
	})
	return err
}

// NormalizeEvent converts a raw whatsmeow message event into a ChannelEvent.
//
// The live path does not use this — whatsmeow delivers typed events, not
// bytes, so onMessage normalizes directly. It exists to satisfy
// channels.ChannelPlugin and to keep the JSON shape testable.
func (p *Plugin) NormalizeEvent(raw []byte) (*channels.ChannelEvent, error) {
	var msg events.Message
	if err := json.Unmarshal(raw, &msg); err != nil {
		return nil, fmt.Errorf("whatsapp: parse message event: %w", err)
	}
	event := p.normalizeMessage(&msg)
	if event == nil {
		return nil, fmt.Errorf("whatsapp: message carries no text content")
	}
	return event, nil
}

// onMessage runs the dedup → admission → dispatch pipeline for one inbound
// message.
func (p *Plugin) onMessage(ctx context.Context, msg *events.Message, handler channels.EventHandler) {
	// Dedup first, and for EVERY message rather than only admitted ones: a
	// history sync replays messages that were already evaluated, and
	// re-running admission on them would re-dispatch anything that passes.
	if p.dedup.markSeen(msg.Info.ID) {
		return
	}

	// Drop replayed history before anything else acts on it.
	if p.isStale(msg.Info.Timestamp) {
		p.logDebug("whatsapp: dropping message predating startup (history replay)", map[string]any{
			"message_id": msg.Info.ID,
			"sent_at":    msg.Info.Timestamp.Format(time.RFC3339),
		})
		return
	}

	chatJID := msg.Info.Chat.String()
	senderJID := msg.Info.Sender.String()
	senderAlt := ""
	if !msg.Info.SenderAlt.IsEmpty() {
		senderAlt = msg.Info.SenderAlt.String()
	}

	// Record BEFORE admission, and regardless of its verdict. The surrounding
	// human conversation in a group is exactly the context that makes a later
	// "@agent summarise this" answerable, and none of those lines are
	// themselves admitted.
	p.recordHistory(msg)

	mentioned := isMentioned(mentionedJIDs(msg.Message), p.ownJID, p.ownLID)

	result := admit(chatJID, senderJID, senderAlt, msg.Info.IsFromMe, mentioned, p.cfg.Admission)
	if !result.admit {
		p.logDebug(result.reason, map[string]any{
			"message_id": msg.Info.ID,
			"chat":       chatJID,
			"sender":     senderJID,
		})
		return
	}

	event := p.normalizeMessage(msg)
	if event == nil {
		// Media with no caption, a reaction, a poll vote — nothing to prompt
		// the agent with. Not an error.
		p.logDebug("whatsapp: dropping message with no text content", map[string]any{
			"message_id": msg.Info.ID,
			"chat":       chatJID,
		})
		return
	}

	// Inject the observed conversation so the agent can answer prompts that
	// refer to the surrounding thread ("summarise the above"). Skips the
	// current message so it isn't duplicated inside its own context.
	if p.history != nil {
		event.Message = prependHistory(
			p.history.recent(chatJID, p.cfg.RecentHistoryCount),
			msg.Info.ID,
			event.Message,
		)
	}

	go p.dispatch(event, msg.Info.Chat, handler)
}

// isStale reports whether an inbound message predates this adapter run by more
// than staleGrace, i.e. it is replayed history rather than live traffic.
func (p *Plugin) isStale(ts time.Time) bool {
	if p.startedAt.IsZero() || ts.IsZero() {
		return false
	}
	return ts.Before(p.startedAt.Add(-staleGrace))
}

// recordHistory adds one observed message to the context window.
func (p *Plugin) recordHistory(msg *events.Message) {
	if p.history == nil {
		return
	}
	text := strings.TrimSpace(extractMessageText(msg.Message))
	if text == "" {
		return
	}
	p.history.record(msg.Info.Chat.String(), historyEntry{
		ID:     msg.Info.ID,
		Author: historyAuthor(msg),
		Text:   text,
		At:     msg.Info.Timestamp,
	})
}

// historyAuthor picks the most human-readable label available for a sender.
// PushName is the display name the sender chose; it is absent for some
// message sources, in which case the phone number is the best we have.
func historyAuthor(msg *events.Message) string {
	if msg.Info.IsFromMe {
		return "agent"
	}
	if msg.Info.PushName != "" {
		return msg.Info.PushName
	}
	return JIDUser(msg.Info.Sender.String())
}

// dispatch forwards one normalized event to the agent and delivers the reply.
func (p *Plugin) dispatch(event *channels.ChannelEvent, chat types.JID, handler channels.EventHandler) {
	// A fresh context: the agent turn must outlive the inbound event's
	// context, which whatsmeow may cancel when the socket cycles.
	taskCtx, cancel := context.WithTimeout(context.Background(), handlerTimeout)
	defer cancel()

	stopTyping := p.startTypingIndicator(taskCtx, chat)
	defer stopTyping()

	// Open channel.whatsapp.deliver around the dispatch so the internal A2A
	// POST (carrying the traceparent injected by the router) nests under it.
	spanCtx, _, finish := channels.StartDeliverSpan(taskCtx, "whatsapp", event)
	var handlerErr error
	defer finish(&handlerErr)

	resp, err := handler(spanCtx, event)
	if err != nil {
		handlerErr = err
		p.logError("whatsapp: handler error", map[string]any{"error": err.Error(), "chat": event.WorkspaceID})
		return
	}

	stopTyping()
	if err := p.SendResponse(event, resp); err != nil {
		handlerErr = err
		p.logError("whatsapp: send response error", map[string]any{"error": err.Error(), "chat": event.WorkspaceID})
	}
}

// normalizeMessage converts a whatsmeow message into a ChannelEvent, or nil
// when the message carries no text the agent could act on.
func (p *Plugin) normalizeMessage(msg *events.Message) *channels.ChannelEvent {
	text := strings.TrimSpace(extractMessageText(msg.Message))
	if text == "" {
		return nil
	}

	// Strip the "@Name" the sender typed to invoke the agent in a group, so
	// the prompt doesn't open with the agent's own handle.
	text = markdown.StripWhatsAppMention(text, p.ownPushName, JIDUser(p.ownJID), JIDToE164(p.ownJID))

	chatJID := msg.Info.Chat.String()
	return &channels.ChannelEvent{
		Channel:     "whatsapp",
		WorkspaceID: chatJID,
		// One durable session per chat: the router keys the A2A task on
		// ThreadID, and a WhatsApp conversation is exactly the unit a user
		// expects to have continuity.
		ThreadID:  chatJID,
		UserID:    JIDUser(msg.Info.Sender.String()),
		MessageID: msg.Info.ID,
		Message:   text,
		// UserEmail is deliberately empty: WhatsApp has no email identity, so
		// delegated (auth.type: user) MCP tools cannot resolve an on-behalf-of
		// subject on this channel. See docs/core-concepts/channels.md.
	}
}

// SendResponse delivers an agent reply back to the originating chat.
//
// Every outbound message ID is recorded in the dedup ring before returning. A
// history sync after reconnect replays our own sends, and while
// MessageSource.IsFromMe catches them on the live path, a replayed message
// sent by the human operator from their own phone is indistinguishable from
// one the agent sent — the ring is what tells them apart.
func (p *Plugin) SendResponse(event *channels.ChannelEvent, response *a2a.Message) error {
	chat, err := types.ParseJID(event.WorkspaceID)
	if err != nil {
		return fmt.Errorf("whatsapp: parsing chat JID %q: %w", event.WorkspaceID, err)
	}

	text := extractText(response)
	body := markdown.ToWhatsAppText(text)

	// Mark replies in the self-chat, where the agent sends as the owner and
	// WhatsApp gives no visual distinction. Every chunk is marked, not just
	// the first: an unmarked continuation is indistinguishable from something
	// the owner typed when scrolling back.
	prefix := ""
	if p.cfg.SelfChatPrefix != "" && p.cfg.Admission.isSelfChat(event.WorkspaceID) {
		prefix = p.cfg.SelfChatPrefix
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	for _, chunk := range markdown.SplitMessageWhatsApp(body) {
		if strings.TrimSpace(chunk) == "" {
			continue
		}
		chunk = applyPrefix(prefix, chunk)
		resp, err := p.client.SendMessage(ctx, chat, &waE2E.Message{
			Conversation: proto.String(chunk),
		})
		if err != nil {
			return fmt.Errorf("whatsapp: sending to %s: %w", chat, err)
		}
		p.markSent(resp.ID)

		// Record our own reply so a follow-up question ("expand on point 2")
		// has the answer it refers to in context.
		if p.history != nil {
			p.history.record(event.WorkspaceID, historyEntry{
				ID:     resp.ID,
				Author: "agent",
				Text:   chunk,
				At:     time.Now(),
			})
		}
	}
	return nil
}

// markSent records an outbound message ID in the dedup ring. Safe to call with
// an empty id.
func (p *Plugin) markSent(id string) {
	if id == "" || p.dedup == nil {
		return
	}
	p.dedup.mark(id)
}

// startTypingIndicator shows "typing…" in the chat until the returned stop
// func is called. WhatsApp expires the indicator after ~10s, so it is
// refreshed on a ticker. The returned func is safe to call more than once.
func (p *Plugin) startTypingIndicator(ctx context.Context, chat types.JID) func() {
	send := func(state types.ChatPresence) {
		if err := p.client.SendChatPresence(ctx, chat, state, types.ChatPresenceMediaText); err != nil {
			p.logDebug("whatsapp: chat presence failed", map[string]any{"error": err.Error()})
		}
	}
	send(types.ChatPresenceComposing)

	done := make(chan struct{})
	var once sync.Once

	go func() {
		ticker := time.NewTicker(typingRefresh)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				send(types.ChatPresenceComposing)
			}
		}
	}()

	return func() {
		once.Do(func() {
			close(done)
			send(types.ChatPresencePaused)
		})
	}
}

// --- message content helpers ---

// extractMessageText pulls the prompt text out of a WhatsApp message.
//
// Text arrives as either a bare Conversation or an ExtendedTextMessage (used
// whenever the message carries context: a mention, a link preview, a reply).
// Media captions count too — "summarise this" under a document is a prompt.
func extractMessageText(m *waE2E.Message) string {
	if m == nil {
		return ""
	}
	if c := m.GetConversation(); c != "" {
		return c
	}
	if t := m.GetExtendedTextMessage().GetText(); t != "" {
		return t
	}
	if c := m.GetImageMessage().GetCaption(); c != "" {
		return c
	}
	if c := m.GetVideoMessage().GetCaption(); c != "" {
		return c
	}
	if c := m.GetDocumentMessage().GetCaption(); c != "" {
		return c
	}
	// A message the user edited arrives wrapped; unwrap one level.
	if e := m.GetEditedMessage().GetMessage(); e != nil {
		return extractMessageText(e)
	}
	return ""
}

// mentionedJIDs returns the JIDs @-mentioned in a message.
//
// WhatsApp carries mentions out-of-band in contextInfo.mentionedJid rather
// than as markup in the body, so this is the only reliable source — scanning
// the text would match a plain "@name" the sender typed by hand.
func mentionedJIDs(m *waE2E.Message) []string {
	if m == nil {
		return nil
	}
	if ci := m.GetExtendedTextMessage().GetContextInfo(); ci != nil {
		return ci.GetMentionedJID()
	}
	return nil
}

// extractText pulls the text content out of an A2A message, mirroring the
// pattern used by Slack, Telegram and Teams.
func extractText(msg *a2a.Message) string {
	if msg == nil {
		return "(no response)"
	}
	var parts []string
	for _, p := range msg.Parts {
		if p.Kind == a2a.PartKindText && p.Text != "" {
			parts = append(parts, p.Text)
		}
	}
	if len(parts) == 0 {
		return "(no text response)"
	}
	return markdown.StripCompressionMarkers(strings.Join(parts, "\n"))
}

// --- logging ---

func (p *Plugin) logInfo(msg string, fields map[string]any) {
	if p.logger != nil {
		p.logger.Info(msg, fields)
		return
	}
	log.Printf("[whatsapp] %s %v", msg, fields)
}

func (p *Plugin) logWarn(msg string, fields map[string]any) {
	if p.logger != nil {
		p.logger.Warn(msg, fields)
		return
	}
	log.Printf("[whatsapp] WARN %s %v", msg, fields)
}

func (p *Plugin) logError(msg string, fields map[string]any) {
	if p.logger != nil {
		p.logger.Error(msg, fields)
		return
	}
	log.Printf("[whatsapp] ERROR %s %v", msg, fields)
}

func (p *Plugin) logDebug(msg string, fields map[string]any) {
	if p.logger != nil {
		p.logger.Debug(msg, fields)
		return
	}
	log.Printf("[whatsapp] DEBUG %s %v", msg, fields)
}

// --- settings helpers ---

func strOrDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return strings.TrimSpace(s)
}

// applyPrefix prepends the reply marker.
//
// A chunk that opens with a fenced code block gets the marker on its own line:
// inlining it would put text before the opening ``` and WhatsApp would render
// the fence literally instead of as code.
func applyPrefix(prefix, chunk string) string {
	if prefix == "" {
		return chunk
	}
	if strings.HasPrefix(chunk, "```") {
		return prefix + "\n" + chunk
	}
	return prefix + chunk
}

// settingOrDefault returns the configured value when the key is present —
// including when it is deliberately empty — and def when it is absent.
func settingOrDefault(settings map[string]string, key, def string) string {
	if v, ok := settings[key]; ok {
		return v
	}
	return def
}

// isAnySender reports whether allowed_senders is the explicit opt-out that
// disables the sender allowlist. Spelled as a word rather than left empty
// because it is the setting that exposes the agent to anyone with the number,
// and an empty value should mean the safe thing, not the open one.
func isAnySender(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "any", "anyone", "*":
		return true
	}
	return false
}

func parseBool(s string, def bool) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	v, err := strconv.ParseBool(s)
	if err != nil {
		return def
	}
	return v
}

func parseInt(s string, def int) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return v
}

// interface guards
var (
	_ channels.ChannelPlugin = (*Plugin)(nil)
	_ channels.LoggerAware   = (*Plugin)(nil)
)
