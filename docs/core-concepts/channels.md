---
title: "Channels"
description: "Bridge messaging platforms like Slack and Telegram to your AI agent."
order: 4
---

Channel adapters bridge messaging platforms (Slack, Telegram) to your A2A-compliant agent. Each adapter normalizes platform-specific events into a common `ChannelEvent` format, forwards them to the agent's A2A server, and delivers responses back to the originating platform.

```
  Slack/Telegram  ──→  Channel Plugin  ──→  Router  ──→  A2A Server
       ↑                                                      │
       └──────────────── SendResponse ←────────────────────────┘
```

Both channels use **outbound-only connections** — no public URLs, no ngrok, no inbound webhooks. Telegram webhook mode binds to `127.0.0.1` only with secret token verification.

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

The Slack adapter resolves the bot's own user ID **and** `bot_id` at startup via `auth.test`. The user ID drives @mention matching; the `bot_id` powers the self-loop guard.

- **Channel messages** — the bot only responds when explicitly @mentioned (e.g. `@ForgeBot what's the status?`)
- **Thread replies** — the bot responds to all messages in a thread it's participating in, unless the message @mentions a different user
- **Direct messages** — all DMs are processed
- Bot mentions are stripped from the message text before passing to the LLM, so it sees clean input

### Bot Authorship Admission

By default the adapter ignores every event whose Slack `bot_id` is non-empty — this prevents bot-to-bot loops. Operators can admit specific bots (scheduler, monitoring tool, CI bot) that should be allowed to @-mention the agent by listing their `bot_id`s in `slack-config.yaml`:

```yaml
adapter: slack
settings:
  app_token_env: SLACK_APP_TOKEN
  bot_token_env: SLACK_BOT_TOKEN
  allow_bot_ids: B0123ABC,B0456DEF
```

Two safeguards keep loops bounded:

| Rule | Scope |
|---|---|
| **Self-loop guard** | The agent's own `bot_id` is always dropped, even if listed in `allow_bot_ids`. No opt-out. |
| **Mention requirement** | Admitted bots still must include `<@FORGE_AGENT_USER_ID>` in the message text — chatter from an allowed bot without an @-mention is ignored. |

Both drop paths emit an operator-actionable log line naming the `bot_id` and pointing at the YAML setting, so debugging is self-service. Find a bot's `bot_id`: Slack admin → Manage apps → app → Bot User OAuth.

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
  # Optional: comma-separated bot_ids whose @mentions are admitted.
  # Default (omit / empty) = no other bots admitted; only humans trigger.
  # The agent's own bot_id is always dropped, regardless of this list.
  # allow_bot_ids: B0123ABC,B0456DEF
```

Environment variables:
- `SLACK_APP_TOKEN` — Socket Mode app-level token (`xapp-...`)
- `SLACK_BOT_TOKEN` — Bot user OAuth token (`xoxb-...`)

See [Bot Authorship Admission](#bot-authorship-admission) for `allow_bot_ids` details.

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
- `webhook` — Receives updates via HTTP webhook (loopback-only binding with secret token verification)

### Telegram Webhook Security

When running in webhook mode, the Telegram adapter applies multiple security controls:

| Control | Detail |
|---------|--------|
| **Loopback binding** | Webhook server binds to `127.0.0.1:<port>` instead of `0.0.0.0`, preventing direct internet exposure |
| **Secret token verification** | A 32-byte random secret is generated at startup and registered with Telegram's `setWebhook` API. Incoming requests must include the matching `X-Telegram-Bot-Api-Secret-Token` header; mismatches return 401 |
| **Content-Type enforcement** | Only `application/json` requests are accepted; others return 415 |
| **Request body limit** | Bodies are limited to 1 MiB via `http.MaxBytesReader`; oversized payloads return 413 |

### Slack Event Deduplication

The Slack adapter deduplicates events by envelope ID to prevent processing the same message multiple times (common during reconnections or network retries):

- Each envelope ID is recorded in an in-memory cache on first receipt
- Subsequent envelopes with the same ID are silently skipped after acknowledgment
- Cache entries older than 5 minutes are evicted automatically every 60 seconds
- Empty envelope IDs are never considered duplicates

## Large Response Handling

When an agent response exceeds 4096 characters (common with research reports), channel adapters automatically split it into a **summary message** and a **file attachment**:

1. A brief summary is sent as a regular inline message
2. The full report is uploaded as a downloadable Markdown file (`research-report.md`)

This works on both Slack (via `files.getUploadURLExternal`) and Telegram (via `sendDocument`). If file upload fails, adapters fall back to chunked messages. Markdown is converted to platform-native formatting (Slack mrkdwn or Telegram HTML).

### Summary Source

The runtime decides what the inline summary contains:

| Condition | Inline summary source |
|---|---|
| Final LLM response > 4096 chars | LLM-generated summary — one extra `Chat()` call asking the model to summarise its own response in 2-4 sentences. Returned to channel adapters as `a2a.Message.Summary` |
| LLM response ≤ 4096 chars but a tool attached a large file part | The LLM's response text itself — it is already a brief summary of the file content. No extra summariser call |
| LLM response ≤ 4096 chars, no file part | The full response is sent inline as chunked messages — no attachment, no summary |

If the summariser call fails or returns empty, channel adapters fall back to head-truncating the response body at the first paragraph boundary (≤ 600 chars) or `truncateAtSentence(text, 500)`. The fallback ensures the channel always delivers *something* even when the LLM is unreachable.

Additionally, the runtime tracks large tool outputs (>8000 characters) and attaches them as file parts in the A2A response. This ensures channel adapters receive the complete, untruncated tool output even when the LLM's text summary is truncated by output token limits. JSON tool outputs (e.g. Tavily Research/Search results) are automatically unwrapped into readable markdown before delivery.

## Container Deployment

When channels are configured in `forge.yaml`, the build pipeline automatically:

1. **Includes channel config files** — `slack-config.yaml`, `telegram-config.yaml`, etc. are copied into the Docker build context alongside `forge.yaml`
2. **Adds `--with` to the entrypoint** — The container entrypoint becomes `["forge", "run", "--host", "0.0.0.0", "--with", "slack,telegram"]`
3. **Surfaces channel env vars in the manifests** — Every `_env`-suffixed setting in each `<channel>-config.yaml` (e.g. `bot_token_env: SLACK_BOT_TOKEN`) is unioned into the Kubernetes `secrets.yaml` and `deployment.yaml` (via `secretKeyRef`) and into the docker-compose adapter services. Both outputs derive from the same source — see [Kubernetes — Env Var Injection](../deployment/kubernetes.md#env-var-injection)
4. **Handles auth loopback** — When [external auth](runtime-engine.md#external-authentication) is configured, channel adapters authenticate to the A2A server using an internal token, bypassing the external auth provider

Pass channel secrets via environment variables:

```bash
docker run \
  -e SLACK_APP_TOKEN=xapp-... \
  -e SLACK_BOT_TOKEN=xoxb-... \
  -e FORGE_AUTH_URL=https://auth.example.com/verify \
  my-agent
```

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
