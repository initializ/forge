---
title: "forge.yaml Schema"
description: "Complete YAML schema reference for Forge agent configuration."
order: 2
---

All Forge agent configuration lives in `forge.yaml` at the project root.

## Full Schema

```yaml
agent_id: "my-agent"                # Required
version: "1.0.0"                    # Required
framework: "forge"                  # forge (default), crewai, langchain
registry: "ghcr.io/org"             # Container registry
entrypoint: "agent.py"              # Required for crewai/langchain, omit for forge

model:
  provider: "openai"                # openai, anthropic, gemini, ollama
  name: "gpt-4o"                    # Model name
  base_url: ""                      # Override the provider's default API host (issue #139)
  organization_id: "org-xxx"        # OpenAI Organization ID (enterprise, optional)
  auth_scheme: ""                   # "" (default) / "x_api_key" / "bearer" / "aws_sigv4" — issue #202
  aws_region: ""                    # Required when auth_scheme: aws_sigv4 — issue #202
  fallbacks:                        # Fallback providers (optional)
    - provider: "anthropic"
      name: "claude-sonnet-4-20250514"
      organization_id: ""           # Per-fallback org ID override (optional)

# Custom URL endpoints (OpenRouter, vLLM, litellm, self-hosted Kimi/Llama,
# Together.ai, Anyscale, Bedrock OpenAI compat, …):
#   provider: "openai" + OPENAI_BASE_URL env  → OpenAI Chat Completions wire format
#   provider: "anthropic" + ANTHROPIC_BASE_URL env → Anthropic Messages wire format
# The forge init wizard's "Custom" option asks which wire format the URL
# speaks and writes the matching provider; generated forge.yaml never
# carries provider: "custom". Issue #202 Phase 1.

# AWS Bedrock with native API key auth is not supported (Bedrock uses
# SigV4 signing). Set auth_scheme: aws_sigv4 + aws_region to use AWS
# credentials (AWS_ACCESS_KEY_ID / _SECRET_ACCESS_KEY / _SESSION_TOKEN
# env) for outbound LLM calls — works against any SigV4-fronted endpoint
# that speaks OpenAI or Anthropic wire format. Issue #202 Phase 2.

tools:
  - name: "web_search"
  - name: "cli_execute"
    config:
      allowed_binaries: ["git", "curl"]
      env_passthrough: ["GITHUB_TOKEN"]

channels:
  - "telegram"
  - "slack"

egress:
  profile: "strict"                 # strict, standard, permissive
  mode: "allowlist"                 # deny-all, allowlist, dev-open
  allowed_domains:                  # Explicit domains
    - "api.example.com"
    - "*.github.com"
  capabilities:                     # Capability bundles
    - "slack"
  allow_private_ips: false          # Allow RFC 1918 IPs (auto: true in containers)

cors_origins:                       # CORS allowed origins for A2A server
  - "https://app.example.com"      # (default: localhost variants)

workflow_propagation:                # Auto-propagate X-Workflow-* / X-Invocation-Caller
  allowed_hosts:                     # headers on outbound HTTP tool calls to these hosts
    - "orchestrator.svc"             # (FORGE-1 / issue #186; opt-in only by default).
    - "*.agents.internal"

server:                              # A2A server tuning (optional)
  rate_limit:                        # per-IP rate limits (issue #110 / FWS-10)
    read_rps: 1.0                    # GET/HEAD/OPTIONS req/sec (default 1.0 = 60/min)
    read_burst: 10                   # GET/HEAD/OPTIONS burst (default 10)
    write_rps: 1.0                   # POST/PUT/DELETE req/sec (default 1.0 = 60/min)
    write_burst: 20                  # POST/PUT/DELETE burst (default 20)
    cancel_exempt: true              # tasks/cancel skips the write bucket (default true)

package:
  alpine: false                     # Prefer Alpine base image
  slim: false                       # Minimize image size
  bin_overrides:                    # Per-binary install overrides. See
                                    # ../core-concepts/binary-dependencies.md
                                    # for the resolution pipeline and source priority.
    forge:
      local: "/path/to/linux/forge" # Host path to local binary file
    jq:
      apt: "jq"                     # APT package name
    custom-tool:
      url: "https://example.com/tool.tar.gz"  # Direct download URL
      dest: "/usr/local/bin/custom-tool"       # Install destination
      chmod: "0755"                            # File permissions

auth:                               # a2a HTTP-server auth chain (optional)
  required: true                    # 401 every unauthenticated request
  providers:                        # ordered; first match wins (fail-closed on rejection)
    - type: "static_token"          # local dev / shared-secret
      settings:
        token_env: "FORGE_AUTH_TOKEN"  # env var name (preferred over literal `token:`)
    - type: "oidc"                  # any IdP with OIDC discovery
      settings:
        issuer:   "https://login.example.com/auth/realms/forge"
        audience: "api://forge"
        client_id: ""               # optional azp fallback
        jwks_url: ""                # overrides discovery
        jwks_cache_ttl: "1h"
        clock_skew: "30s"
        claim_map: {groups: "roles"}
    - type: "http_verifier"         # legacy external /verify endpoint
      settings:
        url:         "https://auth.example.com/verify"
        default_org: "acme"
        timeout:     "10s"
    - type: "aws_sigv4"             # Phase 2: AWS IAM via pre-signed STS URL
      settings:
        region:    "us-east-1"      # required
        audience:  "api://forge"    # informational, emitted in audit Claims
        allowed_accounts:           # ergonomic: "anyone in these AWS accounts"
          - "412664885516"
        allowed_principals:         # explicit globs (path.Match)
          - "arn:aws:sts::412664885516:assumed-role/ci-deploy/*"
        identity_cache_ttl: "60s"
        max_token_expires:  "15m"   # caps caller's X-Amz-Expires claim
        clock_skew:         "5m"
    - type: "gcp_iap"               # Phase 2: GCP IAP-fronted Forge
      settings:
        audience: "/projects/PNUM/global/backendServices/BACKEND_ID"
        jwks_refresh_ttl: "1h"
    - type: "azure_ad"              # Phase 2: Microsoft Entra ID
      settings:
        tenant_id: "00000000-1111-..."   # required unless allow_multi_tenant
        audience:  "api://forge"
        allow_multi_tenant: false
        allowed_tenants:                  # required when multi-tenant + want allowlist
          - "55555555-6666-..."
        groups_mode:   "claim"            # "claim" | "graph"
        graph_timeout: "5s"

secrets:
  providers:                        # Secret providers (order matters)
    - "encrypted-file"              # AES-256-GCM encrypted file
    - "env"                         # Environment variables

memory:
  persistence: true                 # Session persistence (default: true)
  sessions_dir: ".forge/sessions"
  char_budget: 200000               # Context budget override
  trigger_ratio: 0.6                # Compaction trigger ratio
  long_term: false                  # Long-term memory (default: false)
  memory_dir: ".forge/memory"
  embedding_provider: ""            # Auto-detect from LLM provider
  embedding_model: ""               # Provider default
  vector_weight: 0.7                # Hybrid search vector weight
  keyword_weight: 0.3               # Hybrid search keyword weight
  decay_half_life_days: 7           # Temporal decay half-life

guardrails_path: "guardrails.json"  # Path to guardrails config (default: "guardrails.json")

schedules:                          # Recurring scheduled tasks (optional)
  - id: "daily-report"
    cron: "@daily"
    task: "Generate daily status report"
    skill: ""                       # Optional skill to invoke
    channel: "telegram"             # Optional channel for delivery
    channel_target: "-100123456"    # Destination chat/channel ID

scheduler:                          # Scheduler backend selection (#162)
  backend: "auto"                   # auto (default) | file | kubernetes
  kubernetes:                       # Tuning for backend=kubernetes (or auto-resolved)
    namespace: ""                   # Defaults to the agent pod's own namespace
    service_url: ""                 # In-cluster URL CronJob trigger pods POST to
    allow_dynamic: false            # Whether schedule_set can create CronJobs at runtime
    trigger_image: ""               # Default: curlimages/curl:8.10.1
    auth_secret_name: ""            # Default: <agent_id>-internal-token

observability:                      # OpenTelemetry tracing (off by default)
  tracing:
    enabled: true                   # Phase 0-6 / OTel Tracing v1 (#108)
    endpoint: https://otel-collector.monitoring.svc.cluster.local:4318/v1/traces
    protocol: "http/protobuf"       # or "grpc"
    sampler: "parentbased_always_on"
    sampler_ratio: 1.0              # only for *traceidratio* samplers
    timeout: 10s                    # per-request exporter timeout
    service_name: ""                # defaults to agent_id
    headers:                        # OTLP request headers (auth tokens etc.)
      x-tenant: demo
    resource_attrs:                 # extra OTel resource attributes
      deployment.environment: prod
    redact: true                    # PII redaction posture flag
    capture_content: false          # reserved — Phase 3 ships metadata-only
```

