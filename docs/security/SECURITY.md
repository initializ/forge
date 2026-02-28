# Security

Forge is designed with security as a foundational principle, not an afterthought. This document describes the complete security architecture — from network-level egress controls to encrypted secrets, build signing, execution sandboxing, and runtime guardrails.

## Security Model

Forge's security is organized in layers, each addressing a different threat surface:

```
┌──────────────────────────────────────────────────────────────┐
│                       Guardrails                             │
│              (content filtering, PII, jailbreak)             │
├──────────────────────────────────────────────────────────────┤
│                    Egress Enforcement                        │
│       (EgressEnforcer + EgressProxy + NetworkPolicy)         │
├──────────────────────────────────────────────────────────────┤
│                  Execution Sandboxing                        │
│    (env isolation, binary allowlists, arg validation)        │
├──────────────────────────────────────────────────────────────┤
│                   Secrets Management                         │
│         (AES-256-GCM, Argon2id, per-agent isolation)         │
├──────────────────────────────────────────────────────────────┤
│                   Build Integrity                            │
│           (Ed25519 signing, SHA-256 checksums)               │
├──────────────────────────────────────────────────────────────┤
│                   Network Posture                            │
│       (outbound-only connections, no public listeners)       │
└──────────────────────────────────────────────────────────────┘
```

## Table of Contents

