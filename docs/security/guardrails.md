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

## Audit Events

Guardrail evaluations are logged as structured audit events:

```json
{"ts":"2026-02-28T10:00:00Z","event":"guardrail_check","correlation_id":"a1b2c3d4","fields":{"guardrail":"no_pii","direction":"outbound","result":"blocked"}}
```

See [Security Overview](overview.md) for the full security architecture.

---
← [Build Signing](signing.md) | [Back to README](../../README.md) | [Scheduling](../scheduling.md) →