## `server.rate_limit` — per-IP A2A rate limits (FWS-10)

Bounds the per-IP request rate on the A2A HTTP server. Defaults
(applied when the block is omitted) target orchestrated workloads —
60/min sustained on both read and write surfaces, with burst headroom
for parallel-task dispatch and cron bursts.

| Field | Default | Notes |
|---|---|---|
| `read_rps` | `1.0` | Sustained req/sec for `GET` / `HEAD` / `OPTIONS`. |
| `read_burst` | `10` | Burst headroom for reads. |
| `write_rps` | `1.0` | Sustained req/sec for `POST` / `PUT` / `DELETE`. Bumped from `10/60` in FWS-10 to absorb orchestrator dispatch. |
| `write_burst` | `20` | Burst headroom for writes. Bumped from `3` in FWS-10. |
| `cancel_exempt` | `true` | When true, `tasks/cancel` JSON-RPC calls skip the write bucket entirely. The cost-ceiling cancel-burst case (orchestrator firing N parallel cancels when a workflow budget trips) is the motivating example — sharing the write bucket with `tasks/send` would throttle cancels at exactly the moment cancellation matters most. DoS via cancel-spam is bounded by the cancellation registry's O(1) unknown-task lookup. |

### Resolution order

Per-field, in precedence:

