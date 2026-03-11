# Channel Adapters

> Part of [Forge Documentation](../README.md)

Channel adapters bridge messaging platforms (Slack, Telegram) to your A2A-compliant agent. Each adapter normalizes platform-specific events into a common `ChannelEvent` format, forwards them to the agent's A2A server, and delivers responses back to the originating platform.

```
  Slack/Telegram  ──→  Channel Plugin  ──→  Router  ──→  A2A Server
       ↑                                                      │
       └──────────────── SendResponse ←────────────────────────┘
```

Both channels use **outbound-only connections** — no public URLs, no ngrok, no inbound webhooks.

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

Standalone mode is useful for running adapters as separate services in production. Each adapter connects to the agent's A2A server via HTTP.

## Slack App Setup

Before running the Slack adapter, create and configure a Slack App:

1. **Create a Slack App** at https://api.slack.com/apps -> "Create New App" -> "From scratch"
2. **Enable Socket Mode** — Settings -> Socket Mode -> toggle **On**
3. **Generate an App-Level Token** — Basic Information -> "App-Level Tokens" -> "Generate Token and Scopes" -> add the `connections:write` scope -> copy the `xapp-...` token
4. **Enable Event Subscriptions** — Features -> Event Subscriptions -> toggle **On** -> Subscribe to bot events:
   - `message.channels` — messages in public channels
   - `message.im` — direct messages
   - `app_mention` — @mentions of your bot
5. **Set Bot Token Scopes** — Features -> OAuth & Permissions -> Bot Token Scopes -> add:
   - `app_mentions:read`
   - `chat:write`
   - `channels:history`
   - `im:history`
   - `files:write` (for large response file uploads)
   - `reactions:write` (for processing indicators)
6. **Install the App** — Settings -> Install App -> "Install to Workspace" -> copy the `xoxb-...` Bot Token
7. **Add tokens to `.env`**:
   ```
   SLACK_APP_TOKEN=xapp-1-...
   SLACK_BOT_TOKEN=xoxb-...
   ```
8. **Invite the bot** to any channel where you want it active: `/invite @YourBot`

### Mention-Aware Filtering

The Slack adapter resolves the bot's own user ID at startup via `auth.test` and uses it for intelligent message filtering:

- **Channel messages** — the bot only responds when explicitly @mentioned (e.g. `@ForgeBot what's the status?`)
- **Thread replies** — the bot responds to all messages in a thread it's participating in, unless the message @mentions a different user
- **Direct messages** — all DMs are processed
- Bot mentions are stripped from the message text before passing to the LLM, so it sees clean input

### Processing Indicators

When the Slack adapter receives a message:

1. An :eyes: reaction is added immediately to acknowledge receipt
2. If the handler takes longer than 15 seconds, an interim message is posted: _"Researching, I'll post the result shortly..."_
3. The :eyes: reaction is removed when the response is ready

This gives users visual feedback that their message is being processed, especially for long-running research queries.

### Telegram Processing Indicators

The Telegram adapter mirrors Slack's processing feedback:

1. A typing indicator ("typing...") is sent immediately and refreshed every 4 seconds
2. If the handler takes longer than 15 seconds, an interim message is posted: _"Working on it — I'll send the result when ready."_
3. The typing indicator stops when the response is ready

**Context isolation:** Each handler goroutine runs with an independent context (10-minute timeout), detached from the polling loop. This prevents in-flight tasks from being cancelled if the polling context is interrupted during server restarts or errors.

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

## Large Response Handling

When an agent response exceeds 4096 characters (common with research reports), channel adapters automatically split it into a **summary message** and a **file attachment**:

1. A brief summary (first paragraph, up to 600 characters) is sent as a regular message
2. The full report is uploaded as a downloadable Markdown file (`research-report.md`)

This works on both Slack (via `files.getUploadURLExternal`) and Telegram (via `sendDocument`). If file upload fails, adapters fall back to chunked messages. Markdown is converted to platform-native formatting (Slack mrkdwn or Telegram HTML).

Additionally, the runtime tracks large tool outputs (>8000 characters) and attaches them as file parts in the A2A response. This ensures channel adapters receive the complete, untruncated tool output even when the LLM's text summary is truncated by output token limits. JSON tool outputs (e.g. Tavily Research/Search results) are automatically unwrapped into readable markdown before delivery.

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
    Name() string
    Init(cfg ChannelConfig) error
    Start(ctx context.Context, handler EventHandler) error
    Stop() error
    NormalizeEvent(raw []byte) (*ChannelEvent, error)
    SendResponse(event *ChannelEvent, response *a2a.Message) error
}
```

### Steps

1. Create a new package under `forge-plugins/channels/yourplatform/`.
2. Implement `ChannelPlugin`.
3. Register the plugin in the channel registry.
4. Add config generation in `generateChannelConfig()` and env vars in `generateEnvVars()`.

---
← [Memory](memory.md) | [Back to README](../README.md) | [Security Overview](security/overview.md) →
