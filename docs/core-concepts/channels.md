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
| MS Teams | `msteams.Plugin` | Graph API polling | — (outbound only) |
| WhatsApp | `whatsapp.Plugin` | WhatsApp Web (paired session) | — (outbound only) |

> **Note:** Slack uses Socket Mode — an outbound WebSocket connection from the agent to Slack's servers. No public URL or ngrok is needed for local development.

## Adding a Channel

```bash
# Add Slack adapter to your project
forge channel add slack

# Add Telegram adapter
forge channel add telegram

# Add WhatsApp adapter (then pair: forge channel whatsapp-login)
forge channel add whatsapp
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

### WhatsApp (`whatsapp-config.yaml`)

```yaml
adapter: whatsapp
settings:
  session_path: .forge/channels/whatsapp-session.db
  admit: dm_or_group_mention   # dm | group_mention | dm_or_group_mention
  allowed_groups: ""           # comma/newline-separated group JIDs; empty = all
  allowed_senders: ""          # empty = OWNER ONLY; "anyone" opens it up
  self_chat: true              # answer your own "Message Yourself" chat
  self_chat_prefix: "⚒ Forge: " # marks replies in the self-chat; "" disables
  include_recent_history: true
  recent_history_count: 20
```

| Setting | Default | Notes |
|---|---|---|
| `session_path` | `.forge/channels/whatsapp-session.db` | The paired session. **This file is the credential.** |
| `admit` | `dm_or_group_mention` | Group traffic always requires an explicit @-mention. |
| `allowed_groups` | *(all)* | Group JIDs; the `@g.us` suffix may be omitted. |
| `allowed_senders` | *(owner only)* | Empty admits only the paired account. List numbers to add people, or set `anyone` to open it up. The owner always passes. |
| `self_chat` | `true` | Answer messages you send yourself. |
| `self_chat_prefix` | `⚒ Forge: ` | Marks the agent's replies in the self-chat. `""` disables. |
| `include_recent_history` | `true` | Injects observed chat context into the prompt. |
| `recent_history_count` | `20` | Per-chat window; block soft-capped at ~5000 chars. |

Unlike every other adapter's list settings, **a space is not a separator** in
`allowed_senders` — a space is part of a written phone number
(`+1 (415) 555-0100`). Use commas or newlines.

### Talking to your own agent

The paired number *is* the agent. The simplest way to use it is WhatsApp's
**Message Yourself** chat: open it on the paired phone and type. No second
account needed.

That works because `self_chat` is on by default. Your own messages arrive
flagged as self-sent, and so do the agent's replies — the loop guard is the
dedup ring, which records every message the agent sends before it can come
back around. Self-messages are accepted **only** in that chat, never in groups.

In that chat the agent sends **as you**, so WhatsApp renders its replies on the
same side, in the same colour, as your own messages — the sender is identical,
and nothing on the wire can change that. Replies are therefore prefixed:

```
what is 2+2?
⚒ Forge: 4
```

Change the marker with `self_chat_prefix`, or set it to `""` to turn it off.
It is only applied in the self-chat; a normal DM already distinguishes sender
from recipient. For real visual separation, message the agent from a second
WhatsApp account instead — a group works too, but note a group containing only
your own number will not: self-messages are accepted in the self-chat only.

To have the agent serve other people instead, list them:

```yaml
allowed_senders: "+1 (415) 555-0100, +44 7700 900123"
```

`allowed_senders` is **owner-only when empty** — a stranger who happens to have
the number gets nothing. That is deliberate: an open agent spends your LLM
budget and reaches whatever tools it has. Opening it up is an explicit choice:

```yaml
allowed_senders: anyone
```

### Messages from before the agent started

Reconnecting delivers a backlog, and a restart begins with an empty dedup ring.
Messages dated more than five minutes before the process started are dropped
rather than answered, so a restart doesn't reply to a replayed conversation —
and, in the self-chat, doesn't answer the agent's own replayed replies.

A brief restart still picks up anything sent while the agent was down.

## WhatsApp Setup

WhatsApp has no bot token. The adapter authenticates by linking itself as a
WhatsApp Web device, exactly like the desktop client:

```bash
forge channel add whatsapp
forge channel whatsapp-login     # renders a QR code in the terminal
# phone: WhatsApp → Settings → Linked Devices → Link a Device → scan
forge run --with whatsapp
```

The pairing is written to `session_path`. Keep it out of version control and
off shared volumes — anyone holding that file can send messages as the linked
account. Re-pairing requires `--force`, so re-running the login command by
accident cannot revoke a working session.

### Terms of Service and ban risk

This adapter speaks the **WhatsApp Web multidevice protocol** (via
[whatsmeow](https://pkg.go.dev/go.mau.fi/whatsmeow)), not the official WhatsApp
Cloud API and not Twilio. Automating that protocol is against WhatsApp's Terms
of Service and can get the linked number banned. The ban attaches to the phone
number, not the machine, and is not reliably reversible.

**Pair a dedicated number, never a personal one.** If you need a
ToS-sanctioned path, the WhatsApp Cloud API is a different integration and is
not what this adapter implements.

### Identities: phone numbers and LIDs

WhatsApp is migrating group participants to hidden-number identifiers (LIDs,
`<id>@lid`) instead of phone numbers. The adapter tracks both identities for
the paired account, so an @-mention resolves under either. For
`allowed_senders`, a LID sender is matched via the phone-number alternate the
server supplies; when no alternate is available the message is dropped and the
log line names the LID so you can add it to the list directly.

### Group history is observed, not fetched

`include_recent_history` works differently here than in the Teams adapter.
Microsoft Graph exposes `/chats/{id}/messages`, so Teams can fetch prior
messages on demand. WhatsApp has no equivalent for a linked device — history
reaches one only through a sync push at pairing time. The adapter therefore
builds its context window from traffic it observes while running.

The consequence: **context covers messages seen since the adapter started, and
is lost on restart.** An agent restarted mid-conversation will not see what
came before.

### Not supported

- **DEFER approvals and MCP delegated consent.** WhatsApp's interactive
  message types are unreliable over the Web protocol, so the adapter
  implements neither `ApprovalDeliverer` nor `ConsentDeliverer`. A
  `security.defer` route naming `whatsapp` will warn at startup; resolve those
  approvals via `POST /tasks/{id}/decisions` instead.
- **`UserEmail` on inbound events.** WhatsApp has no email identity, so
  delegated (`auth.type: user`) MCP tools cannot resolve an on-behalf-of
  subject on this channel.
- **Media.** Attachments are not downloaded or sent; captions on inbound media
  are read as prompt text.

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

### Never dump raw tool JSON to the channel (#384)

Two hardening rules keep a large tool result from becoming an unreadable channel dump:

- **Raw structured data is never auto-attached.** Large JSON/YAML tool output (e.g. an MCP search result) is *not* forwarded as a file part — the model must summarize it. Only genuine prose (markdown) deliverables attach. This also defeats a subtle failure mode: once the context-compression hook has run, a JSON blob carries `<<ctxzip:…>>` markers and is no longer valid JSON, so content-sniffing alone would misclassify it as markdown and attach it — the suppression is keyed on the tool being MCP/structured, not just on sniffing.
- **Compression markers are stripped before delivery.** Any `<<ctxzip:…>>` context-compression pointer that leaks into channel-bound text is removed by `markdown.StripCompressionMarkers` across **Slack, Telegram, and MS Teams**, applied to the message text, the file content, *and* `response.Summary` (which is composed over the possibly-compressed context — the most likely leak site).

On Slack specifically, chunked replies (when a message is split) all carry `thread_ts`, so a multi-part reply stays in one thread instead of half landing in the main channel.

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
5. Wrap your per-message handler with `channels.StartDeliverSpan(ctx, "<adapter>", event)` so the dispatch lands in traces as `channel.<adapter>.deliver` and the downstream A2A POST nests under it via the W3C `traceparent` injected by the router. See [Observability — Tracing › `channel.<adapter>.deliver`](../core-concepts/observability-tracing.md#channeladapterdeliver).

## Tracing

When tracing is enabled, each inbound message produces a `channel.<adapter>.deliver` span that wraps the adapter's per-message handler. The internal A2A POST in `forge-cli/channels/router.go` injects the W3C `traceparent` from that span's context, so the agent server's `a2a.tasks/send` span nests under the deliver span. Operators can finally answer "how long does Slack→agent take?" from the flame graph alone, without correlating two unconnected trace roots.

Attributes (Slack / Telegram / Teams alike): `forge.channel.adapter`, `forge.channel.target` (Slack channel ID / Telegram chat ID / Teams chat ID), `forge.channel.message_id` (pivot back to the upstream system), `forge.channel.user_id`. Span Status is set to `Error` on handler / send failure. See [Observability — Tracing](../core-concepts/observability-tracing.md#channeladapterdeliver) for the full attribute reference.
