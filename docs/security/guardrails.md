# Content Guardrails

> Part of [Forge Documentation](../../README.md)

The guardrail engine checks inbound and outbound messages against configurable policy rules.

## Built-in Guardrails

| Guardrail | Direction | Description |
|-----------|-----------|-------------|
| `content_filter` | Inbound + Outbound | Blocks messages containing configured blocked words |
| `no_pii` | Outbound | Detects email addresses, phone numbers, and SSNs via regex |
| `jailbreak_protection` | Inbound | Detects common jailbreak phrases ("ignore previous instructions", etc.) |

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
    }
  }
}
```

## Runtime

```bash
# Run with guardrails enforced
forge run --enforce-guardrails

# Default: warn mode (log only)
forge run
```

## Audit Events

Guardrail evaluations are logged as structured audit events:

```json
{"ts":"2026-02-28T10:00:00Z","event":"guardrail_check","correlation_id":"a1b2c3d4","fields":{"guardrail":"no_pii","direction":"outbound","result":"blocked"}}
```

See [Security Overview](overview.md) for the full security architecture.

---
← [Build Signing](signing.md) | [Back to README](../../README.md) | [Scheduling](../scheduling.md) →
