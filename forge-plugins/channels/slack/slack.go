// Package slack implements the Slack channel plugin for the forge channel system.
package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/channels"
	"github.com/initializ/forge/forge-plugins/channels/markdown"
)

const slackAPIBase = "https://slack.com/api"

// longRunningThreshold is how long to wait before sending an interim
// "Researching..." message for slow handler responses.
const longRunningThreshold = 15 * time.Second

// Plugin implements channels.ChannelPlugin for Slack using Socket Mode.
type Plugin struct {
	appToken  string
	botToken  string
	botUserID string // resolved at startup via auth.test
	wsConn    *websocket.Conn
	connMu    sync.Mutex
	stopCh    chan struct{}
	client    *http.Client
	apiBase   string // overridable for tests
}

// New creates an uninitialised Slack plugin.
func New() *Plugin {
	return &Plugin{
		client:  &http.Client{Timeout: 30 * time.Second},
		apiBase: slackAPIBase,
	}
}

func (p *Plugin) Name() string { return "slack" }

func (p *Plugin) Init(cfg channels.ChannelConfig) error {
	settings := channels.ResolveEnvVars(&cfg)

	p.appToken = settings["app_token"]
	if p.appToken == "" {
		return fmt.Errorf("slack: app_token is required (set SLACK_APP_TOKEN)")
	}
	p.botToken = settings["bot_token"]
	if p.botToken == "" {
		return fmt.Errorf("slack: bot_token is required (set SLACK_BOT_TOKEN)")
	}

	return nil
}

