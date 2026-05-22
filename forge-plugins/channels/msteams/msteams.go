// Package msteams implements the Microsoft Teams channel plugin via Graph
// API polling. It is outbound-only — no inbound webhooks, no public
// endpoint. See FORGE_MSTEAMS_CHANNEL_GRAPH_POLLING.md for the design.
package msteams

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/channels"
	"github.com/initializ/forge/forge-plugins/channels/markdown"
)

const (
	defaultPollIntervalSec = 5
	minPollIntervalSec     = 3
	maxPollIntervalSec     = 60
	defaultGraphBaseURL    = "https://graph.microsoft.com/v1.0"
	defaultLoginBaseURL    = "https://login.microsoftonline.com"
	cursorRelativePath     = ".forge/channels/msteams-cursor.json"
)

// Plugin implements channels.ChannelPlugin for Microsoft Teams.
type Plugin struct {
	// Resolved at Init.
	cfg          adapterConfig
	graphBaseURL string
	loginBaseURL string

	// Built at Start.
	auth   *authManager
	graph  *graphClient
	cursor *cursor
	dedup  *dedup

	// Cached identity captured at Start via /me.
	ownUserID      string
	ownDisplayName string

	// Lifecycle.
	stopCh chan struct{}
	once   sync.Once
}

type adapterConfig struct {
	TenantID     string
	ClientID     string
	ClientSecret string
	RefreshToken string
	UserID       string
	Flow         AuthFlow

	PollInterval time.Duration
	AdmitMode    AdmitMode
	AllowBotIDs  map[string]bool

	// IncludeRecentHistory: when true (default), every dispatched message
	// is prepended with a chronological block of the chat's recent
	// messages so the LLM has conversational context. Teams chats are
	// thread-oriented — without this, the agent only sees the literal
	// mention or DM and can't satisfy "summarise this week" prompts.
	IncludeRecentHistory bool
	// RecentHistoryCount: how many recent messages to fetch (capped at
	// 50 by Graph's per-request limit on /chats/{id}/messages).
	RecentHistoryCount int

	CursorPath string // resolved against working dir at Init
}

// New returns an uninitialised plugin. Init must be called before Start.
func New() *Plugin {
	return &Plugin{
		stopCh: make(chan struct{}),
	}
}

func (p *Plugin) Name() string { return "msteams" }

func (p *Plugin) Init(cfg channels.ChannelConfig) error {
	settings := channels.ResolveEnvVars(&cfg)

	ac := adapterConfig{
		TenantID:     settings["tenant_id"],
		ClientID:     settings["client_id"],
		ClientSecret: settings["client_secret"],
		RefreshToken: settings["refresh_token"],
		UserID:       settings["user_id"],
		Flow:         AuthFlow(strOrDefault(settings["auth_flow"], string(FlowDelegated))),
		AdmitMode:    AdmitMode(strOrDefault(settings["admit"], string(AdmitMentionOrDM))),
		AllowBotIDs:  parseAllowBotIDs(settings["allow_bot_ids"]),
		CursorPath:   cursorRelativePath,
	}

	if ac.TenantID == "" {
		return fmt.Errorf("msteams: tenant_id is required (set MSTEAMS_TENANT_ID)")
	}
	if ac.ClientID == "" {
		return fmt.Errorf("msteams: client_id is required (set MSTEAMS_CLIENT_ID)")
	}
	switch ac.Flow {
	case FlowDelegated:
		if ac.RefreshToken == "" {
			return fmt.Errorf("msteams: refresh_token is required for delegated flow (set MSTEAMS_REFRESH_TOKEN)")
		}
	case FlowClientCredentials:
		if ac.ClientSecret == "" {
			return fmt.Errorf("msteams: client_secret is required for client_credentials flow")
		}
		if ac.UserID == "" {
			return fmt.Errorf("msteams: user_id is required for client_credentials flow (no /me context)")
		}
	default:
		return fmt.Errorf("msteams: auth_flow must be 'delegated' or 'client_credentials', got %q", ac.Flow)
	}

	// Poll interval: floor 3s, default 5s, ceiling 60s.
	pollSec := defaultPollIntervalSec
	if raw, ok := settings["poll_interval_seconds"]; ok && raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			pollSec = n
		}
	}
	if pollSec < minPollIntervalSec {
		pollSec = minPollIntervalSec
	}
	if pollSec > maxPollIntervalSec {
		pollSec = maxPollIntervalSec
	}
	ac.PollInterval = time.Duration(pollSec) * time.Second

	// History context — default ON for Teams (chats are thread-shaped;
	// without this the LLM sees only the literal mention/DM with no
	// prior conversation and can't satisfy "summarise this week" style
	// prompts).
	ac.IncludeRecentHistory = true
	if raw, ok := settings["include_recent_history"]; ok && raw != "" {
		ac.IncludeRecentHistory = raw != "false" && raw != "0" && raw != "no"
	}
	ac.RecentHistoryCount = 20
	if raw, ok := settings["recent_history_count"]; ok && raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			if n > 50 {
				n = 50 // Graph's per-request cap on /chats/{id}/messages
			}
			ac.RecentHistoryCount = n
		}
	}

	p.cfg = ac
	p.graphBaseURL = strOrDefault(settings["graph_base_url"], defaultGraphBaseURL)
	p.loginBaseURL = defaultLoginBaseURL // tenant authority lives under here regardless of cloud
	return nil
}

