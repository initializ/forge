---
title: "MCP — Delegated Consent (Platform Integration)"
description: "How a managing platform deploys type: user MCP servers and drives the auth-required gate to obtain and manage per-user consent."
order: 6
---

This is the operational runbook for a **platform** that manages Forge agent
deployments and wants per-user (delegated-identity) MCP access — replacing any
prior use of [DEFER](../security/defer-decisions.md) for MCP human-in-the-loop
consent. For the concept and config-field reference, see
[Configuration → the auth-required gate](./configuration.md#delegated-consent--the-auth-required-gate-330);
for the event fields, see [Audit events](./audit-events.md).

## First: the auth gate is not DEFER

DEFER and the auth-required gate solve different problems. Migrate **only** the
MCP-consent case; keep DEFER for per-action approval.

| | DEFER — `POST /tasks/{id}/decisions` | Auth gate — `POST /mcp/consent` |
|---|---|---|
| Question | "Approve/reject *this write action*?" | "Has the user *authorized* the agent to act as them on this server?" |
| Keyed by | `taskID` (1 decision : 1 task) | `{subject, server}` — fans out; one consent unblocks **all** of that user's parked calls for that server |
| Outcome | approve / reject (approver allowlist) | granted / timeout / canceled |
| Trigger | every matching tool call | only a `type: user` call with **no grant** for the requesting user |
| Token | n/a | Forge never receives it — the resume is a "a grant now exists, re-resolve" signal |

If a human must still approve a specific MCP *write*, that stays on DEFER. The
auth gate is purely for delegated-identity **consent** (the user hasn't
connected their Atlassian / Linear / etc. account yet).

## 1. Deploy-time — forge.yaml

Mark each delegated MCP server `auth.type: user` and point Forge at the
platform token resolver:

```yaml
platform:
  token_endpoint: https://platform.internal/agents/{id}/mcp/token   # your resolver

mcp:
  servers:
    - name: atlassian
      transport: http
      url: https://mcp.atlassian.example/mcp
      required: false                 # MUST be false — no user exists at startup
      auth:
        type: user                    # delegated per-user identity; activates the gate
        ref: atlassian-registry-entry # platform tool-registry key the resolver authorizes against
      tools:
        allow: ["*"]                  # or an explicit allowlist (default-deny)
```

Invariants the runtime enforces:

| Rule | Why |
|------|-----|
| `type: user` must not be `required: true` | Delegated identity is lazy — there is no user at boot, so the server can't be a startup gate. |
| No `token_store_path`, no `forge mcp login` for `type: user` | The token is materialized by your `token_endpoint`, keyed `{server, subject}` — never stored on disk by the agent. |
| The gate attaches to every MCP tool automatically | It only trips on an `ErrNoToken` from the per-user resolver, so non-delegated servers are unaffected — nothing to disable. |

Remove those MCP tools from any DEFER policy that was standing in for consent.

## 2. Detect a pending consent

When a `type: user` call has no grant, the runtime surfaces the need three ways
(no polling of internal state required):

1. **Task status flips to `auth-required`** (A2A `TaskState`). A `GET /tasks/{id}`
   reader sees state `auth-required` with the agent message
   *"Authorization required: connect \<server\> to continue (as \<subject\>)"*.
2. **Audit event `mcp_auth_required`** — emitted once per `{subject, server}`
   (not per call):

   ```json
   {"event":"mcp_auth_required","correlation_id":"…","task_id":"…",
    "fields":{"server":"atlassian","subject":"user@corp.com",
              "deadline":"2026-07-19T12:34:56Z","timeout_ms":600000}}
   ```

3. Resolution emits **`mcp_auth_resolved`**; expiry emits **`mcp_auth_timeout`**.

`subject` is the user's email (falls back to the opaque user ID), `server` is the
MCP server name, `deadline` is the hard park window — **default 10 minutes**,
after which the call fails with an auth-required error and the LLM sees a
`no_token` result. Consume the audit stream and/or watch task status.

## 3. Deliver consent, then resume

The platform owns delivery, the OAuth callback, and token custody (managed
mode). The end-to-end sequence:

```
type: user call, no grant for subject
  → resolver calls token_endpoint {server, subject} → no grant → ErrNoToken
  → gate PARKS; task → auth-required; emit mcp_auth_required{subject, server, deadline}
  → platform delivers consent to subject (Slack DM "Connect Atlassian", console…)
  → subject completes OAuth; platform VAULTS the grant
  → platform: POST /mcp/consent {subject, server, granted:true}
  → gate resolves → parked call re-resolves → token_endpoint now returns a token → call proceeds
```

The resume call:

```http
POST /mcp/consent                       # on the agent's A2A server
Authorization: Bearer <agent token>     # NOT auth-exempt — you must authenticate
Content-Type: application/json

{ "subject": "user@corp.com", "server": "atlassian", "granted": true }
```

Response codes:

| Code | Meaning |
|------|---------|
| `200` | Gate resolved; every parked call for `{subject, server}` re-resolves and re-fetches the token. |
| `404` | No call is currently parked for that `{subject, server}`. |
| `400` | Missing `subject`/`server`, or malformed body. |
| `409` | Race — the gate already resolved (e.g. timed out) between check and resolve. |

- **`granted: false`** reports a refusal so the parked call **fails fast**
  instead of idling to the deadline. Omitting `granted` defaults to `true`.
- **Ordering is load-bearing:** send `granted: true` **only after** your
  `token_endpoint` can return a token for that subject. The consent endpoint
  merely unblocks re-resolution; if the grant isn't vaulted yet, the re-resolve
  hits `ErrNoToken` again and re-parks. Delegation follows authorization.
- **No token crosses the boundary.** Forge fetches the token itself via the
  delegated resolver (`POST {token_endpoint}` with `{server, subject}`) on
  re-resolution (AARM R10; `design-tool-registry.md` §18.5).

## 4. Embedding Forge (optional)

The stock binary is fully drivable via the HTTP signals above. If you link Forge
as a library and prefer push-delivery over consuming the audit stream, set the
deliverer seam so Forge calls you directly when a gate opens:

```go
runner.SetConsentDeliverer(func(ctx context.Context, subject, server, taskID string, deadline time.Time) error {
    return platform.DeliverConsentPrompt(subject, server, taskID, deadline) // Slack DM, etc.
})
```

Delivery is best-effort — an error is logged, never fatal; the park/resume still
works. The standalone loopback callback (`GET /mcp/oauth/callback`, public) is
for the Forge-hosts-its-own-callback case and is **not** used in managed mode.

## Durable token custody across replicas

The per-user access-token cache is the `SubjectTokenStore` interface (in-process
by default). A managed broker can substitute a shared/durable implementation so
grants survive restarts and are shared across replicas — the resolver and agent
are unchanged. See
[Configuration → the auth-required gate](./configuration.md#delegated-consent--the-auth-required-gate-330).

## Migration checklist

- [ ] `auth.type: user` + `ref` on the delegated servers; `required: false`; `platform.token_endpoint` set.
- [ ] Remove those MCP tools from any DEFER policy that was standing in for consent.
- [ ] Subscribe to `mcp_auth_required` / `mcp_auth_resolved` / `mcp_auth_timeout` (and/or watch task `auth-required`).
- [ ] Wire consent delivery → OAuth → **vault grant** → authenticated `POST /mcp/consent`.
- [ ] Ensure `token_endpoint` returns `{server, subject}` tokens **before** posting `granted: true`.
- [ ] Keep DEFER for genuine per-action approve/reject.

## Related docs

- [Configuration reference](./configuration.md) — every `mcp:` field + the gate concept
- [Audit events](./audit-events.md) — `mcp_auth_*` fields
- [DEFER decisions](../security/defer-decisions.md) — per-action approval (the tool you keep)
- [Authentication](../security/authentication.md) — the agent auth that guards `POST /mcp/consent`
