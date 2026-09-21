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
| 1 | **Managed** | `managed-settings.json` in the fixed OS system dir (+ `managed-settings.d/*.json`) — no env/flag override |
| 2 | **Command line** | `--settings <file>` |
| 3 | **Project local** | `.forge/settings.local.json` |
| 4 | **Shared project** | `.forge/settings.json` |
| 5 | **User** | `~/.forge/settings.json` (or `FORGE_USER_SETTINGS`) |

**Merge:** list keys (`channels.enabled`, `tools.builtins.enabled`, `skills.enabled`, and — when unlocked — `models.available_models`) are **unioned** across layers. Scalars (`models.default.*`, `models.gateway.*`) take the highest layer's non-empty value. `models.gateways` merges **per provider** — a higher layer's entry for a given provider replaces the lower one, others are kept. `env` maps merge with higher keys winning.

**Managed settings are not developer-overridable.** They load from a **fixed OS system path with no env or flag override** — a developer running the shipped binary cannot redirect, replace, or drop the managed layer (matching Claude Code, whose managed path is fixed for exactly this reason). Tamper-resistance is the OS file permissions on that path: on a managed machine it is root-owned and not user-writable.

**Managed lock:** when a **managed** layer sets `models.available_models`, it replaces the union rather than adding to it, so no lower layer — and no developer at runtime — can widen it. Combined with the fixed path above, this is a real allowlist within the settings surface. [Platform policy](../security/platform-policy.md) (server-side, control-plane injected) remains the defense-in-depth forbidden-model enforcement — use settings for the org's allowed/default set, policy to hard-forbid regardless of client state. An empty/absent `available_models` is "unset" (no lock), not "lock to zero models".

### Managed settings locations (per OS)

| OS | Path |
|---|---|
| macOS | `/Library/Application Support/forge/managed-settings.json` |
| Linux / WSL | `/etc/forge/managed-settings.json` |
| Windows | `C:\Program Files\forge\managed-settings.json` |

These paths are **fixed** — there is no env var to redirect them (a developer must not be able to point the managed layer at a file they control). Drop-in directory `managed-settings.d/` next to the file is merged in alphabetical order (primary first) — name files `10-…`, `20-…` to control order. Deliver via MDM, an image build, or config management, and ensure the path is root-owned / not user-writable. The forge system dir is shared with the policy layer, so managed settings sit **alongside** `policy.yaml` (the deny layer) — e.g. `/etc/forge/managed-settings.json` next to `/etc/forge/policy.yaml`.

## Schema

```json
{
  "channels": {
    "enabled": ["slack", "telegram"]
  },
  "models": {
    "default":         { "provider": "anthropic", "model": "claude-sonnet-4-6" },
    "available_models": ["anthropic/claude-sonnet-4-6", "openai/gpt-4o"],
    "gateway":         { "base_url": "https://gw.corp/v1", "auth_scheme": "apikey_header", "auth_header_name": "apikey" },
    "gateways": [
      { "provider": "openai",    "base_url": "https://gw.corp/openai/v1",    "auth_scheme": "bearer", "api_key_helper": "/opt/forge/openai-helper.sh" },
      { "provider": "anthropic", "base_url": "https://gw.corp/anthropic/v1", "auth_scheme": "bearer", "api_key_helper": "/opt/forge/anthropic-helper.sh",
        "env": { "OKTA_CLIENT_ID": "0oaXXXX", "OKTA_ISSUER": "https://acme.okta.com/oauth2/default" } }
    ]
  },
  "tools": {
    "builtins": { "enabled": ["http_request", "datetime_now", "math_calculate"] }
  },
  "skills": {
    "enabled": ["weather", "github"]
  },
  "optimizer": { "enabled": true },
  "env": { "HTTP_PROXY": "http://proxy.corp:8080" }
}
```

