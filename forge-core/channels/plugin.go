// Package channels defines the channel adapter architecture for exposing
// self-hosted agents via messaging platforms like Slack and Telegram.
package channels

import (
	"context"
	"encoding/json"

	"github.com/initializ/forge/forge-core/a2a"
)

// ChannelPlugin is the interface every channel adapter must implement.
type ChannelPlugin interface {
	// Name returns the adapter name (e.g. "slack", "telegram").
	Name() string
	// Init configures the plugin from a ChannelConfig.
	Init(cfg ChannelConfig) error
	// Start begins listening for events and dispatching them to handler.
	// It blocks until ctx is cancelled.
	Start(ctx context.Context, handler EventHandler) error
	// Stop gracefully shuts down the plugin.
	Stop() error
	// NormalizeEvent converts raw platform bytes into a ChannelEvent.
	NormalizeEvent(raw []byte) (*ChannelEvent, error)
	// SendResponse delivers an A2A response back to the originating platform.
	SendResponse(event *ChannelEvent, response *a2a.Message) error
}

// EventHandler is the callback signature provided by the router.
// The plugin calls it when a message arrives; the handler forwards the event
// to the A2A server and returns the agent's response.
type EventHandler func(ctx context.Context, event *ChannelEvent) (*a2a.Message, error)

// ChannelConfig holds per-adapter configuration loaded from YAML.
type ChannelConfig struct {
	Adapter     string            `yaml:"adapter"`
	WebhookPort int               `yaml:"webhook_port,omitempty"`
	WebhookPath string            `yaml:"webhook_path,omitempty"`
	Settings    map[string]string `yaml:"settings,omitempty"`
}

// ChannelEvent is the normalized representation of an inbound message
// from any supported platform.
type ChannelEvent struct {
	Channel     string          `json:"channel"`
	WorkspaceID string          `json:"workspace_id"`
	UserID      string          `json:"user_id"`
	ThreadID    string          `json:"thread_id,omitempty"`
	Message     string          `json:"message"`
	Attachments []Attachment    `json:"attachments,omitempty"`
	Raw         json.RawMessage `json:"raw,omitempty"`
}

// Attachment represents a file or media item attached to a channel message.
type Attachment struct {
	Name     string `json:"name,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	URL      string `json:"url,omitempty"`
}
