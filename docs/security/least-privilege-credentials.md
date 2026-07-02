# Just-in-time credentials (governance R9)

Forge can mint short-lived, per-tool-call credentials at the moment
of tool execution rather than passing a long-lived environment
variable through the whole session. This narrows the blast radius of
a leaked token: if the agent leaks a JIT AWS token, an attacker gets
15 minutes of a scope-down role, not the deployment's full role.

## Provider model

Each credential comes from a **Provider** (a plugin), configured
per-tool via a `CredentialSpec` in `forge.yaml`. At startup the
runner resolves every spec against the plugin registry; on every
matching tool call it calls the provider's `Materialize` to produce
a fresh set of env vars, merges them into the tool's subprocess
env, and (if the provider supports it) revokes the credential after
the call completes.

Built-in providers:

| Name                | What it mints                                   | Config keys                                                           |
|---------------------|-------------------------------------------------|-----------------------------------------------------------------------|
| `static`            | Fixed env / headers from operator config        | `env`, `headers`, `ttl` (informational)                              |
| `sts_assume_role`   | Short-lived AWS creds via STS AssumeRole        | `role_arn` (required), `session_name`, `external_id`, `duration`, `session_policy`, `region`, `endpoint` |

`static` is passthrough — useful as a bootstrap or in tests. Real
least-privilege posture comes from `sts_assume_role` (or a Vault
dynamic-secrets provider, tracked as a follow-up).

## Declaring specs in `forge.yaml`

```yaml
credentials:
  # AWS CLI: 15-minute scoped read-only role, external-id tied to skill.
  - tool: cli_execute
    binary: aws
    provider: sts_assume_role
    spec:
      role_arn: arn:aws:iam::123456789012:role/forge-skill-read
      external_id: skill-alpha
      session_name: forge-agent-jit
      duration: 15m

  # kubectl: static passthrough (until a Vault provider ships).
  - tool: cli_execute
    binary: kubectl
    provider: static
    spec:
      env:
        KUBECONFIG: /var/run/secrets/skill-alpha/kubeconfig
      ttl: 1h
```

### Field reference

- **tool** — the tool name (e.g. `cli_execute`, `http_request`).
  Empty matches every tool.
- **binary** — for `cli_execute` only, the binary being invoked.
  Ignored on other tools.
- **provider** — the plugin name.
- **spec** — an object whose shape depends on the plugin.

Only the FIRST matching spec fires — order specs from most to
least specific.

## `sts_assume_role` details

The provider issues a single AWS STS AssumeRole POST signed with
SigV4. The source credentials (the identity Forge itself runs
under) come from `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` /
`AWS_SESSION_TOKEN` env vars — the standard AWS chain. Override
the source env-var names via `source_access_key` / `source_secret_key`
/ `source_token` in the spec if the operator runs multiple identities
side-by-side.

The `duration` field must be between 15 minutes and 12 hours (AWS
STS bounds). Below 15m gets rejected at startup, not silently
truncated.

`external_id` is threaded through to STS so operators can scope the
target role's trust policy to a specific skill: only the STS call
carrying the right external-id can assume it.

The generated `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, and
`AWS_SESSION_TOKEN` are appended to the subprocess env — overriding
any same-name values from the operator's static passthrough. Any
AWS CLI (or SDK) the subprocess launches uses them automatically.

## Audit events

Two events fire per JIT credential lifecycle:

- `credential_issued` — after materialize, before the tool runs.
  Fields: `provider`, `tool`, `binary`, `ttl`, `env_keys`,
  `header_keys`, `duration_ms`.
- `credential_revoked` — after the tool completes (whether or not
  the provider actually revokes). Fields: `provider`, `tool`,
  `binary`. Carries `error` if revocation failed.
- `credential_failed` — when materialize fails (STS 403, network
  error). The tool call is then aborted.

Payloads never contain the credential material itself — only the
key names, so SIEMs can validate that expected credentials are being
minted without exfiltrating secrets through the audit trail.

## Threat model

Solves:
- Long-lived deployment credentials in reach of a compromised skill.
- Cross-skill credential sharing (spec is per skill+tool+binary).
- Post-hoc audit of "which role did this action run as" via
  `credential_issued` events.

Does not solve:
- Live-token exfiltration during the tool call itself (the token IS
  in the subprocess env for the duration of the call).
- Provider-side compromise (a stolen STS AssumeRole call from the
  Forge deployment role still works).
- Revocation-under-attack when the underlying provider is offline.

Combine JIT credentials (#215) with per-skill egress allow-lists
(existing) + audit signing (#213) + hash chaining (#212) for full
governance R9 posture.
