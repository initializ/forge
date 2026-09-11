---
title: "Settings"
description: "Layered developer-surface settings — enabled channels, model defaults + gateway, builtin tools — with a user layer and an enterprise-managed (MDM) layer."
order: 8
---

# Settings

Forge settings are the **developer-surface** configuration: which channels are offered/enabled, the default model and the model **gateway** endpoint, and which builtin tools are offered. They layer, with a **user** level and an enterprise **managed** level an org can drop via MDM — modeled on [Claude Code settings](https://code.claude.com/docs/en/settings) + [managed settings](https://code.claude.com/docs/en/managed-settings).

> **Settings vs. policy.** Settings are the *positive* surface — enablement, defaults, gateway injection. They are **separate** from [platform policy](../security/platform-policy.md), the *negative* surface (deny / restrict / tighten), which the control plane injects server-side. A managed settings **lock** (an authoritative allowlist) is the positive counterpart to a policy **deny**; both can coexist.

## Precedence

Highest wins. A managed value cannot be overridden by a lower layer.

| # | Layer | Source |
|---|---|---|
| 1 | **Managed** | `managed-settings.json` in the OS system dir (+ `managed-settings.d/*.json`), or `FORGE_MANAGED_SETTINGS` |
| 2 | **Command line** | `--settings <file>` |
| 3 | **Project local** | `.forge/settings.local.json` |
| 4 | **Shared project** | `.forge/settings.json` |
| 5 | **User** | `~/.forge/settings.json` (or `FORGE_USER_SETTINGS`) |

**Merge:** list keys (`channels.enabled`, `tools.builtins.enabled`, and — when unlocked — `models.available_models`) are **unioned** across layers. Scalars (`models.default.*`, `models.gateway.*`) take the highest layer's non-empty value. `env` maps merge with higher keys winning.

**Managed lock:** when a **managed** layer sets `models.available_models`, it replaces the union rather than adding to it, so no lower **settings** layer widens it — the authoritative allowlist *among the settings layers*, an org default. It is **not a tamper-proof control**: the managed source is redirectable via `FORGE_MANAGED_SETTINGS` (as the policy loader's system path is via `FORGE_SYSTEM_POLICY`), so a determined user can point it elsewhere. Non-overridable **forbidden-model enforcement is [platform policy](../security/platform-policy.md)'s job** — server-side, control-plane injected. Use settings for the org's preferred/default set; use policy to actually forbid. An empty/absent `available_models` is "unset" (no lock), not "lock to zero models".

### Managed settings locations (per OS)

| OS | Path |
|---|---|
| macOS | `/Library/Application Support/forge/managed-settings.json` |
| Linux / WSL | `/etc/forge/managed-settings.json` |
| Windows | `C:\Program Files\forge\managed-settings.json` |

Drop-in directory `managed-settings.d/` next to the file is merged in alphabetical order (primary first) — name files `10-…`, `20-…` to control order. Deliver via MDM, an image build, or config management. Managed settings can be dropped **alongside** a `policy.yaml` (the deny layer) on the same fleet.

## Schema

```json
{
  "channels": {
    "enabled": ["slack", "telegram"]
  },
  "models": {
    "default":         { "provider": "anthropic", "model": "claude-sonnet-4-6" },
    "available_models": ["anthropic/claude-sonnet-4-6", "openai/gpt-4o"],
    "gateway":         { "base_url": "https://gw.corp/v1", "auth_scheme": "apikey_header", "auth_header_name": "apikey" }
  },
  "tools": {
    "builtins": { "enabled": ["http_request", "datetime_now", "math_calculate"] }
  },
  "env": { "HTTP_PROXY": "http://proxy.corp:8080" }
}
```

| Key | Type | Effect |
|---|---|---|
| `channels.enabled` | `[]string` | Channel adapters offered/enabled by `forge init` / `run --with` / `channel add` |
| `models.default` | `{provider, model}` | Default provider+model when none is given explicitly (seeds `forge try`/`init`) |
| `models.available_models` | `[]string` | Allowlist of `<provider>/<model>`; a **managed** value is the authoritative settings-layer allowlist (an org default, not a hard boundary — see Managed lock). Empty = unset |
| `models.gateway` | `{base_url, auth_scheme, auth_header_name}` | Model gateway endpoint injected into the scaffolded `forge.yaml` model block — mirrors the [`model` config](forge-yaml-schema.md) fields and the outbound [`auth_scheme`](../security/authentication.md) |
| `tools.builtins.enabled` | `[]string` | Builtin tools offered/defaulted |
| `env` | `map[string]string` | Environment defaults |

## Inspect

```bash
forge settings          # loaded layers (with their paths) + effective settings
forge settings show --json
forge settings show --settings ./ci-settings.json   # add a CLI-precedence layer
```

The output lists each loaded layer lowest → highest, flags a managed `available_models` **LOCK**, and prints the merged effective settings. The default (human) view **masks `env` values** as `***` (keys shown) since `env` may carry secrets; `--json` emits values **verbatim** for your own machine-readable dump — don't paste it into shared channels.

## What consumes settings today

- **`forge try`**: a configured `models.default` seeds the provider/model when no flag is given; a configured `models.gateway` is injected into the scaffolded agent's `forge.yaml` model block (`base_url` / `auth_scheme` / `auth_header_name`) — so an org points `forge try` at its model gateway with no per-agent config.

Additional consumers (`forge init`, `forge run --with` channel gating, builtin-tool offering) land in follow-up work tracked on the settings epic.

## See also

- [Platform Policy](../security/platform-policy.md) — the deny surface (separate from settings)
- [Authentication](../security/authentication.md) — outbound model `auth_scheme` (referenced by `models.gateway`)
- [forge.yaml schema](forge-yaml-schema.md) — the per-agent `model` block settings inject into
