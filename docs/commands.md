# CLI Reference

> Part of [Forge Documentation](../README.md)

Complete reference for all Forge CLI commands.

## Global Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--config` | | `forge.yaml` | Config file path |
| `--verbose` | `-v` | `false` | Enable verbose output |
| `--output-dir` | `-o` | `.` | Output directory |

---

## `forge init`

Initialize a new agent project.

```
forge init [name] [flags]
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--name` | `-n` | | Agent name |
| `--framework` | `-f` | | Framework: `crewai`, `langchain`, or `custom` |
| `--language` | `-l` | | Language: `python`, `typescript`, or `go` |
| `--model-provider` | `-m` | | Model provider: `openai`, `anthropic`, `ollama`, or `custom` |
| `--channels` | | | Channel adapters (e.g., `slack,telegram`) |
| `--tools` | | | Builtin tools to enable (e.g., `web_search,http_request`) |
| `--skills` | | | Registry skills to include (e.g., `github,weather`) |
| `--api-key` | | | LLM provider API key |
| `--org-id` | | | OpenAI Organization ID (enterprise) |
| `--from-skills` | | | Path to a SKILL.md file for auto-configuration |
| `--non-interactive` | | `false` | Skip interactive prompts |

### Examples

```bash
# Interactive mode (default)
forge init my-agent

# Non-interactive with all options
forge init my-agent \
  --framework langchain \
  --language python \
  --model-provider openai \
  --channels slack,telegram \
  --non-interactive

# From a skills file
forge init my-agent --from-skills SKILL.md

# With builtin tools and registry skills
forge init my-agent \
  --framework custom \
  --model-provider openai \
  --tools web_search,http_request \
  --skills github \
  --api-key sk-... \
  --non-interactive

# OpenAI enterprise with organization ID
forge init my-agent \
  --model-provider openai \
  --api-key sk-... \
  --org-id org-xxxxxxxxxxxxxxxxxxxxxxxx \
  --non-interactive
```

---

## `forge build`

Build the agent container artifact. Runs the full 8-stage build pipeline.

```
forge build [flags]
```

Uses global `--config` and `--output-dir` flags. Output is written to `.forge-output/` by default.

### Examples

```bash
# Build with default config
forge build

# Build with custom config and output
forge build --config agent.yaml --output-dir ./build
```

---

## `forge validate`

Validate agent spec and forge.yaml.

```
forge validate [flags]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--strict` | `false` | Treat warnings as errors |
| `--command-compat` | `false` | Check Command platform import compatibility |

### Examples

```bash
# Basic validation
forge validate

# Strict mode
forge validate --strict

# Check Command compatibility
forge validate --command-compat
```

---

## `forge run`

Run the agent locally with an A2A-compliant dev server.

```
forge run [flags]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--port` | `8080` | Port for the A2A dev server |
| `--host` | `""` (all interfaces) | Bind address |
| `--shutdown-timeout` | `0` (immediate) | Graceful shutdown timeout |
| `--mock-tools` | `false` | Use mock runtime instead of subprocess |
| `--enforce-guardrails` | `false` | Enforce guardrail violations as errors |
| `--model` | | Override model name (sets `MODEL_NAME` env var) |
| `--provider` | | LLM provider: `openai`, `anthropic`, or `ollama` |
| `--env` | `.env` | Path to .env file |
| `--with` | | Comma-separated channel adapters (e.g., `slack,telegram`) |
| `--cors-origins` | localhost | Comma-separated CORS allowed origins (e.g., `https://app.example.com,https://admin.example.com`). Use `*` to allow all origins |

### Examples

```bash
# Run with defaults
forge run

# Run with mock tools on custom port
forge run --port 9090 --mock-tools

# Run with LLM provider and channels
forge run --provider openai --model gpt-4 --with slack

# Container deployment
forge run --host 0.0.0.0 --shutdown-timeout 30s

# Run with guardrails enforced
forge run --enforce-guardrails --env .env.production

# Run with custom CORS origins (for K8s ingress)
forge run --cors-origins 'https://app.example.com,https://admin.example.com'
```

---

## `forge serve`

Manage the agent as a background daemon process.

```
forge serve [start|stop|status|logs] [flags]
```

### Subcommands

| Subcommand | Description |
|------------|-------------|
| `start` (default) | Start the daemon in background |
| `stop` | Send SIGTERM (10s timeout, SIGKILL fallback) |
| `status` | Show PID, listen address, health check |
| `logs` | Tail `.forge/serve.log` |

### Flags (start)

| Flag | Default | Description |
|------|---------|-------------|
| `--port` | `8080` | HTTP server port |
| `--host` | `127.0.0.1` | Bind address (secure default) |
| `--with` | | Channel adapters |
| `--cors-origins` | localhost | Comma-separated CORS allowed origins |

