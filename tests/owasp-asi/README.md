# OWASP ASI 2026 Conformance Suite

Eval-first conformance tests for the OWASP Top 10 for Agentic Applications
(ASI01–ASI10, Dec 2025). Each entry is `hypothesis -> dataset -> grader ->
measured rate with a threshold`, not a single hand-picked assertion.

Companion docs: `docs/security/owasp-asi-conformance.md` (matrix),
`docs/security/owasp-asi-gaps.md` (gap register + issue links).

## Running

```sh
cd tests/owasp-asi
go test ./... -v
```

or from the repo root: `make owasp-asi` (runs the suite and writes a report to
`tests/owasp-asi/report/`).

## Two tiers

1. **Instrumented tier (authoritative for "contained").** Drives real Forge
   controls in-process and asserts the control *fired* via an instrumented
   signal — an audit event (`egress_blocked`, `guardrail_check`,
   `policy_violation_at_build_time`, `invocation_cancelled`), a
   policy-violation return, or a sandbox validation error. A containment claim
   **must** read such a signal; "no bad output happened" is never sufficient.
2. **Black-box tier.** Observed behavior only; may flag *suspected* leakage but
   may not upgrade a grade to `Enforced` on its own. The `cmd/a2a-redteam`
   tooling referenced by the conformance plan is **not present in this repo**,
   so the black-box tier is documented here but not wired. When that tooling
   lands, the black-box driver hangs off these same datasets.

## Graders (`graders/`)

- `audit_assert.go` — parses the audit NDJSON stream; `Has(event, field, val)`,
  `Count`, `Rate`. Authoritative event reader.
- `egress_assert.go` — runs the real `security.DomainMatcher` and emits the
  runner's `egress_allowed`/`egress_blocked` event; `EgressBlocked` asserts it.
- `policy_assert.go` — runs the real `security.EnforcePolicy` and records
  `policy_violation_at_build_time`; `PolicyViolationRecorded` asserts the
  attributing layer.

## Coverage map

| Entry | Grade | Instrumented (passes) | xfail targets (issue) |
|---|---|---|---|
| ASI01 | Partial | egress blast-radius block rate | intent gate (#229) |
| ASI02 | Enforced | cli_execute misuse containment rate | MCP pinning (#231), HITL (#223) |
| ASI03 | Partial | secrets encrypted at rest | task-scoped tokens (#232) |
| ASI04 | Partial | checksum tamper rejection | remote verify (#228), AIBOM (#227) |
| ASI05 | Enforced | RCE arg containment rate | lockfile guard (#234) |
| ASI06 | Partial | (canonical property: core unit tests) | self-reingestion, write-scan (#225) |
| ASI07 | Partial | (A2A: server tests) | replay, spoof (#226); mTLS (Deployer) |
| ASI08 | Partial | policy union-of-deny attribution | circuit breaker (#233) |
| ASI09 | Partial | — | preview/effect gate (#223) |
| ASI10 | Partial | kill-switch audit event | audit signing (#224), manifest (#230) |

`xfail` = `t.Skip` with a reason and the backlog issue number. No silent skips:
every skip names the gap it tracks, so the suite stays green on shipped scope
while enumerating the unmet surface.

## Datasets (`datasets/`)

ASCII-only attack corpora, one directory per entry. `asi02/tool_misuse.jsonl`
and `asi05/rce_args.jsonl` are JSONL (`{name, binary, args, expect}`);
`asi01/exfil_domains.txt` is one host per line.
