// Package telegram implements the Telegram channel plugin for the forge channel system.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/channels"
	"github.com/initializ/forge/forge-plugins/channels/markdown"
)

const (
	defaultWebhookPort = 3001
	defaultWebhookPath = "/telegram/webhook"
	telegramAPIBase    = "https://api.telegram.org"
	pollingTimeout     = 30 // seconds for long polling
)

// Plugin implements channels.ChannelPlugin for Telegram.
type Plugin struct {
	botToken    string
	mode        string // "polling" or "webhook"
	webhookPort int
	webhookPath string
	srv         *http.Server
	client      *http.Client
	apiBase     string // overridable for tests
	stopCh      chan struct{}
}

// New creates an uninitialised Telegram plugin.
func New() *Plugin {
	return &Plugin{
		client:  &http.Client{Timeout: 60 * time.Second},
		apiBase: telegramAPIBase,
		stopCh:  make(chan struct{}),
	}
}

func (p *Plugin) Name() string { return "telegram" }

func (p *Plugin) Init(cfg channels.ChannelConfig) error {
	settings := channels.ResolveEnvVars(&cfg)

	p.botToken = settings["bot_token"]
	if p.botToken == "" {
		return fmt.Errorf("telegram: bot_token is required (set TELEGRAM_BOT_TOKEN)")
	}

	p.mode = settings["mode"]
	if p.mode == "" {
		p.mode = "polling"
	}
	if p.mode != "polling" && p.mode != "webhook" {
		return fmt.Errorf("telegram: mode must be 'polling' or 'webhook', got %q", p.mode)
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
	if p.mode == "webhook" {
		return p.startWebhook(ctx, handler)
	}
	return p.startPolling(ctx, handler)
}

func (p *Plugin) Stop() error {
	// Signal polling goroutine to stop
	select {
	case <-p.stopCh:
	default:
		close(p.stopCh)
	}

	if p.srv != nil {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return p.srv.Shutdown(shutCtx)
	}
	return nil
}

func (p *Plugin) startWebhook(ctx context.Context, handler channels.EventHandler) error {
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

	fmt.Printf("  Telegram adapter (webhook) listening on :%d%s\n", p.webhookPort, p.webhookPath)
	if err := p.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
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

		event, err := p.NormalizeEvent(body)
		if err != nil {
			http.Error(w, "invalid update", http.StatusBadRequest)
			return
		}

		w.WriteHeader(http.StatusOK)

		go func() {
			ctx := context.Background()
			stopTyping := p.startTypingIndicator(ctx, event.WorkspaceID)
			resp, err := handler(ctx, event)
			stopTyping()
			if err != nil {
				fmt.Printf("telegram: handler error: %v\n", err)
				return
			}
			if err := p.SendResponse(event, resp); err != nil {
				fmt.Printf("telegram: send response error: %v\n", err)
			}
		}()
	}
}

func (p *Plugin) startPolling(ctx context.Context, handler channels.EventHandler) error {
	fmt.Printf("  Telegram adapter (polling) started\n")

	var offset int64

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-p.stopCh:
			return nil
		default:
		}

		updates, err := p.getUpdates(ctx, offset)
		if err != nil {
			// Don't flood on errors, sleep briefly
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(2 * time.Second):
			}
			continue
		}

		for _, update := range updates {
			if update.UpdateID >= offset {
				offset = update.UpdateID + 1
			}

			if update.Message == nil {
				continue
			}

			raw, _ := json.Marshal(update)
			event, err := p.NormalizeEvent(raw)
			if err != nil {
				continue
			}

			go func() {
				stopTyping := p.startTypingIndicator(ctx, event.WorkspaceID)
				resp, err := handler(ctx, event)
				stopTyping()
				if err != nil {
					fmt.Printf("telegram: handler error: %v\n", err)
					return
				}
				if err := p.SendResponse(event, resp); err != nil {
					fmt.Printf("telegram: send response error: %v\n", err)
				}
			}()
		}
	}
}

