package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/initializ/forge/forge-core/channels"
)

// MCP delegated-consent (#343) delivery over the SAME Socket Mode connection +
// bot token the adapter already uses. The bot presents a "Connect <server>"
// Block Kit message to the requesting user — in the origin thread when the
// request came via Slack, else a DM resolved by email — with a URL button that
// opens the login link. The Connect button is a plain link-open (the OAuth
// callback resolves the gate, not a Slack round-trip); an optional "Cancel"
// button fails the parked call fast via the wired canceler.
var _ channels.ConsentDeliverer = (*Plugin)(nil)

const (
	// consentConnectActionID tags the URL button. URL buttons still emit a
	// block_actions event on click; we ignore it (the browser opens the link).
	consentConnectActionID = "forge_mcp_consent_connect"
	// consentCancelActionID tags the Cancel button; its click routes to the
	// wired ConsentCanceler.
	consentCancelActionID = "forge_mcp_consent_cancel"
)

// SetConsentCanceler wires the callback invoked when the user cancels a consent
// prompt. Called once by the runtime at startup; may be nil (no Cancel button).
func (p *Plugin) SetConsentCanceler(c channels.ConsentCanceler) {
	p.consentCanceler = c
}

// DeliverConsent presents the consent login link to the requesting user. It
// reaches them in the origin thread if the request came via Slack, else opens a
// DM resolved from their email. Delivery failure is returned (the runtime logs
// it, non-fatal — the parked call still resumes when the callback lands, and
// mcp_auth_required was already emitted as the platform-read backstop).
func (p *Plugin) DeliverConsent(ctx context.Context, req channels.ConsentPrompt) error {
	if req.AuthorizeURL == "" {
		return fmt.Errorf("slack DeliverConsent: empty authorize URL")
	}
	channelID, threadTS, err := p.consentTarget(ctx, req)
	if err != nil {
		return fmt.Errorf("slack DeliverConsent: %w", err)
	}
	payload := buildConsentPayload(req, p.consentCanceler != nil)
	payload["channel"] = channelID
	if threadTS != "" {
		payload["thread_ts"] = threadTS
	}
	return p.postMessage(payload)
}

// consentTarget resolves where to post: the origin Slack thread when present,
// else a DM to the subject resolved by email. Returns the channel id and an
// optional thread_ts.
func (p *Plugin) consentTarget(ctx context.Context, req channels.ConsentPrompt) (channelID, threadTS string, err error) {
	if o := req.Origin; o != nil && strings.EqualFold(o.Adapter, "slack") && o.Channel != "" {
		return o.Channel, o.ThreadTS, nil
	}
	// Cold DM: email → user id → open DM channel.
	if req.Subject == "" {
		return "", "", fmt.Errorf("no subject to DM and no slack origin")
	}
	userID, err := p.lookupUserIDByEmail(ctx, req.Subject)
	if err != nil {
		return "", "", err
	}
	dmID, err := p.openDM(ctx, userID)
	if err != nil {
		return "", "", err
	}
	return dmID, "", nil
}

// lookupUserIDByEmail resolves an email → Slack user id via users.lookupByEmail
// (a single call, unlike paging users.list), cached. Requires
// users:read.email. Fails when the email isn't a workspace member.
func (p *Plugin) lookupUserIDByEmail(ctx context.Context, email string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return "", fmt.Errorf("empty email")
	}
	p.userMu.Lock()
	if id, ok := p.userIDByEmail[email]; ok {
		p.userMu.Unlock()
		return id, nil
	}
	p.userMu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.apiBase+"/users.lookupByEmail?email="+url.QueryEscape(email), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+p.botToken)
	resp, err := p.client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
		User  struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("users.lookupByEmail decode: %w", err)
	}
	if !out.OK || out.User.ID == "" {
		return "", fmt.Errorf("users.lookupByEmail(%s): %s (need users:read.email; is the user in this workspace?)", email, out.Error)
	}
	p.userMu.Lock()
	if p.userIDByEmail == nil {
		p.userIDByEmail = map[string]string{}
	}
	p.userIDByEmail[email] = out.User.ID
	p.userMu.Unlock()
	return out.User.ID, nil
}