func (p *Plugin) Start(ctx context.Context, handler channels.EventHandler) error {
	httpClient := &http.Client{Timeout: 360 * time.Second}

	p.auth = newAuthManager(authConfig{
		TenantID:     p.cfg.TenantID,
		ClientID:     p.cfg.ClientID,
		ClientSecret: p.cfg.ClientSecret,
		RefreshToken: p.cfg.RefreshToken,
		Flow:         p.cfg.Flow,
		LoginBaseURL: p.loginBaseURL,
		// OnRefreshTokenRotated is left nil here — refresh-token persistence
		// to the secret store is the runner's responsibility and lives outside
		// the channel adapter to keep this package free of secrets.Store deps.
	}, httpClient)

	p.graph = newGraphClient(p.graphBaseURL, httpClient, p.auth)
	p.dedup = newDedup(1000)
	p.cursor = newCursor(p.cfg.CursorPath)

	// Resolve our own identity for the self-loop guard. Fail-fast if this errors.
	switch p.cfg.Flow {
	case FlowDelegated:
		me, err := p.graph.Me(ctx)
		if err != nil {
			return fmt.Errorf("msteams: GET /me: %w", err)
		}
		p.ownUserID = me.ID
		p.ownDisplayName = me.DisplayName
	case FlowClientCredentials:
		me, err := p.graph.User(ctx, p.cfg.UserID)
		if err != nil {
			return fmt.Errorf("msteams: GET /users/%s: %w", p.cfg.UserID, err)
		}
		p.ownUserID = me.ID
		p.ownDisplayName = me.DisplayName
	}

	fmt.Printf("  msteams adapter (Graph polling) started as %s [%s]\n",
		p.ownDisplayName, p.ownUserID)

	return p.pollLoop(ctx, handler)
}

func (p *Plugin) Stop() error {
	p.once.Do(func() { close(p.stopCh) })
	return nil
}

// NormalizeEvent decodes a raw Graph chatMessage JSON into a ChannelEvent.
// Used by the polling loop and by tests that want to feed a stored payload
// through the adapter.
func (p *Plugin) NormalizeEvent(raw []byte) (*channels.ChannelEvent, error) {
	var msg ChatMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return nil, fmt.Errorf("msteams: parse chatMessage: %w", err)
	}
	return p.normalizeChatMessage(&msg)
}

func (p *Plugin) normalizeChatMessage(msg *ChatMessage) (*channels.ChannelEvent, error) {
	if msg.From == nil || msg.From.User == nil {
		return nil, fmt.Errorf("msteams: message has no user from")
	}
	text := msg.Body.Content
	if msg.Body.ContentType == "html" {
		text = markdown.TeamsHTMLToPlain(text)
	}
	text = stripBotMention(text, p.ownDisplayName)

	return &channels.ChannelEvent{
		Channel:     "msteams",
		WorkspaceID: msg.ChatID,
		UserID:      msg.From.User.ID,
		MessageID:   msg.ID,
		Message:     text,
	}, nil
}

