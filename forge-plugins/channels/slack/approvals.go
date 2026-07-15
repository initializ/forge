package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
// Slack channel like "#oncall"). The buttons carry the task id so the click
// can be routed back to the deferral. Delivery failure is returned to the
// caller (the runtime logs it, non-fatal — the approver can still POST the
// decision directly).
func (p *Plugin) DeliverApproval(ctx context.Context, req channels.ApprovalRequest) error {
	if req.Target == "" {
		return fmt.Errorf("slack DeliverApproval: empty target channel")
	}
	return p.postMessage(buildApprovalPayload(req))
}

// buildApprovalPayload renders the chat.postMessage body for an approval
// request. Split out (pure) so tests can assert the Block Kit shape without a
// live Slack. `text` is the notification fallback; the blocks carry the
// interactive buttons whose `value` is the task id (the resolution key).
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
		"channel": req.Target,
		"text":    fmt.Sprintf("Approval required for %s", req.Tool), // fallback / a11y
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
func parseApprovalInteraction(payload []byte) (dec channels.ApprovalDecision, channelID, msgTS string, ok bool) {
	var in slackInteraction
	if err := json.Unmarshal(payload, &in); err != nil {
		return dec, "", "", false
	}
	if in.Type != "block_actions" || len(in.Actions) == 0 {
		return dec, "", "", false
	}
	a := in.Actions[0]
	var decision string
	switch a.ActionID {
	case approveActionID:
		decision = "approve"
	case rejectActionID:
		decision = "reject"
	default:
		return dec, "", "", false // not our button
	}
	if a.Value == "" {
		return dec, "", "", false
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
	}, in.Channel.ID, in.Message.TS, true
}

// handleInteractive resolves an approval button click. No-op for interactions
// that aren't ours. Best-effort updates the source message with the outcome.
func (p *Plugin) handleInteractive(ctx context.Context, payload []byte) error {
	dec, channelID, msgTS, ok := parseApprovalInteraction(payload)
	if !ok {
		return nil // not a forge approval interaction; ignore quietly
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