// openDM opens (or returns the cached) IM channel with a user via
// conversations.open. Requires im:write. The DM channel id is stable per user,
// so caching it never goes stale.
func (p *Plugin) openDM(ctx context.Context, userID string) (string, error) {
	if userID == "" {
		return "", fmt.Errorf("empty user id")
	}
	p.userMu.Lock()
	if id, ok := p.dmChannel[userID]; ok {
		p.userMu.Unlock()
		return id, nil
	}
	p.userMu.Unlock()

	body, _ := json.Marshal(map[string]any{"users": userID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.apiBase+"/conversations.open", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.botToken)
	resp, err := p.client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		OK      bool   `json:"ok"`
		Error   string `json:"error"`
		Channel struct {
			ID string `json:"id"`
		} `json:"channel"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("conversations.open decode: %w", err)
	}
	if !out.OK || out.Channel.ID == "" {
		return "", fmt.Errorf("conversations.open: %s (need im:write)", out.Error)
	}
	p.userMu.Lock()
	if p.dmChannel == nil {
		p.dmChannel = map[string]string{}
	}
	p.dmChannel[userID] = out.Channel.ID
	p.userMu.Unlock()
	return out.Channel.ID, nil
}

// consentCancelValue is what the Cancel button carries so its click resolves
// the right gate ({subject, server}).
type consentCancelValue struct {
	Subject string `json:"subject"`
	Server  string `json:"server"`
}

// buildConsentPayload renders the chat.postMessage body (minus the channel,
// which DeliverConsent sets). Pure so tests can assert the Block Kit shape.
// withCancel adds a Cancel button (only when a canceler is wired).
func buildConsentPayload(req channels.ConsentPrompt, withCancel bool) map[string]any {
	detail := fmt.Sprintf("*Authorization required* to connect *%s*.\nOpen the link to sign in and grant access.", req.Server)
	if !req.Deadline.IsZero() {
		detail += fmt.Sprintf("\n_Expires %s._", req.Deadline.UTC().Format(time.RFC1123))
	}
	elements := []any{
		map[string]any{
			"type":      "button",
			"action_id": consentConnectActionID,
			"style":     "primary",
			"text":      map[string]any{"type": "plain_text", "text": "Connect " + req.Server},
			"url":       req.AuthorizeURL,
		},
	}
	if withCancel {
		val, _ := json.Marshal(consentCancelValue{Subject: req.Subject, Server: req.Server})
		elements = append(elements, map[string]any{
			"type":      "button",
			"action_id": consentCancelActionID,
			"style":     "danger",
			"text":      map[string]any{"type": "plain_text", "text": "Cancel"},
			"value":     string(val),
		})
	}
	return map[string]any{
		"text": fmt.Sprintf("Authorization required to connect %s", req.Server), // fallback / a11y
		"blocks": []any{
			map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": detail}},
			map[string]any{"type": "actions", "block_id": "forge_mcp_consent", "elements": elements},
		},
	}
}

// parseConsentCancel extracts {subject, server} from a Cancel-button click.
// Pure + testable. ok=false for any interaction that isn't our Cancel button.
func parseConsentCancel(payload []byte) (subject, server, channelID, msgTS string, ok bool) {
	var in slackInteraction
	if err := json.Unmarshal(payload, &in); err != nil {
		return "", "", "", "", false
	}
	if in.Type != "block_actions" || len(in.Actions) == 0 {
		return "", "", "", "", false
	}
	a := in.Actions[0]
	if a.ActionID != consentCancelActionID {
		return "", "", "", "", false
	}
	var v consentCancelValue
	if err := json.Unmarshal([]byte(a.Value), &v); err != nil || v.Server == "" {
		return "", "", "", "", false
	}
	return v.Subject, v.Server, in.Channel.ID, in.Message.TS, true
}

// handleConsentCancel routes a Cancel-button click to the wired canceler and
// updates the message. Returns ok=false when the payload isn't our Cancel
// button so the caller falls through to other interaction handlers.
func (p *Plugin) handleConsentCancel(ctx context.Context, payload []byte) (handled bool, err error) {
	subject, server, channelID, msgTS, ok := parseConsentCancel(payload)
	if !ok {
		return false, nil
	}
	if p.consentCanceler == nil {
		return true, fmt.Errorf("consent cancel click for %s/%s but no canceler wired", subject, server)
	}
	if cErr := p.consentCanceler(ctx, subject, server); cErr != nil {
		p.updateApprovalMessage(channelID, msgTS, fmt.Sprintf(":warning: could not cancel: %v", cErr))
		return true, cErr
	}
	p.updateApprovalMessage(channelID, msgTS, ":no_entry: Authorization canceled.")
	return true, nil
}