func (p *Plugin) pollLoop(ctx context.Context, handler channels.EventHandler) error {
	// Two polling strategies, chosen by auth flow:
	//
	//   client_credentials → app-only token → /users/{id}/chats/getAllMessages/delta
	//     One global delta cursor. Requires Chat.Read.All application permission
	//     with admin consent. Most efficient (one HTTP call per tick).
	//
	//   delegated → user token → /chats/{id}/messages/delta per chat
	//     getAllMessages/delta is app-only and returns HTTP 412 PreconditionFailed
	//     with a delegated token. Instead, list /me/chats and maintain one delta
	//     cursor per chat. More HTTP per tick but works with personal API perms
	//     (Chat.Read) and needs no admin consent.
	if p.cfg.Flow == FlowDelegated {
		return p.pollLoopDelegated(ctx, handler)
	}
	return p.pollLoopAppOnly(ctx, handler)
}

func (p *Plugin) pollLoopAppOnly(ctx context.Context, handler channels.EventHandler) error {
	pageURL := p.cursor.load()
	if pageURL == "" {
		// First run: skip the historical backlog.
		pageURL = p.graph.InitialDeltaURL(p.ownUserID, time.Now().UTC())
	}

	// Backoff state for transient errors. Reset on each successful poll.
	backoff := 5 * time.Second
	maxBackoff := 60 * time.Second

	timer := time.NewTimer(p.cfg.PollInterval)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-p.stopCh:
			return nil
		case <-timer.C:
		}

		page, err := p.graph.FetchDeltaPage(ctx, pageURL)
		if err != nil {
			retry := p.handlePollError(err, &pageURL, &backoff, maxBackoff)
			timer.Reset(retry)
			continue
		}
		// Success — reset backoff.
		backoff = 5 * time.Second

		for i := range page.Messages {
			p.dispatch(ctx, &page.Messages[i], "", handler)
		}

		// Drain pagination immediately (no sleep mid-batch).
		if page.NextLink != "" {
			pageURL = page.NextLink
			timer.Reset(0)
			continue
		}
		if page.DeltaLink != "" {
			pageURL = page.DeltaLink
			if serr := p.cursor.save(page.DeltaLink); serr != nil {
				log.Printf("[msteams] cursor save failed: %v", serr)
			}
		}
		timer.Reset(p.cfg.PollInterval)
	}
}

