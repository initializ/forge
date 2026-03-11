# Content Guardrails

> Part of [Forge Documentation](../../README.md)

The guardrail engine checks inbound and outbound messages against configurable policy rules.

## Built-in Guardrails

| Guardrail | Direction | Description |
|-----------|-----------|-------------|
| `content_filter` | Inbound + Outbound | Blocks messages containing configured blocked words |
| `no_pii` | Outbound | Detects email addresses, phone numbers, and SSNs via regex |
| `jailbreak_protection` | Inbound | Detects common jailbreak phrases ("ignore previous instructions", etc.) |
| `no_secrets` | Outbound | Detects API keys, tokens, and private keys (OpenAI, Anthropic, AWS, GitHub, Slack, Telegram, etc.) |

## Modes

| Mode | Behavior |
|------|----------|
| `enforce` | Blocks violating messages, returns error to caller |
| `warn` | Logs violation, allows message to pass |

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

Guardrail evaluations are logged as structured audit events:

```json
{"ts":"2026-02-28T10:00:00Z","event":"guardrail_check","correlation_id":"a1b2c3d4","fields":{"guardrail":"no_pii","direction":"outbound","result":"blocked"}}
```

See [Security Overview](overview.md) for the full security architecture.

---
← [Build Signing](signing.md) | [Back to README](../../README.md) | [Scheduling](../scheduling.md) →
