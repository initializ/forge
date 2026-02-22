# ✨ Forge

Turn a `SKILL.md` into a portable, secure, runnable AI agent.

Forge is a portable runtime for building and running secure AI agents from simple skill definitions. It take Agent Skills and makes it:

* A runnable AI agent
* A portable bundle
* A local HTTP / A2A service
* A Slack or Telegram bot
* A secure, restricted execution environment

No Docker required. No inbound tunnels required. No cloud lock-in.

---

## 🚀 Why Forge?

**Instant Agent From a Single Command**

Write a SKILL.md. Run `forge init`. Your agent is live.

The wizard configures your model provider, validates your API key,
connects Slack or Telegram, picks skills, and starts your agent.
Zero to running in under 5 minutes.

### 🔐 Secure by Default

Forge is designed for safe execution:

* ❌ Does NOT create public tunnels
* ❌ Does NOT expose webhooks automatically
* ✅ Uses outbound-only connections (Slack Socket Mode, Telegram polling)
* ✅ Enforces outbound domain allowlists
* ✅ Supports restricted network profiles

No accidental exposure. No hidden listeners.

---

## ⚡ Get Started in 60 Seconds

```bash
# Install
curl -sSL https://github.com/initializ/forge/releases/latest/download/forge-$(uname -s)-$(uname -m).tar.gz | tar xz
sudo mv forge /usr/local/bin/

# Initialize a new agent (interactive wizard)
forge init my-agent

# Run locally
cd my-agent && forge run

# Run with Telegram
forge run --with telegram
```

The `forge init` wizard walks you through model provider, API key, tools, skills, and channel setup. Use `--non-interactive` with flags for scripted setups.

---

## Install

### macOS (Homebrew)
```bash
brew install initializ/tap/forge
```

### Linux / macOS (binary)
```bash
curl -sSL https://github.com/initializ/forge/releases/latest/download/forge-$(uname -s)-$(uname -m).tar.gz | tar xz
sudo mv forge /usr/local/bin/
```

### Windows