### Examples

```bash
# Start daemon (secure defaults: 127.0.0.1, 30s shutdown timeout)
forge serve

# Start on custom port
forge serve start --port 9090 --host 0.0.0.0

# Stop the daemon
forge serve stop

# Check status (PID, uptime, health)
forge serve status

# View recent logs (last 100 lines)
forge serve logs
```

The daemon forks `forge run` in the background with `setsid`, writes state to `.forge/serve.json`, and redirects output to `.forge/serve.log`.

---

## `forge export`

Export agent spec for Command platform import.

```
forge export [flags]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--output` | `{agent_id}-forge.json` | Output file path |
| `--pretty` | `false` | Format JSON with indentation |
| `--include-schemas` | `false` | Embed tool schemas inline |
| `--simulate-import` | `false` | Print simulated import result |
| `--dev` | `false` | Include dev-category tools in export |

### Examples

```bash
# Export with defaults
forge export

# Pretty-print with embedded schemas
forge export --pretty --include-schemas

# Simulate Command import
forge export --simulate-import
```

---

## `forge package`

Build a container image for the agent.

```
forge package [flags]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--push` | `false` | Push image to registry after building |
| `--platform` | | Target platform (e.g., `linux/amd64`) |
| `--no-cache` | `false` | Disable layer cache |
| `--dev` | `false` | Include dev tools in image |
| `--prod` | `false` | Production build (rejects dev tools and dev-open egress) |
| `--verify` | `false` | Smoke-test container after build |
| `--registry` | | Registry prefix (e.g., `ghcr.io/org`) |
| `--builder` | | Force builder: `docker`, `podman`, or `buildah` |
| `--skip-build` | `false` | Skip re-running forge build |
| `--with-channels` | `false` | Generate docker-compose.yaml with channel adapters |

### Examples

```bash
# Build image with auto-detected builder
forge package

# Build and push to registry
forge package --registry ghcr.io/myorg --push

# Build for specific platform with no cache
forge package --platform linux/amd64 --no-cache

# Generate docker-compose with channels
forge package --with-channels
```

---

## `forge schedule`

Manage cron schedules.

```
forge schedule list
```

Lists all configured cron schedules (both YAML-defined and LLM-created).

---

## `forge tool`

Manage and inspect agent tools.

### `forge tool list`

List all available tools.

```bash
forge tool list
```

### `forge tool describe`

Show tool details and input schema.

```bash
forge tool describe <name>
```

---

## `forge channel`

Manage agent communication channels.

### `forge channel add`

Add a channel adapter to the project.

```bash
forge channel add <slack|telegram>
```

### `forge channel serve`

Run a standalone channel adapter.

```bash
forge channel serve <slack|telegram>
```

Requires the `AGENT_URL` environment variable to be set.

### `forge channel list`

List available channel adapters.

```bash
forge channel list
```

### `forge channel status`

Show configured channels from `forge.yaml`.

```bash
forge channel status
```

---

## `forge secret`

Manage encrypted secrets.

```bash
# Store a secret (prompts for value securely)
forge secret set OPENAI_API_KEY

# Store with inline value
forge secret set SLACK_BOT_TOKEN xoxb-...

# Retrieve a secret (shows source)
forge secret get OPENAI_API_KEY

# List all secret keys
forge secret list

# Delete a secret
forge secret delete OLD_KEY

# Agent-local secret
forge secret set API_KEY --local
```

---

## `forge key`

Manage Ed25519 signing keys.

```bash
# Generate an Ed25519 signing keypair
forge key generate

# Generate with a custom name
forge key generate --name ci-key

# Add a public key to the trusted keyring
forge key trust ~/.forge/signing-key.pub

# List signing and trusted keys
forge key list
```

---

## `forge skills`

Manage agent skills.

```bash
# Add a skill from the registry
forge skills add <skill-name>

# List available skills
forge skills list

# Filter by category
forge skills list --category sre

# Filter by tags
forge skills list --tags kubernetes,incident-response

# Validate skill requirements
forge skills validate

# Audit skill security
forge skills audit --embedded

# Sign a skill
forge skills sign

# Generate a signing key
forge skills keygen

# Generate trust report
forge skills trust-report
```

---

## `forge ui`

Launch the local web dashboard.

```bash
# Launch with defaults
forge ui

# Specify workspace and port
forge ui --dir /path/to/workspace --port 4200

# Launch without auto-opening browser
forge ui --no-open
```

See [Dashboard](dashboard.md) for full documentation.

---
← [Hooks](hooks.md) | [Back to README](../README.md) | [Configuration](configuration.md) →