// pollLoopDelegated polls each known chat for new messages using the
// non-delta /chats/{id}/messages endpoint. Microsoft Graph's v1.0 endpoint
// exposes no delta primitive for chatMessage in delegated context — both
// /chats/{id}/messages/delta and /users/{id}/chats/getAllMessages/delta
// reject delegated tokens — so we track per-chat watermarks ourselves
// using lastModifiedDateTime.
//
// Strategy per chat:
//
//  1. Bootstrap: if LastSeenTime is empty, set it to "now" and skip
//     dispatch on this tick. The agent only sees messages received AFTER
//     it started running; existing chat history is intentionally invisible.
//  2. Steady state: list the most recent 50 messages, dispatch those
//     newer than LastSeenTime (in chronological order, since Graph
//     returns newest-first), then advance LastSeenTime to the newest
//     message's lastModifiedDateTime.
//
// Chat enumeration via /me/chats runs once at startup and every
// chatRefreshInterval (default 60 s) to pick up new chats.
func (p *Plugin) pollLoopDelegated(ctx context.Context, handler channels.EventHandler) error {
	const chatRefreshInterval = 60 * time.Second
	const maxChats = 50

	chats, err := p.graph.ListChats(ctx, maxChats)
	if err != nil {
		log.Printf("[msteams] WARN initial ListChats failed (will retry): %v", err)
	} else {
		log.Printf("[msteams] discovered %d chats", len(chats))
	}

	chatType := make(map[string]string, len(chats))
	for _, ch := range chats {
		chatType[ch.ID] = ch.ChatType
	}

	cursors := p.cursor.loadChats()
	if cursors == nil {
		cursors = map[string]chatCursorState{}
	}

	// forbiddenChats tracks chats we cannot read with the current delegated
	// token. Meeting chats (chat IDs of the form 19:meeting_...@thread.v2)
	// commonly 403 — Microsoft requires per-meeting consent beyond the
	// delegated Chat.Read scope. Once a chat returns 403 we skip it on
	// subsequent ticks; the set is cleared every chatRefreshInterval so
	// permission grants are eventually picked up.
	forbiddenChats := map[string]bool{}

	backoff := 5 * time.Second
	maxBackoff := 60 * time.Second

	pollTimer := time.NewTimer(p.cfg.PollInterval)
	refreshTimer := time.NewTimer(chatRefreshInterval)
	defer pollTimer.Stop()
	defer refreshTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-p.stopCh:
			return nil
		case <-refreshTimer.C:
			latest, lerr := p.graph.ListChats(ctx, maxChats)
			if lerr != nil {
				log.Printf("[msteams] WARN ListChats refresh failed: %v", lerr)
			} else {
				for _, ch := range latest {
					chatType[ch.ID] = ch.ChatType
				}
				chats = latest
			}
			// Re-evaluate forbidden chats — Microsoft may have granted
			// permission since we last checked.
			forbiddenChats = map[string]bool{}
			refreshTimer.Reset(chatRefreshInterval)
			continue
		case <-pollTimer.C:
		}

		anyErr := false
		for _, ch := range chats {
			// Skip chats we've already learned we cannot read.
			if forbiddenChats[ch.ID] {
				continue
			}

			state := cursors[ch.ID]

			if state.LastSeenTime == "" {
				// First contact with this chat: anchor the watermark at
				// "now" so historical messages stay invisible. No poll —
				// no need to GET when we're going to throw the results
				// away anyway. The next tick starts dispatching real
				// new messages.
				state.LastSeenTime = time.Now().UTC().Format(time.RFC3339Nano)
				cursors[ch.ID] = state
				continue
			}

			msgs, ferr := p.graph.ListChatMessages(ctx, ch.ID, 50)
			if ferr != nil {
				// 401 and 429 are GLOBAL — affect every chat, so break
				// out of the chat loop and apply a global backoff.
				if errIs(ferr, errUnauthorized) {
					if _, rerr := p.auth.ForceRefresh(context.Background()); rerr != nil {
						log.Printf("[msteams] ERROR token refresh failed: %v", rerr)
					}
					anyErr = true
					break
				}
				if errIsRateLimited(ferr) {
					retry := rateLimitRetry(ferr)
					log.Printf("[msteams] WARN 429 rate limited; honouring Retry-After=%s", retry)
					pollTimer.Reset(retry)
					anyErr = true
					break
				}
				// 403 is PER-CHAT — we lack permission to read this
				// specific chat (most often a meeting chat that
				// requires extra consent). Mark it forbidden so we
				// stop polling it, log once, and continue iterating
				// the other chats. The set is cleared on the next
				// chat-list refresh in case the permission was added.
				if errIs(ferr, errForbidden) {
					log.Printf("[msteams] 403 forbidden on chat %s (type=%s); skipping. "+
						"Meeting chats often require additional consent beyond delegated Chat.Read.",
						ch.ID, chatType[ch.ID])
					forbiddenChats[ch.ID] = true
					continue
				}
				// Other transient errors (5xx, network): log + continue
				// with other chats. We'll apply global backoff once the
				// whole iteration finishes.
				log.Printf("[msteams] WARN poll error on chat %s (backoff=%s): %v", ch.ID, backoff, ferr)
				anyErr = true
				continue
			}

			// Find messages newer than the watermark. Graph returns
			// newest-first; we dispatch in chronological order so
			// session continuity works as expected on the handler side.
			var fresh []*ChatMessage
			for i := range msgs {
				if compareTimestamps(msgs[i].LastModifiedDateTime, state.LastSeenTime) > 0 {
					fresh = append(fresh, &msgs[i])
				}
			}
			// Reverse: dispatch oldest-first.
			for i, j := 0, len(fresh)-1; i < j; i, j = i+1, j-1 {
				fresh[i], fresh[j] = fresh[j], fresh[i]
			}
			for _, msg := range fresh {
				msg.ChatID = ch.ID
				if msg.ChatType == "" {
					msg.ChatType = chatType[ch.ID]
				}
				p.dispatch(ctx, msg, chatType[ch.ID], handler)
			}

			// Advance watermark to the newest message we saw (newest is
			// msgs[0] because Graph returned them sorted desc).
			if len(msgs) > 0 && compareTimestamps(msgs[0].LastModifiedDateTime, state.LastSeenTime) > 0 {
				state.LastSeenTime = msgs[0].LastModifiedDateTime
			}
			cursors[ch.ID] = state
		}

		if anyErr {
			d := backoff
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			pollTimer.Reset(d)
			continue
		}
		backoff = 5 * time.Second
		if serr := p.cursor.saveChats(cursors); serr != nil {
			log.Printf("[msteams] cursor saveChats failed: %v", serr)
		}
		pollTimer.Reset(p.cfg.PollInterval)
	}
}