// resolveBotID calls auth.test to discover the bot's own Slack user ID.
func (p *Plugin) resolveBotID() error {
	req, err := http.NewRequest(http.MethodPost, p.apiBase+"/auth.test", nil)
	if err != nil {
		return fmt.Errorf("creating auth.test request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+p.botToken)

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("calling auth.test: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading auth.test response: %w", err)
	}

	var result struct {
		OK     bool   `json:"ok"`
		UserID string `json:"user_id"`
		Error  string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("parsing auth.test response: %w", err)
	}
	if !result.OK {
		return fmt.Errorf("auth.test error: %s", result.Error)
	}

	p.botUserID = result.UserID
	return nil
}

// extractMentions scans text for Slack user mentions (<@UXXXX> or <@UXXXX|name>)
// and returns a slice of the mentioned user IDs.
func extractMentions(text string) []string {
	var mentions []string
	for {
		start := strings.Index(text, "<@")
		if start == -1 {
			break
		}
		end := strings.Index(text[start:], ">")
		if end == -1 {
			break
		}
		inner := text[start+2 : start+end] // e.g. "U0123ABC" or "U0123ABC|bob"
		if pipeIdx := strings.Index(inner, "|"); pipeIdx != -1 {
			inner = inner[:pipeIdx]
		}
		if inner != "" {
			mentions = append(mentions, inner)
		}
		text = text[start+end+1:]
	}
	return mentions
}

// stripBotMention removes all occurrences of <@botUserID> (with optional
// display name) from the message text, collapsing extra whitespace.
func stripBotMention(text, botUserID string) string {
	for {
		start := strings.Index(text, "<@"+botUserID)
		if start == -1 {
			break
		}
		end := strings.Index(text[start:], ">")
		if end == -1 {
			break
		}
		text = text[:start] + text[start+end+1:]
	}
	// Collapse runs of multiple spaces into a single space.
	for strings.Contains(text, "  ") {
		text = strings.ReplaceAll(text, "  ", " ")
	}
	return strings.TrimSpace(text)
}

// openConnection calls apps.connections.open to obtain a WebSocket URL.
func (p *Plugin) openConnection() (string, error) {
	url := p.apiBase + "/apps.connections.open"
	req, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		return "", fmt.Errorf("creating connections.open request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+p.appToken)

	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("calling apps.connections.open: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading connections.open response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("apps.connections.open HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		OK    bool   `json:"ok"`
		URL   string `json:"url"`
		Error string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("parsing connections.open response: %w", err)
	}
	if !result.OK {
		return "", fmt.Errorf("apps.connections.open error: %s", result.Error)
	}
	return result.URL, nil
}

func (p *Plugin) Start(ctx context.Context, handler channels.EventHandler) error {
	p.stopCh = make(chan struct{})

	// Resolve the bot's own user ID so we can filter mentions.
	if err := p.resolveBotID(); err != nil {
		fmt.Printf("  slack: warning: could not resolve bot user ID: %v (will respond to all messages)\n", err)
	} else {
		fmt.Printf("  Slack bot user ID: %s\n", p.botUserID)
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-p.stopCh:
			return nil
		default:
		}

		wsURL, err := p.openConnection()
		if err != nil {
			fmt.Printf("  slack: failed to open connection: %v (retrying in 2s)\n", err)
			select {
			case <-time.After(2 * time.Second):
				continue
			case <-ctx.Done():
				return nil
			case <-p.stopCh:
				return nil
			}
		}

		fmt.Println("  Slack adapter connected via Socket Mode")

		conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
		if err != nil {
			fmt.Printf("  slack: websocket dial failed: %v (retrying in 2s)\n", err)
			select {
			case <-time.After(2 * time.Second):
				continue
			case <-ctx.Done():
				return nil
			case <-p.stopCh:
				return nil
			}
		}

		p.connMu.Lock()
		p.wsConn = conn
		p.connMu.Unlock()

		// Read loop — exits on error or close, then reconnects.
		if err := p.readLoop(ctx, conn, handler); err != nil {
			fmt.Printf("  slack: read loop error: %v (reconnecting in 2s)\n", err)
		}

		p.connMu.Lock()
		p.wsConn = nil
		p.connMu.Unlock()

		select {
		case <-time.After(2 * time.Second):
		case <-ctx.Done():
			return nil
		case <-p.stopCh:
			return nil
		}
	}
}

func (p *Plugin) readLoop(ctx context.Context, conn *websocket.Conn, handler channels.EventHandler) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-p.stopCh:
			return nil
		default:
		}

		_, message, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("reading websocket message: %w", err)
		}

		var envelope socketEnvelope
		if err := json.Unmarshal(message, &envelope); err != nil {
			fmt.Printf("  slack: invalid envelope JSON: %v\n", err)
			continue
		}

		// Acknowledge the envelope immediately.
		if envelope.EnvelopeID != "" {
			ack := map[string]string{"envelope_id": envelope.EnvelopeID}
			if err := conn.WriteJSON(ack); err != nil {
				return fmt.Errorf("sending ack: %w", err)
			}
		}

		// Handle disconnect requests from Slack.
		if envelope.Type == "disconnect" {
			fmt.Println("  slack: received disconnect, will reconnect")
			return nil
		}

		if envelope.Type != "events_api" {
			continue
		}

		// Parse the inner event payload.
		var payload slackEventPayload
		if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
			fmt.Printf("  slack: invalid event payload: %v\n", err)
			continue
		}

		// Skip bot messages.
		if payload.Event.BotID != "" {
			continue
		}

		// Skip message subtypes (message_deleted, message_changed,
		// channel_join, etc.) — only process plain user messages.
		if payload.Event.SubType != "" {
			continue
		}

		// Skip app_mention events — message events already include
		// @mentions, so processing both would cause duplicates.
		if payload.Event.Type == "app_mention" {
			continue
		}

		// Mention-aware filtering (only when bot user ID is known).
		if p.botUserID != "" {
			if payload.Event.ThreadTS == "" {
				// Channel-level message: only respond when the bot is @mentioned.
				if !strings.Contains(payload.Event.Text, "<@"+p.botUserID) {
					continue
				}
			} else {
				// Threaded message: respond unless another user (not the bot)
				// is explicitly @mentioned, meaning the question is directed
				// at someone else.
				mentions := extractMentions(payload.Event.Text)
				if len(mentions) > 0 && !slices.Contains(mentions, p.botUserID) {
					continue
				}
			}
		}

		event, err := p.NormalizeEvent(envelope.Payload)
		if err != nil {
			fmt.Printf("  slack: normalisation failed: %v\n", err)
			continue
		}

		// Strip bot mention from the message text so the LLM sees clean text.
		if p.botUserID != "" {
			event.Message = stripBotMention(event.Message, p.botUserID)
		}

		go func() {
			// Add :eyes: reaction to indicate we received the message.
			_ = p.addReaction(event.WorkspaceID, event.MessageID, "eyes")

			// If the handler takes longer than 15s, send an interim message.
			done := make(chan struct{})
			go func() {
				select {
				case <-time.After(longRunningThreshold):
					threadTS := event.ThreadID
					if threadTS == "" {
						threadTS = event.MessageID
					}
					payload := map[string]any{
						"channel": event.WorkspaceID,
						"text":    "Researching, I'll post the result shortly...",
						"mrkdwn":  true,
					}
					if threadTS != "" {
						payload["thread_ts"] = threadTS
					}
					_ = p.postMessage(payload)
				case <-done:
				}
			}()

			resp, err := handler(ctx, event)
			close(done)

			// Remove the :eyes: reaction.
			_ = p.removeReaction(event.WorkspaceID, event.MessageID, "eyes")

			if err != nil {
				fmt.Printf("slack: handler error: %v\n", err)
				return
			}
			if err := p.SendResponse(event, resp); err != nil {
				fmt.Printf("slack: send response error: %v\n", err)
			}
		}()
	}
}

