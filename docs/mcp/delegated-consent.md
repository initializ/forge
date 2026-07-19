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

Mark each delegated MCP server `auth.type: user` and wire the platform block.
The gate activates automatically for `type: user` servers.

```yaml
platform:
  token_endpoint:     https://platform.internal/agents/{id}/mcp/token      # returns the access token
  authorize_endpoint: https://platform.internal/agents/{id}/mcp/authorize  # returns the consent login URL (#343)
  agent_identity:     ${FORGE_PLATFORM_TOKEN}   # Bearer the agent sends on BOTH calls

mcp:
  servers:
    - name: atlassian
      transport: http
      url: https://mcp.atlassian.example/mcp
      required: false                 # MUST be false — no user exists at startup
      auth:
        type: user                    # delegated per-user identity; activates the gate
        ref: atlassian-registry-entry # platform tool-registry key BOTH endpoints authorize against
      tools:
        allow: ["*"]                  # or an explicit allowlist (default-deny)
```

- **`token_endpoint`** (required) — where Forge fetches the per-user access
  token. See [§2](#2-the-two-platform-endpoints).
- **`authorize_endpoint`** (optional) — where Forge fetches the consent **login
  URL** so it can deliver the prompt itself (e.g. over Slack). Omit it if the
  **platform** delivers the prompt instead (see [§4](#4-two-delivery-models)).
- **`agent_identity`** — the agent's platform credential, sent as
  `Authorization: Bearer …` on both calls (the platform injects it as a pod
  secret; `${VAR}` is expanded at request time, so rotation needs no restart).

To let Forge deliver over Slack, also start the adapter — `forge run --with
slack` — with bot scopes `chat:write`, `users:read.email`, `im:write`.

Invariants the runtime enforces:

| Rule | Why |
|------|-----|
| `type: user` must not be `required: true` | Delegated identity is lazy — there is no user at boot, so the server can't be a startup gate. |
| No `token_store_path`, no `forge mcp login` for `type: user` | The token is materialized by your `token_endpoint`, keyed `{server, subject}` — never stored on disk by the agent. |
| The gate attaches to every MCP tool automatically | It only trips on an `ErrNoToken` from the per-user resolver, so non-delegated servers are unaffected — nothing to disable. |
| Both endpoint hosts are auto-merged into the egress allowlist | Forge's outbound calls to them ride the same allowlist as the rest of the agent. |

Remove those MCP tools from any DEFER policy that was standing in for consent.

## 2. The two platform endpoints

The platform implements two HTTP endpoints. Both take the **same** request shape
— `POST` with `Authorization: Bearer <agent_identity>`, `Org-Id` / `Workspace-Id`
tenancy headers (from `FORGE_ORG_ID` / `FORGE_WORKSPACE_ID`, when set), and a JSON
body `{"server": "<ref>", "subject": "<email>"}` — and are keyed on the same
`{ref, subject}` so you can correlate them.

### `token_endpoint` — provide the token

```http
POST {token_endpoint}
Authorization: Bearer <agent_identity>
{ "server": "atlassian-registry-entry", "subject": "user@corp.com" }
```

| Response | Meaning |
|----------|---------|
| `200 {"access_token": "…", "expires_in": 3600}` | The delegated access token for this user. Forge caches it per-subject (`expires_in` seconds; default 5 min if omitted). **Return only a short-lived access token — never the refresh token** (it stays in your vault). |
| `401` / `403` / `404` | **No grant for this user yet** → Forge treats it as `ErrNoToken`, which trips the auth-required gate and parks the call. This is the signal that starts the consent flow. |
| other non-200 | Protocol error — the call fails (not parked). |

### `authorize_endpoint` — provide the authorization URL

```http
POST {authorize_endpoint}
Authorization: Bearer <agent_identity>
{ "server": "atlassian-registry-entry", "subject": "user@corp.com" }

→ 200 { "authorize_url": "https://auth.atlassian.com/authorize?client_id=<yours>&redirect_uri=<your-callback>&state=<yours>&…" }
```

- You build the URL with **your own** `client_id`, `redirect_uri` (pointing at
  **your** callback), `state`, PKCE, and scopes. Forge treats it as **opaque** —
  it only delivers it.
- Because `redirect_uri` is yours, the browser lands back at **your** callback:
  you receive the authorization `code`, exchange it, and vault the refresh token.
  **Forge never sees the code or the refresh token.**
- Must be an absolute `https://` URL (Forge rejects anything else before it
  reaches a clickable button). Non-200 fails delivery (the call still surfaces
  via `mcp_auth_required`).

## 3. Detect a pending consent

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

## 4. Two delivery models

Presenting the link is independent of who receives the code — so you choose who
**delivers** the "Connect" prompt. Token custody is the platform's either way.

| | **Forge delivers (over Slack)** | **Platform delivers** |
|---|---|---|
| Who shows the link | Forge DMs the subject over its own Slack bot | Your platform (its Slack/console/email) |
| Where the URL comes from | Forge `POST`s your **`authorize_endpoint`** | You build it internally — no `authorize_endpoint` needed |
| Config | set `authorize_endpoint` + run `--with slack` | omit `authorize_endpoint`; consume `mcp_auth_required` |
| Callback / refresh token | **yours** (the URL's `redirect_uri` is yours) | **yours** |

Either way the link is **also** published on the task's A2A `auth-required`
artifact as a durable record, so a UI/A2A client can render it and a per-subject
Slack failure never strands the user.

**End-to-end (Forge-delivers-over-Slack):**

```
type: user call, no grant for subject
  → Forge POSTs token_endpoint {server, subject} → 401/403/404 → ErrNoToken
  → gate PARKS; task → auth-required; emit mcp_auth_required{subject, server, deadline}
  → Forge POSTs authorize_endpoint {server, subject} → {authorize_url}
  → Forge DMs the subject the "Connect" link (+ publishes it on the task artifact)
  → subject signs in at YOUR callback → you exchange the code + VAULT the refresh token
  → platform: POST /mcp/consent {subject, server, granted:true}
  → gate resolves → parked call re-resolves → token_endpoint now returns a token → call proceeds
```

(For the platform-delivers model, drop the two Forge lines: you deliver the
prompt off the `mcp_auth_required` event and host the whole OAuth yourself.)

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
- **Cancel = fast-fail.** In the Forge-delivers-over-Slack model, a "Cancel"
  button posts `{granted:false}` to the same endpoint for you, so the parked
  call fails immediately instead of idling to the deadline.

## 5. Embedding Forge (optional)

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

- [ ] `auth.type: user` + `ref` on the delegated servers; `required: false`; `platform.token_endpoint` + `agent_identity` set.
- [ ] Implement `token_endpoint` — `200 {access_token, expires_in}` when granted, `401/403/404` when not (this trips the gate). Return **no refresh token**.
- [ ] Choose a delivery model: **Forge-over-Slack** (set `authorize_endpoint` returning `{authorize_url}` + run `--with slack`) **or** platform-delivers (consume `mcp_auth_required`).
- [ ] Point the authorize URL's `redirect_uri` at **your** callback → you exchange the code and **vault the refresh token**.
- [ ] After vaulting, `POST /mcp/consent {subject, server, granted:true}` (authenticated) — **only after** `token_endpoint` can return the token.
- [ ] Subscribe to `mcp_auth_required` / `mcp_auth_resolved` / `mcp_auth_timeout` (and/or watch task `auth-required`).
- [ ] Keep DEFER for genuine per-action approve/reject.

## Related docs

- [Configuration reference](./configuration.md) — every `mcp:` field + the gate concept
- [Audit events](./audit-events.md) — `mcp_auth_*` fields
- [DEFER decisions](../security/defer-decisions.md) — per-action approval (the tool you keep)
- [Authentication](../security/authentication.md) — the agent auth that guards `POST /mcp/consent`
