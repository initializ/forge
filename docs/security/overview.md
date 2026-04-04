# Security

Forge is designed with security as a foundational principle, not an afterthought. This document describes the complete security architecture — from network-level egress controls to encrypted secrets, build signing, execution sandboxing, and runtime guardrails.

## Security Model

Forge's security is organized in layers, each addressing a different threat surface:

```
┌──────────────────────────────────────────────────────────────┐
│                    Skill Guardrails                           │
│    (deny commands/output/prompts/responses per skill)         │
├──────────────────────────────────────────────────────────────┤
│                    Global Guardrails                          │
│              (content filtering, PII, jailbreak)             │
├──────────────────────────────────────────────────────────────┤
│                    Egress Enforcement                        │
│  (EgressEnforcer + EgressProxy + SafeDialer + NetworkPolicy) │
├──────────────────────────────────────────────────────────────┤
│                  Execution Sandboxing                        │
│  (env isolation, binary allowlists, arg validation,          │
│   file:// blocking, shell denylist)                          │
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
  - Telegram: Long-polling via `getUpdates`; webhook mode binds to `127.0.0.1` only
- **Local-only HTTP server** — The A2A dev server binds to `localhost` by default
- **Rate limiting** — Per-IP token bucket rate limiting on the A2A server (read: 60 req/min, write: 10 req/min) with automatic 429 responses and `Retry-After` headers
- **Request size limits** — A2A server enforces `MaxHeaderBytes` (1 MiB) and request body limits (2 MiB via `http.MaxBytesReader`) to prevent denial-of-service via oversized payloads
- **CORS restriction** — The A2A server restricts `Access-Control-Allow-Origin` to localhost by default; configurable via `--cors-origins` flag, `FORGE_CORS_ORIGINS` env var, or `cors_origins` in `forge.yaml`
- **Security response headers** — All A2A responses include `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, `X-Frame-Options: DENY`, and `Content-Security-Policy: default-src 'none'`
- **No hidden listeners** — Every network binding is explicit and logged

This means a running Forge agent has zero inbound attack surface by default.

---

## Egress Enforcement

Forge restricts outbound network access at multiple levels:

### 1. IP Validation

All egress paths reject non-standard IP formats (octal, hex, packed decimal, leading zeros) that could bypass allowlist checks. IPv6 transition addresses (NAT64, 6to4, Teredo) embedding private IPv4 addresses are also blocked.

### 2. In-Process Enforcer

The `EgressEnforcer` is a Go `http.RoundTripper` backed by a `SafeTransport` that validates resolved IPs post-DNS. Every outbound HTTP request from in-process tools (`http_request`, `web_search`, LLM API calls) is checked against IP validation, domain allowlist, and post-resolution CIDR blocking.

### 3. Subprocess Proxy

Skill scripts and `cli_execute` subprocesses bypass Go-level enforcement. A local `EgressProxy` on `127.0.0.1:<random-port>` validates domains and resolved IPs for subprocess HTTP traffic via `HTTP_PROXY`/`HTTPS_PROXY` env var injection.

### 4. Redirect Credential Stripping

HTTP clients used by `http_request` and `webhook_call` tools strip `Authorization`, `Cookie`, and `Proxy-Authorization` headers when a redirect crosses origin boundaries (different scheme, host, or port).

### 5. Kubernetes NetworkPolicy

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
| **OAuth token resolution** | When `OPENAI_API_KEY` is set to the sentinel `__oauth__`, the executor resolves OAuth credentials and injects the access token and `OPENAI_BASE_URL` |
| **Model passthrough** | The configured LLM model name is injected as `REVIEW_MODEL` so skill scripts use the correct model |
| **Configurable timeout** | Per-skill `timeout_hint` in YAML frontmatter (default: 120s) |
| **No shell** | Runs `bash <script> <json-input>`, not through a shell interpreter |
| **Scoped env vars** | Only env vars declared in the skill's `requires.env` section are passed |

### CLIExecuteTool

The `cli_execute` tool (`forge-cli/tools/cli_execute.go`) provides 13 security layers:

