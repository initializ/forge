# OWASP ASI Conformance — Phase 0 Recon Notes

Branch: `owasp-asi-conformance`. Date: 2026-07-01. All evidence gathered by
reading source (no edits). Line numbers are from the tree at branch creation.

## Baseline build/test

Workspace is a multi-module `go.work` (Go 1.25): `forge-cli`, `forge-core`,
`forge-plugins`, `forge-skills`, `forge-ui`. `go build ./...` from the repo root
does **not** span workspace modules ("directory prefix . does not contain
modules"), so build/test must be run per module.

Build (per module): **all five OK**.

Test baseline (per module, `go test ./...`):

| Module | Exit | ok pkgs | notest pkgs | Notes |
|---|---|---|---|---|
| forge-core | 0 | 31 | 4 | green |
| forge-cli | **1** | 11 | 7 | one **flaky** failure (see below) |
| forge-plugins | 0 | 4 | 0 | green |
| forge-skills | 0 | 9 | 0 | green |
| forge-ui | 0 | 2 | 1 | green |

**Known baseline flake (pre-existing, NOT caused by this work):**
`forge-cli/runtime` `TestRunner_MockIntegration` — "server did not start within
5s". Reproduces only under full-suite parallel load; passes in isolation
(`go test ./runtime/ -run TestRunner_MockIntegration` → ok 1.557s). Timing
contention against a 5s startup deadline, not a logic failure. Later phases must
show no *new* failures beyond this one.

---

## Security surface inventory (file:symbol — enforcement)

### Egress enforcement — `forge-core/security/`
- `types.go:17-19` — `ModeDenyAll`/`ModeAllowlist`/`ModeDevOpen` (`"deny-all"`/`"allowlist"`/`"dev-open"`).
- `types.go:8-10` — `ProfileStrict`/`ProfileStandard`/`ProfilePermissive`.
- `resolver.go:12` — `DefaultMode()` returns `ModeDenyAll` — **fail-closed default**.
- `resolver.go:15-56` — `Resolve()` merges by mode: deny-all → no domains; dev-open → unrestricted; allowlist → union of explicit `AllowedDomains` + `InferToolDomains(toolNames)` + `ResolveCapabilities(capabilities)`, deduped/sorted (`dedup()` `:76-87`).
- `tool_domains.go:4/:23` — `DefaultToolDomains` map + `InferToolDomains()` (per-tool required domains).
- `capabilities.go:4/:15` — `DefaultCapabilityBundles` + `ResolveCapabilities()` (slack/telegram/msteams bundles).
- `ip_validator.go:61` — `IsBlockedIP()`: always blocks `169.254.169.254/32` (metadata), `127.0.0.0/8`, `::1/128`, `0.0.0.0/8` (`:23-28`); blocks RFC1918 `10/8`,`172.16/12`,`192.168/16`, link-local `169.254/16`, CGNAT `100.64/10`, IPv6 ULA `fc00::/7`, link-local `fe80::/10` when `allowPrivate=false` (`:32-40`). **nil IP fails closed** (`:62-64`).
- `ip_validator.go:90` — `isBlockedIPv6Transition` blocks NAT64/6to4/Teredo embedding of blocked IPv4.
- `ip_validator.go:50/:187` — `ParseStrictIPv4`/`ValidateHostIP` reject octal/hex/packed/leading-zero IPv4 evasions.
- `egress_proxy.go:17-73` — `EgressProxy` localhost-only forward proxy; `OnAttempt(domain, allowed)` callback; `ProxyURL()` for env injection.
- `container.go:7` — `InContainer()`: K8s via `KUBERNETES_SERVICE_HOST`, Docker via `/.dockerenv`. In-container → skip local proxy (NetworkPolicy enforces = **Deployer**).

Subprocess proxy injection:
- `forge-cli/tools/exec.go:147-153` — injects `HTTP_PROXY`/`HTTPS_PROXY` (+lowercase) into skill subprocess.
- `forge-cli/tools/cli_execute.go:309-328` — same, plus `NO_PROXY` bypass for `kubectl`/`helm`.
- `forge-cli/runtime/runner.go:494-557` — split: in-process HTTP always enforced via `NewEgressEnforcer`; local subprocess proxy started only when `!InContainer() && mode != dev-open`.

Egress audit: `audit.go:21/:22` `egress_allowed`/`egress_blocked`; emitted at
`runner.go:503-514` (in-process) and `:546-555` (proxy), fields `{domain, mode[, source]}`.

### cli_execute sandbox — `forge-cli/tools/cli_execute.go`
(Note: builtin lives in **forge-cli**, not `forge-core/tools/builtins/`.) Uses
`exec.CommandContext` directly — **no shell**. Layers:
1. `:95` `NewCLIExecuteTool` — `exec.LookPath` resolves allowlisted binaries at startup; unresolved → `missing`.
2. `:166` — reject shell interpreters even if allowlisted (`deniedShells` `:398`).
3. `:171` — reject binary not in `allowedSet`.
4. `:176` — reject binary with no resolved absolute path.
5. `:183`→`validateArg` — per-arg injection checks.
6. `:407` reject `$(`; `:410` reject backtick; `:413` reject newline/CR; `:417` reject `file://` (case-insensitive).
7. `:192`→`validatePathArg` (`:426`) — reject path args inside `$HOME` but outside `workDir` (blocks `~/.ssh/...`, `../../`).
8. `:199` — `context.WithTimeout`, default 120s (`:59`).
9. `:204` — `exec.CommandContext(ctx, absPath, args...)` argv, never a shell string.
10. `:212`→`buildEnv` (`:268`) — env isolation (PATH/HOME/LANG + explicit passthrough + per-binary cred scoping + proxy).
11. `:220`→`newLimitedWriter` (`:523`) — output cap, default 1 MB per stream (`:62`), `Truncated=true` on overflow.

### Guardrails
Global (interface `forge-core/runtime/guardrails.go`): `CheckInbound` `:25`,
`CheckOutbound` `:30`, `CheckToolCall` `:36`, `CheckToolOutput` `:41`,
`CheckContext` `:52`, `CheckStream` `:62`. Engine impl in
`forge-cli/runtime/guardrails_engine.go` (`:157/:226/:298/:365/:431/:492`).
The four named policy checks (`content_filter`, `no_pii`, `jailbreak_protection`,
`no_secrets`) are the default policy scaffold at
`forge-cli/build/policy_stage.go:25-30`; validated in
`forge-core/validate/forge_config.go:22-26`.
Audit: `audit.go:24` `guardrail_check`; emitted by
`forge-cli/runtime/guardrails_audit.go:128 emitGuardrailEvent` (fields
`decision` ∈ {masked,warned,blocked}, `gate`, `guardrail`, `category`, `tool`,
`violation_count`, `evidence` only if `CaptureEvidence`).

Skill guardrails — `forge-core/runtime/skill_guardrails.go` (four hook points):
`CheckCommandInput` `:108` (deny_commands), `CheckCommandOutput` `:145`
(deny_output block/redact), `CheckUserInput` `:175` (deny_prompts),
`CheckLLMResponse` `:201` (deny_responses). Call sites `runner.go:2238-2258`.
**Caveat:** skill guardrails only `logger.Warn` — they do **not** emit
`guardrail_check` audit events (only `LibraryGuardrailEngine` does).

### Audit — `forge-core/runtime/audit.go`
Event constants incl.: `session_start/end` `:18-19`, `tool_exec` `:20`,
`egress_allowed/blocked` `:21-22`, `llm_call` `:23`, `guardrail_check` `:24`,
schedule events `:25-28`, `auth_verify/fail` `:33-34`, MCP events `:41-47`,
`agent_card_published` `:55`, `invocation_complete` `:61`,
`llm_call_cancelled` `:66`, `policy_loaded` `:74`,
`policy_violation_at_build_time` `:84`, `channel_denied_by_policy` `:102`,
`invocation_cancelled` `:110`, `task_admission_denied` `:136`.
- Monotonic seq: `audit_schema.go:32` `atomic.Int64`; `NextSequence` `:67-73`; stamped `audit.go:635-636`; **per-invocation scope**, not global.
- schema_version: `audit_schema.go:22` const `"1.0"`; set `audit.go:524-525`.
- **Append-only / cryptographic integrity: NOT implemented.** No Ed25519/HMAC/hash-chain on the audit stream. Plain JSONL to writer/stderr. (`card_sha256` `:52` is Agent-Card drift, not log integrity.) — **corrects the plan's "signed audit logs" hypothesis for ASI10/ASI08/ASI09.**
- Metadata-only default enforced by test: `forge-core/runtime/audit_hardening_test.go:207 TestNoPayloadByDefault_LLMCall`. Doc: `docs/security/audit-logging.md:552`.

### Platform policy — `forge-core/security/`
- Layers: `platform_policy_layers.go:48-50` system/user/workspace; `LoadAllPolicyLayers` `:102`.
- Union-of-deny: `platform_policy_enforce.go:176 EffectiveEgressAllowlist`, `:194 EffectiveDeniedTools`, `:228 EffectiveToolCount`.
- Smallest-non-zero-bound: `platform_policy_layers.go:181 MostRestrictiveEgressMax`, `:198 MostRestrictiveToolMax`.
- First-layer-to-deny attribution: `platform_policy_layers.go:138-168 FirstLayerDenying*`; stamped onto `PolicyViolation.Layer/LayerPath` by `EnforcePolicy` `:75`.
- Build-time violation event: `policy_violation_at_build_time` (`audit.go:84`), emitted before runner aborts.

### Skill trust / supply chain — `forge-skills/`
- Trust levels: `contract/types.go:209-214` `TrustBuiltin`/`TrustVerified`/`TrustLocal`/`TrustUntrusted`; ordering `trust/types.go:29 trustOrd`; `DefaultTrustPolicy` accepts `TrustLocal` unsigned (`types.go:15-17`).
- Provenance: `contract/types.go:216-224` `{Source(embedded|local|remote), Trust, Checksum "sha256:...", SignedBy}`.
- Integrity: `trust/integrity.go` `ComputeChecksum` (SHA-256) `:26`, `GenerateManifest`/`VerifyManifest` `:37/:61`.
- Signing: `trust/signature.go` Ed25519 `Sign`/`Verify`/`SignSkill`/`VerifySkill`.
- Build signing: `forge-cli/build/signing_stage.go` `SigningStage` computes SHA-256 of all generated files → `checksums.json`; **Ed25519 signature is OPTIONAL** — only applied when a signing key exists (`:64-91`).
- **Remote-skill tier: NOT IMPLEMENTED.** `"remote"` appears only as a doc-comment enum value in `contract/types.go:218`; no remote loader/fetcher/verifier exists in `forge-skills`. Provenance/registry are embedded + local only.
- **SBOM/AIBOM: NOT emitted** anywhere (no cyclonedx/spdx/sbom/aibom references).
- Skill security scan: `forge-skills/analyzer/scoring.go` risk scoring over categories (egress/binary/env, `SECRET` keyword `:59`); build secret scan `forge-cli/build/secret_safety_stage.go`.

### Inter-agent / A2A — `forge-cli/server/a2a_server.go`
- **No inbound attack surface by default beyond the A2A endpoint**; auth middleware is optional/pluggable (`:70 AuthMiddleware`, applied `:186-187`).
- Per-IP rate limiting: `:37-116` `RateLimitConfig`, default 60/min reads+writes (FWS-10 / issue #110); `defaultRateLimitConfig`.
- Agent Card published event: `agent_card_published` (`audit.go:55`), incl. `card_sha256` drift, `security_schemes`, `protocol_version` 0.3.0.
- **Message signing / anti-replay nonce / timestamp binding: ABSENT** (no nonce/replay/message-signature code in `server/*.go`).
- Identity: external auth chain `FORGE_ORG_ID` / `X-Org-ID` header → `org_id` in verifier request (`forge-cli/runtime/auth_chain*`, `admission_loader.go:30/:89`).

### Memory — `forge-core/memory/`
- **Markdown canonical, index derived:** `manager.go:52` ensures `MEMORY.md` exists; `IndexAll`/`IndexFile` (`:104/:125`) build the vector index *from* the markdown; `vectorstore.go:40 FileVectorStore` is a rebuildable JSON file (`:61` "Corrupted index — start fresh"). **Decay never applies to `MEMORY.md`** — `search.go:108/:168` `if DecayEnabled && Chunk.Source != "MEMORY.md"` — confirming MEMORY.md is the durable canon and daily logs are ephemeral.
- **Index never enters LLM context directly:** retrieval is via `memory_search`/`memory_get` tools (`runner.go:3290-3294`) returning `Chunk.Content` (markdown text `chunker.go:20`), not the vector JSON.
- Recency decay exists: `DecayHalfLife` (`runner.go:3273`, `search.go`).
- **Self-reingestion surface: PRESENT and unguarded** — `memory_compactor.go:374` writes the agent's own summarized `observations` via `AppendDailyLog` → indexed → retrievable. No write-validation, no provenance/trust scoring on writes, no per-tenant namespace (single `MemoryDir`), no trust-based expiry (only recency decay). **Genuine ASI06 gaps.**

### Identity / secrets
- Secrets: chained providers `forge-core/secrets/chain_provider.go` (env → encrypted-file), AES at rest `encrypted_file_provider.go:222-261`. `GetWithSource` `:34` reports origin.
- **Cross-category token-reuse detection: NOT found** as described in the plan hypothesis — record honestly (ASI03).
- Tokens: auth chain verifies per request against external verifier; **no per-invocation task-scoped short-lived token minting** in forge-core (that is Platform's lane — ASI03 #1).

### Agent loop — `forge-cli/runtime/runner.go`
- Cancellation: `:1466 context.WithCancelCause`; `:1506 EmitInvocationCancelled` → `invocation_cancelled` audit event.
- Correlation-id threading: `:1225 GenerateID` → `:1227 WithCorrelationID`; stamped on audit envelope `:510/:1259`; task_id in logs.
- Rate limiting: server-level per-IP (see A2A above).

---

## Red-team tooling status
`cmd/a2a-redteam` and the `agent-redteam` skill referenced in the plan are
**NOT present in this repo** (no matches for `a2a_recon`/`a2a_attack`/
`a2a_grade`/`redteam` in `*.go`/`*.md`). Phase 4's black-box tier must either
build this tooling or be scoped down to the instrumented tier. **Record as a
plan/repo discrepancy — do not assume it exists.**

---

## Discrepancies vs plan hypotheses (to carry into the matrix)
1. `cli_execute` is in **forge-cli**, not `forge-core/tools/builtins/`. Documented "13 layers" map to ~11 concrete guard points in code (enumerated above).
2. Audit logs are **NOT signed / append-only** (plan asserted "signed audit logs" for ASI08/09/10). Only build artifacts (`checksums.json`) get optional Ed25519. This weakens ASI10/ASI09/ASI08 non-repudiation claims — grade accordingly.
3. **Remote skill tier is unimplemented** (confirmed) — ASI04 must record this, not assume verification.
4. **SBOM/AIBOM absent** — ASI04 gap.
5. **Message signing / anti-replay absent** in A2A — ASI07 gap (forge-core/Platform), distinct from transport mTLS (Deployer).
6. **Red-team tooling absent** — affects Phase 4 black-box tier scope.
7. Cross-category secret-reuse detection **not found** — ASI03 note.