Download the latest `.zip` from [GitHub Releases](https://github.com/initializ/forge/releases/latest) and add to your PATH.

### Verify
```bash
forge --version
```

---

## How It Works

```
SKILL.md ─→ Parse ─→ Discover tools/requirements ─→ Compile AgentSpec
                                                            │
                                                            v
                                                    Apply security policy
                                                            │
                                                            v
                                                      Run LLM loop
                                                    (tool calling agent)
```

1. You write a `SKILL.md` that describes what the agent can do
2. Forge parses the skill definitions and optional YAML frontmatter (binary deps, env vars)
3. The build pipeline discovers tools, resolves egress domains, and compiles an `AgentSpec`
4. Security policies (egress allowlists, capability bundles) are applied
5. The runtime executes an LLM-powered tool-calling loop against your skills

---

## Skills

Skills are defined in Markdown with optional YAML frontmatter for requirements:

```markdown
---
name: weather
description: Weather data skill
metadata:
  forge:
    requires:
      bins:
        - curl
      env:
        required: []
        one_of: []
        optional: []
---
## Tool: weather_current

Get current weather for a location.

**Input:** location (string) - City name or coordinates
**Output:** Current temperature, conditions, humidity, and wind speed

## Tool: weather_forecast

Get weather forecast for a location.

**Input:** location (string), days (integer: 1-7)
**Output:** Daily forecast with high/low temperatures and conditions
```

Each `## Tool:` heading defines a tool the agent can call. The frontmatter declares binary dependencies and environment variable requirements. Skills compile into JSON artifacts and prompt text during `forge build`.

---

## Tools

Forge ships with 7 built-in tools:

| Tool | Description |
|------|-------------|
| `http_request` | Make HTTP requests (GET, POST, etc.) |
| `json_parse` | Parse and query JSON data |
| `csv_parse` | Parse CSV data into structured records |
| `datetime_now` | Get current date and time |
| `uuid_generate` | Generate UUID v4 identifiers |
| `math_calculate` | Evaluate mathematical expressions |
| `web_search` | Search the web using Perplexity API |

```bash
# List all registered tools
forge tool list

# Show details for a specific tool
forge tool describe web_search
```

Tools can also be added via adapters (webhook, MCP, OpenAPI) or as custom tools discovered from your project.

---

## LLM Providers

Forge supports three LLM providers out of the box:

| Provider | Default Model | Base URL Override |
|----------|--------------|-------------------|
| `openai` | `gpt-4o` | `OPENAI_BASE_URL` |
| `anthropic` | `claude-sonnet-4-20250514` | `ANTHROPIC_BASE_URL` |
| `ollama` | `llama3` | `OLLAMA_BASE_URL` |

Configure in `forge.yaml`:

```yaml
model:
  provider: openai
  name: gpt-4o
```

Or override with environment variables:

```bash
export FORGE_MODEL_PROVIDER=anthropic
export ANTHROPIC_API_KEY=sk-ant-...
forge run
```

Provider is auto-detected from available API keys if not explicitly set.

---

## Channel Connectors

Forge connects agents to messaging platforms via channel adapters. Both use **outbound-only connections** — no public URLs, no ngrok, no inbound webhooks.

| Channel | Mode | How It Works |
|---------|------|-------------|
| Slack | Socket Mode | Outbound WebSocket via `apps.connections.open` |
| Telegram | Polling (default) | Long-polling via `getUpdates`, no public URL needed |

```bash
# Add Slack adapter to your project
forge channel add slack

# Run agent with Slack connected
forge run --with slack

# Run with multiple channels
forge run --with slack,telegram
```

Channels can also run standalone as separate services:

```bash
export AGENT_URL=http://localhost:8080
forge channel serve slack
```

---

## 🔐 Security

Forge generates egress security controls at build time. Every `forge build` produces an `egress_allowlist.json` and Kubernetes NetworkPolicy manifest.

### Egress Profiles

| Profile | Description | Default Mode |
|---------|-------------|-------------|
| `strict` | Maximum restriction, deny by default | `deny-all` |
| `standard` | Balanced, allow known domains | `allowlist` |
| `permissive` | Minimal restriction for development | `dev-open` |

### Egress Modes

| Mode | Behavior |
|------|----------|
| `deny-all` | No outbound network access |
| `allowlist` | Only explicitly allowed domains |
| `dev-open` | Unrestricted outbound access (development only) |

### Configuration

```yaml
egress:
  profile: standard
  mode: allowlist
  allowed_domains:
    - api.example.com
  capabilities:
    - slack
```

Capability bundles (e.g., `slack`, `telegram`) automatically include their required domains. Tool domains are inferred from registered tools (e.g., `web_search` adds `api.perplexity.ai`). The runtime enforces tool-level restrictions based on the compiled allowlist.

---

## Running Modes

| | `forge run` | `forge serve` |
|---|------------|--------------|
| **Purpose** | Development | Production service |
| **Channels** | `--with slack,telegram` | Reads from `forge.yaml` |
| **Sessions** | Single session | Multi-session with TTL |
| **Logging** | Human-readable | JSON structured logs |
| **Lifecycle** | Interactive | PID file, graceful shutdown |

```bash
# Development
forge run --with slack --port 8080

# Production
forge serve --port 8080 --session-ttl 30m
```

---

## Packaging & Deployment

```bash
# Build a container image (auto-detects Docker/Podman/Buildah)
forge package

# Production build (rejects dev tools and dev-open egress)
forge package --prod

# Build and push to registry
forge package --registry ghcr.io/myorg --push

# Generate docker-compose with channel sidecars
forge package --with-channels

# Export for Initializ Command platform
forge export --pretty --include-schemas
```

`forge package` generates a Dockerfile and Kubernetes manifests. Use `--prod` to strip dev tools and enforce strict egress. Use `--verify` to smoke-test the built container.

---

## Command Reference

| Command | Description |
|---------|-------------|
| `forge init [name]` | Initialize a new agent project (interactive wizard) |
| `forge build` | Compile agent artifacts (AgentSpec, egress allowlist, skills) |
| `forge validate [--strict] [--command-compat]` | Validate agent spec and forge.yaml |
| `forge run [--with slack,telegram] [--port 8080]` | Run agent locally with A2A dev server |
| `forge serve [--port 8080] [--session-ttl 30m]` | Run as production service |
| `forge package [--push] [--prod] [--registry] [--with-channels]` | Build container image |
| `forge export [--pretty] [--include-schemas] [--simulate-import]` | Export for Command platform |
| `forge tool list\|describe` | List or inspect registered tools |
| `forge channel add\|serve\|list\|status` | Manage channel adapters |

See [docs/commands.md](docs/commands.md) for full flags and examples.

---

## 🔮 Upcoming

- 🔌 CrewAI / LangChain agent import
- 🧩 WASM tool plugins
- ☁️ One-click deploy to **initializ**
- 🧠 Persistent file-based memory
- 📦 Community skill registry

---


## 💡 Philosophy


Running agents that do real work requires more than prompts.

It requires:

### 🧱 Atomicity

Agents must be packaged as clear, self-contained units:

* Explicit skills
* Defined tools
* Declared dependencies
* Deterministic behavior

No hidden state. No invisible glue code.

### 🔐 Security

Agents must run safely:

* Restricted outbound access
* Explicit capability bundles
* No automatic inbound exposure
* Transparent execution boundaries

If an agent can touch the outside world, it must declare how.

### 📦 Portability

Agents should not be locked to a framework, a cloud, or a vendor.

A Forge agent:

- Runs locally
- Runs in containers
- Runs in Kubernetes
- Runs in cloud
- Runs inside **initializ**
- Speaks A2A

*Same agent. Anywhere.*

**Forge is built on a simple belief:**

> Real agent systems require atomicity, security, and portability.

Forge provides those building blocks.

---

## Documentation

- [Architecture](docs/architecture.md) — System design and data flows
- [Commands](docs/commands.md) — CLI reference with all flags and examples
- [Runtime](docs/runtime.md) — LLM agent loop, providers, and memory
- [Tools](docs/tools.md) — Tool system: builtins, adapters, custom tools
- [Skills](docs/skills.md) — Skills definition and compilation
- [Security & Egress](docs/security-egress.md) — Egress security controls
- [Hooks](docs/hooks.md) — Agent loop hook system
- [Plugins](docs/plugins.md) — Framework plugin system
- [Channels](docs/channels.md) — Channel adapter architecture
- [Contributing](docs/contributing.md) — Development guide and PR process

## License

See [LICENSE](LICENSE) for details.