// compareTimestamps compares two RFC 3339 timestamp strings lexicographically.
// This works for all ISO 8601 formats Graph returns because they're already
// sortable as strings (fixed-width year/month/day/etc.).
func compareTimestamps(a, b string) int {
	if a == b {
		return 0
	}
	if a < b {
		return -1
	}
	return 1
}

// handlePollError classifies the error per the §9 table and returns the
// next-poll delay. May mutate *pageURL (cursor reset on 410) or *backoff.
func (p *Plugin) handlePollError(err error, pageURL *string, backoff *time.Duration, maxBackoff time.Duration) time.Duration {
	switch {
	case errIs(err, errCursorExpired):
		// 410 Gone — discard cursor, reinit from "now".
		log.Printf("[msteams] WARN delta cursor expired (410); reinitialising from now")
		*pageURL = p.graph.InitialDeltaURL(p.ownUserID, time.Now().UTC())
		_ = p.cursor.save("") // wipe the file
		return p.cfg.PollInterval

	case errIs(err, errUnauthorized):
		// 401 — force token refresh, retry on next tick.
		if _, rerr := p.auth.ForceRefresh(context.Background()); rerr != nil {
			log.Printf("[msteams] ERROR token refresh failed: %v; backing off 60s", rerr)
			return 60 * time.Second
		}
		log.Printf("[msteams] INFO token refreshed after 401; retrying next tick")
		return p.cfg.PollInterval

	case errIs(err, errForbidden):
		// 403 — permission issue. Log + 60s backoff. Don't spam.
		log.Printf("[msteams] ERROR 403 forbidden; check Chat.Read permission. See docs/channels/msteams.md. Backing off 60s.")
		return 60 * time.Second

	case errIsRateLimited(err):
		retry := rateLimitRetry(err)
		log.Printf("[msteams] WARN 429 rate limited; honouring Retry-After=%s", retry)
		return retry

	default:
		// 5xx, network, or anything else — exponential backoff.
		log.Printf("[msteams] WARN poll error (backoff=%s): %v", *backoff, err)
		d := *backoff
		*backoff = *backoff * 2
		if *backoff > maxBackoff {
			*backoff = maxBackoff
		}
		return d
	}
}