1. **CLI flag** — `--rate-limit-read-rps`, `--rate-limit-read-burst`, `--rate-limit-write-rps`, `--rate-limit-write-burst`, `--rate-limit-cancel-exempt` (works on both `forge run` and `forge serve start`)
2. **Env var** — `FORGE_RATE_LIMIT_READ_RPS`, `FORGE_RATE_LIMIT_READ_BURST`, `FORGE_RATE_LIMIT_WRITE_RPS`, `FORGE_RATE_LIMIT_WRITE_BURST`, `FORGE_RATE_LIMIT_CANCEL_EXEMPT`
3. **`server.rate_limit:` block in `forge.yaml`**
4. **Built-in defaults** (the table above)

Each field falls through layer by layer — a CLI flag for `--rate-limit-write-rps` overrides only that one field; the others still come from env / yaml / default.

### Stricter than default (public-facing agent)

A typical config for a public-facing agent on the open internet:

```yaml
server:
  rate_limit:
    read_rps: 0.5     # 30/min reads
    read_burst: 5
    write_rps: 0.1    # 6/min writes — anonymous DoS protection
    write_burst: 3
    cancel_exempt: true  # keep cancel responsive even under attack
```

### Per-IP grouping limitation

The limiter keys on the remote IP. In Kubernetes, multiple orchestrator
pods behind a single service IP share one bucket. If that becomes a
practical problem, the right fix is auth-aware rate limiting (per-user
buckets keyed by `auth.user_id`) — out of scope for FWS-10; file
separately.

## `observability.tracing` — OpenTelemetry distributed tracing

Off by default. When enabled, Forge exports OTLP spans covering the
A2A dispatcher, the executor loop, every LLM completion, every tool
call, and every outbound HTTP request. See the dedicated
[Observability — Tracing](../core-concepts/observability-tracing.md)
doc for the full reference (span hierarchy, propagation, audit
cross-link, build-time egress).

