---
name: forge
description: Complete Forge knowledge — capabilities, architecture, security model, A2A protocol, CLI, forge.yaml schema, audit pipeline, and how to build agents + skills. Load this when working on the Forge codebase or answering Forge questions.
---

# Forge knowledge skill

A single self-contained reference for Forge. Drop this file into any
Claude conversation to seed full context — capabilities, architecture,
security, APIs, configuration, CLI, the audit contract, and the
end-to-end flows for creating agents and skills. For a guided
authoring experience on writing a Forge skill specifically, load the
companion **`forge-skill-builder`** skill
(`.claude/skills/forge-skill-builder.md`).

Every section links to the canonical document under `docs/`. When a
claim cites a file path, that path is real — verify with `grep` in the
repo.

## Table of contents

1. [What Forge is](#1-what-forge-is)
2. [Module layout](#2-module-layout)
3. [How an agent works end-to-end](#3-how-an-agent-works-end-to-end)
4. [A2A 0.3.0 protocol surface](#4-a2a-030-protocol-surface)
5. [LLM provider abstraction](#5-llm-provider-abstraction)
6. [Tool system](#6-tool-system)
7. [Channel adapters](#7-channel-adapters)
8. [Memory system](#8-memory-system)
9. [Scheduling](#9-scheduling)
10. [Secrets management](#10-secrets-management)
11. [Build pipeline](#11-build-pipeline)
12. [Security model](#12-security-model)
13. [CLI surface](#13-cli-surface)
14. [`forge.yaml` schema reference](#14-forgeyaml-schema-reference)
15. [How to create an agent](#15-how-to-create-an-agent)
16. [How to create a skill](#16-how-to-create-a-skill)
17. [Audit event reference](#17-audit-event-reference)
18. [Workstream recap — FWS-1 through FWS-10](#18-workstream-recap--fws-1-through-fws-10)
19. [Docs map](#19-docs-map)
20. [Recipes — common questions](#20-recipes--common-questions)

---

## 1. What Forge is

Forge is an open-source runtime for building, packaging, and operating
LLM-backed agents that do real work. The design commitment is three
properties at once — **atomicity** (explicit skills, declared tools,
declared dependencies), **security** (restricted egress, encrypted
secrets, end-to-end audit), and **portability** (the same agent runs
locally, in a container, or in Kubernetes with the same `forge.yaml`).
The runtime speaks A2A 0.3.0 over JSON-RPC and REST, integrates with
multiple LLM providers behind a common interface, and ships with a
pluggable channel system (Slack / Telegram / MS Teams) plus an MCP
client for external tool servers.

```
┌─────────────────────────────────────────────────────────────────┐
│                       LLM provider                              │
│             (Anthropic / OpenAI / Ollama / OAI-compat)          │
└─────────────────────────────────────────────────────────────────┘
                                ▲
                                │ Chat / streaming
┌─────────────────────────────────────────────────────────────────┐
│                       Runtime engine                            │
│  ─────────────────────────────────────────────────────────────  │
│  AgentExecutor loop  •  Hook system  •  Tool registry           │
│  Session memory      •  Cancellation registry (FWS-4)           │
└─────────────────────────────────────────────────────────────────┘
            ▲                                       │
            │ tasks/send                            │ tool calls
            │ tasks/cancel                          ▼
┌─────────────────────────────────┐   ┌─────────────────────────────┐
│      A2A HTTP server            │   │     Tool execution layer    │
│  /, /healthz,                   │   │  builtins • skill scripts   │
│  /.well-known/agent-card.json   │   │  MCP • cli_execute          │
│  Auth chain • Rate limiter      │   └─────────────────────────────┘
│  CORS • Audit middleware        │
└─────────────────────────────────┘
            ▲
            │ JSON-RPC / REST
┌─────────────────────────────────────────────────────────────────┐
│     Caller (initializ platform / CLI / channel adapter / curl)  │
└─────────────────────────────────────────────────────────────────┘

Surrounding policy + observability:
  • Egress enforcer + subprocess proxy (HTTP_PROXY injection)
  • Three-layer platform policy (system / user / workspace)  — FWS-6
  • Audit NDJSON → stderr + UDS sink + HTTP fallback        — FWS-7
  • Audit schema 1.0 + monotonic seq + opt-in payload      — FWS-8
  • Stdout = ops logs / stderr = audit                     — FWS-9
  • Per-IP rate limit + cancel exemption                   — FWS-10
```

**Read first**: `README.md`, `docs/core-concepts/how-forge-works.md`.

---

## 2. Module layout

Multi-module Go workspace (`go.work` at repo root). Each module has its
own `go.mod`.

| Module | Purpose |
|---|---|
| **`forge-core/`** | Pure library, no CLI dependencies. Runtime engine (`runtime/`), LLM provider abstraction (`llm/`), tool interfaces + builtins (`tools/`), channel interfaces (`channels/`), security subsystem (`security/`: auth chain, egress enforcer, platform policy), MCP client (`mcp/`), audit logger + sinks (`runtime/audit*.go`), memory (`memory/`), scheduler (`scheduler/`), encrypted secrets (`secrets/`), validation (`validate/`), shared types (`types/`), A2A protocol types (`a2a/`). |
| **`forge-cli/`** | Cobra CLI on top of `forge-core`. Subcommands (`cmd/`), the dev-mode runner (`runtime/runner.go`), build pipeline stages (`build/`), framework plugins for CrewAI / LangChain / custom (`plugins/`), the A2A HTTP server (`server/a2a_server.go`), container builders + packaging (`container/`), the TUI wizard (`internal/tui/`), init templates (`templates/`). |
| **`forge-ui/`** | Local Web Dashboard embedded into the `forge` binary (`forge ui` subcommand). Static SPA + Go HTTP handlers — agent discovery, chat proxy, settings, the LLM-powered Skill Builder. Same prompt this codebase exports to `.claude/skills/forge-skill-builder.md`. |
| **`forge-plugins/`** | Channel-adapter plugin implementations. `channels/slack/` (Socket Mode), `channels/telegram/`, `channels/msteams/`, plus the `channels/markdown/` formatter. Implement `corechannels.ChannelPlugin`. |
| **`forge-skills/`** | Skill registry, parser, compiler. `parser/` consumes `SKILL.md` frontmatter + body; `embedded/` ships ready-made skills (`tavily-research`, `k8s-incident-triage`, etc.); compiler turns SKILL.md into runtime tool definitions. Walks `skills/*.md`, `skills/*/SKILL.md`, and the main agent skill at `skills.path` or `SKILL.md`. |

Top-level: `docs/` (43 markdown files), `scripts/` (manual-test scripts + AWS sigv4 client), `examples/`, `CLAUDE.md` (developer conventions), `CHANGELOG.md`, `Dockerfile`.

**Read**: `docs/core-concepts/how-forge-works.md` (the canonical map),
the README sections under each module folder.

---

## 3. How an agent works end-to-end

The path of a single A2A invocation:

```
1. Inbound HTTP request → POST / with JSON-RPC envelope
   forge-cli/server/a2a_server.go:handleJSONRPC
2. Middleware chain (outermost → innermost):
     installSequenceCounterMiddleware                — installs the
       per-request SequenceCounter on r.Context()
       BEFORE the auth chain runs so auth_verify /
       auth_fail land seq=1 (FWS-8, fix #174)
     auth (forge-core/auth/middleware.go)            — verifies bearer;
       OnAuth callback emits via EmitFromContext so
       seq + trace_id + workflow tags are stamped
     rate-limit                                      — per-IP, FWS-10
     CORS, security headers, request-size limits
3. Dispatch to method handler:
     tasks/send   → runs the agent (long)
     tasks/cancel → signals an in-flight invocation (instant)
     tasks/get    → returns stored task state
     tasks/list   → returns the task index
4. Request entry (forge-cli/runtime/runner.go):
     correlation_id generated
     task_id from params.ID
     EnsureSequenceCounter reuses the auth-installed counter
       (or installs a fresh one on the --no-auth path)     — FWS-8
     LLMUsageAccumulator installed (FWS-3)
     CancellationRegistry registers a CancelCauseFunc (FWS-4)
5. AgentExecutor loop:
     for each iteration:
       check ctx.Err()                              — honors cancel
       call LLM (provider client.Chat / Stream)
       Hook: AfterLLMCall                           — emits llm_call audit
       extract tool calls
       for each tool call:
         check ctx.Err()                            — honors cancel
         Hook: BeforeToolExec                       — tool_exec start
         execute (builtin / skill script / MCP)
         Hook: AfterToolExec                        — tool_exec end
       append assistant + tool messages to history
     until model returns assistant message with no tool calls
6. Response handler:
     emit invocation_complete (or invocation_cancelled)
     write X-Forge-Tokens-In/Out, X-Forge-Duration-Ms, X-Forge-Model,
       X-Forge-Provider response headers (FWS-3)
     return JSON-RPC result
```

The runner also installs `agent_card_published` (startup + hot-reload,
FWS-1), `policy_loaded` per non-empty layer (FWS-5/6),
`audit_export_status` every 60s when an export sink is configured
(FWS-7).

**Read**: `docs/core-concepts/runtime-engine.md`,
`docs/core-concepts/hooks.md`, `forge-cli/runtime/runner.go`.

---

## 4. A2A 0.3.0 protocol surface

**HTTP routes** (`forge-cli/server/a2a_server.go`):

| Route | Method | Purpose |
|---|---|---|
| `/` | POST | JSON-RPC 2.0 dispatch — `tasks/send`, `tasks/cancel`, `tasks/get`, `tasks/list` |
| `/` | GET | Agent Card (same body as the well-known route) |
| `/.well-known/agent-card.json` | GET | A2A 0.3.0 canonical Agent Card; public (`DefaultSkipPaths`) |
| `/.well-known/agent.json` | GET | Legacy alias; same body + `Deprecation: true` + `Link: rel=successor-version` |
| `/healthz` | GET | Liveness probe; public |

**JSON-RPC methods**:

| Method | Behavior |
|---|---|
| `tasks/send` | Submits a task. Streaming (`stream: true`) returns SSE: `status`, `progress`, `result` events. Synchronous returns the final task. |
| `tasks/cancel` | Cancels an in-flight task by ID; idempotent. Signals a typed reason (`workflow_failure` / `cost_limit_exceeded` / `timeout` / `external_signal`); honored at next iteration / tool-call boundary. Exempt from the write rate-limit bucket by default (FWS-10). |
| `tasks/get` | Returns the stored task state. |
| `tasks/list` | Returns the task index. |

**Workflow correlation headers** (FWS-2) — extracted on every request,
threaded into context, stamped on every audit event:

- `X-Workflow-ID` — workflow DEFINITION (stable across runs of the same workflow). Use for definition-level rollups ("top failing workflows").
- `X-Workflow-Execution-ID` — per-run instance (unique per workflow execution). Use for per-run timelines ("show me every event in this specific run"). Split from the formerly-overloaded `X-Workflow-ID` in FORGE-2 / issue #185.
- `X-Workflow-Stage-ID` — workflow stage
- `X-Workflow-Step-ID` — workflow step
- `X-Invocation-Caller` — upstream caller identifier

**Response headers** (FWS-3): `X-Forge-Tokens-In`, `X-Forge-Tokens-Out`,
`X-Forge-Duration-Ms`, `X-Forge-Model`, `X-Forge-Provider`.

**Agent Card** carries `name`, `description`, `url`, `version`,
`protocolVersion: "0.3.0"`, `defaultInputModes` /
`defaultOutputModes` (`["text/plain", "application/json"]`), `skills[]`
(from SKILL.md frontmatter), `capabilities`, `securitySchemes` /
`security` (derived from the `auth.providers` chain).

**Read**: `docs/reference/a2a-agent-card.md`,
`docs/security/workflow-correlation.md`,
`forge-cli/server/a2a_server.go`.

---

## 5. LLM provider abstraction

Common `llm.Client` interface (`forge-core/llm/`). Providers:

| Provider | Notes |
|---|---|
| `anthropic` | Claude family. Native messages API. |
| `openai` | GPT family. Also covers OpenAI-compatible endpoints (OpenRouter, vLLM, litellm, self-hosted Kimi/Llama) — set `provider: openai` + `OPENAI_BASE_URL`. Wizard's "Custom" option normalizes to this shape; `forge.yaml` never carries `provider: "custom"`. |
| `ollama` | Local OSS models via the Ollama daemon. |
| `gemini` | Google. |

Configured in `forge.yaml`:

```yaml
model:
  provider: openai
  name: gpt-4o
  organization_id: org-xxx     # OpenAI enterprise org ID, optional
  fallbacks:
    - provider: anthropic
      name: claude-sonnet-4-20250514
```

Fallbacks fire in order on provider error. CLI flags
(`--model`, `--provider`) override the yaml at `forge run` time.

Token usage and request IDs are captured per provider at the call site
and folded into the `llm_call` audit event (FWS-3) and into the
per-invocation `LLMUsageAccumulator` so the response headers + the
final `invocation_complete` event carry totals.

**Read**: `docs/core-concepts/runtime-engine.md`, `forge-core/llm/`.

---

## 6. Tool system

The agent loop calls tools the LLM asks for. The registry merges:

- **Builtin tools** (`forge-core/tools/builtins/`): `web_search`,
  `http_request`, `json_parse`, `csv_parse`, `datetime_now`,
  `uuid_generate`, `math_calculate`, `cli_execute`, `read_skill`.
- **Skill tools**: script-backed `SKILL.md` skills auto-register as
  first-class LLM tools (one tool per `## Tool:` heading). Binary-backed
  skills inline their full body into the system prompt instead.
- **MCP tools**: discovered from MCP servers declared in `forge.yaml`'s
  `mcp.servers[]` block. Names are namespaced `<server>__<tool>` (e.g.
  `linear__create_issue`). Phase 1 ships HTTP transport only; stdio is
  rejected at validate time with a roadmap pointer. OAuth 2.1 PKCE
  supported via `forge mcp login`.

`cli_execute` ships 13 security layers — shell denylist, binary
allowlist, `LookPath` resolution at startup, argument validation
(rejects `$(`, backticks, newlines, `file://`), path confinement,
no-shell execution, env isolation (`PATH`, `HOME`, `LANG` + explicit
passthrough + `GH_CONFIG_DIR` for `gh`, `KUBECONFIG` for
`kubectl`/`helm`), output size cap, skill guardrail patterns,
entrypoint validation for custom tools. See
`docs/security/overview.md` § Execution Sandboxing.

**Read**: `docs/core-concepts/tools-and-builtins.md`, `docs/mcp/*.md`.

---

## 7. Channel adapters

Channels are incoming connection bridges from messaging platforms.
Each implements `corechannels.ChannelPlugin` and lives under
`forge-plugins/channels/`.

| Channel | Wire model | Token shape |
|---|---|---|
| **Slack** | Socket Mode (outbound WebSocket via `apps.connections.open`) | `SLACK_BOT_TOKEN` + `SLACK_APP_TOKEN`; optional signing secret |
| **Telegram** | Long-polling via `getUpdates` (webhook mode binds 127.0.0.1 only) | `TELEGRAM_BOT_TOKEN` |
| **MS Teams** | Bot Framework | per-channel config |

Forge does NOT create public tunnels and Forge agents have **no inbound
attack surface by default** — all channels use outbound connections.

Lifecycle:

```bash
forge channel add slack             # adds adapter + scaffolds .env
forge channel list                  # inventory
forge channel status                # per-channel health
forge channel serve <name>          # run one adapter as a standalone process
forge channel disable <name>        # writes ~/.forge/policy.yaml (user layer)
forge channel disable <name> --system   # writes /etc/forge/policy.yaml (system layer)
forge channel enable <name>         # removes from the same layer
```

`forge run --with slack,telegram` starts the dev-mode A2A server +
specified adapters in the same process. The three-layer platform
policy (FWS-6) can deny channels at any layer; denied channels are
skipped at startup with a `channel_denied_by_policy` audit event
attributed to the deciding layer.

**Read**: `docs/core-concepts/channels.md`,
`docs/security/platform-policy.md` § Channels.

---

## 8. Memory system

Default-on **session memory**: per-session chat transcript + metadata
under `<workdir>/.forge/sessions/`. Bounded by `memory.sessions_dir`.

Opt-in **long-term memory**: vector-backed semantic + keyword hybrid
retrieval with temporal decay (configurable half-life), context-budget
caps, compaction triggers. Embedding provider auto-detects from the
LLM provider (Anthropic → `voyage-3` family; OpenAI → `text-embedding-3-small`)
unless `memory.embedding_provider` is explicit.

**Read**: `docs/core-concepts/memory-system.md`.

---

## 9. Scheduling

`forge.yaml` declares cron-like recurring tasks:

```yaml
schedules:
  - id: daily-report
    cron: "@daily"                     # standard cron OR @daily/@weekly/@monthly
    task: "Generate yesterday's summary"
    skill: ""                          # optional — invoke a specific skill
    channel: telegram                  # optional — deliver via this channel
    channel_target: "-100123456"       # chat/channel ID for delivery
```

The scheduler runs each fired task as a normal A2A invocation — full
audit trail (`schedule_fire`, `schedule_complete`, `schedule_skip`,
`schedule_modify`), correlation IDs, token accounting. Schedules can
also be created at runtime via the A2A API and managed with
`forge schedule`.

**Hybrid backend (#162)**. Two implementations behind one
`scheduler.Backend` interface, selected at startup:

| `scheduler.backend` | Storage | Timing |
|---------------------|---------|--------|
| `file` | `<WorkDir>/.forge/memory/SCHEDULES.md` | 30s in-process goroutine ticker |
| `kubernetes` | K8s `CronJob` resources (etcd) | Cluster's CronJob controller |
| `auto` (default) | Resolves to `kubernetes` when in-cluster (`/var/run/secrets/kubernetes.io/serviceaccount/token` present), `file` otherwise | — |

```yaml
scheduler:
  backend: auto
  kubernetes:
    namespace: ""             # defaults to pod's own namespace
    service_url: ""           # in-cluster URL CronJob trigger pods POST to
    allow_dynamic: false      # whether the LLM (schedule_set) can create CronJobs
    trigger_image: ""         # default: curlimages/curl:8.10.1
    auth_secret_name: ""      # default: <agent_id>-internal-token
```

`scheduler.InCluster()` is the detection helper. `FORGE_IN_CLUSTER=true|false` overrides for testing. `KubernetesBackend.Sync` reconciles CronJobs against the declared yaml entries — creates missing, updates on spec drift, prunes dropped yaml entries, **preserves LLM-sourced** (label `forge.schedule.source=llm`) entries unconditionally. `AllowDynamic: false` (default) keeps the LLM from creating CronJobs at runtime; `forge package` materializes yaml entries at build time instead (see § 11).

**Read**: `docs/core-concepts/scheduling.md`, `docs/deployment/scheduler-kubernetes.md`.

---

## 10. Secrets management

Three-tier resolution: **agent-local** (`<workdir>/.forge/secrets.enc`) →
**global** (`~/.forge/secrets.enc`) → **environment**. Providers
declared in `forge.yaml` `secrets.providers[]` (default
`[encrypted-file, env]`).

Encryption: AES-256-GCM with Argon2id key derivation from a
passphrase. Passphrase sourced from `FORGE_PASSPHRASE` env, an
in-memory cache, or an interactive prompt at startup.

Cross-category reuse detection at startup (FWS-3 era): if the same
secret value appears under two different category names (e.g.
`OPENAI_API_KEY` and `TELEGRAM_BOT_TOKEN` set to the same value), the
runtime warns — one stolen token shouldn't unlock multiple surfaces.

CLI:

```bash
forge secret set OPENAI_API_KEY        # prompts if no value
forge secret set ANTHROPIC_API_KEY sk-...
forge secret get OPENAI_API_KEY        # masked by default
forge secret list                       # names only, never values
forge secret delete OPENAI_API_KEY
```

**Read**: `docs/security/secret-management.md`.

---

## 11. Build pipeline

`forge build` runs ordered stages (`forge-cli/build/`):

| # | Stage | Output |
|---|---|---|
| 1 | **FrameworkAdapterStage** | Detects `forge` / `crewai` / `langchain` / `custom`; generates the A2A wrapper script for non-forge frameworks |
| 2 | **AgentSpecStage** | Canonical `agent.json` (`agentspec.AgentSpec`) from `forge.yaml`, including `a2a.skills` populated from SKILL.md frontmatter so post-build A2A clients see the skill surface (FWS-1) |
| 3 | **SkillsStage** | Compiles SKILL.md tree → `compiled/skills/skills.json` + `compiled/prompt.txt`; updates the spec with `skills_spec_version` |
| 4 | **ToolsStage** | Enumerates registered tools (builtins + skill tools + MCP) |
| 5 | **EgressStage** | Resolves capability bundles, validates against platform policy, writes `egress_allowlist.json` |
| 6 | **PackageStage** | Generates `Dockerfile` + Kubernetes manifests (`deployment.yaml` with `FORGE_PLATFORM_POLICY` env + `optional: true` ConfigMap volume) |
| 6a | **ScheduleManifestStage** | When `forge.yaml schedules[]` is non-empty AND `scheduler.backend ≠ file`, emits one `k8s/cronjob-<id>.yaml` per schedule, a credential-less `k8s/internal-token-secret.yaml` template, and a `k8s/scheduler-role.yaml` + `k8s/scheduler-rolebinding.yaml` pair scoped to the agent's namespace. RBAC verbs are gated by `scheduler.kubernetes.allow_dynamic`. Operator populates the Secret out-of-band via `forge auth secret-yaml` — see § 13 (#162 part 3) |
| 7 | **SigningStage** (opt-in) | Ed25519 signature of every artifact; `checksums.json` with SHA-256s |

Artifacts land in `.forge-output/`. `forge run` from a built directory
loads `agent.json` (`AgentCardFromSpec` path); without one, the runner
walks SKILL.md at startup (`AgentCardFromConfig` + runtime enrichment)
— both paths produce a byte-identical card.

**Read**: `docs/core-concepts/how-forge-works.md` § Build Pipeline.

---

## 12. Security model

### 12.1 Auth provider chain

Configured in `forge.yaml` `auth.providers[]` — ordered, **first match
wins**, **fail-closed on rejection** (a malformed token of type A
never falls through to type B).

| Provider | Use case | Wire format | Phase |
|---|---|---|---|
| `static_token` | Loopback / dev / shared-secret | `Authorization: Bearer <token>`; constant-time SHA-256 compare | 1 |
| `oidc` | Any IdP with OIDC discovery (Keycloak, Auth0, Okta, Google) | `Authorization: Bearer <jwt>`; JWKS cache + stale-grace | 1 |
| `http_verifier` | Legacy / custom external `/verify` endpoint | Opaque bearer | 1 |
| `aws_sigv4` | AWS-IAM callers | `Authorization: Bearer forge-aws-v1.<base64-url>` (pre-signed STS URL) | 2 |
| `gcp_iap` | Behind GCP HTTPS LB + IAP | `X-Goog-Iap-Jwt-Assertion: <jwt>`; hardcoded JWKS host | 2 |
| `azure_ad` | Microsoft Entra ID | `Authorization: Bearer <aad-jwt>`; composed `oidc` + tenant gate | 2 |

Loopback `static_token` is auto-prepended for `.forge/runtime.token`
so channel adapters and the local Web UI can call the A2A server
without any external auth configured.

Auth provider hosts are auto-added to the egress allowlist (issuer for
OIDC, `sts.<region>.amazonaws.com` for sigv4, `www.gstatic.com` for
GCP IAP, `login.microsoftonline.com` for AAD).

The auth chain projects into the Agent Card's `securitySchemes` /
`security` blocks (FWS-1):

| Provider | A2A scheme |
|---|---|
| `static_token` / `http_verifier` | `http` + `bearer` |
| `oidc` / `azure_ad` | `openIdConnect` (with discovery URL) |
| `gcp_iap` | `apiKey` in header |
| `aws_sigv4` | `http` + `bearer` with `bearerFormat: "forge-aws-v1"` |

**Read**: `docs/security/authentication.md`,
`docs/reference/a2a-agent-card.md` § Security schemes.

### 12.2 Egress enforcement

Modes (`forge.yaml` `egress.mode`):

| Mode | Behavior |
|---|---|
| `deny-all` | Block all non-localhost outbound |
| `allowlist` | Only listed domains (exact + wildcard) |
| `dev-open` | All traffic allowed (development only — refused by `forge package --prod`) |

In-process tools (LLM calls, `http_request`, `web_search`) check the
`EgressEnforcer` (an `http.RoundTripper` over `SafeTransport`) with
post-DNS IP validation. Subprocess tools (`cli_execute`, skill scripts)
get `HTTP_PROXY` / `HTTPS_PROXY` set to a local `EgressProxy` on
`127.0.0.1:<random>` that re-validates the request. Redirect handling
strips `Authorization` / `Cookie` / `Proxy-Authorization` on
cross-origin hops.

In containers (detected via `KUBERNETES_SERVICE_HOST` or `/.dockerenv`),
the in-process proxy isn't started — `NetworkPolicy` (generated by
`forge package`) handles egress at the pod level.

Emits `egress_allowed` / `egress_blocked` per request with `source`
(in-process vs proxy) so consumers can attribute.

**Read**: `docs/security/egress-control.md`, `docs/security/overview.md`.

### 12.3 Three-layer platform policy (FWS-5 / FWS-6)

Three independent files, same schema:

| Layer | Path | Set by |
|---|---|---|
| **system** | `/etc/forge/policy.yaml` (override `FORGE_SYSTEM_POLICY`) | Sysadmin (MDM, corporate image) |
| **user** | `~/.forge/policy.yaml` | Developer (via `forge channel disable` or Web UI chip) |
| **workspace** | path at `FORGE_PLATFORM_POLICY` | Operator (Initializ Command, GitOps tooling); `forge package` wires this env into the generated Deployment |

Fields: `denied_egress_domains`, `denied_tools`, `forbidden_models`
(provider+name pairs), `denied_channels`, `max_egress_allowlist_size`,
`max_tool_count`.

Resolution:

- Deny lists → **union** across layers
- Max bounds → **smallest non-zero value wins**
- Audit attribution → **first layer to deny in load order (system →
  user → workspace) takes credit**

Channel deny is non-fatal (the adapter is skipped + a
`channel_denied_by_policy` event fires). Egress / tool / model
violations are hard errors — `policy_violation_at_build_time` event +
the runner returns a multi-line error from `NewRunner` naming the
deciding layer + path. `forge.yaml` does **not** have a per-agent
`disabled_channels` field — channel disable is laptop or workspace
level, never declaration.

**Read**: `docs/security/platform-policy.md`,
`examples/platform-policy.yaml`.

### 12.4 Audit logging — schema, sinks, payload (FWS-7 / FWS-8 / FWS-9)

Every event is one line of NDJSON, schema-versioned, sequence-numbered,
fan-out across configured sinks.

**Schema invariants** (FWS-8):

- `ts` (RFC3339 UTC), `event` (constant string), `schema_version`
  (`"1.0"`) on every event
- `correlation_id`, `task_id` on every request-scoped event
- `workflow_id` (definition) / `workflow_execution_id` (per-run) /
  `stage_id` / `step_id` / `invocation_caller` when the orchestrator
  sent the matching `X-Workflow-*` headers (FWS-2; the
  definition/execution split landed in FORGE-2 / #185)
- `seq` (monotonic int64) on every event emitted inside an
  invocation; absent on startup events (`policy_loaded`,
  `agent_card_published`, `audit_export_status`)
- **Default posture is metadata-only.** No prompt text, no completion
  text, no raw tool args / results. The `TestNoPayloadByDefault_LLMCall`
  regression test pins this invariant.
- Opt-in `AuditPayloadCapture` (`LLMMessages` / `LLMResponse` /
  `ToolArgs` / `ToolResult`) restores raw bytes with per-field byte
  caps (default 16 KiB) + `…[truncated:N]` markers. Operators are
  responsible for routing captured payloads to a store appropriate to
  their sensitivity — Forge does not redact.

**Emit invariant.** Every audit emission inside an A2A invocation
scope MUST go through `AuditLogger.EmitFromContext(ctx, ...)` (or one
of the typed helpers — `EmitLLMCall`, `EmitToolExec`, etc.). That's
the path that picks the per-invocation `SequenceCounter` off ctx and
stamps `seq` + `trace_id` + `span_id` + workflow tags. Plain `Emit`
emits raw — no seq, no trace link, no workflow tags. Regression pins:
`TestToolExecAudit_CarriesSequenceFromContext` (PR #173,
`tool_exec` + `session_end`),
`TestAuthAudit_SeqStampedWhenCounterInstalled` (PR #176, `auth_verify`
once the counter is installed by the middleware wrapper). Sites that
still call plain `Emit` are explicitly outside any invocation scope:
the egress proxy's subprocess CONNECT (no Go ctx tying back),
MCP-server startup (`mcp_server_started` / `_failed` / `_degraded`,
pre-invocation), and the scheduler tick (`schedule_fire` /
`schedule_complete`, runs on its own timer). Those events
intentionally have no `seq`.

**Sinks** (FWS-7):

| Sink | Always on? | Notes |
|---|---|---|
| stderr (NDJSON) | yes | The safety net — container log collectors capture it by default |
| Unix Domain Socket | when `--audit-socket` / `FORGE_AUDIT_SOCKET` | Lazy reconnect, 50ms per-write timeout, exponential backoff 100ms → 5s cap, drop on timeout |
| Localhost HTTP | when `--audit-http-endpoint` / `FORGE_AUDIT_HTTP_ENDPOINT` (socket wins when both set) | Same fire-and-forget discipline |

Events are byte-identical across sinks. A periodic
`audit_export_status` event (every 60s) carries per-sink
`writes_ok` / `drops_timeout` / `drops_dial` / `connected` so
operators tail the audit stream itself to confirm export health.

**Streams** (FWS-9):

- **stdout** carries ops logs (`r.logger.Info/Warn/Error` —
  `JSONLogger`).
- **stderr** carries audit NDJSON + the human-readable startup banner.
- `forge run > ops.log 2> audit.log` splits cleanly without payload
  parsing. Container log collectors that capture both streams are
  unaffected.

**Read**: `docs/security/audit-logging.md`,
`docs/deployment/monitoring.md`.

### 12.5 Rate limiter (FWS-10)

Per-IP token bucket on the A2A server. Defaults (bumped from #31's
originals to be orchestration-friendly):

| Field | Default | Notes |
|---|---|---|
| `ReadRPS` | 1.0 (60/min) | GET / HEAD / OPTIONS |
| `ReadBurst` | 10 | |
| `WriteRPS` | 1.0 (60/min) | POST / PUT / DELETE — bumped from 10/60 |
| `WriteBurst` | 20 | Bumped from 3 |
| `CancelExempt` | `true` | `tasks/cancel` skips the write bucket; DoS via cancel-spam is naturally bounded by the registry's O(1) unknown-task lookup |

Configurable per-field via `forge.yaml` `server.rate_limit:`, CLI flags
(`--rate-limit-{read,write}-{rps,burst}`, `--rate-limit-cancel-exempt`),
or `FORGE_RATE_LIMIT_*` env vars. Precedence: **CLI > env > yaml >
defaults**. The cancel exemption uses a body peek (capped at 4 KiB,
fail-closed on malformed JSON) to recognize `tasks/cancel` requests
without breaking other JSON-RPC methods.

### 12.6 Build signing

Ed25519 keypair management via `forge key generate / sign / verify`.
`forge build --sign` signs every artifact + writes `checksums.json`.
`forge run` can verify artifacts against trusted keys before execution.

**Read**: `docs/security/build-signing.md`.

### 12.7 Guardrails

Two layers:

- **Global guardrails** (`guardrails.json` at the project root) —
  content filtering, PII detection, jailbreak protection. Mode
  `enforce` (block) or `warn` (log). Emits `guardrail_check` audit.
- **Skill guardrails** (in `SKILL.md` `metadata.forge.guardrails`) —
  four hook points: `deny_commands` (block `cli_execute` patterns),
  `deny_output` (block or redact tool output), `deny_prompts`
  (block capability-enumerating user messages), `deny_responses`
  (replace binary-enumerating LLM responses).

**Read**: `docs/security/guardrails.md`.

### 12.8 Trust model

The caller → Forge → LLM trust boundary, plus the channel-adapter
loopback contract, the cross-category secret-reuse detection, and the
"Forge holds no IdP secrets" posture (every provider verifies against
a third party).

**Read**: `docs/security/trust-model.md`, `docs/security/overview.md`.

### 12.9 Observability — OpenTelemetry tracing (OTel v1, #108)

Off by default. When enabled (`observability.tracing.enabled: true` in
`forge.yaml`), Forge exports OTLP spans covering A2A dispatch, agent
execution, every LLM completion, every tool call, and every outbound
HTTP request. Span hierarchy:

```
a2a.<method>                          [SpanKindServer; dispatcher]
└── agent.execute                     [outer loop; root for the task]
    ├── llm.completion (× N turns)    [per LLM provider call]
    │   └── http.client (× outbound)  [auto via otelhttp]
    └── tool.<tool_name> (× M calls)
        └── http.client (if HTTP)
```

Key design points:

- **Off by default.** The tracer seam (Phase 0) returns a noop tracer
  unless the cli explicitly installs one. Audit pipeline is the
  always-on compliance stream; tracing is the opt-in observability
  stream.
- **Standard config surface.** All 10 standard OTel SDK env vars are
  honored (`OTEL_EXPORTER_OTLP_*`, `OTEL_TRACES_SAMPLER`,
  `OTEL_SERVICE_NAME`, ...). Precedence: defaults < yaml < env < CLI
  flags.
- **Egress-enforced.** The OTLP HTTP exporter rides through the same
  egress enforcer as every other in-process client — a misconfigured
  collector URL cannot exfiltrate spans to an unapproved destination.
- **End-to-end propagation.** Composite W3C `tracecontext + baggage`
  propagator installed at startup. The dispatcher extracts inbound
  `traceparent` so multi-hop A2A flows show as one connected trace.
  Outbound HTTP through the egress-enforced transport auto-injects
  the current span's `traceparent` (via otelhttp).
- **Audit cross-link.** `EmitFromContext` stamps the active span's
  `trace_id` + `span_id` on every audit event. Operators paste either
  value into their backend to pivot audit row ↔ span node. Both
  fields use `omitempty` — when tracing is off, audit JSON is
  byte-identical to the pre-Phase-4 shape.
- **Build-time egress merge.** `forge package` extracts the collector
  hostname and auto-injects it into `egress_allowlist.json` (mirrored
  at `forge run`). Disabled tracing produces no entry. No second
  egress edit, no NetworkPolicy patch.
- **Telemetry failures never crash the agent.** Bad endpoint,
  malformed traceparent, unreachable collector — every failure mode
  falls through to the noop tracer with a warning in the ops log.

GenAI semconv attributes on `llm.completion`: `gen_ai.system`,
`gen_ai.request.model`, `gen_ai.usage.input_tokens`,
`gen_ai.usage.output_tokens`, `gen_ai.response.finish_reasons`.
Forge-specific attributes use the `forge.*` namespace
(`forge.task.id`, `forge.task.final_state`, `forge.tool.name`,
`forge.workflow.id`, ...).

**Default posture is metadata-only.** Prompts, completions, tool
args, and tool results are NOT stamped on spans unless
`observability.tracing.capture_content: true` is set (Phase 3.5 /
#130). When opted-in: `llm.completion` gains `gen_ai.input.messages`
(JSON array of role+content sent to the model) +
`gen_ai.output.messages` (JSON single-element array for the response,
current OTel GenAI semconv; supersedes the deprecated flat-string
`gen_ai.prompt` / `gen_ai.completion`);
`tool.<name>` gains `forge.tool.args` + `forge.tool.result`.
Captured values pass through a redactor (vendor secret-token shapes:
Anthropic / OpenAI / GitHub / AWS / Slack / private keys / Telegram)
when `redact: true` (default with capture). Each value is byte-capped
at 4 KiB with a `…[truncated:N]` marker byte-identical to the audit
payload-capture marker, so an operator grepping `[truncated:` across
spans and audit rows sees aligned output. `redact: false` is the
enterprise raw-capture path.

**Read**: `docs/core-concepts/observability-tracing.md`,
`docs/reference/forge-yaml-schema.md` § `observability.tracing`,
`docs/security/audit-logging.md` § Trace cross-link,
`docs/security/egress-control.md` § OTel collector auto-extension.

---

## 13. CLI surface

Full reference: `docs/reference/cli-reference.md`.

| Subcommand | Purpose | Key flags |
|---|---|---|
| `forge init` | Scaffold a new agent: `forge.yaml`, `.env`, `SKILL.md`, `guardrails.json`. Interactive TUI by default; `--non-interactive` for CI | `--model-provider`, `--model-name`, `--channels`, `--auth`, `--from-skills` |
| `forge build` | Run the build pipeline → `.forge-output/agent.json` + container Dockerfile + K8s manifests + (optional) signature | `--output-dir`, `--sign` |
| `forge validate` | Lint `forge.yaml` + SKILL.md. `--platform-policy=PATH` lints a policy file standalone | `--strict`, `--command-compat`, `--platform-policy` |
| `forge run` | Dev-mode A2A server with hot-reload | `--port`, `--host`, `--with slack,telegram`, `--mock-tools`, `--no-auth`, `--cors-origins`, `--audit-socket`, `--audit-http-endpoint`, `--rate-limit-*`, `--otel-enabled`, `--otel-endpoint`, `--otel-sampler` |
| `forge serve start \| stop \| status \| logs` | Daemonized A2A server (forks `forge run`). Forwards CLI flags + env to the child | `--port`, `--shutdown-timeout`, `--with` |
| `forge export` | Export `agent.json` for registry upload | |
| `forge package` | Generate Dockerfile + Kubernetes manifests + `egress_allowlist.json`. `--prod` rejects `dev-open` egress + dev-only tools | `--registry`, `--tag`, `--base`, `--prod` |
| `forge schedule list \| run \| logs` | Manage cron-backed tasks | |
| `forge tool list \| describe` | Enumerate registered tools, show schemas | |
| `forge channel add \| list \| status \| serve \| disable \| enable` | Channel adapters; disable/enable edit the user policy layer by default; `--system` retargets `/etc/forge/policy.yaml` | `--with`, `--system` |
| `forge secret set \| get \| list \| delete` | Encrypted secrets | |
| `forge auth show-token \| mint-token \| secret-yaml` | Operator UX for the internal bearer token at `<root>/.forge/runtime.token` (same token channel adapters + K8s CronJob trigger pods use). `secret-yaml` prints a ready-to-apply K8s Secret manifest sourced from the local token; `mint-token` is for first-deploy bootstrap. `forge.agent.id` label always tracks `forge.yaml` `agent_id`, never the `--name` override. (#162 part 1, PR #168) | `--namespace`, `--name` |
| `forge key generate \| sign \| verify` | Ed25519 build artifact signing | |
| `forge skills add \| list \| validate \| audit` | Registry: install, search, validate binary/env deps, security audit `--embedded` | `--category`, `--tags`, `--embedded` |
| `forge mcp list \| test \| login \| logout` | Manage MCP servers + OAuth tokens | `--call <tool>`, `--args '<json>'` |
| `forge ui` | Launch the local Web Dashboard | `--port` |

---

## 14. `forge.yaml` schema reference

Source of truth: `docs/reference/forge-yaml-schema.md`.

```yaml
agent_id: my-agent                   # required, kebab-case
version: 1.0.0                       # required, semver
framework: forge                     # forge (default) | crewai | langchain
registry: ghcr.io/org                # container registry for build/package
entrypoint: agent.py                 # required for crewai/langchain

model:
  provider: openai                   # openai | anthropic | gemini | ollama
  name: gpt-4o
  organization_id: org-xxx           # OpenAI enterprise org
  fallbacks:
    - provider: anthropic
      name: claude-sonnet-4-20250514

tools:                               # builtin tool registry entries
  - name: web_search
  - name: cli_execute
    config:
      allowed_binaries: [git, curl]
      env_passthrough: [GITHUB_TOKEN]

channels: [slack, telegram]          # declared adapters

egress:
  profile: standard                  # strict | standard | permissive
  mode: allowlist                    # deny-all | allowlist | dev-open
  allowed_domains: ["api.example.com", "*.github.com"]
  capabilities: [slack]              # auto-expansion bundles
  allow_private_ips: false           # RFC 1918; auto true in containers

cors_origins: ["https://app.example.com"]

server:                              # FWS-10 — per-IP rate limits
  rate_limit:
    read_rps: 1.0
    read_burst: 10
    write_rps: 1.0
    write_burst: 20
    cancel_exempt: true

auth:
  required: true
  providers:
    - type: oidc
      settings:
        issuer: "https://login.example.com/auth/realms/forge"
        audience: api://forge

secrets:
  providers: [encrypted-file, env]

memory:
  persistence: true
  sessions_dir: .forge/sessions
  long_term: false
  embedding_provider: openai

mcp:
  token_store_path: ~/.forge/mcp-tokens.enc
  servers:
    - name: linear
      transport: http
      url: https://mcp.linear.app/sse

schedules:
  - id: daily-report
    cron: "@daily"
    task: "Generate yesterday's summary"
    channel: telegram
    channel_target: "-100123456"

scheduler:                           # Backend selection (#162)
  backend: auto                      # auto (default) | file | kubernetes
  kubernetes:
    namespace: ""                    # defaults to pod's own namespace
    service_url: ""                  # in-cluster URL CronJob trigger pods POST to
    allow_dynamic: false             # whether schedule_set can create CronJobs at runtime
    trigger_image: ""                # default: curlimages/curl:8.10.1
    auth_secret_name: ""             # default: <agent_id>-internal-token

package:
  alpine: false
  slim: false
  bin_overrides:
    forge: { local: "/path/to/forge" }
    jq: { apt: "jq" }

observability:                       # OTel Tracing v1 (#108) — off by default
  tracing:
    enabled: true
    endpoint: https://otel-collector.monitoring.svc.cluster.local:4318/v1/traces
    protocol: http/protobuf          # or "grpc"
    sampler: parentbased_always_on   # standard OTEL_TRACES_SAMPLER name
    sampler_ratio: 1.0
    timeout: 10s
    service_name: ""                 # default: agent_id
    headers: { x-tenant: demo }
    resource_attrs: { deployment.environment: prod }
    redact: true                     # scrub vendor secrets when capture is on
    capture_content: false           # off by default; opt in to span content

skills:
  path: SKILL.md                     # main agent skill file
guardrails_path: guardrails.json
```

---

## 15. How to create an agent

```bash
# 1. Scaffold
forge init my-agent --model-provider anthropic --channels slack --non-interactive
cd my-agent

# 2. Add skills (registry-installed or custom)
forge skills add tavily-research
# OR write skills/<name>/SKILL.md by hand (see § 16)

# 3. Configure secrets
forge secret set ANTHROPIC_API_KEY sk-...
forge secret set SLACK_BOT_TOKEN xoxb-...

# 4. Run locally with channels
forge run --port 8080 --with slack

# 5. Validate before deploying
forge validate --strict

# 6. Build + package
forge build
forge package --registry ghcr.io/myorg --tag latest

# 7. Deploy to Kubernetes
docker push ghcr.io/myorg/my-agent:latest
kubectl apply -f .forge-output/deployment.yaml

# Optional: create the platform policy ConfigMap if you want bounds
kubectl create configmap forge-platform-policy \
  --from-file=platform-policy.yaml=./platform-policy.yaml
```

**Read**: `docs/getting-started/quick-start.md`,
`docs/getting-started/your-first-skill.md`,
`docs/deployment/kubernetes.md`,
`docs/deployment/production-checklist.md`.

---

## 16. How to create a skill

A Forge skill is a Markdown file with YAML frontmatter. Two flavors:

- **Binary-backed** — declares dependencies in `requires.bins`; the
  full body is injected into the LLM's system prompt; the LLM uses
  `cli_execute` to drive the binary. Use for skills that orchestrate
  existing CLIs (kubectl, gh, git, terraform).
- **Script-backed** — provides executable scripts under `scripts/`;
  each `## Tool: <name>` becomes a first-class LLM tool the model
  calls directly. Tool name `my_search` → `scripts/my-search.sh`.

Minimal frontmatter:

```yaml
---
name: weather                        # required, kebab-case, max 64 chars
icon: 🌤️                              # required for embedded skills
category: utilities                  # lowercase kebab-case
tags: [weather, forecast, api]       # lowercase kebab-case
description: Weather data skill      # required, one-line summary
metadata:
  forge:
    requires:
      bins: [curl]                   # binaries that must be in PATH
      env:
        required: [WEATHER_API_KEY]
        one_of: []
        optional: []
    egress_domains: [api.openweathermap.org]
    denied_tools: [http_request]     # tools this skill must NOT use
    timeout_hint: 60                 # suggested execution timeout in seconds
---

## Tool: weather_current
Get current weather for a location.

**Input:** location (string) — city name or coordinates
**Output:** Current temperature, conditions, humidity, wind speed
```

Frontmatter also accepts a `metadata.forge.guardrails` block matching
the four hook points described in § 12.7 (`deny_commands`,
`deny_output`, `deny_prompts`, `deny_responses`).

The same frontmatter (name, description, category, tags) is projected
into the agent's published A2A Agent Card under `skills[]` at both
build time and runtime (FWS-1).

**For step-by-step skill authoring, use the
`forge-skill-builder` skill** (`.claude/skills/forge-skill-builder.md`)
— it's the same prompt the `forge ui` Skill Builder uses and walks
through input tables, output schemas, examples, safety constraints,
and script generation.

**Read**: `docs/core-concepts/skill-md-format.md`,
`docs/skills/writing-custom-skills.md`,
`docs/skills/contributing-a-skill.md`.

---

## 17. Audit event reference

Sourced from `forge-core/runtime/audit.go` constants. See
`docs/security/audit-logging.md` for the full per-event field
inventory.

Every event emitted via `EmitFromContext` (the typed helpers —
`EmitLLMCall`, `EmitToolExec`, `EmitInvocationComplete`,
`EmitInvocationCancelled`, the egress allow/block emit, the FWS-3
stamping path) auto-includes optional `trace_id` + `span_id` fields
when OTel tracing is enabled (OTel v1 / Phase 4 / #105). Both use
`omitempty` — tracing-off deploys see byte-identical pre-Phase-4 JSON.

| Event constant | Wire value | When |
|---|---|---|
| `AuditSessionStart` | `session_start` | New task session begins |
| `AuditSessionEnd` | `session_end` | Task session completes (with final state) |
| `AuditToolExec` | `tool_exec` | Tool execution `phase: start` / `phase: end`; carries `tool`, `args_size`, `result_size`, `duration_ms` |
| `AuditEgressAllowed` | `egress_allowed` | Outbound request allowed (with domain, mode, source) |
| `AuditEgressBlocked` | `egress_blocked` | Outbound request blocked |
| `AuditLLMCall` | `llm_call` | LLM provider call complete; `model`, `provider`, `input_tokens`, `output_tokens`, `duration_ms`, `request_id` |
| `AuditLLMCallCancelled` | `llm_call_cancelled` | Streaming call aborted mid-flight; partial usage counts |
| `AuditGuardrail` | `guardrail_check` | Mask / block / warn decision. Fields: `gate` (`input` / `context` / `tool_call` / `output` / `stream` — from library `Result.Gate`), `decision` (`masked` / `warned` / `blocked`), `guardrail`, `category`, `violation_count`, optional `tool`. Opt-in `evidence` (redacted + truncated triggering text) via `FORGE_GUARDRAIL_CAPTURE_EVIDENCE=true`. |
| `AuditScheduleFire` | `schedule_fire` | Cron task triggered |
| `AuditScheduleComplete` | `schedule_complete` | Cron task finished |
| `AuditScheduleSkip` | `schedule_skip` | Cron task skipped (e.g. agent busy) |
| `AuditScheduleModify` | `schedule_modify` | Schedule mutated at runtime |
| `EventAuthVerify` | `auth_verify` | Inbound request authenticated (`provider`, `user_id`, `org_id`, `token_kind`) |
| `EventAuthFail` | `auth_fail` | Inbound request rejected (`reason`, `token_kind`) |
| `EventMCPServerStarted` | `mcp_server_started` | MCP server handshake succeeded |
| `EventMCPServerFailed` | `mcp_server_failed` | MCP server dial / handshake failed |
| `EventMCPServerDegraded` | `mcp_server_degraded` | MCP server in soft-fail |
| `EventMCPToolCall` | `mcp_tool_call` | MCP tool invocation; `server`, `tool`, `args_size` |
| `EventMCPToolResult` | `mcp_tool_result` | MCP tool result; `result_size`, `duration_ms` |
| `EventMCPToolConflict` | `mcp_tool_conflict` | Namespaced tool collision detected |
| `EventMCPTokenRefresh` | `mcp_token_refresh` | OAuth 2.1 token refresh result |
| `EventAgentCardPublished` | `agent_card_published` | Agent Card finalized at startup / hot-reload; `name`, `version`, `protocol_version`, `url`, `skill_count`, `capabilities`, `security_schemes`, `card_size_bytes`, `card_sha256` (FWS-1) |
| `AuditInvocationComplete` | `invocation_complete` | A2A invocation closed; `duration_ms`, `input_tokens_total`, `output_tokens_total`, `llm_call_count`, `model`, `provider` (FWS-3) |
| `AuditInvocationCancelled` | `invocation_cancelled` | A2A invocation cancelled via `tasks/cancel`; classified `reason` + partial token totals (FWS-4) |
| `AuditPolicyLoaded` | `policy_loaded` | One per non-empty policy layer at startup; `layer`, `source`, per-list size counters (FWS-5/6) |
| `AuditPolicyViolationAtBuildTime` | `policy_violation_at_build_time` | `violation_kind`, `offending_value`, `forge_yaml_field`, `layer`, `source` (FWS-5/6) |
| `AuditChannelDeniedByPolicy` | `channel_denied_by_policy` | `channel`, `layer`, `source` (FWS-6) |
| `audit_export_status` | `audit_export_status` | Every 60s when an export sink is configured; per-sink `writes_ok`, `drops_timeout`, `drops_dial`, `connected` (FWS-7) |

Every event also carries `schema_version: "1.0"` (FWS-8) and `seq`
(when emitted inside an invocation scope).

**Read**: `docs/security/audit-logging.md`.

---

## 18. Workstream recap — FWS-1 through FWS-10 + OTel v1

| # | Issue | Title | Doc |
|---|---|---|---|
| **FWS-1** | #85 | A2A 0.3.0 Agent Card conformance — canonical `/.well-known/agent-card.json` path + required fields + auth-chain-derived `securitySchemes` + SKILL.md → `AgentSkill` bridge + `agent_card_published` audit | `docs/reference/a2a-agent-card.md` |
| **FWS-2** | #86, FORGE-2 / #185 | Workflow correlation ID threading — `X-Workflow-ID` (definition) + `X-Workflow-Execution-ID` (per-run) + stage / step / caller headers stamped on every audit event | `docs/security/workflow-correlation.md` |
| **FWS-3** | #87 | Token usage + execution duration emission — OTel-aligned field names, `X-Forge-*` response headers, `invocation_complete` event with totals | `docs/security/audit-logging.md` § Token usage |
| **FWS-4** | #88 | Cancellation signal handling — `tasks/cancel` actually cancels via `context.CancelCauseFunc`; `invocation_cancelled` audit event with classified reason + partial token counts | `docs/security/audit-logging.md` § Cancellation |
| **FWS-5** | #89 | Platform policy enforcement at runtime — workspace-level deploy-time bounds (egress / tools / models / sizes); `forge package` policy-ready manifests | `docs/security/platform-policy.md` |
| **FWS-6** | #90 | Three-layer platform policy + channel scope — system / user / workspace layers compose by union + most-restrictive; `denied_channels` first-class; `forge channel disable/enable` edits the user layer | `docs/security/platform-policy.md` |
| **FWS-7** | #95 | Audit event export capability — Unix Domain Socket sink + HTTP localhost fallback; fire-and-forget; `audit_export_status` periodic per-sink health | `docs/security/audit-logging.md` § Audit Event Export |
| **FWS-8** | #91 | Hardened audit emission — `schema_version` + monotonic `seq` per invocation; default metadata-only invariant pinned by regression test; opt-in `AuditPayloadCapture` with per-field byte caps. Follow-ups: #173 (PR closed the seq gap on `tool_exec` / `session_end` — 3 sites switched from plain `Emit` to `EmitFromContext`) and #174 (PR moved the `SequenceCounter` installation upstream of the auth middleware via a wrapper + `EnsureSequenceCounter` so `auth_verify` / `auth_fail` land seq=1). #175 tracks a follow-up lint to catch future `Emit`-instead-of-`EmitFromContext` drift. | `docs/security/audit-logging.md` § Schema contract / § Sequence numbers |
| **FWS-9** | #100 | Ops logger output stream separation — stdout for `JSONLogger`, stderr stays for audit NDJSON | `docs/security/audit-logging.md` § Streams |
| **FWS-10** | #110 | Rate-limit configurability + orchestration-friendly defaults + cancel exemption — `server.rate_limit:` yaml block + CLI flags + env; `tasks/cancel` exempt from the write bucket by default | `docs/reference/forge-yaml-schema.md` § `server.rate_limit` |
| **OTel v1** | #108 | OpenTelemetry tracing — shipped across phases #101-#107 (PRs #122-#128). Tracer seam → OTLP provider → config resolver + CLI flags → span instrumentation across A2A/executor/LLM/tool → audit ↔ trace cross-link → end-to-end A2A propagation → build-time egress merge. Off by default; reuses the egress-enforced transport. | `docs/core-concepts/observability-tracing.md` |
| **Guardrails audit** | #155, #159, #161 | `guardrail_check` audit emission at all 5 library gates (`input` / `context` / `tool_call` / `output` / `stream`), unified on the library `gate` vocabulary (PR #160 replaced the early `direction` field); opt-in `fields.evidence` via `FORGE_GUARDRAIL_CAPTURE_EVIDENCE`; OTel `guardrail.<gate>` spans symmetric to the audit event with `forge.guardrail.*` attributes and `Error` status on block decisions (PR #167) | `docs/security/guardrails.md`, `docs/core-concepts/observability-tracing.md` § Guardrail spans |
| **Tenancy + entity stamping** | #157, #164 | Top-level `org_id` / `workspace_id` (env + per-request header layer) and `entity_id` / `entity_type=agent` (env / forge.yaml) stamped on every audit event so SIEM filters `(org_id, workspace_id, entity_id)` uniquely identify a deploy. Field names match the guardrails library's MongoDB columns 1:1. | `docs/security/tenancy.md`, `docs/security/audit-logging.md` § Entity stamping |
| **K8s scheduler** | #162 | Hybrid scheduler backend: `scheduler.Backend` interface, `FileBackend` (existing behavior), `KubernetesBackend` (`k8s.io/client-go`, CronJob CRUD), `InCluster()` detection, `forge package` emits `cronjob-*.yaml` + credential-less Secret template + Role/RoleBinding. `forge auth` subcommand for operator token UX. | `docs/deployment/scheduler-kubernetes.md`, `docs/core-concepts/scheduling.md` |

Side issues filed during this run: FWS-9 was filed as a companion to
FWS-7's "stream separation would be cleaner" callout; FWS-10 was filed
during FWS-4 manual testing when the legacy `WriteBurst=3` default
throttled the cancel-burst test.

**Read**: `CHANGELOG.md` (full per-PR detail).

---

## 19. Docs map

```
docs/
├── getting-started/
│   ├── quick-start.md            ← read this first
│   ├── installation.md
│   ├── configuration.md
│   ├── your-first-skill.md       ← then this
│   └── contributing.md
├── core-concepts/
│   ├── how-forge-works.md        ← architecture map (canonical)
│   ├── runtime-engine.md         ← the agent loop
│   ├── hooks.md                  ← BeforeLLMCall / AfterToolExec / …
│   ├── tools-and-builtins.md
│   ├── skill-md-format.md        ← SKILL.md schema
│   ├── channels.md
│   ├── memory-system.md
│   ├── scheduling.md
│   └── observability-tracing.md  ← OTel v1 (#108) — spans, propagation, audit cross-link
├── security/
│   ├── overview.md               ← start here for security
│   ├── trust-model.md
│   ├── authentication.md         ← auth provider chain
│   ├── egress-control.md
│   ├── secret-management.md
│   ├── build-signing.md
│   ├── guardrails.md             ← incl. guardrail_check audit + capture-evidence (#155/#159)
│   ├── platform-policy.md        ← FWS-5 / FWS-6
│   ├── audit-logging.md          ← FWS-3 / FWS-7 / FWS-8 / FWS-9 + entity stamping (#164)
│   ├── tenancy.md                ← org_id / workspace_id / entity_id stamping (#157 / #164)
│   └── workflow-correlation.md   ← FWS-2
├── reference/
│   ├── cli-reference.md          ← every subcommand
│   ├── forge-yaml-schema.md      ← every yaml field
│   ├── a2a-agent-card.md         ← FWS-1
│   ├── environment-variables.md
│   ├── web-dashboard.md
│   ├── framework-plugins.md
│   ├── command-integration.md
│   └── agent-skills-compatibility.md
├── skills/
│   ├── writing-custom-skills.md
│   ├── embedded-skills.md
│   ├── contributing-a-skill.md
│   └── skills-cli.md
├── deployment/
│   ├── docker.md
│   ├── kubernetes.md
│   ├── scheduler-kubernetes.md   ← hybrid scheduler backend (#162) — CronJobs, RBAC, token plumbing
│   ├── monitoring.md
│   └── production-checklist.md
├── mcp/
│   ├── index.md
│   ├── configuration.md
│   ├── cli-reference.md
│   ├── audit-events.md
│   └── troubleshooting.md
├── ui/
│   └── skill-builder-llm.md      ← Web UI Skill Builder LLM config
└── faq.md
```

---

## 20. Recipes — common questions

These are the questions a Forge user or contributor most often asks.
Each pointer says **where to start reading** in this skill file (§N)
and which canonical doc to deep-dive.

| Question | Start | Then |
|---|---|---|
| "How do I deploy an agent with platform-policy bounds?" | § 15 + § 12.3 | `docs/security/platform-policy.md`, `docs/deployment/kubernetes.md` |
| "How do I add a new audit event?" | § 17 | `forge-core/runtime/audit.go` constants, `docs/security/audit-logging.md` § Schema contract — add the constant, the emitter helper, the field-inventory row in the docs table, and a `seq`-stamped test |
| "How do I add a new MCP server to my agent?" | § 6 | `docs/mcp/configuration.md`, `forge.yaml` `mcp.servers[]` |
| "How does `tasks/cancel` actually stop in-flight work?" | § 3 + § 4 + FWS-4 row | `docs/security/audit-logging.md` § Cancellation; `forge-core/runtime/cancellation*.go`; the runner's `CancellationRegistry` + `context.CancelCauseFunc` |
| "What changed in FWS-10?" | § 18 row | `CHANGELOG.md` FWS-10 entry, `docs/reference/forge-yaml-schema.md` § `server.rate_limit`, PR #117 |
| "How do I write a skill that calls a CLI binary?" | § 16 (binary-backed) | Use the `forge-skill-builder` skill; or copy a real binary-backed example like `forge-skills/embedded/k8s-incident-triage/SKILL.md` |
| "How do I write a skill with custom Python logic?" | § 16 (script-backed) | Same — use `forge-skill-builder`; note: shell scripts are preferred; Python only when shell+jq genuinely can't do it |
| "Where do I add a new auth provider?" | § 12.1 | `forge-core/auth/` — implement `Verifier`; register in the chain factory; add the host-to-egress-allowlist auto-extension; add the Agent Card scheme mapping (FWS-1); write `auth_verify` / `auth_fail` audit coverage |
| "Where does the rate-limit body peek for `tasks/cancel` live?" | § 12.5 | `forge-cli/server/a2a_server.go` `isTasksCancel` + the middleware branch; capped at 4 KiB, fail-closed on malformed JSON |
| "How do I add a new builtin tool?" | § 6 | `docs/core-concepts/tools-and-builtins.md`; implement `tools.Tool` under `forge-core/tools/builtins/`; register in `defaultRegistry` |
| "What does the audit `seq` field guarantee?" | § 12.4 + § 17 | `docs/security/audit-logging.md` § Sequence numbers; monotonic 1..N within `(correlation_id, task_id)`; absent on startup events |
| "How do I run only the audit pipeline in tests?" | § 12.4 | `forge-core/runtime/audit_sink_test.go`, `forge-core/runtime/audit_hardening_test.go`; `NewAuditLogger(io.Writer)` is preserved for tests; use `bytes.Buffer` as the sink |
| "Can I disable rate limiting entirely?" | § 12.5 | Don't. Set `WriteRPS` / `ReadRPS` very high in `server.rate_limit` if you trust your network, but anonymous public-facing agents need the limiter for DoS protection |
| "How do I capture LLM prompts in audit for debugging?" | § 12.4 | `AuditPayloadCapture{LLMMessages: true, LLMResponse: true}`; payloads truncated to 16 KiB per field with `…[truncated:N]` markers; route the audit stream to a store appropriate to the captured content's sensitivity |
| "How do I extend `sync-docs` for my new doc?" | n/a | `.claude/commands/sync-docs.md` — add a row to the mapping table mapping the changed code path to the affected doc |
| "How do I enable distributed tracing?" | § 12.9 | `docs/core-concepts/observability-tracing.md`; set `observability.tracing.enabled: true` + `endpoint` in forge.yaml (or `--otel-enabled --otel-endpoint`); collector host is auto-allowlisted at build time |
| "How do I pivot from an audit row to a trace?" | § 12.4 + § 12.9 | Audit rows carry `trace_id` + `span_id` when tracing is on; paste either into Tempo / Jaeger / Honeycomb. `docs/security/audit-logging.md` § Trace cross-link |
| "How do multi-hop A2A traces connect?" | § 12.9 | Phase 5 (#106): the dispatcher extracts the inbound W3C `traceparent` header; outbound HTTP through the egress-enforced transport auto-injects `traceparent` via otelhttp. Both pair to form one connected trace tree |
