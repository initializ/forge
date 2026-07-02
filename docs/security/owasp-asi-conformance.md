# OWASP Top 10 for Agentic Applications (ASI 2026) — Forge Conformance Matrix

**Standard:** OWASP Top 10 for Agentic Applications 2026 (ASI01–ASI10), Dec 2025.
**Scope of claim:** what the Forge **runtime + control/audit boundary** enforces
in-process. Not a claim about model refusal behavior.
**Grading:** `Enforced` (control fires, provable by an instrumented signal) /
`Partial` (some guidelines met, material gaps remain) /
`Deployer` (operator's job: NetworkPolicy, mTLS, IAM) /
`Gap` (not implemented). Grades are derived from code, not from prior hypotheses.
Evidence line numbers are from branch `owasp-asi-conformance` at creation.

**Containment rule:** a cell may claim a control "fires" only when an
instrumented signal proves it (audit event / policy error / blocked-egress
record), never from absence of bad output alone.

## Summary

| ASI | Title | Locus | Grade | Δ vs hypothesis |
|---|---|---|---|---|
| ASI01 | Agent Goal Hijack | forge-core + Model | **Partial** | = (but "locked prompt" claim falsified) |
| ASI02 | Tool Misuse & Exploitation | forge-core | **Enforced** (w/ 1 shared gap) | = |
| ASI03 | Identity & Privilege Abuse | forge-core + Platform | **Partial** | = |
| ASI04 | Agentic Supply Chain | forge-core | **Partial** | = (remote tier confirmed absent) |
| ASI05 | Unexpected Code Execution | forge-core | **Enforced** | = |
| ASI06 | Memory & Context Poisoning | forge-core | **Partial** | = |
| ASI07 | Insecure Inter-Agent Comms | forge-core + Deployer | **Partial** | = |
| ASI08 | Cascading Failures | forge-core + Platform | **Partial** | = (audit non-repudiation weaker than hypothesized) |
| ASI09 | Human-Agent Trust Exploitation | forge-core + Human | **Partial** | = |
| ASI10 | Rogue Agents | forge-core + Platform | **Partial** | = (audit not signed — weaker than hypothesized) |

Full matrix row (Locus / Grade / Evidence / guidelines met / gaps) follows per entry.

---

## ASI01 — Agent Goal Hijack · Locus: forge-core + **Model** · Grade: **Partial**

**Evidence (met):**
- Guardrails on untrusted NL: `jailbreak_protection` + `content_filter` scaffold
  (`forge-cli/build/policy_stage.go:29,25`), enforced via
  `LibraryGuardrailEngine` gates (`forge-cli/runtime/guardrails_engine.go:157`
  inbound / `:431` context). **Audit:** `guardrail_check` with
  `decision: blocked` (`forge-core/runtime/audit.go:24`;
  `guardrails_audit.go:128`).
- Blast-radius cap via fail-closed egress allowlist
  (`forge-core/security/resolver.go:12` default deny-all;
  `resolver.go:15` `Resolve`). **Audit:** `egress_blocked` (`audit.go:22`).
- Least tool privilege: `denied_tools` union + tool inference
  (`security/tool_domains.go:23`). **Audit:** `tool_exec` (`audit.go:20`).
- Correlation-id threading through audit (`runner.go:1225-1259`) — baseline for
  detecting goal drift across a task.

**Guidelines met:** #1 (untrusted NL → guardrails), #2 (least privilege on tools),
#7 (logging/baseline → audit).

**Gaps (not met):** #3 **partially falsified** — there is **no compiled/locked
system prompt artifact**; the runtime re-globs `SKILL.md` on every startup
(`forge-cli/build/skills_stage.go:63-70`, issue #147). Prompt immutability
therefore rests on `SKILL.md` integrity (trust checksums), not a locked
`prompt.txt`. #4 (runtime intent validation before goal-changing actions),
#5 (signed intent capsule), #6 (CDR / prompt-carrier detection on connected
data) are **absent**.

**Model limitation:** instruction/data non-separation is inherent to the LLM.
Forge bounds blast radius; it does not "solve" prompt injection.

**Δ note:** grade unchanged (Partial), but the ASI01 hypothesis' "locked prompt
at build (`compiled/prompt.txt`)" is **false** — code deliberately omits it.

---

## ASI02 — Tool Misuse & Exploitation · Locus: forge-core · Grade: **Enforced** (one shared gap → ASI09)

**Evidence (met):**
- `cli_execute` no-shell exec + binary allowlist + startup `LookPath`
  (`forge-cli/tools/cli_execute.go:95,166,171,176,204`); arg-injection rejection
  of `$(`/backtick/newline/`file://` (`:407,410,413,417`); `$HOME`-escape path
  confinement (`:426`); 1 MB output cap (`:523,62`); timeout (`:199`).
- Per-tool egress allowlists + capability bundles
  (`security/resolver.go:44-52`, `capabilities.go:15`). **Audit:**
  `egress_blocked` (`audit.go:22`).
- Policy `denied_tools` union + `max_tool_count` bound
  (`security/platform_policy_enforce.go:194,228`).
- Tool-output guardrail scan (`guardrails_engine.go:365 CheckToolOutput`).
  **Audit:** `guardrail_check` (`audit.go:24`).
- Tool invocation logging: `tool_exec` (`audit.go:20`).

**Guidelines met:** #1 (least agency/privilege), #3 (execution sandbox + egress),
#4 (PEP/PDP — via policy layer), #8 (immutable-*ish* tool logs — but see ASI10
non-repudiation caveat).

