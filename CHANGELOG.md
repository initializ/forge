# Changelog

## Unreleased

### Added

- **Hardened audit emission — sequence numbers + schema version + opt-in
  payload capture (issue #91, FWS-8).** Every audit event now carries
  `schema_version: "1.0"` (the audit schema is documented as a stable,
  additive-by-default contract — version only bumps on removals or
  semantic changes). Every event emitted on behalf of an A2A invocation
  also carries a monotonically increasing `seq` field starting at `1`,
  so consumers detect gaps and reordering by grouping
  `(correlation_id, task_id)`. Sequences are scoped per-invocation;
  startup events (`policy_loaded`, `agent_card_published`,
  `audit_export_status`) omit `seq`. The default audit posture remains
  metadata-only: token counts, sizes, durations, tool names — never raw
  prompt text, completion text, or tool args / results. A new
  `AuditPayloadCapture` config (off by default; opt-in field by field
  via `LLMMessages` / `LLMResponse` / `ToolArgs` / `ToolResult`) lets
  customers who need raw payloads in audit (debug, supervised-learning
  corpora, compliance replay) capture them, with per-field byte caps
  and `…[truncated:N]` markers so a runaway prompt or gigabyte tool
  output cannot bloat one event. A regression test (`TestNoPayloadByDefault_LLMCall`)
  pins the metadata-only invariant — any future caller that smuggles
  raw user content into a default audit event will fail it. Audit-event
  signing is deferred per the issue's architectural recommendation
  ("ship if a customer asks") — sequence numbers cover gap detection
  in the meantime. See
  `docs/security/audit-logging.md#schema-contract-fws-8`.
- **Audit event export capability — Unix Domain Socket sink + HTTP
  fallback (issue #95, FWS-7).** Audit events can now be exported to a
  local Unix Domain Socket (preferred) or localhost HTTP endpoint
  *in addition to* the existing NDJSON-to-stderr stream — letting an
  in-pod sidecar (e.g. the initializ platform receiver) consume audit
  with low latency while preserving stderr as the safety-net fallback.
  Configure via `--audit-socket=/path/to/audit.sock`,
  `--audit-http-endpoint=http://127.0.0.1:9097/v1/audit`, or the
  matching `FORGE_AUDIT_SOCKET` / `FORGE_AUDIT_HTTP_ENDPOINT` /
  `FORGE_AUDIT_WRITE_TIMEOUT` env vars (works on both `forge run` and
  `forge serve start`; flag wins over env). The default zero config is
  unchanged from pre-FWS-7 — stderr only — so existing deployments are
  unaffected. New `coreruntime.Sink` interface with three
  implementations: `writerSink` (the safety net), `socketSink` (UDS
  with lazy reconnect + 50ms per-write timeout + exponential backoff,
  drops on timeout without back-pressuring the emitter), and `httpSink`
  (localhost POST fallback). Per-sink stats counters (`writes_ok`,
  `drops_timeout`, `drops_dial`, `connected`) feed a new
  `audit_export_status` audit event emitted every 60s so operators can
  tail the audit stream itself to confirm export health. Sinks are
  fire-and-forget: buffering is the sidecar's concern. Events leaving
  each sink are byte-identical; no sink transforms the payload. The
  audit event schema, the event types, and the `AuditLogger.Emit()`
  API are unchanged — this is purely an additive transport layer. See
  `docs/security/audit-logging.md`.
- **Three-layer platform policy + channel scope (issue #90, FWS-6).**
  Forge now reads platform policy from three layers at startup
  (`/etc/forge/policy.yaml`, `~/.forge/policy.yaml`, and the path at
  `FORGE_PLATFORM_POLICY` — system, user, and workspace respectively).
  The schema is unchanged from FWS-5 and applies identically at every
  layer; resolution unions deny lists and takes the smallest non-zero
  max-bound across layers ("most restrictive wins"). For audit
  attribution, the first layer (in load order: system → user →
  workspace) to contain an offending value takes credit so operators
  grepping `layer=system` see every sysadmin-enforced violation
  without false positives from per-user overrides. Every audit event
  the policy subsystem emits (`policy_loaded`,
  `policy_violation_at_build_time`, `channel_denied_by_policy`) now
  carries `fields.layer` (`system` / `user` / `workspace`) and
  `fields.source` (the on-disk path). Channel deny is now first-class:
  `denied_channels` in any layer skips the named adapter at startup
  with a `channel_denied_by_policy` event; `forge run --with` filters
  and `forge channel serve` refuses to start a denied target outright.
  Channel skip is non-fatal — the agent runs with the remaining
  channels. New `forge channel disable <name>` and
  `forge channel enable <name>` CLI subcommands edit
  `~/.forge/policy.yaml` by default (the user layer); pass `--system`
  to edit `/etc/forge/policy.yaml` instead (warns when not root). Both
  are idempotent and remove the policy file entirely when the
  resulting document is empty. New `GET /api/user-policy` and
  `PUT /api/user-policy` endpoints in `forge ui` surface all three
  layers (user editable, system + workspace read-only); the agent
  card renders denied channels as locked / dimmed chips and clicking
  an editable chip flips the entry in the user layer.
  **Migration from FWS-6's first cut:** the `disabled_channels:`
  field that briefly shipped in `forge.yaml` was rejected on review —
  channel disable is laptop-level or workspace-level, never agent
  declaration. Move any `disabled_channels:` block from `forge.yaml`
  into `~/.forge/policy.yaml`'s `denied_channels:` (developer scope),
  `/etc/forge/policy.yaml` (laptop-wide), or the workspace ConfigMap
  (deployed-agent). `forge channel disable <name>` does this
  automatically. The `channel_disabled_by_config` audit event was
  retired in the same pass; `channel_denied_by_policy` (with layer
  attribution) carries every skip. See
  `docs/security/platform-policy.md` and
  `examples/platform-policy.yaml`.
- **Platform policy enforcement at runtime (issue #89, FWS-5).** Forge
  agents now accept a deploy-time policy file defining workspace-level
  upper bounds on egress destinations, registered tools, allowed
  models, and configuration sizes. The agent's `forge.yaml` is what it
  claims to do; the platform policy is the ceiling — the agent
  refuses to start when its declaration exceeds the bound. Read via
  `FORGE_PLATFORM_POLICY` env var at startup; absence (or missing
  file) maps to no constraints, fully backward compatible. Two audit
  events: `policy_loaded` once at startup when a non-zero policy is
  active, and `policy_violation_at_build_time` one-per-violation when
  `forge.yaml` conflicts (carrying `violation_kind`, `offending_value`,
  `forge_yaml_field`). Egress allowlist is the set-difference of
  `forge.yaml`'s declaration minus the policy deny list; denied tools
  is the union; user-selected builtins survive `forge.yaml` denies but
  NOT platform-policy denies. **`forge package` Deployment manifests
  are now policy-ready by default** — every generated deployment.yaml
  has the `FORGE_PLATFORM_POLICY` env, the `/etc/forge/policy`
  volumeMount, and an `optional: true` ConfigMap volume referencing
  `forge-platform-policy`. Operators (or platform deployers like
  initializ Command, custom controllers, GitOps tooling) just create
  the ConfigMap to apply bounds; absence preserves today's behavior.
  The ConfigMap itself is **not** generated by `forge package` —
  policy is an operator concern, not a developer concern. New
  `forge validate --platform-policy=PATH` standalone linter for CI
  gating. Schema reserves a `denied_channels` slot for FWS-6 (#90).
  See `docs/security/platform-policy.md` and
  `examples/platform-policy.yaml`.
- **Cancellation signal handling (issue #88, FWS-4).** The A2A
  `tasks/cancel` JSON-RPC method now actually cancels in-flight
  invocations instead of merely flipping the stored task state. A
  per-Runner `CancellationRegistry` tracks every active invocation
  by task ID; the cancel handler signals the registered
  `context.CancelCauseFunc` with a typed reason
  (`workflow_failure` / `cost_limit_exceeded` / `timeout` /
  `external_signal`), which propagates through the executor's ctx.
  The agent loop honors cancellation at the iteration boundary and
  between tool calls within an iteration, so cancellation latency is
  bounded by the current LLM call or tool exec. A new
  `invocation_cancelled` audit event closes every cancelled
  invocation with the classified reason, `duration_ms` up to
  cancellation, and partial token totals consumed before the signal
  (from the FWS-3 `LLMUsageAccumulator`). The A2A response carries
  state `canceled` plus a `cancelled: <reason>` message so the
  orchestrator can react. Cancel-after-complete is idempotent — a
  cancel for a task that already finished returns the stored state
  unchanged rather than corrupting it. `CancelTaskParams` gains an
  optional `reason` field (unknown values are forwarded verbatim to
  audit). The grace-period / hard-cancel concept maps to bounded
  cancellation latency: Go's runtime can't kill a goroutine, so
  Forge honors the signal at the next safe checkpoint and the
  orchestrator-side timeout is its own concern. See
  `docs/security/audit-logging.md#cancellation`.
- **Token usage and execution duration emission (issue #87, FWS-3).**
  Every `llm_call` audit event now carries `input_tokens`,
  `output_tokens`, `model`, `provider`, `duration_ms`, and `request_id`
  captured directly from provider response metadata (Anthropic, OpenAI,
  Ollama via the OpenAI-compatible path, OpenAI Responses). Field
  naming aligns with OTel GenAI semantic conventions
  (`gen_ai.usage.input_tokens` / `gen_ai.usage.output_tokens`) so audit
  consumers can correlate to OTel traces without a translation table.
  When a provider returns no usage (some self-hosted Ollama setups),
  the event flags `tokens_unavailable: true` rather than silent zeros.
  Each `tool_exec` event gains `duration_ms` plus structured arg-shape
  metadata (`args_size`, `result_size`) — raw arg values are not
  emitted (payload stripping is FWS-8's concern). A new
  `invocation_complete` event closes every A2A invocation with total
  wall-clock duration and aggregated `input_tokens_total` /
  `output_tokens_total` / `llm_call_count`. A2A responses now carry
  the same totals inline as `X-Forge-Tokens-In`, `X-Forge-Tokens-Out`,
  `X-Forge-Duration-Ms`, `X-Forge-Model`, `X-Forge-Provider` headers
  so orchestrators can enforce cost ceilings during parallel workflow
  execution without subscribing to the audit stream. Headers populate
  regardless of OTel-tracing state. Cost calculation is deliberately
  not in Forge — Forge emits tokens, the platform applies price tables.
  The new emitters route through `AuditLogger.EmitFromContext` so
  workflow-correlation fields (FWS-2) auto-tag every `llm_call` /
  `tool_exec` / `invocation_complete` event when the inbound request
  carried orchestrator headers. Schema additivity: existing audit
  consumers reading the pre-FWS-3 shape continue to work unchanged. See
  `docs/security/audit-logging.md#token-usage-and-execution-duration`.

  Internal API change as part of this work: `llm.UsageInfo` field
  names were renamed `PromptTokens` → `InputTokens` and
  `CompletionTokens` → `OutputTokens` (JSON tags too) to align with
  OTel GenAI semconv. The type is internal to `forge-core/llm` and not
  consumed outside that package, so no external callers are affected.
- **Workflow correlation ID threading (issue #86, FWS-2).** Forge agents
  now extract orchestration headers — `X-Workflow-ID`,
  `X-Workflow-Stage-ID`, `X-Workflow-Step-ID`, `X-Invocation-Caller` —
  at the A2A dispatch boundary (JSON-RPC + REST handlers) and inject
  them into `context.Context` as
  a `WorkflowContext` value. Every audit event emitted during the
  invocation is then auto-tagged via a new `AuditLogger.EmitFromContext`
  with the matching `workflow_id` / `stage_id` / `step_id` /
  `invocation_caller` fields, letting audit consumers correlate events
  across multiple agents participating in one workflow run. Direct A2A
  invocations (no orchestrator headers) leave the fields unset —
  emitted JSON is byte-for-byte identical to the pre-FWS-2 shape, so
  existing audit consumers keep working. A
  `WorkflowContext.ApplyToHTTPHeaders` helper is exposed for tools
  that want to propagate the headers onto outbound agent-to-agent A2A
  calls; auto-propagation is deliberately off by default to prevent
  leaking workflow identity to third-party APIs. See
  `docs/security/workflow-correlation.md`.
- **A2A 0.3.0 Agent Card conformance (issue #85, FWS-1).** Forge now
  serves a spec-conformant Agent Card at the A2A 0.3.0 canonical path
  `/.well-known/agent-card.json`. The card carries every required A2A
  0.3.0 field — `version`, `protocolVersion` (pinned to `0.3.0`),
  `defaultInputModes`, `defaultOutputModes` — plus `securitySchemes`
  derived from the configured auth chain (`static_token` → HTTP
  bearer, `oidc` → openIdConnect with discovery URL, `gcp_iap` → apiKey
  in header, `aws_sigv4` → custom bearer format, etc.), and emits an
  `agent_card_published` audit event on startup carrying the card's
  identity + size + a sha256 hash so downstream consumers can detect
  config drift. Identical card shape across `forge dev` and deployed
  modes. See `docs/reference/a2a-agent-card.md`.
- **Workspace-level skill-builder LLM config (issue #92).** The `forge ui`
  skill builder now reads its LLM configuration from
  `<workspace>/.forge/ui.yaml` (or `~/.forge/ui.yaml` as a machine-wide
  fallback) instead of borrowing credentials from whichever agent the
  operator picked. The skill-builder LLM is decoupled from any agent's
  runtime LLM, so the same configuration works across every agent in
  the workspace and is usable before any agent has been scaffolded.
  - New `GET` / `PUT` endpoints at `/api/settings/skill-builder` plus a
    Settings modal in the skill-builder UI.
  - New `GET /api/skill-builder/provider` (path-less) for first-run
    detection in an empty workspace.
  - Status banner surfaces the resolution source (`workspace` / `user` /
    `agent_fallback` / `unset`) and a deprecation warning when the
    agent-fallback compat shim resolves.
  - The Settings modal accepts the API key value inline (password field)
    and persists it to `<workspace>/.forge/.env` with mode 0600. An
    auto-generated `<workspace>/.forge/.gitignore` protects the file
    from accidental commits. The key value never appears in `ui.yaml`
    and is never echoed back by the GET endpoint.
  - See `docs/ui/skill-builder-llm.md` for the configuration reference.

### Changed

- **`SkillBuilderCodegenModel` no longer overrides the operator's model
  (issue #92).** The function previously forced `gpt-4.1` for openai and
  `claude-opus-4-6` for anthropic regardless of what the agent (or
  workspace) had configured. The override is removed; the operator's
  chosen model is used verbatim. This unblocks agents pointed at custom
  OpenAI-compatible endpoints (OpenRouter, vLLM, litellm, self-hosted
  Kimi/Llama) where the hardcoded "stronger" model isn't hosted.
- **Skill-builder handlers no longer call `os.Setenv` (issue #92).** The
  pre-#92 handlers leaked the picked agent's `.env` into the `forge ui`
  process's environment via `os.Setenv` calls, which caused cross-agent
  credential stomping when switching agents in the UI. Credentials are
  now threaded as request-scoped values.

### Deprecated

- **Legacy Agent Card path `/.well-known/agent.json` (issue #85).** Still
  served and returns the same body as the canonical
  `/.well-known/agent-card.json`, but now emits a `Deprecation: true`
  response header per RFC 8594 plus a `Link` header pointing at the
  successor path. Scheduled for removal in the release after next.

### Fixed

- **`forge init` Custom provider now produces a runnable agent (issue #83).**
  Picking the **Custom** provider in `forge init` (or the Web UI wizard)
  previously wrote `provider: custom` to `forge.yaml` plus
  `MODEL_BASE_URL` / `MODEL_API_KEY` env vars, neither of which the runtime
  understood — agents fell back to `StubExecutor` and every task failed
  with `agent execution not configured for framework "forge"`. Scaffold
  now normalizes Custom → `provider: openai` + `OPENAI_BASE_URL` /
  `OPENAI_API_KEY`, matching the OpenAI-compatible code path the runtime
  resolver already supports. Affects both TUI and Web UI flows.
- **OAuth-credentials path no longer silently overrides
  `OPENAI_BASE_URL` (issue #83).** When the runtime or skill builder
  found stored ChatGPT OAuth credentials AND no `OPENAI_API_KEY`, it
  ignored an explicitly-set `OPENAI_BASE_URL` and routed traffic to
  `chatgpt.com/backend-api/codex` — manifesting as a 400 from ChatGPT
  rejecting the operator's model name. Both `forge run` and `forge ui`
  now refuse this combination with a clear error explaining what to set.

### Migration

- If you have `provider: custom` in a checked-in `forge.yaml` from an
  earlier `forge init` run, change it to `provider: openai` and rename
  the `.env` keys from `MODEL_BASE_URL` / `MODEL_API_KEY` to
  `OPENAI_BASE_URL` / `OPENAI_API_KEY`. No new `forge init` is required.

## v0.12.0 — Phase 1: MCP integration (HTTP transport) — in progress

### Added

- **Model Context Protocol (MCP) HTTP client support.** Configure servers
  under a new `mcp:` block in `forge.yaml`; discovered tools are
  registered as namespaced `<server>__<tool>` first-class tools that
  flow through the existing LLM executor.
- **`forge mcp` subcommands:**
  - `forge mcp list` — show every configured server, its state, and
    the number of tools it exposes after filtering.
  - `forge mcp test <name>` — connect, list tools, optionally call one
    with `--call <tool> --args '<json>'`.
  - `forge mcp login <name>` — laptop-time OAuth 2.1 PKCE flow.
  - `forge mcp logout <name>` — remove stored OAuth tokens.
- **OAuth 2.1 PKCE** for hosted MCP servers (Linear, Notion, Atlassian,
  GitHub hosted MCP, etc.). Tokens persist via the existing
  AES-256-GCM keyring at `~/.forge/credentials/mcp_<name>.json`
  (encrypted when `FORGE_PASSPHRASE` is set).
- **Audit events** (NDJSON to stderr, no byte payload ever):
  `mcp_server_started`, `mcp_server_failed`, `mcp_server_degraded`,
  `mcp_tool_call`, `mcp_tool_result`, `mcp_tool_conflict`,
  `mcp_token_refresh`.
- **Egress integration.** MCP server hosts auto-merged into the egress
  allowlist (mirroring `auth_domains`) so an HTTP MCP call cannot
  silently be blocked at runtime.
- **Tool namespacing.** `tools.Registry.Register` rejects names
  containing `__` unless the tool implements the new
  `tools.MCPSource` marker interface, preventing builtins from
  shadowing MCP-namespaced tools.

### Removed

- **`mcp_call` adapter tool removed.** Superseded by the new `mcp:`
  configuration block in `forge.yaml`, which exposes each MCP
  server's tools as first-class namespaced tools — strictly better UX
  for the LLM than a single meta-tool. See `docs/mcp/index.md` for
  the migration path.

### Notes

- **Phase 1 supports HTTP transport only.** Stdio MCP servers (Notion,
  Linear community, Atlassian, the modelcontextprotocol/servers
  reference set) are on the roadmap. `transport: stdio` is rejected at
  `forge validate` time with the message
  `"stdio is on the roadmap; Phase 1 supports HTTP transport only"`.
- **MCP protocol version pinned to `2025-06-18`**. Handshake hard-fails
  on mismatch — version negotiation is intentionally absent.
- **OAuth callback** runs on a `127.0.0.1` loopback listener; it is a
  laptop-time operation. For K8s deployments, run
  `forge mcp login <name>` locally, then mount the resulting
  credentials file as a Secret and point `MCP_TOKEN_STORE_PATH` at it.
- **No new top-level dependencies** — JSON Schema validation reuses
  the existing `xeipuuv/gojsonschema` already in `go.mod`.

---

## v0.11.0 — Phase 2: cloud-native auth providers (in progress)

### Added

- **`aws_sigv4` auth provider.** Authenticate AWS-IAM callers by reflecting
  their Sigv4 signature to AWS STS `GetCallerIdentity`. No `aws-sdk-go-v2`
  dependency.
- **`gcp_iap` auth provider.** Verify the JWT IAP forwards as
  `X-Goog-Iap-Jwt-Assertion` when Forge sits behind a GCP HTTPS Load
  Balancer with IAP enabled.
- **`azure_ad` auth provider.** Verify Microsoft Entra ID Bearer tokens
  with tenant lock-in and optional Microsoft Graph group enrichment.
- Non-interactive `forge init` flags for the three new providers:
  `--auth-aws-region`, `--auth-aws-allowed-principal` (repeatable),
  `--auth-gcp-iap-audience`, `--auth-azure-tenant`,
  `--auth-azure-multi-tenant`, `--auth-azure-groups-mode`.
- Web UI exposes the three new types via the `/api/wizard-meta` endpoint;
  server-side validation rejects malformed payloads before scaffold.
- `egress_hosts` automatically extended for each new provider
  (`sts.<region>.amazonaws.com`, `www.gstatic.com`,
  `login.microsoftonline.com`, `graph.microsoft.com` when applicable).

### Changed

- Middleware now consults the auth chain **even when no Bearer token is
  extracted**, so non-Bearer formats (Sigv4 `Authorization`, IAP
  `X-Goog-Iap-Jwt-Assertion`) can be recognized. Existing Bearer + JWT
  flows are unchanged.
- `auth.HeadersFromRequest` widened with `X-Goog-Iap-Jwt-Assertion`
  for `gcp_iap`. Providers that don't consume this header are unaffected.
- `auth.TokenKind` recognizes the `forge-aws-v1.` Bearer prefix and
  returns `"sigv4"`. The audit `token_kind` field now has five possible
  values: `empty`, `opaque`, `jwt`, `sigv4`, `iap_jwt`.
- `validate.ValidateAuthConfig` admits the three new provider types and
  enforces their per-type required keys (`aws_sigv4.region`,
  `gcp_iap.audience`, `azure_ad.audience`, `azure_ad.tenant_id`-unless-
  multi-tenant, `azure_ad.groups_mode` whitelist).

### Notes for upgraders

- **No forge.yaml changes are required** for callers continuing to use
  Phase 1 providers (`static_token`, `oidc`, `http_verifier`). Phase 1
  test suite passes without modification.
- If you wrote a custom provider that inspects headers, the `Headers`
  map now contains additional keys. Existing keys are unchanged.
- The `oidc` package gained an internal `SkipIssuerCheck` field carrying
  `yaml:"-"` — it cannot be set via `forge.yaml` and is reachable only
  from Go callers (currently only `azure_ad` multi-tenant). Operators see
  no change.

### `allowed_accounts` shortcut for whole-account trust

For "any IAM principal in these AWS accounts" without writing
glob patterns:

```yaml
auth:
  providers:
    - type: aws_sigv4
      settings:
        region: us-east-1
        allowed_accounts: ["412664885516", "109887654321"]
```

Internally expands to the canonical glob set covering all identity
shapes (IAM users, IAM roles, STS assumed-roles, federated users)
for each account. Composes with `allowed_principals` — you can list
specific roles AND whole accounts in the same provider entry.

For AWS-Org-wide trust without enumerating accounts, use AWS IAM
Identity Center (SSO) — SSO permission sets gate Org membership at
sign-in, and you can match Identity Center-assumed roles with the
existing `allowed_principals` globs.

### `azure_ad.allowed_tenants` — explicit allowlist for multi-tenant mode

```yaml
auth:
  providers:
    - type: azure_ad
      settings:
        audience: api://forge
        allow_multi_tenant: true
        allowed_tenants:
          - "00000000-1111-2222-3333-444444444444"   # partner A
          - "55555555-6666-7777-8888-999999999999"   # partner B
```

When `allow_multi_tenant: true`, the `tid` claim must be in
`allowed_tenants` (case-insensitive GUID match). Empty list +
multi-tenant remains the documented "any tenant globally" mode for
back-compat, but `forge validate` now emits a warning when the list
is empty to make the trade-off explicit. Non-interactive flag:
`--auth-azure-allowed-tenant` (repeatable).

### TUI wizard supports Phase 2 providers

`forge init`'s TUI picker now includes `AWS Sigv4 (IAM)`,
`GCP Identity-Aware Proxy`, and `Azure AD / Entra ID` entries with
step-by-step input flows. AAD is single-tenant in the TUI;
multi-tenant remains a deliberate YAML edit (security default).

### Client experience for `aws_sigv4`

The client side is a Bearer token with a 3-line mint:

```python
import boto3, base64
url   = boto3.client('sts', region_name='us-east-1').generate_presigned_url(
            'get_caller_identity', ExpiresIn=900)
token = 'forge-aws-v1.' + base64.urlsafe_b64encode(url.encode()).rstrip(b'=').decode()

requests.post(forge_url, headers={'Authorization': f'Bearer {token}'}, data=msg)
```

Pattern is identical to `aws-iam-authenticator` for EKS. Reference client
in `scripts/forge-aws-sign.py` — use it directly or as a template for
Go / Java / Node clients. Wire format is documented in the package
docstring of `forge-core/auth/providers/aws_sigv4/provider.go`.

### Known deferred work

- (none for Phase 2)