// dispatch runs the admission gate and, if admitted, forwards the event to
// the handler on a background goroutine. The chatTypeHint allows the
// delegated polling path to supply chatType from /me/chats when Graph's
// chatMessage payload omits it.
func (p *Plugin) dispatch(ctx context.Context, msg *ChatMessage, chatTypeHint string, handler channels.EventHandler) {
	// Dedup first — applies to dropped messages too so we don't re-evaluate
	// the same ID across paginated pages.
	if p.dedup.seen(msg.ID) {
		return
	}
	p.dedup.mark(msg.ID)

	// Determine the chat type. Trust the chatMessage payload when present,
	// then fall back to the hint from /me/chats. Both can be empty for the
	// app-only path until we extend it; treat that as non-DM so the mention
	// path still gates appropriately.
	chatType := msg.ChatType
	if chatType == "" {
		chatType = chatTypeHint
	}
	if chatType == "" {
		chatType = "unknown"
	}

	result := admit(msg, p.ownUserID, p.cfg.AllowBotIDs, p.cfg.AdmitMode, chatType)
	if !result.admit {
		// On mention-mode drops, surface the mentions array so operators
		// can see WHY the message wasn't recognised as a mention. The
		// most common confusion in delegated mode: the user types
		// "@AgentName" thinking they're tagging the agent, but in
		// delegated mode the agent has no Teams display name distinct
		// from the user — they must @-mention their own display name
		// (Teams autocomplete required, not just typing @+text).
		if strings.Contains(result.reason, "non-mention") {
			log.Printf("[msteams] DEBUG %s (msg_id=%s, mentions=%d, ownUserID=%s)",
				result.reason, msg.ID, len(msg.Mentions), p.ownUserID)
			for i, m := range msg.Mentions {
				log.Printf("[msteams] DEBUG   mention[%d]: text=%q user.id=%q displayName=%q",
					i, m.Text, m.Mentioned.User.ID, m.Mentioned.User.DisplayName)
			}
			if len(msg.Mentions) == 0 {
				log.Printf("[msteams] DEBUG   no formal mentions in payload — in delegated mode you must @-mention your own Teams display name (autocomplete required); the agent shares your identity (%s)", p.ownUserID)
			}
		} else {
			log.Printf("[msteams] DEBUG %s (msg_id=%s)", result.reason, msg.ID)
		}
		return
	}

	event, err := p.normalizeChatMessage(msg)
	if err != nil {
		log.Printf("[msteams] WARN normalise failed: %v", err)
		return
	}

	// Inject recent chat history so the LLM has conversational context
	// (Teams chats are thread-shaped; without this the agent sees only
	// the literal mention/DM and can't answer prompts like "summarise
	// this week's updates"). Best-effort: an error fetching history
	// downgrades to dispatch without context.
	if p.cfg.IncludeRecentHistory {
		if history, herr := p.graph.ListChatMessages(ctx, msg.ChatID, p.cfg.RecentHistoryCount); herr == nil {
			event.Message = prependChatHistory(history, msg.ID, event.Message)
		} else {
			log.Printf("[msteams] WARN history fetch failed for chat %s (continuing without context): %v", msg.ChatID, herr)
		}
	}

	go func() {
		taskCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		resp, herr := handler(taskCtx, event)
		if herr != nil {
			log.Printf("[msteams] handler error: %v", herr)
			return
		}
		if serr := p.SendResponse(event, resp); serr != nil {
			log.Printf("[msteams] send response error: %v", serr)
		}
	}()
}