func (p *Plugin) Stop() error {
	if p.stopCh != nil {
		select {
		case <-p.stopCh:
			// already closed
		default:
			close(p.stopCh)
		}
	}
	p.connMu.Lock()
	defer p.connMu.Unlock()
	if p.wsConn != nil {
		return p.wsConn.Close()
	}
	return nil
}

// NormalizeEvent parses raw Slack event JSON into a ChannelEvent.
func (p *Plugin) NormalizeEvent(raw []byte) (*channels.ChannelEvent, error) {
	var payload slackEventPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("parsing slack event: %w", err)
	}

	threadID := payload.Event.ThreadTS
	messageID := payload.Event.TS
	if threadID == "" {
		threadID = messageID // Use message TS so thread replies find the same session
	}

	return &channels.ChannelEvent{
		Channel:     "slack",
		WorkspaceID: payload.Event.Channel,
		UserID:      payload.Event.User,
		ThreadID:    threadID,
		MessageID:   messageID,
		Message:     payload.Event.Text,
		Raw:         raw,
	}, nil
}

// SendResponse posts a message back to Slack via chat.postMessage.
// If the runtime attached file parts (large tool outputs), those are used
// for the file upload since the LLM text may be truncated.
// For large responses (>4096 chars), uploads the full report as a file
// with a summary message. Falls back to chunked messages on failure.
func (p *Plugin) SendResponse(event *channels.ChannelEvent, response *a2a.Message) error {
	text := extractText(response)
	fileContent, fileName := extractLargestFile(response)

	// If we have a file part from the runtime, upload it and send the text
	// as a summary. The file part contains the complete, untruncated tool
	// output; the text is the LLM's (potentially truncated) summary.
	if fileContent != "" {
		if fileName == "" {
			fileName = "research-report.md"
		}

		threadTS := event.ThreadID
		if threadTS == "" {
			threadTS = event.MessageID
		}

		if err := p.uploadFile(event, fileName, fileContent); err != nil {
			fmt.Printf("slack: file upload failed: %v (falling back to chunked messages)\n", err)
			// Fall back: send the file content as chunked messages.
			return p.sendChunked(event, fileContent)
		}

		// File uploaded — send the LLM text as summary with "attached" note.
		summary := text
		if len(summary) > 600 {
			summary, _ = markdown.SplitSummaryAndReport(summary)
		}
		summaryText := summary + "\n\n_Full report attached as file above._"
		summaryMrkdwn := markdown.ToSlackMrkdwn(summaryText)
		payload := map[string]any{
			"channel": event.WorkspaceID,
			"text":    summaryMrkdwn,
			"mrkdwn":  true,
		}
		if threadTS != "" {
			payload["thread_ts"] = threadTS
		}
		return p.postMessage(payload)
	}

	// No file parts — use text-based logic.
	if len(text) > 4096 {
		summary, report := markdown.SplitSummaryAndReport(text)

		threadTS := event.ThreadID
		if threadTS == "" {
			threadTS = event.MessageID
		}

		if err := p.uploadFile(event, "research-report.md", report); err != nil {
			fmt.Printf("slack: file upload failed: %v (falling back to chunked messages)\n", err)
			return p.sendChunked(event, text)
		}

		summaryText := summary + "\n\n_Full report attached as file above._"
		summaryMrkdwn := markdown.ToSlackMrkdwn(summaryText)
		payload := map[string]any{
			"channel": event.WorkspaceID,
			"text":    summaryMrkdwn,
			"mrkdwn":  true,
		}
		if threadTS != "" {
			payload["thread_ts"] = threadTS
		}
		return p.postMessage(payload)
	}

	mrkdwn := markdown.ToSlackMrkdwn(text)
	chunks := markdown.SplitMessage(mrkdwn, 4000)

	for i, chunk := range chunks {
		payload := map[string]any{
			"channel": event.WorkspaceID,
			"text":    chunk,
			"mrkdwn":  true,
		}
		if i == 0 {
			if event.ThreadID != "" {
				payload["thread_ts"] = event.ThreadID
			} else if event.MessageID != "" {
				payload["thread_ts"] = event.MessageID
			}
		}
		if err := p.postMessage(payload); err != nil {
			return err
		}
	}
	return nil
}