func (p *Plugin) getUpdates(ctx context.Context, offset int64) ([]telegramUpdate, error) {
	url := fmt.Sprintf("%s/bot%s/getUpdates?offset=%d&timeout=%d",
		p.apiBase, p.botToken, offset, pollingTimeout)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var result struct {
		OK     bool             `json:"ok"`
		Result []telegramUpdate `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	if !result.OK {
		return nil, fmt.Errorf("telegram getUpdates failed")
	}

	return result.Result, nil
}

// NormalizeEvent parses a Telegram Update JSON into a ChannelEvent.
func (p *Plugin) NormalizeEvent(raw []byte) (*channels.ChannelEvent, error) {
	var update telegramUpdate
	if err := json.Unmarshal(raw, &update); err != nil {
		return nil, fmt.Errorf("parsing telegram update: %w", err)
	}

	if update.Message == nil {
		return nil, fmt.Errorf("telegram update has no message")
	}

	return &channels.ChannelEvent{
		Channel:     "telegram",
		WorkspaceID: strconv.FormatInt(update.Message.Chat.ID, 10),
		UserID:      strconv.FormatInt(update.Message.From.ID, 10),
		ThreadID:    strconv.FormatInt(update.Message.MessageID, 10),
		Message:     update.Message.Text,
		Raw:         raw,
	}, nil
}

// SendResponse sends a text message back to the Telegram chat.
func (p *Plugin) SendResponse(event *channels.ChannelEvent, response *a2a.Message) error {
	text := extractText(response)
	html := markdown.ToTelegramHTML(text)
	chunks := markdown.SplitMessage(html, 4096)

	for i, chunk := range chunks {
		payload := map[string]any{
			"chat_id":    event.WorkspaceID,
			"text":       chunk,
			"parse_mode": "HTML",
		}
		if i == 0 {
			payload["reply_to_message_id"] = event.ThreadID
		}
		if err := p.sendMessage(payload); err != nil {
			// Fallback: retry without parse_mode (plain text)
			delete(payload, "parse_mode")
			payload["text"] = text
			if fbErr := p.sendMessage(payload); fbErr != nil {
				return fbErr
			}
		}
	}
	return nil
}

// sendChatAction sends a chat action (e.g. "typing") to indicate activity.
func (p *Plugin) sendChatAction(chatID, action string) error {
	payload := map[string]string{
		"chat_id": chatID,
		"action":  action,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshalling chat action: %w", err)
	}

	url := fmt.Sprintf("%s/bot%s/sendChatAction", p.apiBase, p.botToken)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating chat action request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.ReadAll(resp.Body)
	return nil
}

// startTypingIndicator sends "typing" chat action repeatedly until the
// returned stop function is called. Telegram's typing indicator expires
// after ~5 seconds, so we resend every 4 seconds.
func (p *Plugin) startTypingIndicator(ctx context.Context, chatID string) (stop func()) {
	done := make(chan struct{})
	stop = func() {
		select {
		case <-done:
		default:
			close(done)
		}
	}

	// Send the first typing indicator immediately.
	_ = p.sendChatAction(chatID, "typing")

	go func() {
		ticker := time.NewTicker(4 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = p.sendChatAction(chatID, "typing")
			}
		}
	}()

	return stop
}

// sendMessage posts a JSON payload to the Telegram sendMessage API.
func (p *Plugin) sendMessage(payload map[string]any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshalling telegram response: %w", err)
	}

	url := fmt.Sprintf("%s/bot%s/sendMessage", p.apiBase, p.botToken)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating telegram request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("posting to telegram: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telegram API error %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
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

// Telegram API types (minimal, for parsing).

type telegramUpdate struct {
	UpdateID int64            `json:"update_id"`
	Message  *telegramMessage `json:"message,omitempty"`
}

type telegramMessage struct {
	MessageID int64        `json:"message_id"`
	From      telegramUser `json:"from"`
	Chat      telegramChat `json:"chat"`
	Text      string       `json:"text"`
}

type telegramUser struct {
	ID int64 `json:"id"`
}

type telegramChat struct {
	ID int64 `json:"id"`
}
