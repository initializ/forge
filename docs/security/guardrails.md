# Content Guardrails

> Part of [Forge Documentation](../../README.md)

The guardrail engine checks inbound and outbound messages against configurable policy rules.

## Built-in Guardrails

| Guardrail | Direction | Description |
|-----------|-----------|-------------|
| `content_filter` | Inbound + Outbound | Blocks messages containing configured blocked words |
| `no_pii` | Outbound | Detects email, phone, SSNs (with structural validation), and credit cards (with Luhn check) |
| `jailbreak_protection` | Inbound | Detects common jailbreak phrases ("ignore previous instructions", etc.) |
| `no_secrets` | Outbound | Detects API keys, tokens, and private keys (OpenAI, Anthropic, AWS, GitHub, Slack, Telegram, etc.) |

## Modes

| Mode | Behavior |
|------|----------|
| `enforce` | Blocks violating inbound messages; **redacts** outbound messages (see below) |
| `warn` | Logs violation, allows message to pass |

### Outbound Redaction

Outbound messages (from the agent to the user) are always **redacted** rather than blocked, even in `enforce` mode. Blocking would discard a potentially useful agent response (e.g., code analysis) over a false positive from broad PII/secret patterns matching source code. Matched content is replaced with `[REDACTED]` and a warning is logged.

### PII Validators

To reduce false positives, PII patterns use structural validators beyond simple regex:

| Pattern | Validator | What it checks |
|---------|-----------|---------------|
| SSN | `validateSSN` | Rejects area=000/666/900+, group=00, serial=0000, all-same digits, known test SSNs |
| Credit card | `validateLuhn` | Luhn checksum validation, 13-19 digit length check |
| Email | — | Regex only |
| Phone | — | Regex only (area code 2-9, separators required) |

## Configuration

Guardrails are defined in the policy scaffold, loaded from `policy-scaffold.json` or generated during `forge build`.

Custom guardrail rules can be added to the policy scaffold:

```json
{
  "guardrails": {
    "content_filter": {
      "mode": "enforce",
      "blocked_words": ["password", "credit card"]
    },
    "no_pii": {
      "mode": "enforce"
    },
    "jailbreak_protection": {
      "mode": "warn"
    },
    "no_secrets": {
      "mode": "enforce"
    }
  }
}
```

## Runtime

```bash
# Default: guardrails enforced (all built-in guardrails active)
forge run

# Explicitly disable guardrail enforcement
forge run --no-guardrails
```

All four built-in guardrails (`content_filter`, `no_pii`, `jailbreak_protection`, `no_secrets`) are active by default, even without running `forge build`. Use `--no-guardrails` to opt out.

## Tool Output Scanning

The guardrail engine scans tool output via an `AfterToolExec` hook, catching secrets and PII before they enter the LLM context or outbound messages.

| Guardrail | What it detects in tool output |
|-----------|-------------------------------|
| `no_secrets` | API keys, tokens, private keys (same patterns as outbound message scanning) |
| `no_pii` | Email addresses, phone numbers, SSNs |

**Behavior by mode:**

| Mode | Behavior |
|------|----------|
| `enforce` | Returns a generic error (`"tool output blocked by content policy"`), blocking the result from entering the LLM context. The error message intentionally omits which guardrail matched to avoid leaking security internals to the LLM or channel. |
| `warn` | Replaces matched patterns with `[REDACTED]`, logs a warning, and allows the redacted output through |

The hook writes the redacted text back to `HookContext.ToolOutput`, which the agent loop reads after all hooks fire. This is backwards-compatible — existing hooks that don't modify `ToolOutput` leave it unchanged.

## Path Containment

The `cli_execute` tool confines filesystem path arguments to the agent's working directory. This prevents social-engineering attacks where an LLM is tricked into listing or reading files outside the project.

### Shell Interpreter Denylist

Shell interpreters (`bash`, `sh`, `zsh`, `dash`, `ksh`, `csh`, `tcsh`, `fish`) are **unconditionally blocked**, even if they appear in `allowed_binaries`. Shells defeat the no-shell `exec.Command` security model by reintroducing argument interpretation and bypassing all path validation (e.g., `bash -c "ls ~/Library/Keychains"`).

### HOME Override

When `workDir` is configured, `$HOME` in the subprocess environment is overridden to `workDir`. This prevents `~` expansion inside subprocesses from reaching the real home directory.

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

## Skill-Specific Guardrails

Skills can declare domain-specific guardrail rules in their SKILL.md frontmatter under `metadata.forge.guardrails`. These are enforced by a separate `SkillGuardrailEngine` that complements the global guardrails.

```yaml
metadata:
  forge:
    guardrails:
      deny_commands:
        - pattern: "rm\\s+-rf\\s+/"
          message: "Destructive filesystem operations are not allowed"
      deny_output:
        - pattern: "password:\\s*\\S+"
          action: redact
      deny_prompts:
        - pattern: "what (tools|binaries|commands) (are|do you have)"
          message: "I can help with specific tasks — just describe what you need."
      deny_responses:
        - pattern: "(?:^|\\n)\\s*[-*]\\s*\\S+.*\\n(\\s*[-*]\\s*\\S+.*\\n){3,}"
          message: "I can help with specific tasks. What would you like me to do?"
```

### Filter Types

| Filter | Applied When | Match Target | Behavior |
|--------|-------------|--------------|----------|
| `deny_commands` | Before tool execution | `"binary arg1 arg2 ..."` command line | Blocks execution, returns custom error to LLM |
| `deny_output` | After tool execution | Tool output text | `block`: hides result; `redact`: replaces matches with `[BLOCKED BY POLICY]` |
| `deny_prompts` | Before LLM receives input | User message text (case-insensitive) | Rejects message with custom error |
| `deny_responses` | After LLM generates output | LLM response text (case-insensitive) | Replaces response with custom message |

### Aggregation

When multiple skills are loaded, their guardrail rules are **merged** (deduplicated by pattern). The aggregated rules are compiled into regex once during agent initialization and reused for all subsequent checks.

## Audit Events

Guardrail evaluations are logged as structured audit events:

```json
{"ts":"2026-02-28T10:00:00Z","event":"guardrail_check","correlation_id":"a1b2c3d4","fields":{"guardrail":"no_pii","direction":"outbound","result":"blocked"}}
```

See [Security Overview](overview.md) for the full security architecture.

---
← [Build Signing](signing.md) | [Back to README](../../README.md) | [Scheduling](../scheduling.md) →
