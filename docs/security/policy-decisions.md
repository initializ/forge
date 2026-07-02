# Policy decisions

Forge's guardrail engine can emit one of five decisions for any
evaluated piece of content:

| Decision  | Meaning                                                    | Where it fires today                                      |
|-----------|------------------------------------------------------------|-----------------------------------------------------------|
| `allow`   | Content passes through unmodified                          | Every gate, default                                       |
| `deny`    | Content is rejected — the caller must not admit it         | InputGate, OutputGate, ToolCallGate, ToolOutputGate       |
| `modify`  | Content is admissible but must be rewritten first          | InputGate (masking), OutputGate (masking), ToolOutputGate (redact) |
| `step_up` | Reserved for R4b (#210) — additional user interaction     | not yet emitted                                           |
| `defer`   | Reserved for R4c (#211) — out-of-band lookup required      | not yet emitted                                           |

The `PolicyDecision` type and `PolicyResult` struct live in
`forge-core/runtime/guardrails.go`. See the interface docstrings on
`GuardrailChecker` for the exact contract.

## MODIFY, generalized (#209)

Before #209, `modify` was implemented for exactly one path —
`cli_execute` output redaction. Skill-authored `deny_output` /
`deny_commands` patterns silently no-op'd for every other tool. #209
removes the `cli_execute` short-circuit so:

- **User prompts** — the LibraryGuardrailEngine's InputGate mask
  decision returns `DecisionModify` from `CheckInbound`.
- **LLM responses** — same via OutputGate on `CheckOutbound`.
- **Any tool output** — `SkillGuardrailEngine.CheckCommandOutput`
  now applies the redact/block loop to output of ANY tool, not just
  cli_execute. The loop is hoisted into a package-level
  `applyOutputPolicy` so future call sites (MCP tool result hook,
  RAG context ingestion) can reuse the same MODIFY semantics.

### ⚠️ Behavior change: match target asymmetry

`deny_commands` patterns match against **different target strings**
depending on the tool:

| Tool             | Match target                                            |
|------------------|---------------------------------------------------------|
| `cli_execute`    | Reconstructed shell command line: `binary arg1 arg2 …`  |
| any other tool   | The raw tool-input JSON verbatim                        |

For `cli_execute` this preserves the pre-#209 semantics — patterns
authored as shell-style regexes (`\bget\s+secrets?\b`, `rm\s+-rf`)
still fire the same way against the parsed `binary`+`args`.

For every other tool, the match runs against the JSON payload as
the LLM produced it. A pattern like `"query":"kubectl get secrets"`
will match a `web_search` invocation whose LLM-provided arguments
happen to contain that substring; a pattern like `rm\s+-rf` will
NOT (there's no reconstructed command line for `web_search`).

**Migration**: operators upgrading a pre-#209 skill should audit
their `deny_commands` list. A permissive shell-style pattern that
previously affected only `cli_execute` may now start denying
`web_search` / `http_request` / MCP calls whose JSON body happens
to contain that text. Split rules that were meant for one tool
family into per-tool patterns anchored on JSON structure (e.g.
`"binary":"kubectl"` for cli_execute-only matches) if the wider
scope is undesirable.

### Block/redact ordering

`applyOutputPolicy` evaluates `deny_output` filters in two passes:

1. **Block pass** — every `block` pattern is checked against the
   **original** content. A single match short-circuits the whole
   call to `Deny`.
2. **Redact pass** — every `redact` pattern is applied cumulatively
   to the (possibly already-partially-redacted) content, returning
   `Modify` if any substitution happened.

The two-pass design guarantees an earlier `redact` cannot suppress a
later `block` match by rewriting the substring the block pattern
would have caught — a `Deny`-worthy string is never silently
downgraded to `Modify`.

## Audit event mapping

Every `guardrail_check` event carries a `fields.decision` string:

- `allowed` — Allow
- `masked` — Modify
- `blocked` — Deny (enforce mode)
- `warned` — Deny (warn mode)

These strings predate the `PolicyDecision` enum by several sprints —
they stay for SIEM stability. Consumers building on the enum should
map `allowed ↔ allow`, `masked ↔ modify`, `blocked ↔ deny`.

## Test surface

- `forge-core/runtime/policy_decision_test.go` — enum semantics +
  `applyOutputPolicy` behavior across tools.
- `forge-core/runtime/skill_guardrails_test.go` — `deny_commands`
  and `deny_output` MODIFY paths fire for non-cli_execute tools.
