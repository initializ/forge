package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/channels"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

// SendResponse posts the agent's reply, then renders any follow-up action
// buttons the response carries (#562). The reply and the buttons are separate
// messages (see sendActions) so the reply rendering is unchanged and unbounded
// by the Block Kit section limit. Action rendering is best-effort and never
// fails the reply.
func (p *Plugin) SendResponse(event *channels.ChannelEvent, response *a2a.Message) error {
	if err := p.sendReply(event, response); err != nil {
		return err
	}
	if response != nil {
		p.sendActions(event, coreruntime.ActionsFromMetadata(response.Metadata))
	}
	return nil
}

// Interactive follow-up actions (#562). The runtime attaches channel-neutral
// actions to a response's metadata; here they render as Block Kit buttons, and
// a click re-enters the agent with the action's prompt as a synthesized
// follow-up message.
const (
	// actionBlockIDPrefix namespaces our actions block and carries the thread
	// the re-entry reply should post into: "forge_action:<threadTS>".
	actionBlockIDPrefix = "forge_action:"
	// actionIDPrefix namespaces each button's action_id ("forge_action_<i>"),
	// distinguishing our buttons from approval / consent buttons.
	actionIDPrefix = "forge_action_"
)

// buildActionsBlocks renders follow-up actions as a single Block Kit actions
// block. threadTS is embedded in the block_id so the click handler knows which
// thread to continue. Button value = the action's prompt (the synthesized
// follow-up). Slack caps button text at 75 chars and value at 2000 — the
// runtime's ActionsCollector already enforces both, so no truncation here.
func buildActionsBlocks(actions []coreruntime.Action, threadTS string) []map[string]any {
	elements := make([]map[string]any, 0, len(actions))
	for i, a := range actions {
		elements = append(elements, map[string]any{
			"type":      "button",
			"action_id": fmt.Sprintf("%s%d", actionIDPrefix, i),
			"text":      map[string]any{"type": "plain_text", "text": a.Label, "emoji": true},
			"value":     a.Prompt,
		})
	}
	return []map[string]any{
		{
			"type":     "actions",
			"block_id": actionBlockIDPrefix + threadTS,
			"elements": elements,
		},
	}
}

// actionsFallbackText is the top-level `text` for an actions message — the
// accessibility / notification fallback a screen reader (or a client that
// doesn't render blocks) reads instead of the buttons.
func actionsFallbackText(actions []coreruntime.Action) string {
	labels := make([]string, len(actions))
	for i, a := range actions {
		labels[i] = a.Label
	}
	return "Suggested next steps: " + strings.Join(labels, " · ")
}

// sendActions posts a compact interactive message carrying the follow-up
// buttons, threaded under the same message as the reply. Kept as a SEPARATE
// message from the reply text so the (potentially long) reply isn't constrained
// by the Block Kit section 3000-char limit, and so the reply rendering is
// untouched. Best-effort: a failure here never fails the main reply.
func (p *Plugin) sendActions(event *channels.ChannelEvent, actions []coreruntime.Action) {
	if len(actions) == 0 {
		return
	}
	threadTS := event.ThreadID
	if threadTS == "" {
		threadTS = event.MessageID
	}
	payload := map[string]any{
		"channel": event.WorkspaceID,
		"text":    actionsFallbackText(actions),
		"blocks":  buildActionsBlocks(actions, threadTS),
	}
	if threadTS != "" {
		payload["thread_ts"] = threadTS
	}
	if err := p.postMessage(payload); err != nil {
		p.logWarn("failed to post interactive actions", map[string]any{"error": err.Error()})
	}
}

// actionClick is a parsed forge_action button press.
type actionClick struct {
	prompt    string // the synthesized follow-up message
	label     string // button label (for the post-click message update)
	threadTS  string // thread to continue (from the block_id)
	channelID string
	msgTS     string // the actions message, to update after the click
	userID    string // the clicker (attribution)
}

// parseActionClick extracts a forge_action press from a block_actions payload.
// Pure + testable. ok=false for any payload that isn't one of our action
// buttons, so the caller ignores it.
func parseActionClick(payload []byte) (actionClick, bool) {
	var in slackInteraction
	if err := json.Unmarshal(payload, &in); err != nil {
		return actionClick{}, false
	}
	if in.Type != "block_actions" || len(in.Actions) == 0 {
		return actionClick{}, false
	}
	a := in.Actions[0]
	if !strings.HasPrefix(a.ActionID, actionIDPrefix) || !strings.HasPrefix(a.BlockID, actionBlockIDPrefix) {
		return actionClick{}, false
	}
	if a.Value == "" {
		return actionClick{}, false
	}
	return actionClick{
		prompt:    a.Value,
		label:     a.Text.Text,
		threadTS:  strings.TrimPrefix(a.BlockID, actionBlockIDPrefix),
		channelID: in.Channel.ID,
		msgTS:     in.Message.TS,
		userID:    in.User.ID,
	}, true
}

// handleActionClick re-enters the agent with the chosen action's prompt as a
// synthesized follow-up message (#562), attributed to the clicking user and
// threaded under the original conversation. It returns quickly: the envelope is
// already acked (readLoop), and the agent turn runs in the background so a slow
// turn doesn't block the socket — the reply is posted when it completes, like a
// normal inbound message. The buttons are disabled immediately so the action
// can't be double-fired.
func (p *Plugin) handleActionClick(ctx context.Context, click actionClick) {
	if p.eventHandler == nil {
		p.logWarn("action click with no event handler wired; ignoring", map[string]any{"thread": click.threadTS})
		return
	}

	// Disable the buttons by replacing the actions message with the chosen
	// outcome, so a second click can't re-fire the turn. Best-effort.
	chosen := click.label
	if chosen == "" {
		chosen = "your selection"
	}
	if click.channelID != "" && click.msgTS != "" {
		p.updateApprovalMessage(click.channelID, click.msgTS, fmt.Sprintf(":white_check_mark: You chose *%s*.", chosen))
	}

	event := &channels.ChannelEvent{
		Channel:     "slack",
		WorkspaceID: click.channelID,
		UserID:      click.userID,
		ThreadID:    click.threadTS,
		MessageID:   click.threadTS,
		Message:     click.prompt,
	}
	// Attribute to the clicker so delegated (auth.type=user) MCP tools key on
	// the human who clicked, not the original asker (mirrors the inbound path).
	if email, err := p.resolveUserEmail(ctx, click.userID); err == nil {
		event.UserEmail = email
	}

	go func() {
		resp, err := p.eventHandler(ctx, event)
		if err != nil {
			p.logWarn("action re-entry failed", map[string]any{"error": err.Error(), "thread": click.threadTS})
			return
		}
		if resp == nil {
			return
		}
		if err := p.SendResponse(event, resp); err != nil {
			p.logWarn("failed to post action re-entry reply", map[string]any{"error": err.Error()})
		}
	}()
}