**Gaps:** #2 (action-level human approval + dry-run diff for destructive actions)
— **absent**; this is the same gap as ASI09 #1 (deduplicated in the gap
register). #7 (MCP fully-qualified tool-name pinning / typosquat-resistant
resolution that fails closed) — not verified present; MCP tool names are not
version-pinned.

**Discovered by the conformance suite (GAP-PATH):** `cli_execute` path
confinement (`cli_execute.go:426 validatePathArg`) blocks arguments that resolve
inside `$HOME` but outside `workDir` (e.g. `~/.ssh/id_rsa` is rejected), but it
does **not** jail an allowlisted reader to `workDir` for arbitrary relative
traversal to files outside `$HOME`. Instrumented proof:
`TestASI02_ToolMisuseContained` observes `cat ../../../../../../etc/passwd`
executing and returning `/etc/passwd` (9344 bytes, exit 0) with `workDir` set to
a temp dir. Injection/allowlist/shell containment is 9/9 (1.00); this is a
distinct path-confinement scope limitation, reported honestly rather than folded
into the Enforced rate. See gap register `GAP-PATH`.

**Δ note:** confirmed strongest entry for injection/allowlist/shell/HOME-escape
(measured 1.00 containment). Graded `Enforced` for those; the destructive-action
HITL gap and the newly-discovered `GAP-PATH` workDir-escape are tracked
separately, not hidden inside the Enforced rate.

---

## ASI03 — Identity & Privilege Abuse · Locus: forge-core + **Platform** · Grade: **Partial**

**Evidence (met):**
- Per-agent org identity via external auth chain: `FORGE_ORG_ID` / `X-Org-ID`
  header → `org_id` in verifier request
  (`forge-cli/runtime/admission_loader.go:30,89`; auth chain
  `forge-cli/runtime/auth_chain*`). **Audit:** `auth_verify`/`auth_fail`
  (`audit.go:33-34`).
- Secrets isolation: chained providers env→encrypted-file
  (`forge-core/secrets/chain_provider.go`), AES at rest
  (`encrypted_file_provider.go:222-261`), origin via `GetWithSource` (`:34`).

**Guidelines met:** #2 (isolate identities/contexts), partial #6 (external IdM —
Platform lane).

**Gaps:** #1 (task-scoped short-lived tokens per invocation) — **absent** in
forge-core; auth verifies a caller-supplied token, it does not mint per-task
tokens. #3 (per-action re-authorization), #5 (OAuth-bound signed intent),
#8/#9 (detect delegated/transitive elevation across agents) — **Platform** lane.
Cross-category token-reuse detection (plan hypothesis) **not found** in code.

**Δ note:** single-agent identity is real; delegation-chain controls are
correctly attributed to Platform, not counted as forge-core gaps.

---

## ASI04 — Agentic Supply Chain · Locus: forge-core · Grade: **Partial**

**Evidence (met):**
- Autowire trust pipeline + trust levels
  (`forge-skills/contract/types.go:209-214`;
  ordering `forge-skills/trust/types.go:29`).
- Provenance with SHA-256 checksum + optional signer
  (`contract/types.go:216-224`; `trust/integrity.go:26 ComputeChecksum`,
  `:61 VerifyManifest`).
- Build integrity: `checksums.json` SHA-256 over all generated files, **optional**
  Ed25519 signature (`forge-cli/build/signing_stage.go:36-105` — signature only
  when a key is present, `:64-91`).
- Skill secret scan: `forge-cli/build/secret_safety_stage.go`;
  risk scoring `forge-skills/analyzer/scoring.go`.

**Guidelines met:** #1 partial (provenance/attestation — checksums always,
signatures optional; **BOM half absent**), #2 (dependency gatekeeping via trust
scan), #3 (sandboxed build), #7 (content-hash pinning).

