# Channel Adapters

## Overview

Channel adapters bridge messaging platforms (Slack, Telegram) to your A2A-compliant agent. Each adapter normalizes platform-specific events into a common `ChannelEvent` format, forwards them to the agent's A2A server, and delivers responses back to the originating platform.

```
  Slack/Telegram  ──→  Channel Plugin  ──→  Router  ──→  A2A Server
       ↑                                                      │
       └──────────────── SendResponse ←────────────────────────┘
```

## Supported Channels

| Channel | Adapter | Mode | Default Port |
|---------|---------|------|-------------|
| Slack | `slack.Plugin` | Socket Mode | 3000 |
| Telegram | `telegram.Plugin` | Polling or Webhook | 3001 |

> **Note:** Slack uses Socket Mode — an outbound WebSocket connection from the agent to Slack's servers. No public URL or ngrok is needed for local development.

## Adding a Channel

```bash
# Add Slack adapter to your project
forge channel add slack

# Add Telegram adapter
forge channel add telegram
```

This command:
1. Generates `{adapter}-config.yaml` with placeholder settings
2. Updates `.env` with required environment variables
3. Adds the channel to `forge.yaml`'s `channels` list
4. Prints setup instructions

## Configuration

### Slack (`slack-config.yaml`)

```yaml
adapter: slack
settings:
  app_token_env: SLACK_APP_TOKEN
  bot_token_env: SLACK_BOT_TOKEN
```

Environment variables:
- `SLACK_APP_TOKEN` — Socket Mode app-level token (`xapp-...`)
- `SLACK_BOT_TOKEN` — Bot user OAuth token (`xoxb-...`)

### Telegram (`telegram-config.yaml`)

```yaml
adapter: telegram
webhook_port: 3001
webhook_path: /telegram/webhook
settings:
  bot_token: TELEGRAM_BOT_TOKEN
  mode: polling
```

Environment variables:
- `TELEGRAM_BOT_TOKEN` — Bot token from @BotFather

Mode options:
- `polling` (default) — Long-polling via `getUpdates`
- `webhook` — Receives updates via HTTP webhook

## Running with Channels

### Alongside the Agent

```bash
# Start agent with Slack and Telegram adapters
forge run --with slack,telegram
```

This starts the A2A dev server and all specified channel adapters in the same process.

### Standalone Mode

```bash
# Run adapter separately (requires AGENT_URL)
export AGENT_URL=http://localhost:8080
forge channel serve slack
```

Standalone mode is useful for running adapters as separate services in production.

## Docker Compose Integration

```bash
# Package agent with channel adapter sidecars
forge package --with-channels
```

This generates a `docker-compose.yaml` with:
- An `agent` service running the A2A server
- Adapter services (e.g., `slack-adapter`, `telegram-adapter`) connecting to the agent

## Writing a Custom Channel Adapter

Implement the `channels.ChannelPlugin` interface:

```go
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
```

### Key Types

```go
// ChannelConfig holds per-adapter configuration loaded from YAML.
type ChannelConfig struct {
    Adapter     string            `yaml:"adapter"`
    WebhookPort int               `yaml:"webhook_port,omitempty"`
    WebhookPath string            `yaml:"webhook_path,omitempty"`
    Settings    map[string]string `yaml:"settings,omitempty"`
}

// ChannelEvent is the normalized representation of an inbound message.
type ChannelEvent struct {
    Channel     string          `json:"channel"`
    WorkspaceID string          `json:"workspace_id"`
    UserID      string          `json:"user_id"`
    ThreadID    string          `json:"thread_id,omitempty"`
    Message     string          `json:"message"`
    Attachments []Attachment    `json:"attachments,omitempty"`
    Raw         json.RawMessage `json:"raw,omitempty"`
}
```

### Steps

1. Create a new package under `internal/channels/yourplatform/`.
2. Implement `ChannelPlugin`.
3. Register the plugin in the channel registry (see `internal/cmd/channel.go`).
4. Add config generation in `generateChannelConfig()` and env vars in `generateEnvVars()`.
