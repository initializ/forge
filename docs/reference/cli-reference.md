---
title: "CLI Reference"
description: "Complete reference for all Forge CLI commands."
order: 1
---

Complete reference for all Forge CLI commands.

## Global Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--config` | | `forge.yaml` | Config file path |
| `--verbose` | `-v` | `false` | Enable verbose output |
| `--output-dir` | `-o` | `.` | Output directory |

---

## `forge init`

Initialize a new agent project. Without `--non-interactive`, a TUI wizard walks through: name → model provider → fallbacks → channel → tools → skills → context compression → authentication → egress review → summary.

```
forge init [name] [flags]
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--name` | `-n` | | Agent name |
| `--framework` | `-f` | | Framework: `crewai`, `langchain`, or `custom` |
| `--language` | `-l` | | Language: `python`, `typescript`, or `go` |
| `--model-provider` | `-m` | | Model provider: `openai`, `anthropic`, `gemini`, `ollama`, or `custom`. The `custom` value scaffolds an OpenAI-compatible endpoint by default (`provider: openai` + `OPENAI_BASE_URL` / `OPENAI_API_KEY`); the interactive wizard additionally offers an Anthropic Messages shape for Custom URLs which scaffolds `provider: anthropic` + `ANTHROPIC_BASE_URL` / `ANTHROPIC_API_KEY` (issue #202 Phase 1). |
| `--channels` | | | Channel adapters (e.g., `slack,telegram`) |
| `--tools` | | | Builtin tools to enable (e.g., `web_search,http_request`) |
| `--skills` | | | Registry skills to include (e.g., `github,weather`) |
| `--api-key` | | | LLM provider API key |
| `--org-id` | | | OpenAI Organization ID (enterprise) |
| `--from-skills` | | | Path to a SKILL.md file for auto-configuration |
| `--non-interactive` | | `false` | Skip interactive prompts |
| `--compression` | | `false` | Enable reversible context compression — writes `compression.enabled: true` to the scaffolded forge.yaml. See [Context Compression](../core-concepts/context-compression.md) |
| `--auth` | | | Auth mode: `none`, `oidc`, `http_verifier`, `aws_sigv4`, `gcp_iap`, `azure_ad`, `custom` |
| `--auth-issuer` | | | OIDC issuer URL (required with `--auth=oidc`) |
| `--auth-audience` | | | OIDC audience (required with `--auth=oidc`) |
| `--auth-url` | | | Verifier URL (required with `--auth=http_verifier`) |
| `--auth-default-org` | | | Default `org_id` for http_verifier |
| `--auth-groups-claim` | | | Custom JWT claim name for groups (oidc, default `groups`) |
| `--auth-aws-region` | | | AWS region for `aws_sigv4` (e.g. `us-east-1`) |
| `--auth-aws-audience` | | | Informational audience for `aws_sigv4` |
| `--auth-aws-allowed-principal` | | | Allowed principal glob for `aws_sigv4` (repeatable) |
| `--auth-aws-allowed-account` | | | Allowed AWS account ID for `aws_sigv4` (repeatable, 12-digit) |
| `--auth-aws-cache-ttl` | | `60s` | `aws_sigv4` identity cache TTL |
| `--auth-gcp-iap-audience` | | | Backend service ID for `gcp_iap` |
| `--auth-azure-tenant` | | | Entra tenant GUID for `azure_ad` |
| `--auth-azure-audience` | | | Audience (Application ID URI) for `azure_ad` |
| `--auth-azure-multi-tenant` | | `false` | Accept tokens from any Entra tenant |
| `--auth-azure-allowed-tenant` | | | Allowed Entra tenant GUID for multi-tenant `azure_ad` (repeatable) |
| `--auth-azure-groups-mode` | | `claim` | `azure_ad` groups mode: `claim` or `graph` |

### Generated Files

`forge init` generates these key files:

| File | Purpose |
|------|---------|
| `forge.yaml` | Agent configuration |
| `guardrails.json` | Guardrail policy config (PII, security, secret patterns, gate config) |
| `SKILL.md` | Agent skill definition |
| `.env` | Environment variables |
| `.gitignore` | Includes `guardrails.json`, `.env`, `.forge/` |

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

# AWS IAM auth (any caller in account 412664885516)
forge init my-agent \
  --model-provider ollama \
  --auth=aws_sigv4 \
  --auth-aws-region=us-east-1 \
  --auth-aws-allowed-account=412664885516 \
  --non-interactive

# Azure AD multi-tenant with explicit partner allowlist
forge init my-agent \
  --model-provider ollama \
  --auth=azure_ad \
  --auth-azure-audience=api://forge \
  --auth-azure-multi-tenant \
  --auth-azure-allowed-tenant=00000000-1111-... \
  --auth-azure-allowed-tenant=55555555-6666-... \
  --non-interactive
```

See [Authentication](../security/authentication.md) for the full auth provider reference.

---

## `forge try`

Talk to a working demo agent in your terminal in under 60 seconds — no build, no cluster, no config. Scaffolds a keyless demo agent (the native forge LLM executor + the `weather` skill + `http_request`/`datetime_now`/`math_calculate` builtins) into a throwaway workspace, resolves whatever model credential is available, and drops you into a chat whose tool calls and egress checks render inline. See the [Quick Start](../getting-started/quick-start.md) for the full walkthrough.

```bash
# Interactive: resolves a credential, then chat
forge try

# One-shot (CI / docs / a quick check)
forge try --once "what's 17% of 4,200?"

# Keep the demo agent to make it your own (writes ./forge-quickstart)
forge try --keep
```

| Flag | Description |
|------|-------------|
| `--provider` | Model provider: `openai`, `anthropic`, `gemini`, or `ollama`. Skips auto-resolution. |
| `--model` | Model name (defaults to the provider's default). |
| `--once <prompt>` | Run a single prompt non-interactively, then exit. |
| `--keep` | Write the demo agent to `./forge-quickstart` instead of an auto-cleaned temp dir. |
| `--quiet` | Hide the inline tool/egress loop lines. |
| `--audit` | Show the full NDJSON audit event stream instead of the compact summary. |

**Credential resolution order:** explicit `--provider`/`--model` → env key (`ANTHROPIC_API_KEY` → `OPENAI_API_KEY` → `GEMINI_API_KEY`) → saved OpenAI OAuth session → local [Ollama](https://ollama.com) daemon → interactive picker (sign in with OpenAI, paste a key, or use Ollama). Nothing is written to disk unless you pass `--keep`; a pasted key is held in memory only. Use [`forge auth logout`](#forge-auth) to clear a saved OAuth session and re-show the picker.

The demo runs the same runtime as `forge run` (tool registry, egress enforcement, audit + progress hooks) in-process — no A2A server, daemon, or port binding. Exit with `/exit`, Ctrl-D, or Ctrl-C.

---

## `forge compression`

Inspect context compression state.

### `forge compression suggestions`

Shows `keep_patterns` candidates mined from `context_expand` retrievals (the [learning loop](../core-concepts/context-compression.md#the-learning-loop)), with a paste-ready `compression.keep_patterns` block for entries that crossed the suggestion threshold.

## `forge build`

Build the agent container artifact. Runs the full 8-stage build pipeline.

```
forge build [flags]
```

Uses global `--config` and `--output-dir` flags. Output is written to `.forge-output/` by default.

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--signing-key` | | Path to Ed25519 private key for signing build output |
| `--slim` | `false` | Minimize image size (skip heavy/optional binaries) |
| `--alpine` | `false` | Prefer Alpine base image |
| `--local-bin` | | Local binary override as `name=/path/to/file` (repeatable). See [Binary Dependencies § Use a local binary file](../core-concepts/binary-dependencies.md#4-use-a-local-binary-file-dev--testing--air-gap). |
| `--policy` | | Path to a YAML `SecurityPolicy` file for the build's `security-analysis` stage (overrides `forge.yaml security.policy_path` and the builtin `DefaultPolicy`). Same schema as `forge skills audit --policy`. See [Skills CLI / Security Audit](../skills/skills-cli.md#security-audit). |

### Examples

```bash
# Build with default config
forge build

# Build with custom config and output
forge build --config agent.yaml --output-dir ./build

# Build with a local binary override
forge build --local-bin forge=/path/to/linux/forge

# Build with Alpine base and slim image
forge build --alpine --slim

# Build with a custom security policy (e.g. acknowledged internal egress domains)
forge build --policy ./security-policy.yaml
```

When the `security-analysis` stage fails the policy check, per-skill rule + message detail is printed to stderr along with the path to `compiled/security-audit.json` (the full risk-factor breakdown) and a hint about overriding the active policy. The legacy "2 error(s)" summary is preserved in the returned error for programmatic consumers.

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
| `--compression` | | Enable reversible context compression; `--compression=false` forces it off. Absent = forge.yaml/env decide (sets `FORGE_COMPRESSION`). See [Context Compression](../core-concepts/context-compression.md) |
| `--env` | `.env` | Path to .env file |
| `--with` | | Comma-separated channel adapters (e.g., `slack,telegram`) |
| `--auth-url` | | External auth provider URL for token validation |
| `--cors-origins` | localhost | Comma-separated CORS allowed origins (e.g., `https://app.example.com,https://admin.example.com`). Use `*` to allow all origins |
| `--otel-enabled` | `false` | Enable OTLP tracing export. Falls back to `OTEL_SDK_DISABLED` env and `observability.tracing.enabled` in forge.yaml. See [Observability — Tracing](../core-concepts/observability-tracing.md). |
| `--otel-endpoint` | | OTLP target URL. Falls back to `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` / `OTEL_EXPORTER_OTLP_ENDPOINT`. |
| `--otel-protocol` | `http/protobuf` | OTLP protocol: `http/protobuf` or `grpc`. HTTP is recommended (the egress enforcer can wrap it). |
| `--otel-sampler` | `parentbased_always_on` | Standard `OTEL_TRACES_SAMPLER` name. |
| `--otel-sampler-ratio` | `1.0` | Ratio for `*traceidratio*` samplers (0.0–1.0). |
| `--otel-timeout` | `10s` | Per-request exporter timeout. |
| `--otel-service-name` | `agent_id` | OTel `service.name` resource attribute. |
| `--otel-capture-content` | `false` | Reserved — enterprise opt-in for prompt/completion content on spans. Phase 3 ships metadata-only. |
| `--otel-redact` | `true` | PII redaction posture flag. |

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

# Run with OpenTelemetry tracing enabled (export to local collector)
forge run --otel-enabled \
  --otel-endpoint http://localhost:4318/v1/traces \
  --otel-sampler always_on

# Same, but service name + protocol override
forge run --otel-enabled \
  --otel-endpoint otel.example.com:4317 \
  --otel-protocol grpc \
  --otel-service-name my-agent-staging
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
| `--compression` | | Enable reversible context compression; `--compression=false` forces it off. Forwarded to the daemon `forge run` only when explicitly passed |

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
| `--slim` | `false` | Minimize image size (skip heavy/optional binaries) |
| `--alpine` | `false` | Prefer Alpine base image |
| `--local-bin` | | Local binary override as `name=/path/to/file` (repeatable). See [Binary Dependencies § Use a local binary file](../core-concepts/binary-dependencies.md#4-use-a-local-binary-file-dev--testing--air-gap). |

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

# Package with a local binary override
forge package --local-bin forge=/path/to/linux/forge

# Package with slim Alpine image
forge package --alpine --slim
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

## `forge auth`

Manage the runtime bearer token Forge mints at agent startup (issue #162 part 1, PR #168). The token is stored at `<agent-root>/.forge/runtime.token` (0600 permissions) and is the same token channel adapters use to call back into the A2A endpoint. Scheduled CronJobs deployed via `forge package` also consume this token through a Kubernetes Secret the operator populates out-of-band — `forge package` never bakes the token into the generated manifests.

```bash
# Print the stored token to stdout. Exits 1 with an actionable error
# when the file is absent.
forge auth show-token

# Generate a fresh 256-bit token, store it (overwriting any existing
# value), and print to stdout. Use for first-deploy bootstrap from a
# clean checkout.
forge auth mint-token

# Print a ready-to-apply Kubernetes Secret YAML containing the token.
# Default name and namespace match what `forge package` emits.
forge auth secret-yaml
forge auth secret-yaml --namespace prod
forge auth secret-yaml --name custom-secret-name

# Common one-liner: populate the Secret a `forge package` deploy
# expects from the local runtime.token.
forge auth secret-yaml | kubectl apply -f -

# Remove a stored LLM OAuth credential (default: openai) so the next
# `forge init` / `forge try` prompts you to sign in again.
forge auth logout
forge auth logout openai
```

The `forge.agent.id` label on the generated Secret is always sourced from `forge.yaml`'s `agent_id` (or the `"forge-agent"` fallback), never from the `--name` override — so operators using `--name` to match an existing cluster convention still see telemetry and label-selectors keyed on the real agent ID.

`forge auth logout` is an operator/laptop command: it deletes the OAuth credential from `~/.forge/credentials` and the encrypted store, and **refuses to run inside an agent runtime** — a container, or when `FORGE_PLATFORM_TOKEN` is set. A deployed agent authenticates with an injected API key or platform token, not the OAuth credential store, so there is nothing there for the runtime to log out of; the refusal is defense-in-depth so Forge is never the tool an agent shells out to in order to wipe an operator's credential.

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
# Add a skill from the registry (prompts for env vars, merges egress domains)
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

See [Dashboard](web-dashboard.md) for full documentation.