| Field | Default | Notes |
|---|---|---|
| `enabled` | `false` | Off by default per the OTel v1 initiative ruling. |
| `endpoint` | — | Required when `enabled: true`. Empty endpoint collapses to "off." |
| `protocol` | `http/protobuf` | Or `grpc`. HTTP is recommended (egress enforcer wraps it). |
| `sampler` | `parentbased_always_on` | Standard `OTEL_TRACES_SAMPLER` name. See [tracing doc](../core-concepts/observability-tracing.md#samplers). |
| `sampler_ratio` | `1.0` | Used by `traceidratio` variants. |
| `timeout` | `10s` | Per-request exporter timeout. |
| `service_name` | `agent_id` | `OTEL_SERVICE_NAME` env wins if set. |
| `headers` | — | OTLP request headers; prefer env (`OTEL_EXPORTER_OTLP_HEADERS`) for secrets. |
| `resource_attrs` | — | Merged with the auto-stamped `service.*` + `forge.runtime.version`. |
| `redact` | `true` | PII redaction posture flag. |
| `capture_content` | `false` | Reserved — Phase 3 ships metadata-only spans. |

### Resolution order

Same pattern as `server.rate_limit`:

1. `--otel-*` CLI flags
2. `OTEL_*` env vars (standard SDK names)
3. `observability.tracing` block in this file
4. Built-in defaults (the table above)

A set-but-empty env var does NOT wipe a non-empty yaml field —
absence-of-value is "no override," not "unset."

### Egress auto-merge

`forge package` and `forge run` both extract the endpoint hostname and
auto-add it to `egress_allowlist.json`. No second egress edit needed.
Disabled tracing produces no entry — turning tracing off in yaml does
NOT leave a stale entry in the generated NetworkPolicy.

## `workflow_propagation` — auto-propagate workflow correlation headers (FORGE-1)

```yaml
workflow_propagation:
  allowed_hosts:
    - "orchestrator.svc"
    - "*.agents.internal"
```

| Field | Default | Notes |
|---|---|---|
| `allowed_hosts` | `[]` (opt-in only) | Hostnames whose outbound HTTP tool calls auto-receive the `X-Workflow-Id` / `X-Workflow-Execution-Id` / `X-Workflow-Stage-Id` / `X-Workflow-Step-Id` / `X-Invocation-Caller` headers from the current request context. Exact entries match a single host (port stripped before comparison); entries beginning with `*.` match any strictly-deeper subdomain. Empty list keeps the pre-#186 opt-in behavior — tools must call `WorkflowContext.ApplyToHTTPHeaders(req.Header)` explicitly. See [Workflow correlation IDs › Outbound propagation](../security/workflow-correlation.md#outbound-propagation-agent-to-agent). Issue #186 / FORGE-1. |

The matcher is consulted by a `RoundTripper` wrapper around the egress transport at runner startup, so every built-in HTTP tool (`http_request`, `webhook_call`, `web_search_*`) inherits the auto-apply without per-tool changes. Empty config = zero-overhead pass-through.

## `security` — build-time security knobs

```yaml
security:
  policy_path: ./security-policy.yaml
```

| Field | Default | Notes |
|---|---|---|
| `policy_path` | `""` | Path to a YAML `SecurityPolicy` file ([schema](../skills/skills-cli.md#policy-yaml)) consumed by the build's `security-analysis` stage. Resolved relative to the `forge.yaml` directory when not absolute. Overridden by `forge build --policy` when both are set. Empty = use the builtin `DefaultPolicy` (`max_risk_score: 90`, deny `nc`/`ncat`/`netcat`/`nmap`/`ssh`/`scp`, warn on scripts). |

The same SecurityPolicy schema is consumed by `forge skills audit --policy`, so a single committed `security-policy.yaml` can gate both interactive audits and `forge build` runs. See [Skills CLI / Security Audit](../skills/skills-cli.md#security-audit) for the policy YAML reference, scoring overrides, and audit output shape.