- [Network Posture](#network-posture)
- [Egress Enforcement](#egress-enforcement)
- [Execution Sandboxing](#execution-sandboxing)
- [Secrets Management](#secrets-management)
- [Build Integrity](#build-integrity)
- [Guardrails](#guardrails)
- [Audit Logging](#audit-logging)
- [Container Security](#container-security)
- [Related Documentation](#related-documentation)

---

## Network Posture

Forge agents are designed to never expose inbound listeners to the public internet:

- **No public tunnels** — Forge does not create ngrok, Cloudflare, or similar tunnels
- **No inbound webhooks** — Channels use outbound-only connections
  - Slack: Socket Mode (outbound WebSocket via `apps.connections.open`)
  - Telegram: Long-polling via `getUpdates`
- **Local-only HTTP server** — The A2A dev server binds to `localhost` by default
- **No hidden listeners** — Every network binding is explicit and logged

This means a running Forge agent has zero inbound attack surface by default.

---

## Egress Enforcement

Forge restricts outbound network access at three levels:

### 1. In-Process Enforcer

The `EgressEnforcer` is a Go `http.RoundTripper` that wraps every outbound HTTP request from in-process tools (`http_request`, `web_search`, LLM API calls). It validates the destination domain against a resolved allowlist before forwarding.

### 2. Subprocess Proxy

Skill scripts and `cli_execute` subprocesses bypass Go-level enforcement. A local `EgressProxy` on `127.0.0.1:<random-port>` validates domains for subprocess HTTP traffic via `HTTP_PROXY`/`HTTPS_PROXY` env var injection.

### 3. Kubernetes NetworkPolicy

In containerized deployments, generated Kubernetes `NetworkPolicy` manifests enforce egress at the pod level, restricting traffic to allowed domains on ports 80/443.

### Modes

| Mode | Behavior |
|------|----------|
| `deny-all` | All non-localhost outbound traffic blocked |
| `allowlist` | Only explicitly allowed domains (exact + wildcard) |
| `dev-open` | All traffic allowed (development only) |

### Domain Resolution

Allowed domains are resolved from three sources:
1. **Explicit domains** — Listed in `forge.yaml` under `egress.allowed_domains`
2. **Tool domains** — Automatically inferred from registered tool names (e.g., `web_search` → `api.tavily.com`)
3. **Capability bundles** — Pre-defined domain sets for common services (e.g., `slack` → `slack.com`, `hooks.slack.com`, `api.slack.com`)

Localhost (`127.0.0.1`, `::1`, `localhost`) is always allowed in all modes.

For full details on egress enforcement, see **[Egress Security](egress.md)**.

---

## Execution Sandboxing

Forge agents execute external code through two sandboxed executors, both designed to minimize the attack surface of subprocess execution.

### SkillCommandExecutor

Skill scripts run via `SkillCommandExecutor` (`forge-cli/tools/exec.go`):

| Control | Detail |
|---------|--------|
| **Environment isolation** | Only `PATH`, `HOME`, and explicitly declared env vars are passed through |
| **Egress proxy injection** | `HTTP_PROXY`/`HTTPS_PROXY` env vars route subprocess HTTP through the egress proxy |
| **Configurable timeout** | Per-skill `timeout_hint` in YAML frontmatter (default: 120s) |
| **No shell** | Runs `bash <script> <json-input>`, not through a shell interpreter |
| **Scoped env vars** | Only env vars declared in the skill's `requires.env` section are passed |

### CLIExecuteTool

The `cli_execute` tool (`forge-cli/tools/cli_execute.go`) provides 7 security layers:

| # | Layer | Detail |
|---|-------|--------|
| 1 | **Binary allowlist** | Only pre-approved binaries can execute |
| 2 | **Binary resolution** | Binaries are resolved to absolute paths via `exec.LookPath` at startup |
| 3 | **Argument validation** | Rejects arguments containing `$(`, backticks, or newlines |
| 4 | **Timeout** | Configurable per-command timeout (default: 120s) |
| 5 | **No shell** | Uses `exec.CommandContext` directly — no shell expansion |
| 6 | **Environment isolation** | Only `PATH`, `HOME`, `LANG`, explicit passthrough vars, and proxy vars |
| 7 | **Output limits** | Configurable max output size (default: 1MB) to prevent memory exhaustion |

### Configuration

```yaml
tools:
  - name: cli_execute
    config:
      allowed_binaries: ["git", "curl", "jq", "python3"]
      env_passthrough: ["GITHUB_TOKEN"]
      timeout: 120
      max_output_bytes: 1048576
```

---

## Secrets Management

Forge provides encrypted secret storage with per-agent isolation and defense-in-depth.

### Encryption

- **Algorithm**: AES-256-GCM (authenticated encryption)
- **Key derivation**: Argon2id (memory-hard, resistant to GPU attacks)
- **File format**: `salt(16 bytes) || nonce(12 bytes) || ciphertext`
- **Plaintext format**: JSON key-value map

### Storage Hierarchy

Secrets are resolved in order, with earlier sources taking priority:

1. **Agent-local** — `<agent-dir>/.forge/secrets.enc`
2. **Global** — `~/.forge/secrets.enc`
3. **Environment variables** — `os.Getenv()`

This enables per-agent key isolation: different agents can use different API keys even on the same machine.

### Passphrase Handling

| Context | Behavior |
|---------|----------|
| `forge run` (TTY) | Prompts interactively if `FORGE_PASSPHRASE` not set |
| `forge run` (CI/CD) | Reads from `FORGE_PASSPHRASE` environment variable |
| `forge init` (first time) | Prompts for passphrase + confirmation |
| `forge init` (subsequent) | Prompts once and validates against existing file |

### File Safety

- `.forge/` directories are automatically added to `.gitignore`
- `*.enc` files are excluded in `.dockerignore`
- Secret files never appear in container images

### Commands

```bash
forge secret set OPENAI_API_KEY              # Prompts for value securely
forge secret set SLACK_BOT_TOKEN xoxb-...    # Inline value
forge secret get OPENAI_API_KEY              # Shows value and source
forge secret list                            # Lists all keys
forge secret delete OLD_KEY                  # Removes a key
forge secret set API_KEY --local             # Agent-local secret
```

### Configuration

```yaml
secrets:
  providers:
    - encrypted-file    # AES-256-GCM encrypted file
    - env               # Environment variables (fallback)
```

---

## Build Integrity

Forge supports Ed25519 signing of build artifacts for supply chain integrity verification.

### Signing Flow

1. `forge build` computes SHA-256 checksums of all generated artifacts
2. If a signing key exists at `~/.forge/signing-key.pem`, the checksums are signed with Ed25519
3. `checksums.json` is written with checksums, signature, and key ID

### Verification Flow

At runtime, `forge run` optionally verifies build artifacts:
1. Validates SHA-256 checksums of all files against `checksums.json`
2. Verifies the Ed25519 signature against trusted keys in `~/.forge/trusted-keys/`
3. If `checksums.json` doesn't exist, verification is skipped (opt-in)

### Key Management

```bash
forge key generate                     # Generate Ed25519 keypair
forge key generate --name ci-key       # Named keypair
forge key trust ~/.forge/signing-key.pub   # Add to trusted keyring
forge key list                         # List signing + trusted keys
```

### Production Build Safety

The build pipeline includes a `secret-safety` stage that:
- Blocks production builds (`--prod`) that only use `encrypted-file` without `env` provider
- Warns if `.dockerignore` is missing alongside a generated Dockerfile
- Rejects `dev-open` egress mode in production builds
- Filters out dev-only tools (`local_shell`, `local_file_browser`)

---

## Guardrails

The guardrail engine checks inbound and outbound messages against configurable policy rules.

### Built-in Guardrails

| Guardrail | Direction | Description |
|-----------|-----------|-------------|
| `content_filter` | Inbound + Outbound | Blocks messages containing configured blocked words |
| `no_pii` | Outbound | Detects email addresses, phone numbers, and SSNs via regex |
| `jailbreak_protection` | Inbound | Detects common jailbreak phrases ("ignore previous instructions", etc.) |

### Modes

| Mode | Behavior |
|------|----------|
| `enforce` | Blocks violating messages, returns error to caller |
| `warn` | Logs violation, allows message to pass |

### Configuration

Guardrails are defined in the policy scaffold, loaded from `policy-scaffold.json` or generated during `forge build`.

### Runtime

```bash
# Run with guardrails enforced
forge run --enforce-guardrails

# Default: warn mode (log only)
forge run
```

---

## Audit Logging

All runtime security events are emitted as structured NDJSON to stderr with correlation IDs for end-to-end tracing.

### Event Types

| Event | Description |
|-------|-------------|
| `session_start` | New task session begins |
| `session_end` | Task session completes (with final state) |
| `tool_exec` | Tool execution start/end (with tool name) |
| `egress_allowed` | Outbound request allowed (with domain, mode) |
| `egress_blocked` | Outbound request blocked (with domain, mode) |
| `llm_call` | LLM API call completed (with token count) |
| `guardrail_check` | Guardrail evaluation result |

### Example

```json
{"ts":"2026-02-28T10:00:00Z","event":"session_start","correlation_id":"a1b2c3d4","task_id":"task-1"}
{"ts":"2026-02-28T10:00:01Z","event":"tool_exec","correlation_id":"a1b2c3d4","fields":{"tool":"tavily_research","phase":"start"}}
{"ts":"2026-02-28T10:00:01Z","event":"egress_allowed","correlation_id":"a1b2c3d4","fields":{"domain":"api.tavily.com","mode":"allowlist","source":"proxy"}}
{"ts":"2026-02-28T10:00:05Z","event":"tool_exec","correlation_id":"a1b2c3d4","fields":{"tool":"tavily_research","phase":"end"}}
{"ts":"2026-02-28T10:00:06Z","event":"session_end","correlation_id":"a1b2c3d4","fields":{"state":"completed"}}
```

The `source` field distinguishes in-process enforcer events from subprocess proxy events.

---

## Container Security

### Build-Time Artifacts

Every `forge build` generates container-ready security artifacts:

| Artifact | Purpose |
|----------|---------|
| `egress_allowlist.json` | Machine-readable domain allowlist |
| `network-policy.yaml` | Kubernetes NetworkPolicy restricting pod egress |
| `Dockerfile` | Container image with minimal attack surface |
| `checksums.json` | SHA-256 checksums + Ed25519 signature |

### Runtime Behavior in Containers

When Forge detects it's running inside a container (via `KUBERNETES_SERVICE_HOST` or `/.dockerenv`):

- The local `EgressProxy` is **not started** — `NetworkPolicy` handles egress enforcement at the infrastructure level
- All other security controls (guardrails, execution sandboxing, audit logging) remain active
- Secrets must use the `env` provider (encrypted files can't be decrypted without a passphrase)

### Production Build Checks

```bash
forge package --prod
```

Production builds enforce:
- No `dev-open` egress mode
- No dev-only tools (`local_shell`, `local_file_browser`)
- Secret provider chain must include `env` (not just `encrypted-file`)
- `.dockerignore` must exist if a Dockerfile is generated

---

## Related Documentation

| Document | Description |
|----------|-------------|
| [Egress Security](egress.md) | Deep dive into egress enforcement: profiles, modes, domain matching, proxy architecture, NetworkPolicy |
| [Architecture](../architecture.md) | System design, module layout, and data flows |
| [Tools](../tools.md) | Tool system including `cli_execute` security layers |
| [Skills](../skills.md) | Skill definitions and runtime execution |
| [Commands](../commands.md) | CLI reference including security-related flags |