// prependChatHistory formats the most recent chat messages chronologically
// (oldest first) and prepends them as a context block before the user's
// current prompt. Skips currentMsgID to avoid duplicating it inside its own
// context. Total block is soft-capped at ~5000 chars so even chatty threads
// don't blow the LLM context budget.
func prependChatHistory(history []ChatMessage, currentMsgID, prompt string) string {
	if len(history) == 0 {
		return prompt
	}

	const softCap = 5000

	// Graph returns newest-first; iterate from oldest by walking the
	// slice in reverse.
	var b strings.Builder
	b.WriteString("[Recent chat history for context — most recent message at the bottom:]\n")
	for i := len(history) - 1; i >= 0; i-- {
		m := history[i]
		if m.ID == currentMsgID {
			continue
		}
		text := m.Body.Content
		if m.Body.ContentType == "html" {
			text = markdown.TeamsHTMLToPlain(text)
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		author := "(unknown)"
		if m.From != nil && m.From.User != nil && m.From.User.DisplayName != "" {
			author = m.From.User.DisplayName
		} else if m.From != nil && m.From.Application != nil && m.From.Application.DisplayName != "" {
			author = m.From.Application.DisplayName + " (bot)"
		}
		entry := fmt.Sprintf("%s  %s: %s\n", shortTimestamp(m.CreatedDateTime), author, text)
		if b.Len()+len(entry) > softCap {
			break
		}
		b.WriteString(entry)
	}
	b.WriteString("[End of history. The user's current message follows:]\n\n")
	b.WriteString(prompt)
	return b.String()
}

// shortTimestamp trims an ISO-8601 timestamp to date + minute precision so
// the history block stays readable: "2026-05-22T01:23:45.123Z" → "05-22 01:23".
func shortTimestamp(iso string) string {
	if len(iso) < 16 {
		return iso
	}
	// Expect "YYYY-MM-DDTHH:MM:..." — slice month-day + hour-minute.
	return iso[5:10] + " " + iso[11:16]
}

// SendResponse delivers an agent reply back to the Teams chat. Mirrors the
// Slack/Telegram large-response handling: small responses inline, large
// responses summary + hosted-content attachment, fallback to chunked text.
//
// Every successful outbound message ID is recorded in the dedup ring via
// markSent. In delegated mode the agent shares the user's Graph identity
// (delegated tokens act as the user), so messages we post come back via
// polling with the same from.user.id as messages the user types directly.
// The dedup ring is the ONLY way to tell our own posts apart from real
// inbound traffic before reaching the admission gate.
func (p *Plugin) SendResponse(event *channels.ChannelEvent, response *a2a.Message) error {
	text := extractText(response)
	html := markdown.MarkdownToTeamsHTML(text)
	ctx := context.Background()

	if len(html) <= 24000 {
		id, err := p.graph.PostChatMessage(ctx, event.WorkspaceID, html)
		p.markSent(id)
		return err
	}

	// Large response. Prefer runtime-generated summary; fall back to
	// head-truncation via SplitSummaryAndReport.
	summary := ""
	full := text
	if response != nil {
		summary = response.Summary
	}
	if summary == "" {
		summary, full = markdown.SplitSummaryAndReport(text)
	}
	summaryHTML := markdown.MarkdownToTeamsHTML(summary + "\n\n*Full report attached as file above.*")

	summaryID, err := p.graph.PostChatMessage(ctx, event.WorkspaceID, summaryHTML)
	if err != nil {
		// If even the summary can't be posted, fall back to chunked plain text.
		return p.sendChunked(ctx, event.WorkspaceID, html)
	}
	p.markSent(summaryID)

	attID, err := p.graph.PostChatMessageWithAttachment(ctx, event.WorkspaceID, "research-report.md", "text/markdown", []byte(full))
	if err != nil {
		log.Printf("[msteams] attachment failed, falling back to chunked send: %v", err)
		return p.sendChunked(ctx, event.WorkspaceID, html)
	}
	p.markSent(attID)
	return nil
}

func (p *Plugin) sendChunked(ctx context.Context, chatID, html string) error {
	for _, chunk := range markdown.SplitMessageTeams(html) {
		id, err := p.graph.PostChatMessage(ctx, chatID, chunk)
		if err != nil {
			return err
		}
		p.markSent(id)
	}
	return nil
}

// markSent records an outbound message ID in the dedup ring so the polling
// loop drops it instead of routing it through admission as a fresh inbound.
// Safe to call with an empty id (no-op) — keeps call sites tidy when the
// post succeeded but Graph somehow omitted the ID.
func (p *Plugin) markSent(id string) {
	if id == "" || p.dedup == nil {
		return
	}
	p.dedup.mark(id)
}

// --- helpers ---

func strOrDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

// parseAllowBotIDs accepts comma- or space-separated bot IDs and builds a
// set. Empty strings are skipped.
func parseAllowBotIDs(s string) map[string]bool {
	out := map[string]bool{}
	if s == "" {
		return out
	}
	for _, raw := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' }) {
		if id := strings.TrimSpace(raw); id != "" {
			out[id] = true
		}
	}
	return out
}

// extractText pulls the text content out of an A2A message, mirroring the
// pattern used by Slack and Telegram.
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
	return strings.Join(parts, "\n")
}

// errIs is a small wrapper around errors.Is that tolerates wrapped sentinels
// (rateLimitedError unwraps to errRateLimited).
func errIs(err, target error) bool {
	for e := err; e != nil; {
		if e == target {
			return true
		}
		if u, ok := e.(interface{ Unwrap() error }); ok {
			e = u.Unwrap()
			continue
		}
		return false
	}
	return false
}

func errIsRateLimited(err error) bool {
	if _, ok := err.(*rateLimitedError); ok {
		return true
	}
	return errIs(err, errRateLimited)
}

func rateLimitRetry(err error) time.Duration {
	if r, ok := err.(*rateLimitedError); ok {
		return r.RetryAfter
	}
	return 10 * time.Second
}
