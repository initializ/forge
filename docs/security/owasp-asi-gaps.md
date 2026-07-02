# OWASP ASI — Forge Gap Register

Deduplicated, actionable gaps derived from `owasp-asi-conformance.md`. Cross-entry
duplicates are merged into a single `GAP-*` record and cross-referenced.
Severity uses AIVSS-style reasoning (blast radius × likelihood), justified inline.

**Attribution:** `forge-core` / `Platform` = code deliverables (→ GitHub issues in
Phase 3). `Deployer` = runbook/doc deliverables (→ docs, **not** code issues).

Format: `ID | ASI refs | Guideline(s) | Locus | Severity | Description | Proposed control | Closing test`

**GitHub issues (created 2026-07-01, repo `initializ/forge`):**

| Gap | Issue | Gap | Issue |
|---|---|---|---|
| GAP-HITL | #223 | GAP-INTENT | #229 |
| GAP-AUDIT-SIGN | #224 | GAP-INTEGRITY | #230 |
| GAP-MEM | #225 | GAP-MCP-PIN | #231 |
| GAP-A2A-MSG | #226 | GAP-TOKEN | #232 |
| GAP-SBOM | #227 | GAP-CIRCUIT | #233 |
| GAP-REMOTE | #228 | GAP-LOCKFILE | #234 |

---

## Code-deliverable gaps (forge-core / Platform)

### GAP-HITL — Action-level approval + dry-run diff + preview/effect separation
- **ASI refs / guidelines:** ASI02 #2, ASI09 #1, ASI09 #7 (deduped: one gap).
- **Locus:** forge-core (gate) + Platform (approval UX).
- **Severity:** **High** — destructive tool calls (email-delete, DB-drop, git
  push --force) have high blast radius; today nothing forces a confirmation or a
  preview before effect, so likelihood of an un-gated destructive action is high.
- **Description:** No explicit human-in-the-loop confirmation and no dry-run/
  preview separation before state-changing tool calls.
- **Proposed control:** policy-layer "requires_approval" tool classification +
  a preview mode that blocks state-changing calls and emits a `guardrail_check`/
  new `approval_required` audit event; approval surfaced by Platform. Prefer a
  policy-layer addition over a new forge-core builtin.
- **Closing test:** `TestASI02_DestructiveActionRequiresApproval`,
  `TestASI09_PreviewBlocksEffect` (instrumented on the block event).

### GAP-AUDIT-SIGN — Tamper-evident (signed / append-only) audit stream
- **ASI refs / guidelines:** ASI08 #10, ASI09 #2, ASI10 #1 (deduped).
- **Locus:** forge-core.
- **Severity:** **High** — audit is the non-repudiation substrate for every
  rogue-agent / forensic claim; without integrity, a compromised agent can edit
  its own trail. Likelihood moderate, impact severe.
- **Description:** Audit JSONL has `schema_version` + monotonic per-invocation
  `seq` but **no** cryptographic integrity (no Ed25519/HMAC/hash-chain).
- **Proposed control:** optional hash-chained records (each event carries
  `prev_hash`) + optional Ed25519 signature over the chain head, keyed like
  `signing_stage.go`. Must preserve metadata-only default and the
  `TestNoPayloadByDefault_LLMCall` invariant.
- **Closing test:** `TestASI10_AuditChainTamperDetected` (mutate a record →
  verification fails).

### GAP-MEM — Memory write validation + provenance + no self-reingestion + trust decay
- **ASI refs / guidelines:** ASI06 #2, #5, #6, #7, #8, #9 (deduped).
- **Locus:** forge-core.
- **Severity:** **High** — the self-reingestion path
  (`memory_compactor.go:374 AppendDailyLog`) is **live**: the agent's own outputs
  are indexed and retrievable, enabling bootstrap poisoning with high likelihood
  over long-running agents.
- **Description:** No write validation/scan, no origin/trust attribution, no
  guard against re-ingesting agent outputs, no per-tenant namespace, only recency
  (not trust) decay.
- **Proposed control:** guardrail scan on memory writes; `origin`/`trust` fields
  on chunks with a self-authored flag excluded from retrieval by default;
  per-tenant memory namespace; trust-weighted decay. Preserve markdown-canonical
  / index-derived property.
- **Closing test:** `TestASI06_SelfAuthoredMemoryNotReingested`,
  `TestASI06_MemoryWriteScanned` (instrumented on write-scan event).

### GAP-A2A-MSG — Inter-agent message signing + anti-replay + fail-closed schema
- **ASI refs / guidelines:** ASI07 #2, #3, #9 (deduped).
- **Locus:** forge-core + Platform.
- **Severity:** **Medium** — requires a second agent/attacker on the A2A path;
  no inbound surface by default lowers likelihood, but replay/spoof impact is
  high where multi-agent is enabled.
- **Description:** No message-level signature, no anti-replay nonce/timestamp
  bound to the task window, schema validation does not provably fail closed on
  down-conversion.