// sendChunked sends text as chunked messages (fallback for failed file upload).
func (p *Plugin) sendChunked(event *channels.ChannelEvent, text string) error {
	mrkdwn := markdown.ToSlackMrkdwn(text)
	chunks := markdown.SplitMessage(mrkdwn, 4000)
	for i, chunk := range chunks {
		payload := map[string]any{
			"channel": event.WorkspaceID,
			"text":    chunk,
			"mrkdwn":  true,
		}
		if i == 0 {
			if event.ThreadID != "" {
				payload["thread_ts"] = event.ThreadID
			} else if event.MessageID != "" {
				payload["thread_ts"] = event.MessageID
			}
		}
		if err := p.postMessage(payload); err != nil {
			return err
		}
	}
	return nil
}

// uploadFile uploads content as a file to a Slack channel using the
// files.getUploadURLExternal + files.completeUploadExternal flow.
func (p *Plugin) uploadFile(event *channels.ChannelEvent, filename, content string) error {
	// Step 1: Get an upload URL from Slack.
	form := url.Values{}
	form.Set("filename", filename)
	form.Set("length", strconv.Itoa(len(content)))

	getURLReq, err := http.NewRequest(http.MethodPost, p.apiBase+"/files.getUploadURLExternal", strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("creating getUploadURLExternal request: %w", err)
	}
	getURLReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	getURLReq.Header.Set("Authorization", "Bearer "+p.botToken)

	getURLResp, err := p.client.Do(getURLReq)
	if err != nil {
		return fmt.Errorf("calling files.getUploadURLExternal: %w", err)
	}
	defer func() { _ = getURLResp.Body.Close() }()

	getURLRespBody, _ := io.ReadAll(getURLResp.Body)
	var uploadURLResult struct {
		OK        bool   `json:"ok"`
		UploadURL string `json:"upload_url"`
		FileID    string `json:"file_id"`
		Error     string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(getURLRespBody, &uploadURLResult); err != nil {
		return fmt.Errorf("parsing getUploadURLExternal response: %w", err)
	}
	if !uploadURLResult.OK {
		return fmt.Errorf("files.getUploadURLExternal error: %s", uploadURLResult.Error)
	}

	// Step 2: Upload the file content to the provided URL.
	uploadReq, err := http.NewRequest(http.MethodPost, uploadURLResult.UploadURL, bytes.NewReader([]byte(content)))
	if err != nil {
		return fmt.Errorf("creating upload request: %w", err)
	}
	uploadReq.Header.Set("Content-Type", "application/octet-stream")

	uploadResp, err := p.client.Do(uploadReq)
	if err != nil {
		return fmt.Errorf("uploading file content: %w", err)
	}
	defer func() { _ = uploadResp.Body.Close() }()
	_, _ = io.ReadAll(uploadResp.Body)

	if uploadResp.StatusCode != http.StatusOK {
		return fmt.Errorf("file upload HTTP %d", uploadResp.StatusCode)
	}

	// Step 3: Complete the upload and share to the channel/thread.
	threadTS := event.ThreadID
	if threadTS == "" {
		threadTS = event.MessageID
	}
	completePayload := map[string]any{
		"files": []map[string]string{
			{"id": uploadURLResult.FileID, "title": filename},
		},
		"channel_id": event.WorkspaceID,
	}
	if threadTS != "" {
		completePayload["thread_ts"] = threadTS
	}

	completeBody, err := json.Marshal(completePayload)
	if err != nil {
		return fmt.Errorf("marshalling completeUploadExternal: %w", err)
	}

	completeReq, err := http.NewRequest(http.MethodPost, p.apiBase+"/files.completeUploadExternal", bytes.NewReader(completeBody))
	if err != nil {
		return fmt.Errorf("creating completeUploadExternal request: %w", err)
	}
	completeReq.Header.Set("Content-Type", "application/json")
	completeReq.Header.Set("Authorization", "Bearer "+p.botToken)

	completeResp, err := p.client.Do(completeReq)
	if err != nil {
		return fmt.Errorf("calling files.completeUploadExternal: %w", err)
	}
	defer func() { _ = completeResp.Body.Close() }()

	completeRespBody, _ := io.ReadAll(completeResp.Body)
	var completeResult struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(completeRespBody, &completeResult); err != nil {
		return fmt.Errorf("parsing completeUploadExternal response: %w", err)
	}
	if !completeResult.OK {
		return fmt.Errorf("files.completeUploadExternal error: %s", completeResult.Error)
	}

	return nil
}

// postMessage posts a JSON payload to the Slack chat.postMessage API.
func (p *Plugin) postMessage(payload map[string]any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshalling slack response: %w", err)
	}

	url := p.apiBase + "/chat.postMessage"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating slack request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.botToken)

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("posting to slack: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("slack API error %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// addReaction adds an emoji reaction to a message.
func (p *Plugin) addReaction(channel, timestamp, emoji string) error {
	return p.reactAPI("reactions.add", channel, timestamp, emoji)
}

// removeReaction removes an emoji reaction from a message.
func (p *Plugin) removeReaction(channel, timestamp, emoji string) error {
	return p.reactAPI("reactions.remove", channel, timestamp, emoji)
}

// reactAPI calls a Slack reactions.* endpoint.
func (p *Plugin) reactAPI(method, channel, timestamp, emoji string) error {
	payload := map[string]string{
		"channel":   channel,
		"timestamp": timestamp,
		"name":      emoji,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshalling reaction: %w", err)
	}

	url := p.apiBase + "/" + method
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating reaction request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.botToken)

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("calling %s: %w", method, err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.ReadAll(resp.Body)
	return nil
}

// unwrapJSONContent checks if text is a JSON object from a tool output and
// converts it to readable markdown. Supports two formats:
//
//  1. Tavily Research: {"content":"…", "sources":[{"url":"…","title":"…"}]}
//  2. Tavily Search:   {"answer":"…", "results":[{"title":"…","url":"…","content":"…"}]}
//
// If the text is not JSON or matches neither format, it is returned unchanged.
func unwrapJSONContent(text string) string {
	trimmed := strings.TrimSpace(text)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return text
	}

	// Try Tavily Research format: top-level "content" + "sources".
	var research struct {
		Content string `json:"content"`
		Sources []struct {
			URL   string `json:"url"`
			Title string `json:"title"`
		} `json:"sources"`
	}
	if err := json.Unmarshal([]byte(trimmed), &research); err == nil && research.Content != "" {
		result := research.Content
		var links []string
		for _, s := range research.Sources {
			if s.URL == "" {
				continue
			}
			if s.Title != "" {
				links = append(links, fmt.Sprintf("- [%s](%s)", s.Title, s.URL))
			} else {
				links = append(links, fmt.Sprintf("- %s", s.URL))
			}
		}
		if len(links) > 0 {
			result += "\n\n**Sources:**\n" + strings.Join(links, "\n")
		}
		return result
	}

	// Try Tavily Search format: "answer" + "results".
	var search struct {
		Answer  string `json:"answer"`
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(trimmed), &search); err == nil && (search.Answer != "" || len(search.Results) > 0) {
		var sb strings.Builder
		if search.Answer != "" {
			sb.WriteString(search.Answer)
		}
		if len(search.Results) > 0 {
			if sb.Len() > 0 {
				sb.WriteString("\n\n")
			}
			sb.WriteString("**Sources:**\n")
			for _, r := range search.Results {
				if r.URL == "" {
					continue
				}
				if r.Title != "" {
					fmt.Fprintf(&sb, "- [%s](%s)", r.Title, r.URL)
				} else {
					fmt.Fprintf(&sb, "- %s", r.URL)
				}
				if r.Content != "" {
					// Include a short excerpt (first 200 chars).
					excerpt := r.Content
					if len(excerpt) > 200 {
						excerpt = excerpt[:200] + "…"
					}
					sb.WriteString(": " + excerpt)
				}
				sb.WriteString("\n")
			}
		}
		if sb.Len() > 0 {
			return strings.TrimRight(sb.String(), "\n")
		}
	}

	return text
}