| Key | Type | Effect |
|---|---|---|
| `channels.enabled` | `[]string` | Channel adapters offered/enabled by `forge init` / `run --with` / `channel add` |
| `models.default` | `{provider, model}` | Default provider+model when none is given explicitly (seeds `forge try`/`init`) |
| `models.available_models` | `[]string` | Allowlist of `<provider>/<model>`; a **managed** value is the authoritative allowlist (no lower layer or developer can widen it — see Managed lock). Empty = unset |
| `models.gateway` | `{provider, base_url, auth_scheme, auth_header_name, api_key_helper}` | Model gateway endpoint. The singular form is the **provider-less catch-all** ("one URL for all providers"). Injected into the scaffolded `forge.yaml` model block by `forge init`/`try`, and consumed by the **runtime overlay** (below) — mirrors the [`model` config](forge-yaml-schema.md) fields and the outbound [`auth_scheme`](../security/authentication.md) |
| `models.gateways` | `[{provider, base_url, auth_scheme, auth_header_name, api_key_helper}]` | **Per-provider** model gateways (one for anthropic, one for openai, …). Consumed by the **runtime overlay** only (not scaffold injection). Merged per-provider (a higher layer's entry for a provider replaces the lower one). See [Local-dev gateway overlay](#local-dev-gateway-overlay--api_key_helper) |
| `models.gateway(s).api_key_helper` | `string` | External command that prints a short-lived token to stdout (the Claude Code apiKeyHelper contract). When set, the runtime injects a helper-minted token as the model credential per `auth_scheme`, instead of a native static API key. A **managed** helper arms the login gate; a user-layer helper uses `forge auth login\|logout\|status` |
| `models.gateway(s).env` | `map[string]string` | Environment injected into the `api_key_helper` subprocess (on top of forge's own env), so the helper's config (e.g. `OKTA_CLIENT_ID`, `OKTA_ISSUER`) lives in settings instead of a shell export. Read from trusted layers only — put IDs/issuers/endpoints here, not secrets |
| `models.gateway(s).oidc` | `{grant, issuer, authorize_url, token_url, client_id, client_secret_env, scopes, redirect_uri}` | **Native OAuth/OIDC** token acquisition (an alternative to `api_key_helper` — forge does the flow itself). `grant`: `client_credentials` (headless/CI — mints with `client_secret_env`, no browser) or `auth_code` (interactive browser login, **PKCE, public client — no secret** — plus silent refresh). Endpoints come from `authorize_url`/`token_url`, or are discovered from `issuer`'s `/.well-known/openid-configuration`. The client secret is never stored — `client_secret_env` names the env var. `redirect_uri` (auth_code) is the loopback callback forge binds; it must be a redirect URI already registered on the IdP client. See [Native OIDC](#native-oidc-gateway-login) |
| `optimizer.enabled` | `bool` (tri-state) | Whether the [`forge` surface](forge-surface.md) offers the context optimizer for the Claude Code path. **Off by default** (unset). Enabling it rewrites `ANTHROPIC_BASE_URL`, which can't be forced under enterprise-managed Claude Code, so it's opt-in. Resolved from **trusted layers only** (user / project-local / CLI / managed) — a checked-in project `.forge/settings.json` cannot enable it. Highest layer wins, so a managed layer can force-enable or force-disable. |
| `tools.builtins.enabled` | `[]string` | Builtin tools offered/defaulted |
| `skills.enabled` | `[]string` | **Registry** skills offered/defaulted in `forge init` (the wizard offers only these; non-interactive `--skills` is gated). Empty = all registry skills. Governs registry-skill *selection* only — NOT `--from-skills` / `--from-skill-dir` custom imports (a developer's own local skills). It is a developer-surface offering, not a hard skill boundary; to forbid skills fleet-wide, use platform policy |
| `env` | `map[string]string` | Environment defaults |

## Inspect

```bash
forge settings          # loaded layers (with their paths) + effective settings
forge settings show --json
forge settings show --settings ./ci-settings.json   # add a CLI-precedence layer
```

The output lists each loaded layer lowest → highest, flags a managed `available_models` **LOCK**, and prints the merged effective settings. The default (human) view **masks `env` values** as `***` (keys shown) since `env` may carry secrets; `--json` emits values **verbatim** for your own machine-readable dump — don't paste it into shared channels.

## What consumes settings today

- **`forge try`**: a configured `models.default` seeds the provider/model when no flag is given; `models.gateway` is injected into the scaffolded `forge.yaml` model block (`base_url` / `auth_scheme` / `auth_header_name`); `tools.builtins.enabled`, when set, overrides the quickstart's default builtin set.
- **`forge init`**: `models.gateway` is injected into the scaffolded `forge.yaml` (both modes). In **non-interactive** mode, `models.default` seeds the provider/model and `tools.builtins.enabled` / `skills.enabled` seed builtins / skills when the corresponding flag is omitted — so a settings default even satisfies the otherwise-required `--model-provider`. `channels.enabled` and `skills.enabled` are enforced: non-interactive `--channels` / `--skills` fail immediately on a non-enabled entry, and the **interactive wizard offers only the enabled channels and skills** (so a disabled one can't be picked; the end-of-wizard gate remains a backstop). Note `skills.enabled` scopes to **registry** skills only — `--from-skills` / `--from-skill-dir` custom imports are the developer's own local skills and are not gated by it (a hard skill boundary is a [platform policy](../security/platform-policy.md) concern, not this developer-surface setting). The interactive wizard also **pre-selects `models.default`**: the provider step highlights the default provider, and the default model — but interactive model pre-selection applies **only where a model-selector step exists (OpenAI today)**; providers with a fixed model default (e.g. anthropic, gemini) have no model step, so their `models.default.model` is *not* applied in the interactive wizard (the provider is still highlighted). The model default **is always honored non-interactively** (and via the scaffolded `forge.yaml` gateway). The user still confirms each wizard choice; the gateway (no wizard step) is injected regardless.
- **`optimizer.enabled`**: consumed by the interactive [`forge` surface](forge-surface.md). In the Claude Code path the optimizer option is **disabled/unselectable** unless a trusted layer set `optimizer.enabled: true` — a checked-in project layer is excluded (same base-url-redirect trust boundary as `models.gateway`). A **managed** layer is the enterprise-approved enablement path.
- **`channels.enabled` gating**: when non-empty, `channels.enabled` is the allowlist of adapters that may run. It gates **all three** channel entry points: **`forge run --with <x>`** and **`forge channel serve <x>`** (the standalone runner) refuse to start a non-enabled adapter, and **`forge channel add <x>`** refuses to scaffold one. The gate runs before the [policy](../security/platform-policy.md) deny filter. Empty = unconstrained (every registered adapter available). This is the positive enablement surface; policy remains the deny surface — settings decide "is it offered?", policy decides "is it forbidden?".

## Local-dev gateway overlay + `api_key_helper`

This is the model for **forge on the server**: a developer checks in a `forge.yaml` with a native `provider` + `auth_scheme` + API keys, and the deployed agent uses those. **Local development** is different — a developer testing on their laptop often must reach the model through the org's **gateway** (Kong/Bedrock) behind an IdP (Okta/Entra), which issues a short-lived token. Rather than editing (and risking committing) the native `forge.yaml`, **managed or user settings auto-wire the call locally**: at runtime, before building the model client, forge overlays the matching settings `gateway` onto the resolved model config.

**Provider-scoped.** The overlay picks the `models.gateways` entry whose `provider` matches the resolved model provider (a provider match wins; a provider-less `models.gateway` is the catch-all). If **none** matches — e.g. `forge.yaml` is `openai` but settings define only an anthropic gateway — there is **no overlay** and the run stays on native auth. So a laptop's settings can define one gateway per provider and each agent picks up only its own.

**`api_key_helper` (the token).** When the matched gateway sets `api_key_helper`, forge runs that external command (the [Claude Code apiKeyHelper](https://code.claude.com/docs/en/settings#apikeyhelper) contract: it prints a token to stdout), caches the token by its JWT `exp`, and injects it as the model credential per the gateway's `auth_scheme` (`bearer` → `Authorization: Bearer`, native `x-api-key` suppressed, and for anthropic the `anthropic-version` header omitted — use this for a Kong **OIDC** route in front of Bedrock/Claude that validates a Bearer JWT; `apikey_header` → a named custom header). The command is run without a shell (argv is tokenized with quote-awareness; shell features belong in a wrapper script). The helper's own configuration (e.g. `OKTA_CLIENT_ID`, `OKTA_ISSUER`) goes in the gateway's `env` map, which forge injects into the helper subprocess — so nothing needs exporting before a run. `env` is applied **on top of** forge's own environment (a gateway value wins on a duplicate key). Because `env` parameterizes the token, the cache key includes it: one shared helper (e.g. a single `okta.sh`) reused across providers with a different `OKTA_ISSUER` per gateway correctly mints and caches a **distinct** token per gateway. `env` keys must look like environment variables (`[A-Za-z_][A-Za-z0-9_]*`) — an invalid key fails loudly rather than silently. The token is stored like every other credential: encrypted when `FORGE_PASSPHRASE` is set, else a `0600` file under `~/.forge/credentials`; it is never logged. An **opaque (non-JWT)** helper token has no readable expiry, so it is treated as always-expired and the helper re-runs on every invocation — configure a helper that emits a JWT to benefit from caching.

> **Trust boundary — the gateway is resolved from trusted layers only.** A gateway can name a command forge will *exec* (`api_key_helper`) and can *redirect* the endpoint (`base_url`), so the **checked-in project `.forge/settings.json` is deliberately excluded** from gateway resolution — a cloned repo must not be able to run a command on your laptop or send your native provider key to an attacker's host. The gateway (and `forge auth login`) honor only the **user**, **project-local** (`.forge/settings.local.json`, gitignored), **CLI** (`--settings`), and **managed** layers. Every *other* setting (channels, skills, `models.default`, `available_models`) still honors the checked-in project layer as normal — only the gateway is trust-restricted.

**Auto-login on expiry.** The token is never sent stale: when the runtime overlay is about to inject the gateway credential and the cached token is missing or within its refresh buffer of expiry, it re-runs the `api_key_helper` to acquire a fresh one first. This holds for **both** managed and user layers, so an expired token triggers a re-login instead of a guaranteed 401. The **managed** login gate (root pre-run on `forge run` / `try` / `serve`) is the *eager, fail-early* variant — it logs in at command start before the runtime spins up; the overlay refresh is the universal safety net. You can still manage a user-layer token explicitly with [`forge auth login | logout | status`](cli-reference.md#forge-auth). A browser-based helper is a local-dev affordance; headless servers use native auth and would not deploy one. Unlike the operator commands `forge auth login`/`logout` (which refuse to run inside a deployed agent runtime), the overlay refresh is **allowed in a deployed runtime** — a server legitimately re-acquires its own model credential from a *non-interactive* managed helper (e.g. `client_credentials`); an *interactive* helper there simply fails, and the command surfaces a warning and proceeds without the gateway token (the call then fails clearly rather than silently). The command comes only from trusted layers (the checked-in project `.forge/settings.json` is excluded), so this exec cannot be hijacked by a cloned repo.

**`available_models` mismatch is a warning, not a block.** If a resolved model isn't in a **managed** `available_models` lock, forge prints a one-line warning and proceeds with native auth — runtime model-deny remains a server-side [platform policy](../security/platform-policy.md) concern (see **Managed lock** above), not a client-side rejection.

## Native OIDC gateway login

Instead of an `api_key_helper` external command, a gateway can set `models.gateway(s).oidc` and have **forge run the OAuth/OIDC flow itself**. Two grants:

- **`client_credentials`** (headless / CI) — forge mints a token from `token_url` (or the `issuer`-discovered endpoint) using `client_id` + the secret in `client_secret_env`, with `scopes`. No browser. The runtime overlay acquires and re-mints it on expiry automatically, so a deployed agent needs no interactive step.
- **`auth_code`** (interactive) — `forge auth login [provider]` opens a browser (auth-code + PKCE) against `authorize_url` (or the `issuer`-discovered endpoint), catches the loopback callback, and caches the token **with its refresh token**. The runtime overlay then refreshes it silently on expiry; it never opens a browser mid-run — if there's no valid cached token it tells you to run `forge auth login`.

```json
"gateways": [
  { "provider": "openai", "base_url": "https://gw.corp/openai/v1", "auth_scheme": "bearer",
    "oidc": { "grant": "client_credentials", "issuer": "https://idp.corp",
              "client_id": "forge-gw", "client_secret_env": "FORGE_GW_CLIENT_SECRET",
              "scopes": ["model.invoke"] } }
]
```

The **client secret is never stored in settings** — `client_secret_env` names the env var holding it. The `auth_code` grant is a **public PKCE client and needs no secret** (it's the same flow as an Okta/Entra native app: `client_id` + `issuer`/endpoints + `scopes` + PKCE). Endpoints are taken from `authorize_url`/`token_url`, or discovered from `issuer`'s `/.well-known/openid-configuration` (which must be **https**; loopback http allowed for a local dev IdP). The interactive `auth_code` flow binds the loopback callback in **`redirect_uri`** — set it to a redirect URI **already registered on the IdP client** (e.g. `http://localhost:8199/oauth/callback`); it defaults to `http://localhost:1455/auth/callback`. It must be an http loopback (`localhost`/`127.0.0.1`/`[::1]`). Like the helper, OIDC config is resolved from **trusted layers only** (the checked-in project `.forge/settings.json` is excluded).

> **Guardrail.** The OIDC flow **refuses to run against the anthropic public URL** (`api.anthropic.com`) — that API authenticates with `x-api-key`, not OAuth. A gateway fronting anthropic behind your own IdP (a different host) is fine.

Remaining follow-ups (tracked on the settings epic): additional managed delivery mechanisms (server-managed control-plane fetch, macOS config profile, Windows registry); per-fallback / per-endpoint gateway auth (today the overlay is primary-endpoint only); the **device-code** OIDC grant (client_credentials + auth_code ship now). The interactive-wizard defaulting (channel/skill filtering + provider/model pre-selection) is complete.

## See also

- [Platform Policy](../security/platform-policy.md) — the deny surface (separate from settings)
- [Authentication](../security/authentication.md) — outbound model `auth_scheme` (referenced by `models.gateway`)
- [forge.yaml schema](forge-yaml-schema.md) — the per-agent `model` block settings inject into