- **Proposed control:** signed A2A task envelopes + nonce/timestamp window +
  strict typed schema that rejects (not coerces) unknown/downgraded fields.
- **Closing test:** `TestASI07_ReplayedTaskRejected`,
  `TestASI07_SpoofedAgentCardRejected`.

### GAP-SBOM — SBOM/AIBOM emission + supply-chain kill switch
- **ASI refs / guidelines:** ASI04 #1 (BOM half), ASI04 #8.
- **Locus:** forge-core (build).
- **Severity:** **Medium** — no BOM impedes incident response (nx/debug,
  postmark-mcp class); likelihood of a poisoned dependency is real, impact
  contained by existing egress/trust.
- **Description:** No SBOM/AIBOM artifact at build; no cross-deployment kill
  switch for a compromised skill/model.
- **Proposed control:** emit CycloneDX SBOM + an AIBOM (models, skills, egress
  domains, trust levels) in the build pipeline alongside `checksums.json`; a
  policy-layer denylist that acts as the kill switch.
- **Closing test:** `TestASI04_BuildEmitsAIBOM`,
  `TestASI04_KillSwitchDeniesSkill`.

### GAP-REMOTE — Remote-skill signature verification (fail-closed)
- **ASI refs / guidelines:** ASI04 #1/#2 (remote tier).
- **Locus:** forge-core.
- **Severity:** **Medium** — currently **latent**: remote tier is unimplemented,
  so there is no live exposure, but if remote loading ships without verification
  it becomes High. Track so it lands verified-by-construction.
- **Description:** `"remote"` provenance is a doc string only; no remote loader/
  verifier. If/when remote loading is built, unsigned remote skills must be
  rejected.
- **Proposed control:** remote loader that requires a valid Ed25519 signature
  against a trusted keyring (`forge-skills/trust/keyring.go`) and fails closed.
- **Closing test:** `TestASI04_UnsignedRemoteSkillRejected` (xfail until remote
  tier exists — references this gap).

### GAP-INTENT — Runtime intent validation gate + signed intent capsule
- **ASI refs / guidelines:** ASI01 #4, #5.
- **Locus:** forge-core + Platform.
- **Severity:** **Medium** — bounds goal-hijack blast radius beyond egress;
  meaningful but overlaps existing guardrail + egress caps.
- **Description:** No runtime validation of intent before goal-changing actions;
  no signed intent capsule pattern.
- **Proposed control:** evaluate a signed intent-capsule; a pre-action check that
  the requested action is within the declared task intent, emitting an audit
  event on divergence.
- **Closing test:** `TestASI01_GoalDivergenceFlagged`.

### GAP-INTEGRITY — Continuous behavioral-integrity vs declared manifest + attestation
- **ASI refs / guidelines:** ASI10 #5, #6; ASI08 #8; ASI09 #9 (deduped —
  behavioral/plan-divergence detection).
- **Locus:** forge-core (detection) + Platform (attestation/keys).
- **Severity:** **Medium** — detection of an agent acting outside its declared
  `SKILL.md`/egress manifest; impact high for rogue agents, likelihood moderate.
- **Description:** No continuous verification that runtime behavior stays within
  the declared skills/egress manifest; no periodic attestation; no plan-divergence
  detection vs an approved baseline.
- **Proposed control:** runtime check comparing invoked tools/egress against the
  declared manifest, emitting a `manifest_deviation` audit event; Platform-side
  periodic attestation with orchestrator-mediated signing (keys never reach
  agents).
- **Closing test:** `TestASI10_ManifestDeviationDetected` (instrumented).

### GAP-MCP-PIN — MCP tool-name pinning / typosquat-resistant resolution
- **ASI refs / guidelines:** ASI02 #7.
- **Locus:** forge-core.
- **Severity:** **Medium** — ambiguous/typosquatted MCP tool resolution can
  redirect a call; likelihood depends on MCP usage.
- **Description:** MCP tool names are not version-pinned; ambiguous resolution is
  not proven to fail closed.
- **Proposed control:** fully-qualified, version-pinned MCP tool identifiers;
  ambiguous resolution fails closed with an audit event.
- **Closing test:** `TestASI02_AmbiguousMCPToolFailsClosed`.

### GAP-TOKEN — Task-scoped short-lived tokens per invocation
- **ASI refs / guidelines:** ASI03 #1.
- **Locus:** forge-core + Platform.
- **Severity:** **Medium** — long-lived caller tokens widen the abuse window;
  mitigated partly by external verification.
- **Description:** Auth verifies a caller-supplied token; no per-task short-lived
  token minting.
- **Proposed control:** integrate task-scoped token minting/exchange (Platform
  IdM) with per-invocation TTL; forge-core threads and audits token scope.
- **Closing test:** `TestASI03_TaskScopedTokenExpires` (Platform xfail).

