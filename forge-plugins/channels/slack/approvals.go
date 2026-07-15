package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/initializ/forge/forge-core/channels"
)

// Interactive DEFER (R4c, #211) human-approval delivery over Socket Mode
// (#310). The bot posts a Block Kit message with Approve/Reject buttons; the
// click returns as an `interactive` envelope on the same outbound WebSocket
// (no inbound exposure), which resolves the deferred task via the wired
// resolver. Compile-time assertion that Plugin satisfies the capability:
var _ channels.ApprovalDeliverer = (*Plugin)(nil)

const (
	approveActionID = "forge_defer_approve"
	rejectActionID  = "forge_defer_reject"
)

// SetApprovalResolver wires the callback invoked when an approver clicks a
// button. Called once by the runtime at startup.
func (p *Plugin) SetApprovalResolver(r channels.ApprovalResolver) {
	p.approvalResolver = r
}

// DeliverApproval posts a Block Kit Approve/Reject message to req.Target (a
// Slack channel ID like "C0123ABC5" or a name like "#oncall"). The buttons
// carry the task id so the click can be routed back to the deferral. Delivery
// failure is returned to the caller (the runtime logs it, non-fatal — the
// approver can still POST the decision directly).
func (p *Plugin) DeliverApproval(ctx context.Context, req channels.ApprovalRequest) error {
	if req.Target == "" {
		return fmt.Errorf("slack DeliverApproval: empty target channel")
	}
	channelID, err := p.resolveChannelID(ctx, req.Target)
	if err != nil {
		return fmt.Errorf("slack DeliverApproval: %w", err)
	}
	payload := buildApprovalPayload(req)
	payload["channel"] = channelID
	return p.postMessage(payload)
}

// channelIDPattern matches an encoded Slack channel/group/DM id (C…/G…/D…),
// which chat.postMessage accepts directly. A "#name" or bare name is resolved.
var channelIDPattern = regexp.MustCompile(`^[CGD][A-Z0-9]{6,}$`)

// resolveChannelID turns a DEFER `to:` channel target into a Slack channel id.
// An encoded id passes through unchanged (no API call). A "#name" or bare name
// is resolved via conversations.list (public + private, matched
// case-insensitively) and cached — Slack ids are stable across renames, so the
// cache never goes stale on a rename. FAILS CLOSED: an unresolvable target
// returns an error so an approval is never silently misrouted (needed because
// chat.postMessage cannot address a PRIVATE channel by #name, and private is
// the recommended approvals channel).
func (p *Plugin) resolveChannelID(ctx context.Context, target string) (string, error) {
	target = strings.TrimSpace(target)
	name := strings.TrimPrefix(target, "#")
	// No "#" prefix AND looks like an encoded id → already an id.
	if target == name && channelIDPattern.MatchString(target) {
		return target, nil
	}
	name = strings.ToLower(name)
	if name == "" {
		return "", fmt.Errorf("empty channel target")
	}

	p.chanMu.Lock()
	if id, ok := p.chanIDCache[name]; ok {
		p.chanMu.Unlock()
		return id, nil
	}
	p.chanMu.Unlock()

	id, err := p.lookupChannelID(ctx, name)
	if err != nil {
		return "", err
	}
	p.chanMu.Lock()
	if p.chanIDCache == nil {
		p.chanIDCache = map[string]string{}
	}
	p.chanIDCache[name] = id
	p.chanMu.Unlock()
	return id, nil
}

type slackConversation struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// lookupChannelID pages through conversations.list to find the channel whose
// name matches (case-insensitive). Bounded to 20 pages (≈20k channels) so a
// pathological workspace can't spin forever. Requires the bot's channels:read
// (public) + groups:read (private) scopes, and the bot must be a member.
func (p *Plugin) lookupChannelID(ctx context.Context, name string) (string, error) {
	cursor := ""
	for range 20 { // bound ≈ 20k channels
		convs, next, err := p.listConversations(ctx, cursor)
		if err != nil {
			return "", err
		}
		for _, c := range convs {
			if strings.EqualFold(c.Name, name) {
				return c.ID, nil
			}
		}
		if next == "" {
			break
		}
		cursor = next
	}
	return "", fmt.Errorf("channel %q not found — is the bot a member, and does it have channels:read + groups:read? (or use the channel id)", "#"+name)
}