| # | Layer | Detail |
|---|-------|--------|
| 1 | **Shell denylist** | Shell interpreters (`bash`, `sh`, `zsh`, etc.) filtered at construction and blocked at execution |
| 2 | **Binary allowlist** | Only pre-approved binaries can execute |
| 3 | **Binary resolution** | Binaries are resolved to absolute paths via `exec.LookPath` at startup |
| 4 | **Argument validation** | Rejects arguments containing `$(`, backticks, newlines, or `file://` URLs |
| 5 | **File protocol blocking** | Blocks `file://` URLs (case-insensitive) to prevent filesystem traversal |
| 6 | **Path confinement** | Path arguments inside `$HOME` but outside `workDir` are blocked |
| 7 | **Timeout** | Configurable per-command timeout (default: 120s) |
| 8 | **No shell** | Uses `exec.CommandContext` directly — no shell expansion |
| 9 | **Working directory** | `cmd.Dir` set to `workDir` for relative path resolution |
| 10 | **Environment isolation** | Only `PATH`, `HOME`, `LANG`, explicit passthrough vars, proxy vars, `GH_CONFIG_DIR` (auto-set **only for `gh`**), and `KUBECONFIG`/`NO_PROXY` (**only for `kubectl`/`helm`** — restores kubeconfig access and bypasses egress proxy for the K8s API server when HOME is overridden) |
| 11 | **Output limits** | Configurable max output size (default: 1MB) to prevent memory exhaustion |
| 12 | **Skill guardrails** | Skill-declared `deny_commands` and `deny_output` patterns via hooks |
| 13 | **Custom tool entrypoint validation** | Custom tool entrypoints are validated against path traversal, symlink escape, absolute paths, and non-regular files |

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

Forge provides AES-256-GCM encrypted secret storage with Argon2id key derivation, per-agent isolation, and a three-tier resolution hierarchy (agent-local -> global -> environment). Secrets are managed via `forge secret set|get|list|delete`.

### Cross-Category Secret Reuse Detection

At startup, the runtime detects when the same secret value is shared across different purpose categories (e.g., `OPENAI_API_KEY` and `TELEGRAM_BOT_TOKEN` having the same value). This prevents credential reuse mistakes that could escalate the impact of a single token compromise. Categories: `llm`, `search`, `telegram`, `slack`.

For full details, see **[Secrets Management](secrets.md)**.

---

## Build Integrity

Forge supports Ed25519 signing and SHA-256 checksumming of build artifacts for supply chain integrity. At runtime, `forge run` can verify artifacts against trusted keys before execution.

For full details, see **[Build Signing & Verification](signing.md)**.

---

## Guardrails

The guardrail engine checks inbound and outbound messages against policy rules including content filtering, PII detection, and jailbreak protection. Guardrails run in `enforce` (blocking) or `warn` (logging) mode.

### Skill Guardrails

Skills can declare domain-specific guardrails in their `SKILL.md` frontmatter. These guardrails operate at four hook points — blocking unauthorized commands (`deny_commands`), redacting sensitive output (`deny_output`), intercepting capability enumeration probes (`deny_prompts`), and replacing binary-enumerating LLM responses (`deny_responses`). Skill guardrails fire at runtime without requiring `forge build`.

For full details, see **[Content Guardrails](guardrails.md)**.

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
| [Egress Security](egress.md) | Deep dive into egress enforcement: IP validation, SafeDialer, profiles, modes, domain matching, proxy architecture, NetworkPolicy |
| [Secrets Management](secrets.md) | Encrypted storage, per-agent secrets, passphrase handling |
| [Build Signing & Verification](signing.md) | Key management, build signing, runtime verification |
| [Content Guardrails](guardrails.md) | PII detection, jailbreak protection, custom rules |
| [Architecture](../architecture.md) | System design, module layout, and data flows |
| [Tools](../tools.md) | Tool system including `cli_execute` security layers |
| [Skills](../skills.md) | Skill definitions and runtime execution |
| [Commands](../commands.md) | CLI reference including security-related flags |

---
← [Channels](../channels.md) | [Back to README](../../README.md) | [Egress Security](egress.md) →
