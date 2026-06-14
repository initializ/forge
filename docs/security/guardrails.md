---
title: "Content Guardrails"
description: "Configurable content filtering, PII detection, and jailbreak protection."
order: 7
---

The guardrail engine validates inbound and outbound messages against configurable policy rules using the [`github.com/initializ/guardrails`](https://github.com/initializ/guardrails) library (pinned at `v0.12.0` in `forge-cli/go.mod`). Both the file-mode `guardrails.json` schema and the MongoDB-mode `AgentConfig` documents share the same shape — `models.StructuredGuardrails` from that library's [`models/config.go`](https://github.com/initializ/guardrails/blob/v0.12.0/models/config.go). The library's `models` package is the authoritative type definition; this page mirrors it for the fields most operators reach for, but field-level additions in newer library versions will surface there first.

## Architecture

Guardrails are implemented as a `GuardrailChecker` interface in forge-core, with the concrete engine in forge-cli wrapping the external guardrails library. Two operational modes are supported:

| Mode | Config Source | Use Case |
|------|--------------|----------|
| **File mode** (default) | `guardrails.json` in project root | Local development, standalone deployments |
| **DB mode** | MongoDB (`AgentConfig` collection) | Platform deployments with centralized config + audit |

### Source precedence

`BuildGuardrailChecker` (`forge-cli/runtime/guardrails_loader.go`) resolves the active config in this exact order at every `forge run`. The first row whose trigger matches is the one that loads — there is no merging across rows.

| # | Trigger | Outcome | What happens to lower-priority sources |
|---|---|---|---|
| 1 | `FORGE_GUARDRAILS_DB` set **and** MongoDB connect + ping succeed | **DB mode** — config loaded per-request from the `AgentConfig` collection in the `Initializ` database, keyed by `(FORGE_AGENT_ID, FORGE_ORG_ID)`. Audit records written back to MongoDB (`EnableAudit: true`). | `guardrails.json` is ignored at runtime even if present in the image. |
| 2 | `FORGE_GUARDRAILS_DB` set **but** connect or ping fails (10-second timeout) | Warns `failed to connect guardrails DB, falling back to file` and continues to row 3. | Falls through. |
| 3 | `guardrails.json` exists at `<workDir>/<cfg.GuardrailsPath \|\| "guardrails.json">` and parses | **File mode** — config is the parsed `StructuredGuardrails`. | Built-in defaults discarded. |
| 4 | File missing, unreadable, or invalid JSON | **Built-in defaults** — bundled PII (email/phone/SSN/credit-card) + jailbreak/prompt-injection/command-injection detection + 11 secret-pattern regexes (see [Default Secret Patterns](#default-secret-patterns)). | N/A — this is the floor. |
| 5 | Engine construction itself errors (after picking 3 or 4) | Warns `failed to create file guardrail engine, using noop` and installs a `NoopGuardrailChecker`. | No checks run; messages pass through unmodified. |

Notable consequences of the order:

- **MongoDB mode bypasses `guardrails.json` entirely** — the file still gets baked into `/app/guardrails.json` by the build, but the runner never opens it when `FORGE_GUARDRAILS_DB` is set. You can flip an agent between modes at runtime by setting / unsetting the env var without rebuilding.
- **DB failure is non-fatal.** A misconfigured URI or transient network issue drops to file mode with a warning, not a hard exit. If you need a hard requirement, monitor the warning in your log pipeline (`failed to connect guardrails DB, falling back to file`).
- **DB mode requires `FORGE_ORG_ID`** to scope the `AgentConfig` lookup. Forgetting to set it usually surfaces as the library returning no config and decisions defaulting through; check that org ID is populated alongside the URI.
- **`cfg.GuardrailsPath`** in `forge.yaml` overrides the default `"guardrails.json"` filename for file mode only — it has no effect in DB mode.
- **Audit sinks differ.** DB mode writes via the library's `EnableAudit` path into MongoDB. File mode emits Forge's normal `guardrail_check` audit events through the configured audit sinks (see [Audit Events](#audit-events)).

## Built-in Evaluators

The guardrails library provides these evaluator categories:

| Category | Direction | Description |
|----------|-----------|-------------|
| PII detection | Inbound + Outbound | Detects email, phone, SSN, credit card numbers |
| Jailbreak detection | Inbound | Detects jailbreak and prompt manipulation attempts |
| Prompt injection | Inbound | Detects injection attacks in user input |
| Command injection | Inbound | Detects shell/command injection patterns |
| Secret detection | Outbound + Tool output | Detects API keys, tokens, and private keys via regex rules |
| Custom rules | Configurable per gate | User-defined regex and keyword rules |

## Modes

| Mode | Behavior |
|------|----------|
| `enforce` | Blocks violating inbound messages; **redacts** outbound messages |
| `warn` | Logs violation, allows message to pass |

### Inbound Masking

When PII or secrets are detected in inbound messages with action `mask`, the content is redacted **before** it reaches the LLM. The LLM never sees the original sensitive data.

### Outbound Redaction

Outbound messages (from the agent to the user) are always **redacted** rather than blocked, even in `enforce` mode. Blocking would discard a potentially useful agent response over a false positive. Matched content is replaced with the library's masked output and a warning is logged.

## Configuration

### `guardrails.json`

Guardrails are configured in `guardrails.json` at the project root. This file is generated by `forge init` and can be customized:

```json
{
  "pii": {
    "enabled": true,
    "action": "mask",
    "categories": {
      "email": { "enabled": true, "action": "mask" },
      "phoneNumber": { "enabled": true, "action": "mask" },
      "ssn": { "enabled": true, "action": "mask" },
      "creditCard": { "enabled": true, "action": "mask" }
    }
  },
  "security": {
    "jailbreakDetection": {
      "enabled": true,
      "confidenceThreshold": 25,
      "action": "block"
    },
    "promptInjection": {
      "enabled": true,
      "confidenceThreshold": 30,
      "action": "block"
    },
    "commandInjection": {
      "enabled": true,
      "confidenceThreshold": 35,
      "action": "block"
    }
  },
  "customRules": {
    "rules": [
      {
        "id": "secret_openai",
        "name": "OpenAI API Key",
        "type": "regex",
        "constraint": "hard",
        "pattern": "sk-[A-Za-z0-9]{20,}",
        "action": "mask",
        "gates": ["output", "tool_call"]
      }
    ]
  },
  "gateConfig": {
    "inputGate": true,
    "toolCallGate": true,
    "outputGate": true,
    "contextGate": false,
    "streamGate": false
  }
}
```

### Custom Path

Override the guardrails config file path in `forge.yaml`:

```yaml
guardrails_path: "config/my-guardrails.json"
```

### Default Secret Patterns

The default `guardrails.json` includes regex rules for these secret types:

| Rule ID | Pattern |
|---------|---------|
| `secret_anthropic` | `sk-ant-[A-Za-z0-9\-]{20,}` |
| `secret_openai` | `sk-[A-Za-z0-9]{20,}` |
| `secret_github_pat` | `ghp_[A-Za-z0-9]{36}` |
| `secret_github_oauth` | `gho_[A-Za-z0-9]{36}` |
| `secret_github_server` | `ghs_[A-Za-z0-9]{36}` |
| `secret_github_fine` | `github_pat_[A-Za-z0-9_]{22,}` |
| `secret_aws` | `AKIA[0-9A-Z]{16}` |
| `secret_slack_bot` | `xoxb-[0-9]{10,}-[A-Za-z0-9-]+` |
| `secret_slack_user` | `xoxp-[0-9]{10,}-[A-Za-z0-9-]+` |
| `secret_private_key` | `-----BEGIN (RSA\|EC\|OPENSSH\|PRIVATE) .*KEY-----` |
| `secret_telegram` | `[0-9]{8,10}:[A-Za-z0-9_-]{35,}` |

### Gate Configuration

Gates control which evaluation points are active:

| Gate | Default | Description |
|------|---------|-------------|
| `inputGate` | `true` | Validates user messages before LLM processing |
| `toolCallGate` | `true` | Validates tool arguments before execution |
| `outputGate` | `true` | Validates agent responses before delivery |
| `contextGate` | `false` | Validates context window content |
| `streamGate` | `false` | Validates streaming chunks |

### Full `guardrails.json` Schema

The `StructuredGuardrails` document (`github.com/initializ/guardrails/models.StructuredGuardrails`) has the following top-level blocks. Every block is optional — omitted blocks disable the corresponding evaluator. Field names use camelCase to match the JSON / BSON tags on the library structs.

| Top-level key | Library type | Purpose |
|---|---|---|
| `pii` | `*PIIConfig` | PII detection (email, phone, SSN, credit-card, …) |
| `moderation` | `*ModerationConfig` | Content-moderation categories (hate, harassment, violence, sexual, …) |
| `security` | `*SecurityConfig` | Jailbreak, prompt injection, SQL injection, command injection, custom security patterns |
| `urlFilter` | `*URLFilterConfig` | Allowlist / denylist URLs in inbound and outbound text |
| `customRules` | `*CustomRulesConfig` | User-defined regex / keyword / phrase rules with gate scoping |
| `approvalGates` | `[]ApprovalCondition` | Per-condition human-approval gates with notification channels |
| `nsfwText` | `*NSFWTextConfig` | NSFW-text confidence-threshold detection |
| `hallucination` | `*HallucinationConfig` | Hallucination detection — `require_sources` or `review` mode |
| `skillConstraints` | `*SkillConstraintsConfig` | Allowed / blocked skill names with per-decision action |
| `gateConfig` | `*GateConfig` | Which gates (input / tool-call / output / context / stream) fire |

#### `pii`

```json
{
  "enabled": true,
  "action": "mask",
  "categories": {
    "email": { "enabled": true, "action": "mask" },
    "phoneNumber": { "enabled": true, "action": "mask" }
  }
}
```

| Field | Type | Notes |
|---|---|---|
| `enabled` | `bool` | Master switch for PII detection |
| `action` | `string` | Global default: `mask` / `block` / `warn` |
| `categories` | `map<string, PIICategoryConfig>` | Per-category overrides; each entry has `enabled`, `action`, optional `id`, `label` |

#### `moderation`

```json
{
  "enabled": true,
  "action": "warn",
  "categories": {
    "hate": { "enabled": true, "action": "block", "threshold": 0.8 }
  }
}
```

Same shape as `pii`, but each `ModerationCategoryConfig` also accepts a `threshold` float (0.0–1.0) and optional `description`.

#### `security`

```json
{
  "jailbreakDetection": { "enabled": true, "confidenceThreshold": 25, "action": "block" },
  "promptInjection":    { "enabled": true, "confidenceThreshold": 30, "action": "block" },
  "sqlInjection":       { "enabled": true, "confidenceThreshold": 40, "action": "block" },
  "commandInjection":   { "enabled": true, "confidenceThreshold": 35, "action": "block" },
  "customPatterns": [
    { "name": "internal-token", "pattern": "INT-[A-Z0-9]{16}", "action": "block", "description": "Internal service tokens" }
  ]
}
```

| Sub-block | Type | Notes |
|---|---|---|
| `jailbreakDetection`, `promptInjection`, `sqlInjection`, `commandInjection` | `*ThresholdConfig` | Each has `enabled` (bool), `confidenceThreshold` (0–100 percent), `action` (string) |
| `customPatterns` | `[]SecurityPattern` | Each has `name`, `pattern` (regex), `action`, optional `description` |

#### `urlFilter`

```json
{
  "enabled": true,
  "mode": "denylist",
  "denylist": ["evil.example.com"],
  "allowlist": [],
  "maskAction": "redact",
  "replaceWith": "[URL REDACTED]",
  "action": "mask"
}
```

| Field | Type | Notes |
|---|---|---|
| `mode` | `string` | `allowlist` / `denylist` / `both` |
| `allowlist`, `denylist` | `[]string` | Host patterns |
| `action` | `string` | `mask` / `block` / `warn` |
| `maskAction`, `replaceWith` | `string` | Used when `action: mask` |

#### `customRules`

```json
{
  "hardConstraints": ["no-pii-leakage"],
  "softConstraints": ["polite-tone"],
  "rules": [
    {
      "id": "secret_openai",
      "name": "OpenAI API Key",
      "type": "regex",
      "constraint": "hard",
      "pattern": "sk-[A-Za-z0-9]{20,}",
      "action": "mask",
      "gates": ["output", "tool_call"]
    }
  ]
}
```

| Rule field | Type | Notes |
|---|---|---|
| `id`, `name` | `string` | Identification |
| `type` | `string` | `regex` / `keyword` / `phrase` |
| `constraint` | `string` | `hard` (fail-fast) / `soft` (logged only) |
| `pattern` | `string` | Required for `regex` type |
| `keywords` | `[]string` | Required for `keyword` / `phrase` types |
| `action` | `string` | `mask` / `block` / `warn` |
| `gates` | `[]string` | Which gates to apply to — `input`, `output`, `tool_call`, `context`, `stream` |
| `caseSensitive` | `bool` | Default false for keyword/phrase types |
| `description` | `string` | Optional human-readable note |

#### `approvalGates`

```json
[
  {
    "id": "high-risk-action",
    "condition": "tool == 'kubectl' && contains(args, 'delete')",
    "action": "require_human_approval",
    "notifyChannels": ["slack:ops"]
  }
]
```

| Field | Type | Notes |
|---|---|---|
| `id`, `condition` | `string` | Required |
| `action` | `string` | `block` / `require_human_approval` / `warn` |
| `notifyChannels` | `[]string` | Channel adapters to notify on trigger |

#### `nsfwText`

```json
{ "enabled": true, "confidenceThreshold": 0.85, "action": "block" }
```

`confidenceThreshold` is a 0.0–1.0 float (different from the 0–100 percentage used by `security.*`).

#### `hallucination`

```json
{
  "enabled": true,
  "mode": "require_sources",
  "minSourceCount": 1,
  "action": "warn"
}
```

| Field | Type | Notes |
|---|---|---|
| `mode` | `string` | `require_sources` / `review` |
| `minSourceCount` | `int` | Minimum source citations required when `mode: require_sources` |

#### `skillConstraints`

```json
{
  "enabled": true,
  "allowedSkills": ["code_review_diff", "review_github_list_prs"],
  "blockedSkills": [],
  "action": "block"
}
```

Allowed / blocked skill names checked against the agent's registered skill set.

#### `gateConfig`

See [Gate Configuration](#gate-configuration) above. Field types are all `bool` in the library struct.

### Compatibility notes

- The `forge init` template generates a `guardrails.json` containing only the `pii`, `security`, `customRules`, and `gateConfig` blocks. The other blocks (`moderation`, `urlFilter`, `approvalGates`, `nsfwText`, `hallucination`, `skillConstraints`) are not bootstrapped but are accepted at runtime — add them by hand if you need them.
- All blocks are pointer-typed in the library struct. Omitting a key in JSON is equivalent to disabling that evaluator; setting an empty object `{}` with `enabled: false` is functionally the same but uses one extra parse cycle.
- camelCase JSON keys are the contract — the BSON tags happen to be identical so a `StructuredGuardrails` document round-trips between MongoDB and `guardrails.json` without translation.
- For evaluator semantics, regex flag handling, and the full action vocabulary, see the library's [`models/config.go`](https://github.com/initializ/guardrails/blob/v0.12.0/models/config.go).

## DB Mode (Platform Deployments)

When `FORGE_GUARDRAILS_DB` is set to a MongoDB connection URI, the engine loads guardrails config from the `AgentConfig` collection and enables audit logging.

```bash
export FORGE_GUARDRAILS_DB="mongodb://localhost:27017"
export FORGE_AGENT_ID="my-agent"
export FORGE_ORG_ID="my-org"
forge run
```

The library queries `AgentConfig` with `{agent_id, org_id}` to load the `StructuredGuardrails` config. If the DB is unreachable, it falls back to file mode.

| Environment Variable | Description |
|---------------------|-------------|
| `FORGE_GUARDRAILS_DB` | MongoDB connection URI |
| `FORGE_AGENT_ID` | Agent identifier (falls back to `agent_id` in `forge.yaml`) |
| `FORGE_ORG_ID` | Organization identifier |

## Runtime

```bash
# Default: guardrails enforced (all evaluators active)
forge run

# Explicitly disable guardrail enforcement
forge run --no-guardrails
```

All configured guardrails are active by default, even without running `forge build`. Use `--no-guardrails` to opt out.

## Tool Output Scanning

The guardrail engine scans tool output via an `AfterToolExec` hook, catching secrets and PII before they enter the LLM context or outbound messages. The engine calls the library's `OutputGate` with tool metadata attached.

**Behavior by mode:**

| Mode | Behavior |
|------|----------|
| `enforce` | Returns an error identifying the violation, blocking the result from entering the LLM context |
| `warn` | Replaces matched patterns with masked content, logs a warning, allows through |

The hook writes the redacted text back to `HookContext.ToolOutput`, which the agent loop reads after all hooks fire.

## Path Containment

The `cli_execute` tool confines filesystem path arguments to the agent's working directory. This prevents social-engineering attacks where an LLM is tricked into listing or reading files outside the project.

### Shell Interpreter Denylist

Shell interpreters (`bash`, `sh`, `zsh`, `dash`, `ksh`, `csh`, `tcsh`, `fish`) are **unconditionally blocked**, even if they appear in `allowed_binaries`. Shells defeat the no-shell `exec.Command` security model by reintroducing argument interpretation and bypassing all path validation (e.g., `bash -c "ls ~/Library/Keychains"`).

### HOME Override

When `workDir` is configured, `$HOME` in the subprocess environment is overridden to `workDir`. This prevents `~` expansion inside subprocesses from reaching the real home directory. To preserve `gh` CLI authentication, `GH_CONFIG_DIR` is automatically set to the real `~/.config/gh` — but **only** when the binary being executed is `gh`. Other binaries do not receive this env var, preventing them from accessing GitHub credentials.

### Path Argument Validation

**Rules:**
- Arguments that look like paths (`/`, `~/`, `./`, `../`) are resolved and checked
- If a resolved path is inside `$HOME` but outside `workDir` → **blocked**
- System paths outside `$HOME` (e.g., `/tmp`, `/etc`) → allowed
- Non-path arguments (e.g., `get`, `pods`, `--namespace=default`) → allowed
- Flag arguments (e.g., `--kubeconfig=~/.kube/config`) → not detected as paths, allowed

Additionally, `cmd.Dir` is set to `workDir` so relative paths in subprocess execution resolve within the agent directory.

**Examples:**

| Command | Result |
|---------|--------|
| `kubectl get pods` | Allowed — no path args |
| `bash -c "ls ~/"` | Blocked — `bash` is a denied shell interpreter |
| `ls ~/Library/Keychains/` | Blocked — inside `$HOME`, outside workDir |
| `cat ../../.ssh/id_rsa` | Blocked — resolves inside `$HOME`, outside workDir |
| `jq '.' /tmp/data.json` | Allowed — system path outside `$HOME` |
| `ls ./data/` | Allowed — within workDir |

## Skill Guardrails

Skills can declare domain-specific guardrails in their `SKILL.md` frontmatter under `metadata.forge.guardrails`. These complement the global guardrails with rules authored by skill developers to enforce least-privilege and prevent capability enumeration.

### Guardrail Types

| Type | Hook Point | Direction | Behavior |
|------|-----------|-----------|----------|
| `deny_commands` | `BeforeToolExec` | Inbound | Blocks `cli_execute` commands matching a regex pattern |
| `deny_output` | `AfterToolExec` | Outbound | Blocks or redacts `cli_execute` output matching a regex pattern |
| `deny_prompts` | `BeforeLLMCall` | Inbound | Blocks user messages matching a regex (capability enumeration probes) |
| `deny_responses` | `AfterLLMCall` | Outbound | Replaces LLM responses matching a regex (binary name leaks) |

### SKILL.md Configuration

```yaml
metadata:
  forge:
    guardrails:
      deny_commands:
        - pattern: '\bget\s+secrets?\b'
          message: "Listing Kubernetes secrets is not permitted"
        - pattern: '\bauth\s+can-i\b'
          message: "Permission enumeration is not permitted"
      deny_output:
        - pattern: 'kind:\s*Secret'
          action: block
        - pattern: 'token:\s*[A-Za-z0-9+/=]{40,}'
          action: redact
      deny_prompts:
        - pattern: '\b(approved|allowed|available)\b.{0,40}\b(tools?|binaries|commands?)\b'
          message: "I help with Kubernetes cost analysis. Ask about cluster costs."
      deny_responses:
        - pattern: '\b(kubectl|jq|awk|bc|curl)\b.*\b(kubectl|jq|awk|bc|curl)\b.*\b(kubectl|jq|awk|bc|curl)\b'
          message: "I can analyze cluster costs. What would you like to know?"
```

### Pattern Details

**`deny_commands`** — Patterns match against the reconstructed command line (`binary arg1 arg2 ...`). Only fires for `cli_execute` tool calls.

**`deny_output`** — Patterns match against tool output text. The `action` field controls behavior:

| Action | Behavior |
|--------|----------|
| `block` | Returns an error, preventing the output from entering the LLM context |
| `redact` | Replaces matched text with `[BLOCKED BY POLICY]` and logs a warning |

**`deny_prompts`** — Patterns are compiled with case-insensitive matching (`(?i)`). Designed to catch capability enumeration probes like "what are the approved tools" or "list available binaries". The `message` field provides a redirect response.

**`deny_responses`** — Patterns are compiled with case-insensitive and dot-matches-newline flags (`(?is)`). Designed to catch LLM responses that enumerate internal binary names. When matched, the entire response is replaced with the `message` text.

### Aggregation

When multiple skills declare guardrails, patterns are aggregated and deduplicated across all active skills. The `SkillGuardrailEngine` runs all patterns from all skills as a single enforcement layer.

### Runtime Fallback

Skill guardrails fire both with and without `forge build`:

- **With build** — Guardrails are serialized into `policy-scaffold.json` during `forge build` and loaded at runtime
- **Without build** — The runner parses `SKILL.md` files at startup and loads guardrails directly, falling back to runtime-parsed rules when no build artifact exists

This ensures guardrails are always active during development (`forge run`) without requiring a full build cycle.

## File Protocol Blocking

The `cli_execute` tool blocks arguments containing `file://` URLs (case-insensitive). This prevents filesystem traversal attacks via tools like `curl file:///etc/passwd` that bypass path validation since `file://` URLs are not detected as filesystem paths by `looksLikePath()`.

| Input | Result |
|-------|--------|
| `curl file:///etc/passwd` | Blocked — `file://` protocol detected |
| `curl FILE:///etc/shadow` | Blocked — case-insensitive check |
| `curl http://example.com` | Allowed — only `file://` is blocked |

## Audit Events

Every mask / block / warn decision emits a `guardrail_check` audit
event through the configured Forge audit sink stack (stderr safety
net + the optional Unix socket / HTTP sink wired via
`FORGE_AUDIT_SOCKET` / `FORGE_AUDIT_HTTP_ENDPOINT`). The event
carries the per-invocation `correlation_id`, `task_id`, sequence
number, and workflow-correlation tags so consumers can join it to
the `session_start` / `llm_call` / `invocation_complete` rows for
the same request.

Default shape (metadata-only):

```json
{
  "ts": "2026-06-14T10:00:00Z",
  "event": "guardrail_check",
  "schema_version": "1.0",
  "seq": 2,
  "correlation_id": "a1b2c3d4",
  "task_id": "slack-...",
  "fields": {
    "gate": "input",
    "decision": "masked",
    "guardrail": "pii",
    "category": "ssn",
    "violation_count": 1
  }
}
```

Field reference:

| Field | Values | Meaning |
|-------|--------|---------|
| `gate` | `input` / `context` / `tool_call` / `output` / `stream` | Library gate type that fired. Single source of truth; pulled from `Result.Gate`. |
| `decision` | `masked` / `warned` / `blocked` | Library decision after policy resolution |
| `guardrail` | `pii` / `moderation` / `security` / `none` / … | First violation's `Type` (`none` when violations list is empty) |
| `category` | `ssn` / `email` / `hate_speech` / … | First violation's `Category`; omitted when empty |
| `violation_count` | integer ≥ 0 | Length of `result.Violations` |
| `tool` | string | Tool name; present when `gate=tool_call`, or when `gate=output` and the OutputGate fire was on a tool's return text |
| `evidence` | string | Captured triggering text; present only when opt-in is on (see below) |

The five gate values and where Forge invokes each:

| `gate` | Call site | Path |
|--------|-----------|------|
| `input` | A2A handler (`CheckInbound`) | User message arrives at `/` |
| `context` | `BeforeLLMCall` hook | Each system-role message before the LLM sees it |
| `tool_call` | `BeforeToolExec` hook | Args the agent is about to pass to a tool |
| `output` | `CheckOutbound` (response to user) + `AfterToolExec` hook (tool return text) | Distinguished by presence of `fields.tool` |
| `stream` | Not auto-wired | `CheckStream` is exposed but Forge's `ExecuteStream` is a buffered wrapper around non-streaming `Execute`. Real per-chunk streaming is a future runtime change. |

> **Migration from pre-#159 agents** — Earlier agent versions emitted
> a `direction` field instead of `gate` (values
> `inbound` / `outbound` / `tool_output`). Consumers that need to
> support both vintages should fall back: `event.fields.gate ?? deriveFromDirection(event.fields.direction)`,
> with `inbound → input`, `outbound → output`, `tool_output → output`
> (with `tool` set). New emissions only carry `gate`.

### Evidence capture (opt-in)

The default posture is **metadata-only**: the offending text never
travels through the audit pipeline. Operators who need it (false-
positive triage, compliance evidence, pattern tuning) opt in per-
deployment via:

| Env var | Default | Meaning |
|---------|---------|---------|
| `FORGE_GUARDRAIL_CAPTURE_EVIDENCE` | `false` | Include `fields.evidence` in the emitted event |
| `FORGE_GUARDRAIL_REDACT` | `true` | Run a vendor-secret regex scrub over the captured evidence before emission |
| `FORGE_GUARDRAIL_MAX_BYTES` | `4096` | Per-event soft cap; overage is truncated with a `…[truncated:N]` marker |

`Redact` is on whenever `CaptureEvidence` is on unless you explicitly
disable it. The scrub matches obvious vendor token shapes (Anthropic
`sk-ant-…`, OpenAI `sk-…`, GitHub `ghp_/gho_/ghs_/github_pat_…`, AWS
`AKIA…`, Slack `xox[bp]-…`, private-key PEM headers, Telegram bot
tokens) and replaces each match with `[REDACTED]`. It is defense-
in-depth — the guardrail library has usually already masked these,
but an unmasked input that hit a different rule (e.g. moderation)
would otherwise carry secrets through verbatim.

The size envelope and `[REDACTED]` marker match the OTel span
content-capture pipeline (issue #130) so the same string travels
through both pipelines under one contract.

#### What evidence actually contains

| Decision | Evidence source |
|----------|-----------------|
| `masked` | The **post-mask** content (`Result.MaskedContent`) — the same payload the LLM saw downstream. PII the library already masked stays masked in the audit stream. |
| `warned` | The original triggering content. No mask was produced (the library only generates a masked variant for `mask` decisions). The redact pass still runs. |
| `blocked` | The original triggering content. Same rationale as `warned`. |

This means a typical PII-mask event emits the redacted version of the
prompt as evidence, not the raw text. Operators auditing for "did our
agent ever see PII?" should treat a `decision=blocked` row as the
only one that can carry plain-text PII through the stream, and gate
their export pipeline accordingly.

### Mode-specific behavior

- **File mode** — every event flows through the Forge audit pipeline.
- **DB mode** — the guardrails library also writes audit records to
  MongoDB when `EnableAudit` is set. Forge still emits the
  `guardrail_check` event on its own audit sinks so SIEM consumers
  reading the export socket see parity regardless of mode.

See [Security Overview](overview.md) for the full security architecture
and [Audit Logging](audit-logging.md) for the sink stack and schema
contract.
