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
	pageURL := p.cursor.load()
	if pageURL == "" {
		// First run: skip the historical backlog.
		userID := p.ownUserID
		pageURL = p.graph.InitialDeltaURL(userID, time.Now().UTC())
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
			p.dispatch(ctx, &page.Messages[i], handler)
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
// the handler on a background goroutine.
func (p *Plugin) dispatch(ctx context.Context, msg *ChatMessage, handler channels.EventHandler) {
	// Dedup first — applies to dropped messages too so we don't re-evaluate
	// the same ID across paginated pages.
	if p.dedup.seen(msg.ID) {
		return
	}
	p.dedup.mark(msg.ID)

	// Determine the chat type. Graph doesn't always include it on
	// chatMessage; for chats we can derive "oneOnOne" only when chatId
	// matches the 1:1 pattern OR when we explicitly fetch the chat.
	// For the inbound gate we treat any message that includes a mention OR
	// arrives from a known 1:1 chat as DM-eligible. The msg.ChatType field
	// is sometimes populated by Graph; trust it when present.
	chatType := msg.ChatType
	if chatType == "" {
		// Best-effort: chat IDs of the form 19:<uuid>_<uuid>@unq.gbl.spaces
		// are 1:1; group/meeting use different suffixes. We err on the side
		// of treating unknown as non-DM so the mention path still gates.
		chatType = "unknown"
	}

	result := admit(msg, p.ownUserID, p.cfg.AllowBotIDs, p.cfg.AdmitMode, chatType)
	if !result.admit {
		log.Printf("[msteams] DEBUG %s (msg_id=%s)", result.reason, msg.ID)
		return
	}

	event, err := p.normalizeChatMessage(msg)
	if err != nil {
		log.Printf("[msteams] WARN normalise failed: %v", err)
		return
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

// SendResponse delivers an agent reply back to the Teams chat. Mirrors the
// Slack/Telegram large-response handling: small responses inline, large
// responses summary + hosted-content attachment, fallback to chunked text.
func (p *Plugin) SendResponse(event *channels.ChannelEvent, response *a2a.Message) error {
	text := extractText(response)
	html := markdown.MarkdownToTeamsHTML(text)
	ctx := context.Background()

	if len(html) <= 24000 {
		return p.graph.PostChatMessage(ctx, event.WorkspaceID, html)
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

	if err := p.graph.PostChatMessage(ctx, event.WorkspaceID, summaryHTML); err != nil {
		// If even the summary can't be posted, fall back to chunked plain text.
		return p.sendChunked(ctx, event.WorkspaceID, html)
	}

	if err := p.graph.PostChatMessageWithAttachment(ctx, event.WorkspaceID, "research-report.md", "text/markdown", []byte(full)); err != nil {
		log.Printf("[msteams] attachment failed, falling back to chunked send: %v", err)
		return p.sendChunked(ctx, event.WorkspaceID, html)
	}
	return nil
}

func (p *Plugin) sendChunked(ctx context.Context, chatID, html string) error {
	for _, chunk := range markdown.SplitMessageTeams(html) {
		if err := p.graph.PostChatMessage(ctx, chatID, chunk); err != nil {
			return err
		}
	}
	return nil
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