### GAP-PATH — cli_execute workDir jailing for path arguments (DISCOVERED by the suite)
- **ASI refs / guidelines:** ASI02 #1/#3, ASI05 #4 (discovered during Phase 4, not in the original hypothesis set).
- **Locus:** forge-core (the `cli_execute` builtin actually lives in forge-cli/tools, but this is a runtime-enforcement control).
- **Severity:** **Medium** — an allowlisted reader (`cat`, and similar) can read
  arbitrary host files outside the agent workspace via relative traversal (e.g.
  `/etc/passwd`), an information-disclosure path. Likelihood depends on which
  binaries are allowlisted; impact is real host-file disclosure. Bounded by the
  binary allowlist (only reads, only allowlisted tools) — hence Medium, not High.
- **Description:** `validatePathArg` (`forge-cli/tools/cli_execute.go:426`)
  rejects path args that resolve inside `$HOME` but outside `workDir`; it does
  **not** confine arbitrary relative traversal to paths outside `$HOME`. Proven:
  `cat ../../../../../../etc/passwd` executes and returns `/etc/passwd`.
- **Proposed control:** confine all resolved path arguments to `workDir` (or an
  explicit allowlist of readable roots), rejecting any resolved path outside it —
  not just `$HOME`-escapes. Emit a validation error (the existing instrumented
  signal).
- **Closing test:** `TestASI02_ToolMisuseContained` — flip the
  `workdir_escape_etc_passwd` case from `known_gap` to `reject` once confined.
- **Status:** discovered during Phase 4; filed as issue **#235**.

### GAP-LOCKFILE — Dependency-lockfile-poisoning guard (low residual)
- **ASI refs / guidelines:** ASI05 (residual).
- **Locus:** forge-core.
- **Severity:** **Low** — narrow surface (only agents that regenerate lockfiles);
  existing sandbox/egress already contains most impact.
- **Description:** No specific guard for agents that regenerate dependency
  lockfiles.
- **Proposed control:** optional lockfile-change detection/approval in the build
  or skill path.
- **Closing test:** `TestASI05_LockfileChangeFlagged` (low priority).

---

## Deployer gaps (docs / runbook — NOT code issues)

### DEP-MTLS — Inter-pod transport encryption (mTLS mesh) + NetworkPolicy
- **ASI refs:** ASI07 #1.
- **Deliverable:** operator runbook documenting the required mTLS service mesh
  and applying the build-generated `NetworkPolicy`. Not a forge-core gap.

### DEP-IAM — External agentic IdM / delegation-chain governance
- **ASI refs:** ASI03 #6/#8/#9, ASI08 #4/#9, ASI10 #3.
- **Deliverable:** Platform documentation for external IdM, delegated-permission
  detection, watchdog/collusion detection, digital-twin replay. Out of scope to
  build here (Initializ Platform lane); enumerated so the surface is not hidden.

---

## Reconciliation vs matrix

Every non-`Enforced`, non-pure-`Deployer` cell maps to exactly one dedup gap:

| ASI | Unmet guideline(s) | Gap(s) |
|---|---|---|
| ASI01 | #4,#5 / #6 | GAP-INTENT (/ CDR folded into GAP-MEM-style write-scan; tracked under GAP-INTENT scope note) |
| ASI02 | #2 / #7 | GAP-HITL / GAP-MCP-PIN |
| ASI03 | #1 | GAP-TOKEN (delegation → DEP-IAM) |
| ASI04 | #1 BOM, #8 / remote | GAP-SBOM / GAP-REMOTE |
| ASI05 | residual | GAP-LOCKFILE |
| ASI06 | #2,#5,#6,#7,#8,#9 | GAP-MEM |
| ASI07 | #2,#3,#9 / #1 | GAP-A2A-MSG / DEP-MTLS |
| ASI08 | #7,#8,#10 / #4,#9 | GAP-CIRCUIT (see note), GAP-AUDIT-SIGN, GAP-INTEGRITY / DEP-IAM |
| ASI09 | #1,#7,#9,#2 | GAP-HITL, GAP-INTEGRITY, GAP-AUDIT-SIGN |
| ASI10 | #1,#5,#6,#7 / #3 | GAP-AUDIT-SIGN, GAP-INTEGRITY / DEP-IAM |

**Note — GAP-CIRCUIT (ASI08 #7):** blast-radius quotas / progress caps / circuit
breakers between planner and executor. Single-agent progress caps are a
forge-core candidate; multi-agent planner/executor separation is Platform. Split:
forge-core portion → issue; multi-agent portion → DEP-IAM/Platform note.
Severity **Medium**. Closing test `TestASI08_ProgressCapTriggersCircuitBreaker`.

**Code-deliverable gap count:** 12 (GAP-HITL, GAP-AUDIT-SIGN, GAP-MEM,
GAP-A2A-MSG, GAP-SBOM, GAP-REMOTE, GAP-INTENT, GAP-INTEGRITY, GAP-MCP-PIN,
GAP-TOKEN, GAP-LOCKFILE, GAP-CIRCUIT).
**Deployer/doc-only:** 2 (DEP-MTLS, DEP-IAM).