// extractText concatenates all text parts from an A2A message.
func extractText(msg *a2a.Message) string {
	if msg == nil {
		return "(no response)"
	}
	var text string
	for _, p := range msg.Parts {
		if p.Kind == a2a.PartKindText {
			if text != "" {
				text += "\n"
			}
			text += unwrapJSONContent(p.Text)
		}
	}
	if text == "" {
		text = "(no text response)"
	}
	return text
}

// extractLargestFile returns the content and filename of the largest file part
// in the message, or empty strings if no file parts exist.
// The runtime attaches large tool outputs as file parts so they aren't
// truncated by LLM output token limits. JSON tool outputs are unwrapped
// into readable markdown.
func extractLargestFile(msg *a2a.Message) (content, filename string) {
	if msg == nil {
		return "", ""
	}
	for _, p := range msg.Parts {
		if p.Kind == a2a.PartKindFile && p.File != nil && len(p.File.Bytes) > len(content) {
			raw := string(p.File.Bytes)
			// Only unwrap JSON content for markdown files.
			// Preserve raw content for explicitly typed files (json, yaml, etc.)
			if strings.HasSuffix(p.File.Name, ".md") {
				raw = unwrapJSONContent(raw)
			}
			content = raw
			filename = p.File.Name
		}
	}
	return content, filename
}

// socketEnvelope is the outer envelope received over the Socket Mode WebSocket.
type socketEnvelope struct {
	EnvelopeID string          `json:"envelope_id"`
	Type       string          `json:"type"`
	Payload    json.RawMessage `json:"payload"`
}

// slackEventPayload represents the outer Slack event callback structure.
type slackEventPayload struct {
	TeamID string     `json:"team_id"`
	Event  slackEvent `json:"event"`
}

// slackEvent represents the inner event fields we care about.
type slackEvent struct {
	Type     string `json:"type"`
	SubType  string `json:"subtype"`
	Channel  string `json:"channel"`
	User     string `json:"user"`
	Text     string `json:"text"`
	TS       string `json:"ts"`
	ThreadTS string `json:"thread_ts"`
	BotID    string `json:"bot_id"`
}