// listConversations fetches one page of the workspace's public + private
// channels the bot can see.
func (p *Plugin) listConversations(ctx context.Context, cursor string) ([]slackConversation, string, error) {
	q := url.Values{}
	q.Set("types", "public_channel,private_channel")
	q.Set("limit", "1000")
	q.Set("exclude_archived", "true")
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.apiBase+"/conversations.list?"+q.Encode(), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+p.botToken)
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		OK               bool                `json:"ok"`
		Error            string              `json:"error"`
		Channels         []slackConversation `json:"channels"`
		ResponseMetadata struct {
			NextCursor string `json:"next_cursor"`
		} `json:"response_metadata"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, "", fmt.Errorf("conversations.list decode: %w", err)
	}
	if !out.OK {
		return nil, "", fmt.Errorf("conversations.list: %s", out.Error)
	}
	return out.Channels, out.ResponseMetadata.NextCursor, nil
}

// buildApprovalPayload renders the chat.postMessage body for an approval
// request (minus the `channel`, which DeliverApproval sets from the resolved
// id). Split out (pure) so tests can assert the Block Kit shape without a live
// Slack. `text` is the notification fallback; the blocks carry the interactive
// buttons whose `value` is the task id (the resolution key).
func buildApprovalPayload(req channels.ApprovalRequest) map[string]any {
	summary := fmt.Sprintf("*Approval required* for `%s`", req.Tool)
	detail := summary
	if req.Context != "" {
		detail += "\n" + req.Context
	}
	if req.Timeout > 0 {
		detail += fmt.Sprintf("\n_Auto-denies in %s._", req.Timeout.Round(time.Second))
	}
	return map[string]any{
		"text": fmt.Sprintf("Approval required for %s", req.Tool), // fallback / a11y
		"blocks": []any{
			map[string]any{
				"type": "section",
				"text": map[string]any{"type": "mrkdwn", "text": detail},
			},
			map[string]any{
				"type":     "actions",
				"block_id": "forge_defer:" + req.TaskID,
				"elements": []any{
					map[string]any{
						"type":      "button",
						"action_id": approveActionID,
						"style":     "primary",
						"text":      map[string]any{"type": "plain_text", "text": "Approve"},
						"value":     req.TaskID,
					},
					map[string]any{
						"type":      "button",
						"action_id": rejectActionID,
						"style":     "danger",
						"text":      map[string]any{"type": "plain_text", "text": "Reject"},
						"value":     req.TaskID,
					},
				},
			},
		},
	}
}

// slackInteraction is the subset of Slack's block_actions payload we consume.
type slackInteraction struct {
	Type string `json:"type"`
	User struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		Name     string `json:"name"`
	} `json:"user"`
	Actions []struct {
		ActionID string `json:"action_id"`
		Value    string `json:"value"`
	} `json:"actions"`
	Channel struct {
		ID string `json:"id"`
	} `json:"channel"`
	Message struct {
		TS string `json:"ts"`
	} `json:"message"`
}

// parseApprovalInteraction extracts an ApprovalDecision from a Slack
// block_actions payload. Pure + testable. Returns ok=false for any payload
// that isn't one of our approval buttons (other apps' interactions, non-button
// types) so the caller ignores it. `channelID`/`msgTS` locate the message for
// the outcome update.
func parseApprovalInteraction(payload []byte) (dec channels.ApprovalDecision, userID, channelID, msgTS string, ok bool) {
	var in slackInteraction
	if err := json.Unmarshal(payload, &in); err != nil {
		return dec, "", "", "", false
	}
	if in.Type != "block_actions" || len(in.Actions) == 0 {
		return dec, "", "", "", false
	}
	a := in.Actions[0]
	var decision string
	switch a.ActionID {
	case approveActionID:
		decision = "approve"
	case rejectActionID:
		decision = "reject"
	default:
		return dec, "", "", "", false // not our button
	}
	if a.Value == "" {
		return dec, "", "", "", false
	}
	approver := in.User.Username
	if approver == "" {
		approver = in.User.Name
	}
	if approver == "" {
		approver = in.User.ID
	}
	return channels.ApprovalDecision{
		TaskID:   a.Value,
		Decision: decision,
		Approver: approver,
	}, in.User.ID, in.Channel.ID, in.Message.TS, true
}

// resolveUserEmail resolves a Slack user id to their email via users.info
// (cached), for the DEFER approver allowlist (#313). Requires the
// users:read.email scope. Returns an error when the email can't be determined
// (missing scope, guest without an email) — the caller leaves ApproverEmail
// empty and the runtime fails closed against a configured allowlist.
func (p *Plugin) resolveUserEmail(ctx context.Context, userID string) (string, error) {
	if userID == "" {
		return "", fmt.Errorf("empty user id")
	}
	p.userMu.Lock()
	if e, ok := p.userEmailCache[userID]; ok {
		p.userMu.Unlock()
		return e, nil
	}
	p.userMu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.apiBase+"/users.info?user="+url.QueryEscape(userID), nil)
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
			Profile struct {
				Email string `json:"email"`
			} `json:"profile"`
		} `json:"user"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("users.info decode: %w", err)
	}
	if !out.OK {
		return "", fmt.Errorf("users.info: %s", out.Error)
	}
	email := strings.ToLower(strings.TrimSpace(out.User.Profile.Email))
	if email == "" {
		return "", fmt.Errorf("no email on profile (guest, or missing users:read.email scope)")
	}
	p.userMu.Lock()
	if p.userEmailCache == nil {
		p.userEmailCache = map[string]string{}
	}
	p.userEmailCache[userID] = email
	p.userMu.Unlock()
	return email, nil
}

