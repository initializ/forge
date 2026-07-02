# Changelog

## Unreleased

### Changed (potentially breaking)

- **Guardrail MODIFY / DENY now apply to every tool, not just
  `cli_execute` (#209 / governance R4a).** Pre-#209 the
  `SkillGuardrailEngine` short-circuited on any tool whose name
  wasn't `cli_execute`, so `deny_commands` / `deny_output` patterns
  silently no-op'd for `web_search`, `http_request`, MCP calls, and
  every custom tool. The short-circuit is removed.
  - **Match-target asymmetry to be aware of**: for `cli_execute`
    the match target is still the reconstructed shell command line
    (`binary arg1 arg2 …`) so existing shell-style patterns keep
    working. For every other tool the match target is the raw
    tool-input JSON as the LLM produced it. See
    `docs/security/policy-decisions.md` for migration guidance.
  - **`GuardrailChecker.CheckInbound` / `CheckOutbound` signatures
    change** to return `(PolicyResult, error)` — the new
    `PolicyDecision` enum (`Allow` < `Modify` < `StepUp` < `Defer`
    < `Deny`, ordered by restrictiveness) is the R4 taxonomy from
    the governance framework. `StepUp`/`Defer` are reserved for R4b
    (#210) / R4c (#211) and not yet emitted; callers today still
    read only Allow / Modify / Deny.
  - Every `guardrail_check` audit event already carried a
    `fields.decision` string; nothing on the audit wire shape
    changes.

### Added

- **Anthropic-format custom URLs + AWS Bedrock SigV4 outbound auth
  (issue #202).** Two-phase rollout:
  - **Phase 1**: `forge init`'s "Custom" provider option now asks
    whether the endpoint speaks OpenAI Chat Completions or Anthropic
    Messages wire format and writes the matching provider into the
    generated `forge.yaml` (`openai` or `anthropic`). Both flows
    accept the same Base URL + API key inputs; the shape picker is
    the only branch. Underlying plumbing already supported
    `provider: anthropic + ANTHROPIC_BASE_URL` symmetric with the
    OpenAI path (#137 / #139); this exposes it through the wizard.
  - **Phase 2**: new `model.auth_scheme: aws_sigv4` + `model.aws_region`
    fields on `ModelRef` (with matching `ClientConfig.AuthScheme` /
    `AWSRegion`) wrap the LLM client's `http.Transport` with an
    AWS SigV4 signer. Credentials resolve via the standard env vars
    (`AWS_ACCESS_KEY_ID` / `_SECRET_ACCESS_KEY` / `_SESSION_TOKEN`).
    Works symmetrically across the openai and anthropic providers
    so an operator can point either at AWS Bedrock or any other
    SigV4-fronted gateway:
    - `provider: anthropic` + `auth_scheme: aws_sigv4` → calls
      Anthropic-shaped endpoints with SigV4 signing instead of
      `x-api-key`; the `anthropic-version` header still rides.
    - `provider: openai` + `auth_scheme: aws_sigv4` → calls
      OpenAI-shaped endpoints with SigV4 signing instead of
      `Authorization: Bearer`; the `OpenAI-Organization` header
      still rides.
  - SigV4 signer is **hand-rolled** (~250 LOC, stdlib only) matching
    the existing `forge-core/auth/providers/aws_sigv4` inbound-auth
    posture — keeps the binary footprint flat (no aws-sdk-go-v2 +
    ~5 MB).
  - `AWS_REGION` env safety-net for the SigV4 path symmetric with
    `OPENAI_BASE_URL` / `ANTHROPIC_BASE_URL` env safety-nets — set
    once at the platform layer instead of in every `forge.yaml`.
  - Bedrock hostnames (`bedrock-runtime.<region>.amazonaws.com`)
    auto-extend the egress allowlist via the existing
    `LLMProviderDomains` parsing — no separate `egress.allowed_domains`
    entry required when `model.base_url` points there.
  - The empty `AuthScheme` default preserves the pre-#202 contract
    byte-for-byte. Existing Anthropic and OpenAI deployments see
    zero behavior change.
  - Web identity tokens / IRSA / EC2 instance metadata resolution is
    NOT yet supported — the credential getter reads env only. Most
    Bedrock deployments today set the AWS env vars at the platform
    layer (via IRSA's env injection, sidecar credential fetcher, or
    `AWS_PROFILE` resolution). STS-based credential refresh is
    tracked as a follow-up.
  - **Out of scope for this PR**: Bedrock-specific URL / body
    rewriting (`/model/<id>/invoke` path + `anthropic_version` body
    field). Operators today either point Forge at a Bedrock-compat
    proxy that handles the translation (litellm, OpenRouter) or wait
    for a Phase 2.5 PR that adds native Bedrock InvokeModel
    passthrough.
  - Pinned by `TestSigV4Transport_{StampsAuthorizationAndAmzDate,
    StampsSecurityTokenWhenTemporary,
    PreservesBodyAndContentLength, DoesNotMutateCallerRequest,
    PropagatesUnderlyingError, MissingCredentialsErrors,
    RequiresRegionAndService, CanonicalQueryOrdering,
    EndToEndAgainstHTTPTestServer}`,
    `TestSigV4CredentialsFromEnv_ParsesStandardVars`,
    `TestAnthropicClient_{DefaultAuthSchemeKeepsXAPIKey,
    SigV4AuthSchemeOmitsXAPIKey, SigV4WrapsTransport,
    DefaultTransportNotWrapped}`,
    `TestOpenAIClient_{DefaultAuthSchemeKeepsBearer,
    SigV4AuthSchemeOmitsBearer, SigV4WrapsTransport,
    OrgIDStillSetUnderSigV4}`,
    `TestLLMProviderDomains_BedrockHostExtracted`,
    `TestNormalizeCustomProvider_{AnthropicShapeRewritesToAnthropic,
    DefaultShapeStaysOpenAI}`.

- **Platform admission hook for per-agent quota / cost-limit gating
  (issue #201).** A new pre-dispatch middleware lets the platform tell
  an agent process to stop accepting new `tasks/send` invocations when
  an agent / workspace / org is over budget — distinct from auth
  (HTTP 401 on bad credentials) and from the per-IP rate limiter
  (HTTP 429 on burst).
  - **Two env vars to engage**: `FORGE_ADMISSION_URL` (the platform's
    admission endpoint) and `FORGE_PLATFORM_TOKEN` (bearer token).
    Both must be set; partial config logs one warn at startup and
    runs without admission. Existing `FORGE_ORG_ID` /
    `FORGE_WORKSPACE_ID` env vars from #157 forward as outbound
    `Org-Id` / `Workspace-Id` headers when set (empty value → header
    omitted entirely).
  - **Baked defaults**: 2s HTTP timeout, 5s decision cache TTL,
    `GET` method. Not env-overridable — keeps the operator surface
    flat.
  - **Wire shape**:
    `GET /v1/admission?agent_id=<id>` with bearer + tenancy headers
    returns `{decision, reason, scope, window, reset_at}`. `decision`
    is `admit` or `deny`; everything else is platform-defined.
  - **Fail-open everywhere**: any failure (timeout, 4xx, 5xx, parse
    error, unknown decision value) produces a logged warn line and
    a cached fail-open admit for the TTL. No `REQUIRED` knob — the
    cascade of "platform degraded → all agents stop serving" is a
    worse production failure than a 5-second quota leak.
  - **Caller-facing on deny**: HTTP 402 Payment Required with
    `Retry-After` (derived from `reset_at`, clamped non-negative)
    and a structured body carrying reason / scope / window.
  - **Pipeline placement**: middleware fires after `auth_middleware`,
    before the dispatcher. Auth runs first so the platform call
    never burns on unauthenticated traffic; admission runs before
    the executor so a denied invocation never reaches the LLM /
    tool stack.
  - **New audit event** `task_admission_denied` carrying
    `fields.reason` / `scope` / `window` / `reset_at` / `cached`.
    `cached` distinguishes "platform actively denied" from "serving
    a few-second-old cached deny" when debugging propagation lag.
  - **New OTel span** `admission.check`, sibling of `auth.verify`
    (issue #187). Attributes:
    `forge.admission.decision` / `.reason` / `.scope` / `.window` /
    `.cached` / `.fallback`. Status=Error on deny. The HTTP call
    nests under it as `http.client` so total + platform latency
    surface cleanly.
  - Pinned by `TestPlatformAdmissionChecker_{AdmitFromPlatform,
    DenyFromPlatform,TenancyHeadersSentAndOmitted,CachesWithinTTL,
    CacheExpires,FailsOpenOnNetworkError,FailsOpenOnPlatform5xx,
    FailsOpenOnAuth4xx,FailsOpenOnMalformedJSON,
    FailsOpenOnUnknownDecision,AppendsAgentIDToExistingQuery,
    TimeoutHonored}`,
    `TestBuildAdmissionChecker_{BothEnvSetReturnsPlatformChecker,
    NeitherEnvSetSilentNoop,PartialConfigWarnsButReturnsNoop}`,
    `TestAdmissionMiddleware_{AdmitPassesThrough,
    DenyReturns402WithStructuredBody,
    DenyClampsNegativeRetryAfter,EmitsAuditEventOnDeny,
    NoopShortCircuits,NilCheckerPasses}`.

- **Three runtime spans for previously-invisible latency / causality
  surfaces: `auth.verify`, `channel.<adapter>.deliver`, `schedule.fire`
  (issue #187).** All three use the existing global `Tracer()` (no new
  install) and respect the off-by-default tracing posture — when
  tracing is disabled the no-op tracer makes them zero-allocation.
  Status=Error on the failure path keeps error-rate dashboards
  consistent across span types.
  - **`auth.verify`** wraps the `Provider.Chain.Verify` call in
    `forge-core/auth/middleware.go`. Provider outbound HTTP calls
    (JWKS / STS / IAP / Graph) now nest under it instead of appearing
    as orphan roots. Attributes: `forge.auth.provider`,
    `forge.auth.token_kind`, `forge.auth.decision`,
    `forge.auth.user_id` / `org_id` (success), `forge.auth.fail_reason`
    (failure). The `auth.FailReason(err) string` helper is exported
    from `forge-core/auth` so the span and the audit `auth_fail` event
    share one reason vocabulary; the forge-cli runtime's local
    `authFailReason` now delegates to it.
  - **`channel.<adapter>.deliver`** wraps the per-message handler in
    every channel adapter (Slack / Telegram / Teams) via a new
    `channels.StartDeliverSpan` helper. The internal A2A POST in
    `forge-cli/channels/router.go` now injects the W3C `traceparent`
    via the global propagator, so the downstream `a2a.tasks/send`
    span nests under the deliver span. Attributes:
    `forge.channel.adapter`, `forge.channel.target`,
    `forge.channel.message_id`, `forge.channel.user_id`. Highest
    user-visible payoff of the three — operators can now answer
    "Slack→agent latency" from the flame graph alone.
  - **`schedule.fire`** wraps `Scheduler.fire` in
    `forge-core/scheduler/scheduler.go` (file backend only).
    Attributes: `forge.schedule.id`, `forge.schedule.cron`,
    `forge.schedule.source` (`yaml` / `llm`). K8s-backend dispatch is
    out of scope for v1 — the trigger Pod is a separate curl-based
    Pod and needs `traceparent` injected into the rendered CronJob
    YAML at `forge package` time (follow-up).
  - Pinned by `TestAuthVerifySpan_{SuccessRecordsProviderTokenKindDecision,
    FailureSetsErrorStatusAndFailReason,
    MissingBearerOpensZeroDurationSpan,
    ParentsProviderHTTPClientSpans}`,
    `TestStartDeliverSpan_{StampsAdapterAndEventAttributes,
    ErrorSetsStatus,AdapterNameDrivesSpanName,
    ChildContextCarriesActiveSpan,NilEventDoesNotCrash}`,
    `TestScheduleFireSpan_{StampsAttributesAndParentsDispatch,
    ErrorSetsStatusError,SourceSurfacesLLMOriginatedSchedules}`.

- **FORGE-1: opt-in auto-propagation of workflow correlation headers
  on outbound HTTP tool calls (issue #186).** Adds a
  `workflow_propagation.allowed_hosts` block to `forge.yaml`. Hosts
  matching the allow-list automatically receive the `X-Workflow-Id` /
  `X-Workflow-Execution-Id` / `X-Workflow-Stage-Id` /
  `X-Workflow-Step-Id` / `X-Invocation-Caller` headers from the
  current request context when invoked from any built-in HTTP tool —
  no per-tool code change needed. Hosts not on the list keep the
  pre-#186 opt-in behavior; the headers stay off so workflow identity
  never leaks to third-party APIs.
  - New `WorkflowPropagationMatcher` mirrors the egress allow-list
    wildcard semantics (exact + `*.suffix.com`, port-stripped,
    lowercase-normalized). Wildcards match strictly-deeper
    subdomains and refuse the apex — `*.agents.internal` does NOT
    match the bare `agents.internal`.
  - New `WrapTransportForWorkflowPropagation` wraps the egress
    `http.Transport` once at runner startup so every HTTP tool
    (`http_request`, `webhook_call`, `web_search_*`, future tools)
    inherits the auto-apply via `security.EgressTransportFromContext`.
    Empty config = the wrapper short-circuits and returns the
    underlying transport identity-equal, zero overhead per request
    on the default-deploy path.
  - The wrapper clones the request before mutating headers (the
    `http.RoundTripper` contract), so a caller's `req.Header` is
    never modified across retries.
  - Builds on FORGE-2 (#185) — the allow-listed propagation set
    includes the new `X-Workflow-Execution-Id` header so downstream
    agents' audit events join 1:1 on per-run timelines.
  - Pinned by `TestWorkflowPropagationMatcher_{Matches,IsEmptyAndNilGuard}`,
    `TestWorkflowPropagationTransport_{AppliesHeadersOnAllowlistedHost,
    OmitsHeadersOnUnlistedHost,NoOpWhenContextIsZero,
    DoesNotMutateOriginalRequest,EndToEnd,
    PropagatesUnderlyingError}`,
    `TestWrapTransportForWorkflowPropagation_EmptyMatcherShortCircuits`,
    `TestNewWorkflowPropagationMatcher_RejectsBadInputCleanly`.

- **FORGE-2: split workflow definition from per-run execution (issue
  #185).** The previously-overloaded `X-Workflow-ID` header now
  carries the workflow DEFINITION id (stable across every run); a new
  `X-Workflow-Execution-ID` header carries the per-run instance id.
  Both surface as top-level fields on every audit event under a
  workflow run (`workflow_id`, `workflow_execution_id`), so SIEM
  consumers can answer "show me every event in this specific run"
  (join on `workflow_execution_id`) AND "top failing workflows" /
  "latency by workflow definition" (group by `workflow_id`) without
  joining on opaque ids. Industry precedent for the split: GitHub
  Actions (workflow + workflow_run_id), Tekton (Pipeline +
  PipelineRun), Argo (Workflow + WorkflowRun).
  - `WorkflowContext` gains `WorkflowExecutionID`;
    `WorkflowContextFromHTTPHeaders` reads both headers,
    `ApplyToHTTPHeaders` writes both for outbound A2A propagation.
  - `AuditEvent` gains `WorkflowExecutionID` with
    `json:"workflow_execution_id,omitempty"`. `EmitFromContext`
    stamps both fields from the request context.
  - New OTel span attribute `forge.workflow.execution.id` stamped on
    `agent.execute` and dispatcher spans, alongside the existing
    `forge.workflow.id`.
  - Both fields use `omitempty` — direct A2A invocations without
    orchestrator headers continue to emit byte-identical JSON to
    pre-FWS-2 consumers. `schema_version` stays `1.0` (additive
    schema-compatible change).
  - Clean break — no backward-compatibility alias (per the issue;
    the contract is pre-production). Orchestrators sending only the
    old `X-Workflow-Id` continue to populate `workflow_id` with run
    semantics; they should switch to the split contract before
    cutting workflow timelines on the new field.
  - Pinned by `TestWorkflowContextFromHTTPHeaders_{DefinitionAndExecutionAreIndependent,ExtractsAllFour}`,
    `TestApplyToHTTPHeaders_PopulatesExecutionID`,
    `TestRoundTripHTTPHeaders`,
    `TestEmitFromContext_{TagsWorkflowFieldsWhenContextHasThem,TagsBothWorkflowDefinitionAndExecution,OmitsWorkflowFieldsWhenContextEmpty}`.

- **Guardrails DB mode hardening: fail-loud + seed helpers + exclusivity
  warning (issue #166).** Three quiet behaviors in the
  `BuildGuardrailChecker` resolution ladder that mismatched what
  operators expect from a "production-grade guardrails" deploy are now
  addressable from the operator surface:
  - New `FORGE_GUARDRAILS_DB_REQUIRED=true` env var: when DB mode is
    selected (`FORGE_GUARDRAILS_DB` set) and the Mongo connect fails,
    the runner logs an Error and returns a non-nil startup error
    instead of silently downgrading to file mode or defaults. Off by
    default for back-compat; recommended ON for platform deployments
    where DB-mode guardrails are security-critical. `BuildGuardrailChecker`
    now returns `(GuardrailChecker, error)` so the runner can propagate
    the failure as a non-zero exit.
  - New `forge guardrails seed-defaults` subcommand: prints
    `DefaultStructuredGuardrails` as JSON suitable for piping into
    MongoDB. Round-trips through `models.StructuredGuardrails` so the
    output is library-consumable verbatim. Closes the
    "DB mode bypasses built-in defaults" footgun — operators have a
    one-line baseline seed.
  - New `forge guardrails validate-db` subcommand: connects to
    `FORGE_GUARDRAILS_DB`, fetches the agent's `AgentConfig` document,
    and reports on baseline coverage (PII config, jailbreak / prompt-
    injection / command-injection thresholds, secret-pattern rule count,
    core gate enablement). Warns when fewer than 5 secret-pattern rules
    are present or PII config is missing. Exits non-zero on missing
    document so CI / deployment hooks can fail rollout.
  - One-shot startup warning when both `FORGE_GUARDRAILS_DB` is set
    AND a `guardrails.json` is present in the workdir. Repo readers
    previously saw the file and assumed it was active; in DB-mode
    deploys it was dead config that drifted. The warning fires through
    the ops logger (not the audit stream) exactly once per process,
    pointing at the specific path being ignored.
  - DB connect timeout in `NewDBGuardrailEngine` trimmed from 10s to
    3s — short enough that a misconfigured URI surfaces during startup,
    long enough to absorb DNS jitter + TLS on a healthy cluster.
  - Pinned by `TestBuildGuardrailChecker_{DBRequired_FailsLoudOnUnreachable,
    DBUnreachable_FallsBackByDefault,DBRequiredAcceptsForgivingParse,
    DBAndFile_WarnsOnce,DBOnly_NoFile_NoExclusivityWarn,
    FileOnly_NoWarn,HonorsCustomGuardrailsPath}`,
    `TestGuardrailsSeedDefaults_RoundTripsThroughLibraryModel`,
    `TestScoreAgentConfig_{FullDefaultsHaveNoWarnings,SnakeCaseCompat,
    EmptyDocFlagsEverything,FewerThan5SecretRulesWarns}`,
    `TestExtractCustomRules_DefensiveOnShape`.

- **Audit payload capture: operator surface + consolidated redact pass
  (issue #163).** FWS-8 raw-payload capture (`AuditPayloadCapture`) is
  now configurable from `forge.yaml` and `FORGE_AUDIT_CAPTURE_*` env
  vars in addition to the existing programmatic `RunnerConfig` path —
  closing the gap that previously made the feature unusable from a
  container deployment without a code change. Capture stays off by
  default; flipping any flag on emits raw `args`, `result`,
  `prompt_messages`, or `completion_text` fields on the corresponding
  `tool_exec` / `llm_call` events.
  - New env vars: `FORGE_AUDIT_CAPTURE_TOOL_ARGS`,
    `FORGE_AUDIT_CAPTURE_TOOL_RESULT`,
    `FORGE_AUDIT_CAPTURE_LLM_MESSAGES`,
    `FORGE_AUDIT_CAPTURE_LLM_RESPONSE`,
    `FORGE_AUDIT_CAPTURE_REDACT` (default `true`),
    `FORGE_AUDIT_CAPTURE_MAX_BYTES` (single-knob 16 KiB default).
  - New `forge.yaml` block under `audit.capture:` with per-field
    `*bool` semantics (nil = fall through to env). Precedence:
    `forge.yaml` > env > default.
  - New `Redact` field on `coreruntime.AuditPayloadCapture`. ON by
    default. When on, captured fields run through the shared
    vendor-secret regex scrub (`PrepareCapturedContent`) BEFORE
    truncation so a token an LLM glued into a `cli_execute` command
    surfaces in audit as `[REDACTED]`, not verbatim. Set `false` only
    when a downstream sink runs its own scrubber.
  - New shared helper `coreruntime.PrepareCapturedContent(s, redact,
    maxBytes)`. The FWS-8 capture hooks (`registerAuditHooks`),
    guardrail evidence pipeline (`prepareEvidence`), and OTel span
    content pipeline (`PrepareSpanContent`) now ALL delegate to this
    one helper so a fix to the regex set propagates to every
    content-capture path. Removes 3 independent copies of the
    vendor-secret regex set.
  - Pinned by `TestAuditPayloadCaptureFromEnv_{Defaults,FlagsParsed,
    RedactEscapeHatch,MaxBytesIsSingleKnob,MaxBytesIgnoresInvalid,
    RejectsZeroAndNegativeMaxBytes}`,
    `TestPrepareCapturedContent_{RedactScrubsVendorTokens,
    NoRedactKeepsSecretsVerbatim,TruncatesAtCap,RedactBeforeTruncate,
    EmptyFastPath,DefaultCap}`,
    `TestPrepareSpanContent_StillDelegatesAndUsesItsOwnDefault`,
    `TestResolveAuditPayloadCapture_{EnvOnly,YAMLWinsOverEnv,
    YAMLNilDoesNotClobberEnv,MaxBytesUniform,AllSurfacesOff}`,
    `TestRegisterAuditHooks_{DefaultPostureOmitsCaptureFields,
    CaptureToolArgs_OnlyStartEventCarries,CaptureRedactsVendorTokens,
    CaptureRedactFalseLeavesSecretsVerbatim,
    CaptureToolResult_Truncates}`.

- **Skill Builder edit mode (issue #193).** The dashboard's Skill Builder
  can now iterate on an already-attached custom skill instead of only
  creating new ones. A new **Skills attached to this agent** panel lists
  each `skills/<name>/SKILL.md` discovered on disk; clicking **Edit**
  loads its current SKILL.md and helper scripts into the editor, primes
  the chat with the existing content, and switches the LLM call to an
  edit-mode prompt that instructs it to preserve `## Tool: <name>`
  headings, default to minimal patches, and emit a `**Changed:**`
  summary. A **Preview changes** Monaco diff modal shows editor-state
  vs disk side-by-side before save. **Confirm save** overwrites the
  existing skill directory in place; helper scripts dropped during the
  edit are removed from disk so the runtime stops discovering them. A
  **Restart agent** banner appears after a successful edit-mode save
  because the running agent's tool registry is captured at startup and
  not live-mutated.
  - New endpoints: `GET /api/agents/{id}/skill-builder/skills`
    (`[]CustomSkillSummary`) and `GET /api/agents/{id}/skill-builder/skills/{name}`
    (`CustomSkillContent`). Both reject path-traversal, slashes,
    backslashes, and non-kebab-case names with `400`.
  - `POST /api/agents/{id}/skill-builder/chat` accepts `mode: "edit"`
    and `editing_name`. The server loads the on-disk SKILL.md itself
    (single source of truth — never trusts UI-provided baseline) and
    appends an `## Edit Mode` trailer to the system prompt.
  - `POST /api/agents/{id}/skill-builder/save` accepts `overwrite: true`
    with `editing_name` matching `skill_name`. Mismatched editing_name
    is rejected at the handler boundary as defense in depth; the writer
    additionally guards against wiping scripts when names don't match.
  - The script loader rejects symlinks whose resolved target escapes
    the skill's own directory so a malicious link to `/etc/passwd`
    never reaches the editor or LLM context.
  - Pinned by `TestListCustomSkills_ReturnsAllForms`,
    `TestGetCustomSkill_{Subdir,Flat,Errors}`,
    `TestReadCustomSkill_SymlinkEscapeRejected`,
    `TestChat_EditMode_PrimesPromptWithExistingSkill`,
    `TestChat_CreateMode_OmitsEditTrailer`,
    `TestSave_OverwriteMismatchedEditingName_Rejected`,
    `TestValidateSkillMD_DuplicateNameSuppression`,
    `TestSaveSkillToDisk_Overwrite_DropsStaleScripts`,
    `TestSaveSkillToDisk_Overwrite_NameMismatch_DoesNotWipeScripts`.

- **Subprocess W3C trace-context propagation + binary-runtime skills (issue
  #182).** Skill / tool subprocesses now receive `TRACEPARENT`,
  `TRACESTATE`, and `BAGGAGE` env vars derived from the parent agent's
  active span, plus a curated `OTEL_*` SDK config subset so the child
  exports to the same collector with consistent sampling. OTel-instrumented
  binaries (infil, an LLM CLI, a Python service) now nest their spans
  under the agent's `tool.<name>` span instead of starting a fresh root.
  `OTEL_EXPORTER_OTLP_HEADERS` is deliberately excluded from the
  passthrough — collector auth tokens are treated as secrets and must be
  declared via SKILL.md `env.optional` like every other credential.
  - Adds `metadata.forge.runtime: binary` to the SKILL.md schema. Binary
    skills exec the first `metadata.forge.requires.bins` entry directly
    (resolved via `exec.LookPath`) — no bash fork, no script file
    required. `runtime: script` (or empty) keeps the legacy
    materialized-bash-script path. Pinned by
    `TestSkillCommandExecutor_TraceparentInjectedWhenCtxHasSpan`,
    `TestSkillCommandExecutor_TraceparentAbsentWhenNoSpan`,
    `TestSkillCommandExecutor_OTelSubsetPassedThrough`,
    `TestNewBinarySkillTool_RunsBinaryDirectly`.
  - When tracing is off the global propagator is a no-op composite —
    subprocess env is byte-identical to pre-#182 deploys.

### Fixed

- **K8s scheduler backend no longer hard-errors when `scheduler.kubernetes.service_url`
  is unset (issue #179).** Pre-fix, an agent deployed in-cluster with a default
  `scheduler.backend: auto` and no explicit `service_url` aborted startup with
  `kubernetes scheduler backend: scheduler.kubernetes.service_url is required` —
  even though the build-time `schedule_manifest_stage` already knew how to default
  the same field to `http://<agent_id>.<namespace>.svc:<port>/`. Runtime now
  mirrors the build-time default: when `ServiceURL` is empty, the constructor
  derives the in-cluster Service DNS using `agent_id` + resolved namespace +
  the runner's listen port (default 8080). Explicit `service_url` overrides
  still pass through untouched, so operators behind an Ingress / Gateway are
  unaffected. Pinned by `TestKubernetesBackend_ServiceURLDefaultDerivation`,
  `TestKubernetesBackend_ServiceURLDefaultPortFallback`, and
  `TestKubernetesBackend_ServiceURLExplicitOverride`.

## v0.14.2 — 2026-06-10

### Fixed

- **`forge build` no longer fails the bundled `code-review` skill on a fresh
  agent (issue #145, PR TBD).** The build's `security-analysis` stage
  evaluated skills against `analyzer.DefaultPolicy` (MaxRiskScore=75), and
  the bundled `code_review_diff` / `code_review_file` skills routinely scored
  100 — three of their declared egress domains (`chatgpt.com`,
  `patch-diff.githubusercontent.com`, `raw.githubusercontent.com`) were not
  in the builtin `trustedDomains` map and racked up 30 points; the 9
  config-knob env vars added another 45. The operator saw only
  `security policy check failed: 2 error(s), 0 warning(s)` — no rule
  detail, no path to the audit JSON, no override knob (only
  `forge skills audit` accepted `--policy`). Three-layer fix:
  - Extended `trustedDomains` with the GitHub-owned content endpoints
    (`raw.githubusercontent.com`, `patch-diff.githubusercontent.com`,
    `gist.githubusercontent.com`, `objects.githubusercontent.com`) and
    `chatgpt.com` (OpenAI product redirect).
  - Capped the env category at 25 points so multi-purpose skills declaring
    many config knobs aren't penalized linearly. Per-item factors are still
    emitted; only the points contribution to the aggregate is bounded.
  - Raised `DefaultPolicy.MaxRiskScore` from 75 → 90 so vetted bundled
    skills clear the default. Operators wanting a stricter posture can
    lower the ceiling in a policy YAML.
  - Added `security.policy_path` to `forge.yaml` and `--policy` to
    `forge build`, mirroring `forge skills audit --policy`. The flag wins
    over the yaml field; both point at the same `analyzer.SecurityPolicy`
    schema. A missing file is a hard load error (no silent fallback).
  - Rewrote the `security-analysis` failure path: per-skill rule + message
    + recommendations + audit JSON path + policy source + remediation hint
    print to stderr; the returned error still includes the violation count
    for programmatic consumers.

## v0.14.1 — 2026-06-09

### Fixed

- **Session persistence no longer poisons followup turns when the LLM returns
  empty content after `finish_reason: length` (issue #131, PR #132).**
  Long-running Slack threads (and any session-persistent channel) succeeded on
  the first message and then failed every subsequent followup with
  `"something went wrong while processing your request, please try again"`.
  The executor wrote the LLM's assistant message to memory unconditionally
  before checking content. When the provider hit `finish_reason: length` and
  returned an assistant turn with empty content AND no tool_calls, that
  invalid-per-OpenAI-spec shape landed in mem. The in-loop empty-response
  recovery papered over it for the current task, but `persistSession` wrote
  the polluted memory to `.forge/sessions/<task_id>.json`. The next request
  recovered it and strict OpenAI-spec providers (Moonshot, hosted OpenRouter,
  OpenAI strict mode) returned HTTP 400. Fix substitutes a placeholder
  content string (`"(continuing — previous response was truncated by output
  token limit)"`) when the LLM returns the bad shape, AND extends
  `sanitizeMessages` on `LoadFromStore` with a new `stripEmptyAssistantTurns`
  pass to rescue sessions already on disk without an `rm` migration.

- **Duplicate user message at the start of every fresh session no longer
  trips strict-mode providers like `gpt-5-nano` (issue #143, PR #144).**
  Same symptom as #131 — `"something went wrong"` on followup — different
  root cause. The runner pre-appends `params.Message` to `task.History`
  before calling `Execute` so SSE clients see the inbound message in the
  in-flight task. The executor's `!recovered` first-interaction path then
  iterated `task.History` AND appended `*msg` separately, producing two
  consecutive identical user turns at the start of every fresh conversation.
  OpenAI reasoning models (`gpt-5-nano`, `o1`, `o3`) and strict
  OpenAI-compatible gateways (Together's Kimi) reject consecutive same-role
  messages with HTTP 400. Fix strips the trailing `task.History` entry when
  it equals `*msg` (new `a2aMessagesEqual` helper) AND extends
  `sanitizeMessages` with a `collapseConsecutiveDuplicates` pass to rescue
  sessions already on disk. The collapse is surgical: only EXACT same-role
  same-content tool-call-free pairs collapse; workflow nudges and
  tool-bearing turns are preserved.

- **The `code-review` skill no longer routes Anthropic-first when both API
  keys are set (issue #133, PR #134).** The skill's `code-review-diff.sh`
  and `code-review-file.sh` scripts picked Anthropic whenever
  `ANTHROPIC_API_KEY` was non-empty, even when the operator's `forge.yaml`
  pointed at an OpenAI-compatible provider (Together.ai, OpenRouter, Groq,
  Fireworks, Anyscale, vLLM, llama.cpp's server) via `OPENAI_BASE_URL` and
  `REVIEW_MODEL` was clearly a non-Anthropic model. Operators with a stale
  `ANTHROPIC_API_KEY` co-resident with a live `OPENAI_API_KEY` in
  `.forge/secrets.enc` got `Anthropic API returned status 401` and assumed
  the skill was broken. Fix adds an explicit `REVIEW_PROVIDER` env var
  (values `anthropic` or `openai`) that wins always. When unset,
  auto-detected from `REVIEW_MODEL` prefix (`claude-*` or `anthropic/*` →
  Anthropic; anything else → OpenAI), then by sole API key, then defaults
  to OpenAI when both are set with no other signal. The
  `OPENAI_BASE_URL`-as-Responses-API-toggle was also wrong (Together,
  OpenRouter, Groq, etc. only implement `/chat/completions`; the OpenAI
  Responses API is proprietary); decoupled into a separate
  `OPENAI_USE_RESPONSES_API=1` opt-in.

- **The `code-review` skill now uses `max_completion_tokens` instead of
  the deprecated `max_tokens` on the OpenAI Chat Completions branch (issue
  #141, PR #142).** OpenAI deprecated `max_tokens` in favor of
  `max_completion_tokens`; reasoning models (`o1`, `o1-preview`, `o3`,
  `gpt-5`) and strict OpenAI-compatible providers (Together.ai's Kimi-K2.6
  series, Moonshot) reject the legacy field with HTTP 400 `"Unsupported
  parameter: 'max_tokens' is not supported with this model. Use
  'max_completion_tokens' instead."`. The Anthropic branch keeps
  `max_tokens` (correct field for Anthropic's API).

- **Skill subprocesses now inherit `OPENAI_BASE_URL`, `ANTHROPIC_BASE_URL`,
  `OLLAMA_BASE_URL`, and `GEMINI_BASE_URL` from the parent env even when
  the SKILL.md doesn't declare them (issue #137, PR #138).** Pre-fix
  `SkillCommandExecutor` built a whitelist-only env where `OPENAI_ORG_ID`
  was always-passed but the standard SDK base-URL pointers were not —
  unless each `SKILL.md` author remembered to declare each variable in
  `env.optional`. Every LLM-calling skill that forgot silently broke for
  OpenAI-compatible deployments. Fix special-cases the four standard SDK
  variables alongside `OPENAI_ORG_ID`, so every skill that uses the
  industry-standard env conventions just works.

- **The CLI wizard now shows `(secrets) — ok` for `one_of` env keys
  encrypted in `.forge/secrets.enc` AND no longer pre-writes a misleading
  `ANTHROPIC_API_KEY=` placeholder when no key is provided (issue #135,
  PR #136).** `forge skills add` didn't validate `one_of` groups at all —
  operators got no confirmation that their encrypted key was detected.
  `forge init`'s fallback wrote `opts.EnvVars[OneOfEnv[0]] = ""` (the
  first key in the list, `ANTHROPIC_API_KEY` for `code-review`), producing
  an empty `.env` line that misled operators about which provider was
  expected. Fix mirrors `RequiredEnv`'s three-source check
  (`os.Getenv` / `.env` / `loadSecretPlaceholders`) for `one_of` groups
  and drops the placeholder pollution.

- **`forge package` and `forge run` now auto-add the LLM provider's
  custom base URL host to the egress allowlist + generated
  `NetworkPolicy` (issue #139, PR #140).** Operators configuring an
  OpenAI-compatible provider via `OPENAI_BASE_URL=https://api.together.ai/v1`
  shipped a `NetworkPolicy` that blocked the provider's hostname —
  deployed agents 401d or timed out depending on which side noticed
  first. Same trap Phase 6 of OTel Tracing v1 fixed for the OTLP
  collector (issue #107), but for the LLM provider. Fix adds
  `ModelRef.BaseURL` and `ModelFallback.BaseURL` fields to the schema and
  two new helpers — `security.LLMProviderDomains` (cfg-driven, used by
  build + runtime) and `security.LLMProviderEnvDomains` (env-driven
  runtime safety net for `OPENAI_BASE_URL` / `ANTHROPIC_BASE_URL` /
  `OLLAMA_BASE_URL` / `GEMINI_BASE_URL`). Both wired into
  `egress_stage.go` and `runner.go` alongside the existing
  `AuthDomains` / `MCPDomains` / `OTelDomain` merges.

## v0.14.0 — 2026-06-09

### Added

- **OpenTelemetry tracing v1 — full end-to-end distributed tracing
  (initiative #108, PRs #122–#128).** Off-by-default OTLP export
  covering A2A dispatch (`a2a.<method>` SpanKindServer), the
  executor loop (`agent.execute`), every LLM completion
  (`llm.completion` with GenAI semconv `gen_ai.usage.*_tokens`),
  every tool call (`tool.<name>`), and every outbound HTTP request
  (auto via `otelhttp` on the egress-enforced transport). New
  `observability.tracing` block in `forge.yaml`, nine `--otel-*`
  CLI flags, and all 10 standard `OTEL_*` env vars
  (`OTEL_EXPORTER_OTLP_*`, `OTEL_TRACES_SAMPLER`, `OTEL_SERVICE_NAME`,
  ...) honored with full precedence (CLI > env > yaml > defaults).
  Audit events emitted via `EmitFromContext` carry `trace_id` +
  `span_id` so operators pivot audit row ↔ trace tree with a single
  copy-paste (`omitempty` — tracing-off audit JSON is byte-identical
  to pre-Phase-4). The composite W3C `tracecontext + baggage`
  propagator is installed at startup; the dispatcher extracts inbound
  `traceparent` and outbound HTTP re-injects it, so multi-hop A2A
  flows display as one connected trace. The OTLP HTTP exporter rides
  through the same egress enforcer as every other in-process client
  — a misconfigured collector URL cannot exfiltrate spans. `forge
  package` and `forge run` auto-add the collector hostname to
  `egress_allowlist.json` so the generated NetworkPolicy admits
  OTLP traffic with no second egress edit. Phase 3 ships
  metadata-only; content capture (the `capture_content` knob) is
  a follow-up that will reuse the FWS-8 audit redactor. See
  `docs/core-concepts/observability-tracing.md` for the full
  reference. Issue breakdown: #101 (Phase 0, seam, PR #122), #102
  (Phase 1, OTLP provider, PR #123), #103 (Phase 2, config wiring,
  PR #124), #104 (Phase 3, span instrumentation, PR #125), #105
  (Phase 4, audit cross-link, PR #126), #106 (Phase 5, inbound
  propagation, PR #127), #107 (Phase 6, build-time egress, PR #128).

- **Per-IP rate-limit configurability — `server.rate_limit` block +
  CLI flags + env vars (issue #110, FWS-10).** The A2A server's
  per-IP rate limiter (originally from issue #31) is now configurable
  end-to-end via a new top-level `server.rate_limit:` block in
  `forge.yaml`, matching `--rate-limit-*` CLI flags on both `forge run`
  and `forge serve start`, and `FORGE_RATE_LIMIT_*` env vars. Resolution
  order is per-field: CLI > env > yaml > defaults. Five fields exposed:
  `read_rps`, `read_burst`, `write_rps`, `write_burst`, `cancel_exempt`.
  See `docs/reference/forge-yaml-schema.md#serverrate_limit--per-ip-a2a-rate-limits-fws-10`.

### Fixed

- **`tasks/send` now rejects malformed messages at the entry point with a
  clear diagnostic instead of letting the executor produce a confused
  reply (issue #119).** The most common failure was a client sending the
  pre-0.3.0 `"type": "text"` discriminator instead of the A2A 0.3.0
  spec-correct `"kind": "text"`. `encoding/json` silently dropped the
  unknown `type` field, leaving `Part.Kind` empty; the executor then
  produced a reply like *"It looks like your message didn't come
  through"* — confusing the caller about what actually went wrong.
  New `Part.Validate()` and `Message.Validate()` (in `forge-core/a2a`)
  catch this case along with empty `Kind` on a populated part,
  unknown `Kind` values, missing `role`, and empty `parts` arrays.
  The validator is invoked on all four `tasks/send` entry points
  (JSON-RPC sync + SSE, REST sync + SSE) and returns
  `JSON-RPC -32602 InvalidParams` / `HTTP 400` with the spec
  divergence named in the message text — e.g. *"parts[0]: part kind
  is required (A2A 0.3.0); got empty kind with non-empty content —
  did you send `\"type\"` instead of `\"kind\"`? `\"type\"` is from
  the pre-0.3.0 dialect and is silently ignored by the decoder"*.
  Operators see a structured `Warn` log
  (`tasks/send rejected: invalid message shape` with `task_id`,
  `reason`, and `remote_addr`) so they can grep for clients still
  emitting the legacy shape. Sentinel errors
  (`ErrPartKindMissing`, `ErrPartKindUnknown`, `ErrMessageRoleMissing`,
  `ErrMessagePartsEmpty`) are exposed for callers that want to branch
  on the specific cause without parsing strings.

### Changed

- **A2A server rate-limit defaults bumped for orchestrated workloads
  (issue #110, FWS-10).** Write limits raised from `10/min` + burst `3`
  to `60/min` + burst `20`. The old defaults predated parallel workflow
  execution and cron bursts — a 10-step parallel stage was getting
  serialized after the 3rd dispatch. **`tasks/cancel` is now exempt
  from the write bucket by default** (configurable via
  `cancel_exempt: false`). The cost-ceiling cancel-burst case
  (orchestrator firing N parallel cancels when a workflow budget trips)
  was hitting `-32603: rate limit exceeded` at exactly the moment
  cancellation matters most — that's the failure mode FWS-4's manual
  test surfaced. Read defaults (`60/min`, burst `10`) unchanged.
  Operators with stricter threat models can lock down via the new
  config surface (example for a public-facing agent in the schema docs).
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

- **`forge run` / `forge serve` ops logs now write to stdout (was
  stderr) — stream separation from audit (issue #100, FWS-9).** The
  structured `JSONLogger` (`r.logger.Info/Warn/Error`: startup banner,
  request lines, runtime errors) now writes to **stdout**. Audit NDJSON
  continues to write to **stderr** (and to the dedicated FWS-7 sink
  when configured). Container log collectors and SIEM pipelines can
  now split ops from audit at the stream level — no payload parsing
  needed. **Operator migration:** if you previously captured ops logs
  via `forge run 2> ops.log`, switch to `forge run > ops.log` (and
  `2> audit.log` for audit). Container deployments that capture both
  streams via the runtime's standard log collector are unaffected.
  Interactive CLI commands (`forge init`, `forge build`,
  `forge channel`) still write user-facing warnings + errors to
  stderr — those are UX messages, not server ops logs, and the
  stream-split policy doesn't apply. See
  `docs/security/audit-logging.md#streams-fws-9`.
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