**Gaps:** SBOM/**AIBOM** emission — **absent** (no cyclonedx/spdx/sbom/aibom in
tree). Remote-skill signature **verification** — **NOT IMPLEMENTED**: `"remote"`
is only a doc-comment enum value (`contract/types.go:218`); no remote
loader/verifier exists. #8 (supply-chain kill switch across deployments) —
absent. Seeds: Appendix D (postmark-mcp, Amazon Q, nx/debug).

**Δ note:** trending Enforced for local/embedded skills; remote tier confirmed
unshipped — recorded honestly, not assumed.

---

## ASI05 — Unexpected Code Execution (RCE) · Locus: forge-core · Grade: **Enforced**

**Evidence (met):**
- No-shell exec: `exec.CommandContext(ctx, absPath, args...)`
  (`cli_execute.go:204`); shell interpreters rejected even if allowlisted
  (`:166`, `deniedShells :398`).
- Binary allowlist + startup `LookPath` (`:95,171,176`).
- `file://` blocked (`:417`); `$HOME`-escape path confinement (`:426`).
- No `eval` surface: Forge runtime is a compiled Go binary — no dynamic-eval path
  (architectural; guideline #3 N/A by design).
- Non-root container: `Dockerfile:46 adduser`, `:50 USER forge`.
- Env isolation (`buildEnv :268`); output cap + timeout.

**Guidelines met:** #3 (ban eval — N/A by architecture, documented), #4 (exec-env
security: non-root, sandbox), #5 (separate generation from execution — LLM emits
args, never a shell string), #6 (allowlist under version control), #7 (static
scan + runtime audit via `tool_exec`).

**Gaps:** dependency-lockfile-poisoning guard for agents that regenerate
lockfiles (low residual). Confirmed **no code path shells to forge-core as a
binary** (library-only invariant holds). Seeds: Appendix D (Replit, Cursor,
Figma MCP).

---

## ASI06 — Memory & Context Poisoning · Locus: forge-core · Grade: **Partial**

**Evidence (met):**
- **Markdown canonical, index derived & rebuildable:** `manager.go:52` ensures
  `MEMORY.md`; index built *from* markdown (`IndexAll :104`, `IndexFile :125`);
  `FileVectorStore` JSON is rebuildable (`vectorstore.go:61` "start fresh" on
  corruption). `MEMORY.md` never decays — `search.go:108,168`
  (`Source != "MEMORY.md"`), proving it is the durable canon.
- **Index never enters LLM context directly:** retrieval via `memory_search` /
  `memory_get` tools returning `Chunk.Content` markdown (`chunker.go:20`;
  `runner.go:3290-3294`), not vector JSON.
- Recency decay for daily logs (`DecayHalfLife`, `runner.go:3273`).

**Guidelines met:** #1 partial (secrets encrypted at rest), #3 (session-scoped
memory dir).

**Gaps (genuine, buildable):** #2 (scan memory writes before commit) — absent.
#5 (provenance/source attribution on entries) — `Chunk` has `Source` (file) but
no trust/origin attribution. #6 (**prevent re-ingestion of the agent's own
outputs**) — **absent and live**: `memory_compactor.go:374 AppendDailyLog` writes
the agent's own summarized observations → indexed → retrievable
(bootstrap-poisoning path). #7/#8/#9 (per-tenant namespaces, trust scores,
trust-based decay/expiry, two-factor surfacing of high-impact memory) — absent
(only recency decay exists). Strong ASI06 backlog candidate.

---

## ASI07 — Insecure Inter-Agent Communication · Locus: forge-core + **Deployer** · Grade: **Partial**

**Evidence (met):**
- A2A 0.3.0 Agent Card with auth-derived security schemes; **no inbound attack
  surface by default** beyond the A2A endpoint; optional auth middleware
  (`forge-cli/server/a2a_server.go:70,186-187`). **Audit:**
  `agent_card_published` incl. `card_sha256` drift, `protocol_version`
  (`audit.go:55`).
- Per-IP rate limiting (`a2a_server.go:37-116`, FWS-10 / issue #110).

**Guidelines met:** #4/#6 (protocol/version pinning — A2A 0.3.0), partial #8
(agent-card drift detection via `card_sha256`).

**Gaps:** #1 (secure channels — **Deployer**: mTLS mesh + generated
`NetworkPolicy`; **not** a forge-core gap). #2 (message signing + intent-diffing)
— **absent**. #3 (anti-replay nonce/timestamp bound to task window) — **absent**.
#9 (typed schema validation failing closed on down-conversion) — partial.
No nonce/replay/message-signature code exists in `server/*.go`.

**Δ note:** be explicit — transport encryption between pods is **Deployer**;
message-level integrity/anti-replay is a legitimate forge-core/Platform gap.

---

## ASI08 — Cascading Failures · Locus: forge-core + **Platform** · Grade: **Partial**

**Evidence (met):**
- Per-IP rate limiting + `tasks/cancel` exemption (`a2a_server.go:37-116`;
  `runner.go:1064`).
- Cancellation: `context.WithCancelCause` (`runner.go:1466`) →
  `invocation_cancelled` (`audit.go:110`; emit `runner.go:1506`).
- Blast-radius cap via egress allowlist (`resolver.go:12`).
- Audit schema + monotonic per-invocation `seq` (`audit_schema.go:22,32,67`).

**Guidelines met:** #2 (isolation/trust boundaries), partial #3 (policy-as-code
per call), #6 (rate limiting), #10 (logging — but see non-repudiation caveat).

**Gaps:** #4 (external planner/executor policy engine) — **Platform**. #7
(blast-radius quotas / progress caps / circuit breakers between planner and
executor) — **absent**. #8 (behavioral/governance drift detection) — absent.
#9 (digital-twin replay gating) — Platform. **Non-repudiation caveat:** audit is
**not signed/append-only** (see ASI10), so guideline #10 is weaker than the
hypothesis assumed.

**Δ note:** single-agent fan-out is limited; multi-agent cascade governance is
Platform. Grade Partial; audit integrity downgraded vs hypothesis.

---

## ASI09 — Human-Agent Trust Exploitation · Locus: forge-core + **Human** · Grade: **Partial**

**Evidence (met):**
- Guardrails (`guardrails_engine.go`), audit trail (`audit.go`), egress +
  provenance annotations in the egress allowlist.

**Guidelines met:** partial #2 (audit logs — but **not immutable/signed**),
partial #6 (content provenance via source annotations).

**Gaps:** #1 (explicit confirmation / HITL before sensitive actions) — **absent**;
**same gap as ASI02 #2** (deduplicated). #7 (separate preview from effect — block
state-changing calls during preview) — absent. #9 (plan-divergence detection vs
approved baseline) — absent.

**Human limitation:** ASI09 is human-side over-reliance; Forge can surface
signals but cannot fix over-trust. Stated, not overclaimed.

---

## ASI10 — Rogue Agents · Locus: forge-core + **Platform** · Grade: **Partial**

**Evidence (met):**
- Kill switches: `tasks/cancel` handler (`runner.go:1405`); policy `denied_*`
  (`platform_policy_enforce.go`); `forge channel disable`
  (`forge-cli/cmd/channel.go:476`). **Audit:** `invocation_cancelled`
  (`audit.go:110`), `channel_denied_by_policy` (`audit.go:102`).
- Container sandbox + non-root (`Dockerfile:50`).
- **Behavioral manifest:** declared `SKILL.md` tools + egress allowlist function
  as the integrity baseline (declared capability = expected behavior;
  `contract/types.go`, egress resolver).

**Guidelines met:** partial #1 (governance/logging — but logs **not signed**),
#2 (isolation/trust zones), #4 (containment/kill-switch), partial #5 (declared
behavioral manifest via skills/egress).

**Gaps:** **Audit is NOT cryptographically signed / append-only** — no
Ed25519/HMAC/hash-chain on the audit stream (only build `checksums.json` is
optionally signed). This **falsifies the hypothesis' "signed audit logs"** for
ASI10. #3 (watchdog/collusion detection) — **Platform**. #5/#6 (continuous
behavioral-integrity verification against the declared manifest; periodic
attestation; orchestrator-mediated signing) — **absent**. #7 (recovery/
reintegration baselines) — absent. Natural home for a red-team detection
counterpart (note: `agent-redteam`/`cmd/a2a-redteam` tooling referenced by the
plan is **not present in this repo**).

**Δ note:** grade Partial; audit non-repudiation is materially weaker than the
hypothesis claimed — the "signed audit logs" evidence does not exist.

---

## Cross-cutting discrepancies (code vs plan hypotheses)

1. **No compiled/locked `prompt.txt`** (ASI01 #3) — runtime re-globs `SKILL.md`
   (`skills_stage.go:63-70`, #147). Immutability rests on SKILL.md checksums.
2. **Audit stream is not signed/append-only** (ASI08/09/10) — only build
   artifacts get optional Ed25519. Non-repudiation claims downgraded.
3. **Remote skill tier unimplemented** (ASI04) — `"remote"` is a doc string only.
4. **SBOM/AIBOM absent** (ASI04).
5. **A2A message signing / anti-replay absent** (ASI07) — distinct from Deployer
   mTLS.
6. **Red-team tooling (`cmd/a2a-redteam`, `agent-redteam`) not in repo** —
   affects Phase 4 black-box tier scope.
7. **Cross-category secret-reuse detection not found** (ASI03).
8. `cli_execute` lives in **forge-cli**, not `forge-core/tools/builtins/`;
   documented "13 layers" = ~11 concrete guard points.

No row claims `Enforced` on black-box behavior alone; every `Enforced`/`Partial`
cell cites a `file:symbol` and an instrumented audit event or policy error.