// handleInteractive resolves an approval button click. No-op for interactions
// that aren't ours. Best-effort updates the source message with the outcome.
func (p *Plugin) handleInteractive(ctx context.Context, payload []byte) error {
	dec, userID, channelID, msgTS, ok := parseApprovalInteraction(payload)
	if !ok {
		return nil // not a forge approval interaction; ignore quietly
	}
	// Resolve the approver's email for the runtime allowlist (#313). Best
	// effort: on failure ApproverEmail stays empty and the runtime fails
	// closed if the tool declares an allowlist. We still log it so a missing
	// users:read.email scope is visible.
	if email, err := p.resolveUserEmail(ctx, userID); err == nil {
		dec.ApproverEmail = email
	} else {
		p.logWarn("could not resolve approver email", map[string]any{"user": userID, "error": err.Error()})
	}
	if p.approvalResolver == nil {
		return fmt.Errorf("approval click for task %s but no resolver wired", dec.TaskID)
	}
	if err := p.approvalResolver(ctx, dec); err != nil {
		// e.g. the deferral already resolved or timed out (404/409). Surface it
		// on the message so the approver isn't left guessing.
		p.updateApprovalMessage(channelID, msgTS, fmt.Sprintf(":warning: could not record %s: %v", dec.Decision, err))
		return err
	}
	p.updateApprovalMessage(channelID, msgTS, approvalOutcomeText(dec))
	return nil
}

// approvalOutcomeText is the message body that replaces the buttons after a
// decision.
func approvalOutcomeText(d channels.ApprovalDecision) string {
	icon := ":white_check_mark:"
	verb := "approved"
	if d.Decision == "reject" {
		icon, verb = ":no_entry:", "rejected"
	}
	return fmt.Sprintf("%s %s by @%s", icon, verb, d.Approver)
}

// updateApprovalMessage replaces the approval message's blocks with a plain
// outcome line via chat.update. Best-effort — a failure to update the UI must
// not affect the (already-recorded) decision.
func (p *Plugin) updateApprovalMessage(channelID, msgTS, text string) {
	if channelID == "" || msgTS == "" {
		return
	}
	body, _ := json.Marshal(map[string]any{
		"channel": channelID,
		"ts":      msgTS,
		"text":    text,
		"blocks":  []any{map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": text}}},
	})
	req, err := http.NewRequest(http.MethodPost, p.apiBase+"/chat.update", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.botToken)
	resp, err := p.client.Do(req)
	if err != nil {
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}
