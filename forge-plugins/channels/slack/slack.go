// Package slack implements the Slack channel plugin for the forge channel system.
package slack

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/channels"
	"github.com/initializ/forge/forge-plugins/channels/markdown"
)

const (
	defaultWebhookPort = 3000
	defaultWebhookPath = "/slack/events"
	replayWindowSec    = 300 // 5 minutes
)

const slackAPIBase = "https://slack.com/api"

// Plugin implements channels.ChannelPlugin for Slack.
type Plugin struct {
	signingSecret string
	botToken      string
	webhookPort   int
	webhookPath   string
	srv           *http.Server
	client        *http.Client
	apiBase       string // overridable for tests
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

	p.signingSecret = settings["signing_secret"]
	if p.signingSecret == "" {
		return fmt.Errorf("slack: signing_secret is required (set SLACK_SIGNING_SECRET)")
	}
	p.botToken = settings["bot_token"]
	if p.botToken == "" {
		return fmt.Errorf("slack: bot_token is required (set SLACK_BOT_TOKEN)")
	}

	p.webhookPort = cfg.WebhookPort
	if p.webhookPort == 0 {
		p.webhookPort = defaultWebhookPort
	}
	p.webhookPath = cfg.WebhookPath
	if p.webhookPath == "" {
		p.webhookPath = defaultWebhookPath
	}

	return nil
}

func (p *Plugin) Start(ctx context.Context, handler channels.EventHandler) error {
	mux := http.NewServeMux()
	mux.HandleFunc(p.webhookPath, p.makeWebhookHandler(handler))

	p.srv = &http.Server{
		Addr:    fmt.Sprintf(":%d", p.webhookPort),
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		p.Stop() //nolint:errcheck
	}()

	fmt.Printf("  Slack adapter listening on :%d%s\n", p.webhookPort, p.webhookPath)
	if err := p.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (p *Plugin) Stop() error {
	if p.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return p.srv.Shutdown(ctx)
	}
	return nil
}

func (p *Plugin) makeWebhookHandler(handler channels.EventHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read body", http.StatusBadRequest)
			return
		}

		// Verify Slack signature
		timestamp := r.Header.Get("X-Slack-Request-Timestamp")
		signature := r.Header.Get("X-Slack-Signature")

		if !verifySlackSignature(p.signingSecret, timestamp, body, signature) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}

		// Replay protection: check timestamp within window
		ts, err := strconv.ParseInt(timestamp, 10, 64)
		if err != nil || math.Abs(float64(time.Now().Unix()-ts)) > replayWindowSec {
			http.Error(w, "request too old", http.StatusUnauthorized)
			return
		}

		// Parse outer envelope to check type
		var envelope struct {
			Type      string `json:"type"`
			Challenge string `json:"challenge"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}

		// Handle url_verification challenge
		if envelope.Type == "url_verification" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"challenge": envelope.Challenge}) //nolint:errcheck
			return
		}

		// Parse event callback
		var payload slackEventPayload
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, "invalid event payload", http.StatusBadRequest)
			return
		}

		// Skip bot messages
		if payload.Event.BotID != "" {
			w.WriteHeader(http.StatusOK)
			return
		}

		// Normalize and dispatch
		event, err := p.NormalizeEvent(body)
		if err != nil {
			http.Error(w, "normalisation failed", http.StatusBadRequest)
			return
		}

		// Acknowledge immediately (Slack requires 200 within 3s)
		w.WriteHeader(http.StatusOK)

		// Process async
		go func() {
			ctx := context.Background()
			resp, err := handler(ctx, event)
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

// NormalizeEvent parses raw Slack event JSON into a ChannelEvent.
func (p *Plugin) NormalizeEvent(raw []byte) (*channels.ChannelEvent, error) {
	var payload slackEventPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("parsing slack event: %w", err)
	}

	threadID := payload.Event.ThreadTS
	if threadID == "" {
		threadID = payload.Event.TS
	}

	return &channels.ChannelEvent{
		Channel:     "slack",
		WorkspaceID: payload.Event.Channel,
		UserID:      payload.Event.User,
		ThreadID:    threadID,
		Message:     payload.Event.Text,
		Raw:         raw,
	}, nil
}

// SendResponse posts a message back to Slack via chat.postMessage.
func (p *Plugin) SendResponse(event *channels.ChannelEvent, response *a2a.Message) error {
	text := extractText(response)
	mrkdwn := markdown.ToSlackMrkdwn(text)
	chunks := markdown.SplitMessage(mrkdwn, 4000)

	for i, chunk := range chunks {
		payload := map[string]any{
			"channel": event.WorkspaceID,
			"text":    chunk,
			"mrkdwn":  true,
		}
		if i == 0 {
			payload["thread_ts"] = event.ThreadID
		}
		if err := p.postMessage(payload); err != nil {
			return err
		}
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

// verifySlackSignature validates the X-Slack-Signature header using HMAC-SHA256.
func verifySlackSignature(signingSecret, timestamp string, body []byte, signature string) bool {
	if signingSecret == "" || timestamp == "" || signature == "" {
		return false
	}

	baseString := fmt.Sprintf("v0:%s:%s", timestamp, body)
	mac := hmac.New(sha256.New, []byte(signingSecret))
	mac.Write([]byte(baseString))
	expected := "v0=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
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
			text += p.Text
		}
	}
	if text == "" {
		text = "(no text response)"
	}
	return text
}

// slackEventPayload represents the outer Slack event callback structure.
type slackEventPayload struct {
	TeamID string     `json:"team_id"`
	Event  slackEvent `json:"event"`
}

// slackEvent represents the inner event fields we care about.
type slackEvent struct {
	Type     string `json:"type"`
	Channel  string `json:"channel"`
	User     string `json:"user"`
	Text     string `json:"text"`
	TS       string `json:"ts"`
	ThreadTS string `json:"thread_ts"`
	BotID    string `json:"bot_id"`
}
