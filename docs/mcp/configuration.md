---
title: "MCP — Configuration Reference"
description: "Full schema for the forge.yaml mcp: block."
order: 2
---

The `mcp:` block in `forge.yaml` declares the MCP servers an agent
connects to. Empty / absent is fine — agents without `mcp:` work
exactly like pre-v0.12.0 agents.

## Schema

```yaml
mcp:
  token_store_path: ~/.forge/credentials   # optional; OAuth token dir
  servers:
    - name: linear                          # required, slug
      transport: http                       # required, "http" only in Phase 1
      url: https://mcp.linear.app/sse       # required for transport: http
      auth:                                 # optional
        type: oauth                         # oauth | bearer | static
        client_id: my-client-id             # optional for oauth (see Discovery)
        scopes: [read, write]               # optional
        authorize_url: https://...          # optional for oauth (discovered if omitted)
        token_url: https://...              # optional for oauth (discovered if omitted)
        token_env: NAME_OF_ENV_VAR          # required if type=bearer|static
      tools:                                # default-deny — at least one of:
        allow: [create_issue, list_issues]  # explicit names or ["*"]
        deny:  [drop_table]                 # subtractive, optional
      timeout: 60s                          # default 60s; min 1s
      required: false                       # default false
```

## Field reference

### `mcp.token_store_path`

Directory where OAuth tokens are persisted by `forge mcp login`.
Default: `~/.forge/credentials/` (each server gets its own
`mcp_<name>.json` file). Override via env `MCP_TOKEN_STORE_PATH`.

### `mcp.servers[].name`

Slug-format identifier, `^[a-z][a-z0-9-]{0,30}$`. Used as the tool
namespace prefix: tool `create_issue` exposed by server `linear`
becomes `linear__create_issue` in the registry.

### `mcp.servers[].transport`

Phase 1: must be `"http"`. `transport: stdio` is rejected at
`forge validate` time with a roadmap pointer.

### `mcp.servers[].url`

The MCP server's Streamable HTTP endpoint. Must be `http://` or
`https://`. Both vendor-hosted (`https://mcp.linear.app/sse`) and
in-cluster (`http://internal-mcp.svc.cluster.local:8080/mcp`) are
supported.

### `mcp.servers[].auth`

Optional. Omit for unauthenticated servers (e.g., trusted in-cluster
MCPs).

| `auth.type` | Required fields | When to use |
|-------------|-----------------|-------------|
| `oauth`     | *(none required — see Discovery)* | Hosted MCPs (Linear, Notion, GitHub hosted) |
| `bearer`    | `token_env`     | In-cluster sidecars; CI machine-to-machine |
| `static`    | `token_env`     | Same as bearer; named for clarity         |
| `platform`  | top-level `platform` block | **Managed:** agent-principal (service) identity resolved by the platform |
| `user`      | top-level `platform` block **or** explicit endpoints (standalone) | Delegated per-requesting-user identity — **managed** (platform-resolved) or **standalone** (Forge runs the OAuth itself, #332) |

`token_env` is the name of an environment variable; the variable's
value is read at runtime — never stored in `forge.yaml`.

#### Managed identity — `type: platform` / `type: user`

In a platform-managed deployment, Forge doesn't hold long-lived
credentials at all: it fetches a **short-lived access token** from the
platform's token endpoint per request. The refresh token stays platform-side
and never reaches the agent. Both types use the top-level `platform` block:

```yaml
platform:
  token_endpoint: ${INITIALIZ_TOKEN_ENDPOINT}   # ${VAR}-expanded at use
  agent_identity: ${FORGE_PLATFORM_TOKEN}       # the agent's platform credential

mcp:
  servers:
    - name: atlassian-read
      url: https://mcp.atlassian.com/mcp
      auth: { type: platform, ref: mcp.atlassian }   # agent-principal
      required: true          # startup-viable: no human needed
    - name: atlassian-write
      url: https://mcp.atlassian.com/mcp
      auth: { type: user, ref: mcp.atlassian }       # delegated per-user
      required: false         # lazy: no user at startup
```

- **`type: platform`** — agent-principal. Forge POSTs `{server: <ref>}` with
  the agent credential; startup-viable (no user, no login). `required: true`
  is valid.
- **`type: user`** (#317) — delegated. Forge resolves the **requesting user's**
  identity from the authenticated request and POSTs `{server: <ref>, subject}`,
  so each user gets **their own** token *and its own connection*. It is
  **inherently lazy**: `required: true` is rejected (there is no user at
  startup), and until the platform has a grant for that user the call
  **pauses** on the [auth-required gate](#delegated-consent--the-auth-required-gate-330)
  (rather than failing) until they consent.
- **`tools.schemas`** is **required** for `type: user` — the server has no
  connection at startup (no user), so it can't run `tools/list`. The platform
  **materializes** the tool schemas from the registry entry into config, and
  Forge registers them without a live connection; the per-user connection is
  established lazily on the first call. See [Materialized tool schemas](#materialized-tool-schemas-317).
- **`ref`** names the platform tool-registry entry the token is authorized
  against (defaults to the server name).
- **Egress:** the platform token-endpoint host is auto-merged into the
  allowlist.

> **Per-user connection isolation.** For `type: user`, Forge establishes one
> MCP **connection per requesting user** — the connection's `initialize` runs
> under that user's token, so identity is bound at the connection level, not
> just per call. This holds even for a **session-stateful** server that binds
> identity at `initialize`: user A's calls ride user A's session, user B's ride
> B's. Connections are established lazily (first call) and per subject.

> The platform materializes both the `platform` block and the per-identity
> server entries (same URL, split by `type`). For a standalone agent-principal,
> use `type: oauth` with `grant: client_credentials` (above); for standalone
> **per-user** delegation, use standalone `type: user` (below).

#### Standalone delegated consent — `type: user` without a platform (#332)

When there is **no** `platform` block, `type: user` runs the delegated OAuth
**in Forge itself**: a grantless call parks on the auth-required gate, the user
is shown a "Connect" login link, they consent in the browser, and Forge stores
their per-user access token in memory (no disk) so the call resumes.

```yaml
server:
  public_url: https://agent.example.com     # or the AGENT_URL env var — see below

mcp:
  servers:
    - name: atlassian
      url: https://mcp.atlassian.com/mcp
      auth:
        type: user
        # standalone requires explicit endpoints + client_id (no runtime discovery):
        client_id: ${ATLASSIAN_CLIENT_ID}
        authorize_url: https://auth.atlassian.com/authorize
        token_url: https://auth.atlassian.com/oauth/token
        scopes: [read:jira-work, write:jira-work]
      required: false
      tools: { allow: ["*"] }
```

- **`grant`** is `authorization_code` (the default) — `client_credentials` is
  rejected for standalone `type: user`.
- **Explicit endpoints + `client_id` are required.** Standalone does not run
  discovery/registration at runtime, so `authorize_url`, `token_url`, and
  `client_id` must be set. (Add a `platform` block instead for managed
  delegation, which resolves tokens without any of these.)
- **`server.public_url`** (falling back to the `AGENT_URL` env var) is the
  agent's externally-reachable base URL. Forge builds `redirect_uri =
  <public_url>/mcp/oauth/callback`, so the URL must be reachable by the user's
  browser after IdP consent.
- **Delivery.** The login link is published on the parked task's A2A
  `auth-required` artifact (a UI/A2A client renders it). Forge-driven Slack
  delivery of the same link is tracked separately (#343).
- **Endpoints registered** (only in standalone mode): `GET /mcp/oauth/start`
  (sets a `forge_session` cookie, then redirects to the IdP) and
  `GET /mcp/oauth/callback` (validates state + session, exchanges the code,
  resumes the parked call). Both are auth-exempt (anonymous browser hops); their
  authenticity rests on the single-use, expiring, session-bound state — see the
  [auth-required gate](#delegated-consent--the-auth-required-gate-330).

> **Trust model / limitation (standalone).** The consent link is a **bearer
> capability**: the browser that completes it is anonymous, so the session
> cookie only proves the *same browser* did `/start` and `/callback` across the
> IdP round-trip — it does **not** prove the completing user is the parked
> subject. If the link leaks, an attacker could authenticate at the IdP as
> *themselves* and have that token filed under the victim's subject (a
> confused-deputy / token-fixation vector). This is bounded by the single-use,
> short-TTL state and, above all, by **delivering the link only over an
> authenticated channel** to the requesting user (the A2A `auth-required`
> artifact in their own session; the Slack DM in #343). Treat the link as a
> secret. The tamper-proof (heavier) alternative — verifying the IdP `userinfo`
> identity against the parked subject at exchange time — is deferred; managed
> mode sidesteps it entirely (the platform owns the callback and token custody).

#### Materialized tool schemas (#317)

A `type: user` server can't discover tools at startup (no user ⇒ no
connection), so its tools are declared under `tools.schemas` — the platform
materializes them from the tool-registry entry. Forge registers them without
a live connection; `allow`/`deny` still filter this set.

```yaml
mcp:
  servers:
    - name: atlassian-write
      transport: http
      url: https://mcp.atlassian.com/mcp
      auth: { type: user, ref: mcp.atlassian }
      required: false
      tools:
        allow: ["*"]
        schemas:                    # platform-materialized; no live tools/list
          - name: create_issue
            description: Create a Jira issue
            input_schema:
              type: object
              properties:
                project: { type: string }
                summary: { type: string }
              required: [project, summary]
```

- Each schema is `name` + optional `description` + `input_schema` (a JSON
  Schema authored as YAML; an omitted `input_schema` defaults to
  `{"type":"object"}`). Names are validated exactly as discovered tools are —
  non-empty and no `__` (the namespace separator).
- The first call by a user establishes that user's connection lazily and runs
  `initialize` under their token; subsequent calls reuse it.

> **The schema set is a global declaration — per-user access is enforced at
> call time.** `tools.schemas` (and the `allow`/`deny` filter) is the same for
> every user: it's what the server *can* expose. A user whose platform grant
> doesn't cover a materialized tool still sees it registered, so the LLM may
> attempt it and get a **runtime auth error from that user's own connection** —
> the per-user gate is the connection, not registration.

> **Staleness.** Materialized schemas are a snapshot. If the MCP server's real
> tool set changes, the `tools.schemas` in `forge.yaml` is stale until the
> platform re-materializes the registry entry (a redeploy) — the same
> snapshot semantics as the `allow: ["*"]` discovery filter.

#### Delegated consent — the auth-required gate (#330)

When a `type: user` call has **no grant yet** for the requesting user, the
tool call does not fail — it **pauses** on the auth-required gate, the user is
prompted to consent, and the call **resumes** with their token once a grant
exists. This is the delegated analog of the [DEFER](../security/defer-decisions.md)
park/resume: DEFER waits for a human to *approve an action*; the auth gate
waits for a user to *complete OAuth consent*.

```
type: user call, no grant  →  PARK (task status → auth-required)
                           →  prompt the user to consent
                           →  user consents → platform holds a grant
                           →  RESUME → the call re-resolves and proceeds
```

- **One prompt per user, not per call.** The gate is keyed by
  `{subject, server}`, so a user's concurrent calls (in any task) share **one**
  gate and **one** consent; a single grant resumes them all.
- **The gate never sees a token.** It only unblocks the executor to re-resolve
  through the normal delegated path — the token is fetched by the resolver, not
  handed through the agent (AARM R10; `design-tool-registry.md` §18.5).
- **Bounded.** A call parks for at most the gate timeout (default 10m), then
  fails auth-required. Audit events: `mcp_auth_required` → `mcp_auth_resolved`
  / `mcp_auth_timeout`.

**Resuming a parked call — two modes (§18.4):**

| Mode | Consent delivery + callback | Resume signal to Forge |
|------|-----------------------------|------------------------|
| **Managed** | The platform prompts (Slack DM / console), hosts the OAuth callback, and holds the token. | `POST /mcp/consent` with `{subject, server}` (optionally `granted:false` to refuse). Carries **no token** — a pure "a grant now exists, re-resolve" signal. |
| **Standalone** | Forge hosts its own loopback callback `GET /mcp/oauth/callback`. | The callback validates the OAuth `state` (single-use, expiring, **session-bound** — cross-session/replayed/expired callbacks are rejected), exchanges the code for a token, then resumes. |

> **Never resume before the grant exists.** In standalone mode the callback
> resumes the gate **only after** the code→token exchange succeeds — resuming
> with no stored token would just re-park the call. Delegation follows
> authorization.

> **Swappable token store.** The per-user access-token cache is the
> `SubjectTokenStore` interface (in-process by default). A managed broker can
> substitute a shared/durable implementation so grants survive restarts and are
> shared across replicas — the resolver and agent are unchanged. Only
> short-lived **access** tokens live here; refresh tokens never leave the
> broker.

#### OAuth discovery & dynamic client registration (#316)

For `type: oauth`, `client_id` / `authorize_url` / `token_url` are all
**optional**. When omitted, Forge discovers them from the server `url`
at `forge mcp login` time, using the MCP Authorization spec:

1. **RFC 9728** — protected-resource metadata (from the server's `401`
   `WWW-Authenticate` header, or `{origin}/.well-known/oauth-protected-resource`)
   to find the authorization server.
2. **RFC 8414** — authorization-server metadata
   (`/.well-known/oauth-authorization-server`, with the OpenID
   `openid-configuration` variant as a fallback) to discover the
   `authorize` / `token` / `registration` endpoints.
3. **RFC 7591** — dynamic client registration mints a `client_id` at
   first login; it is persisted (encrypted, alongside the token) and
   **reused** on refresh — never re-minted per run.

So a fully zero-config OAuth server is just:

```yaml
- name: linear
  transport: http
  url: https://mcp.linear.app/mcp
  auth:
    type: oauth
    scopes: [read, write]   # optional; discovery uses scopes_supported when omitted
  tools:
    allow: [create_issue, list_issues]
```

**Precedence & rules:**

- **Discovery is the standalone default; explicit config always wins.**
  Setting `client_id`/`authorize_url`/`token_url` is both the *static
  override* (for servers that don't advertise metadata or don't support
  DCR) **and the platform-materialized path** — the normal case when a
  control plane materializes these fields from a registry entry. Explicit
  config is not just the exception; under managed/admission-time
  provisioning it is the common case.
- `authorize_url` and `token_url` must be set **together** (or both
  omitted); a partial pair is a validation error.
- **Fail-closed:** if a server advertises no metadata / no
  `registration_endpoint` and no `client_id` is configured, login fails
  with a clear message — supply the fields explicitly, or use a
  discovery-capable server.
- **Egress:** the discovered authorization-server host isn't in
  `forge.yaml` to pre-seed the allowlist, so it is learned from the
  login-time registration record and merged into the egress allowlist at
  runtime automatically. (Discovery itself runs at laptop-time
  `forge mcp login`, off the egress-enforced path.)
- **Recovery (revoked/expired client):** a dynamically-registered client
  is minted once and never re-minted. If the authorization server revokes
  it (or `client_secret_expires_at` passes), run `forge mcp logout <name>`
  — that clears both the token and the stored registration — then
  `forge mcp login <name>` again to re-discover and re-register.
- **Confidential clients are not supported** *for the interactive grant*.
  Forge registers a public (PKCE) client and sends no `client_secret`. If
  a server insists on issuing a confidential client, login fails closed —
  configure `client_id`/`authorize_url`/`token_url` explicitly for that
  server. (Confidential credentials *are* used by the `client_credentials`
  grant below.)

#### Agent-principal — `grant: client_credentials` (2LO, #324)

The default `oauth` grant is 3-legged: a **user** consents once via
`forge mcp login`. Set `grant: client_credentials` for the
**agent-principal** path — the deployed agent authenticates as **itself**,
with no user and no browser, so it works **headless**:

```yaml
mcp:
  servers:
    - name: internal-api
      transport: http
      url: https://mcp.internal.corp/mcp
      auth:
        type: oauth
        grant: client_credentials       # 2-legged; agent acts as itself
        client_id: forge-agent
        client_secret_env: MCP_INTERNAL_SECRET   # NAME of an env var; never in yaml
        token_url: https://mcp.internal.corp/token
        scopes: [read]
      tools: { allow: ["*"] }
```

- **Requires** an explicit `client_id`, `client_secret_env`, and
  `token_url` (2LO has no authorization endpoint and no dynamic
  registration). `authorize_url` is not used.
- `client_secret_env` names an environment variable (like `token_env`) —
  the secret is read at runtime and rotated by redeploy, never stored in
  `forge.yaml`.
- **No `forge mcp login`** — the token is minted at runtime and re-minted
  on expiry. `forge mcp login <name>` on such a server prints "no login
  needed."
- **`required: true` is valid here** (unlike a delegated/per-user server,
  which has no user at startup): the agent-principal token resolves at
  startup, so a required server can gate readiness.
- The token endpoint host is auto-added to the egress allowlist (from
  `token_url`).

> Use this where the MCP server supports the client_credentials / 2LO
> grant and the agent should act as a service identity. For per-user
> (delegated) identity, see #317. Managed platform-brokered tokens are
> #324's follow-on (the platform holds the service refresh token).

### `mcp.servers[].tools`

**Default-deny.** Validation rejects entries where both `allow` and
`deny` are empty — operators must be explicit about exposure.

- `allow: ["*"]` exposes every tool discovered at first connect
  (**snapshot semantics** — tools the server adds later do NOT
  appear without a rebuild).
- `deny` subtracts from either an explicit allow set or the
  wildcard.
- A tool listed in both `allow` and `deny` is a validation error.
- Tool names follow `^[a-zA-Z0-9_]{1,64}$`.

### `mcp.servers[].timeout`

Per-RPC timeout. Default 60s. Minimum 1s.

### `mcp.servers[].required`

- `true` — failure during startup (e.g. unreachable URL, OAuth
  refresh denied) aborts `forge run` with a non-zero exit. K8s
  observes `CrashLoopBackOff`.
- `false` (default) — failure logs a warning; the agent starts
  without that server's tools.

## Worked examples

### Vendor-hosted MCP with OAuth (discovery — preferred)

Point at the server's Streamable HTTP endpoint (`/mcp`, not the legacy
`/sse`) and let discovery resolve everything:

```yaml
mcp:
  servers:
    - name: linear
      transport: http
      url: https://mcp.linear.app/mcp
      auth:
        type: oauth
        scopes: [read, write]     # client_id + endpoints discovered (#316)
      tools:
        allow: [create_issue, list_issues]
      required: true
```

Then `forge mcp login linear` once — Forge discovers the endpoints and
registers a client automatically.

### Vendor-hosted MCP with OAuth (explicit override)

For a server that doesn't advertise metadata or doesn't support dynamic
client registration, pin the fields (this overrides discovery):

```yaml
mcp:
  servers:
    - name: linear
      transport: http
      url: https://mcp.linear.app/mcp
      auth:
        type: oauth
        client_id: ${LINEAR_OAUTH_CLIENT_ID}
        scopes: [read, write]
        authorize_url: https://linear.app/oauth/authorize
        token_url: https://api.linear.app/oauth/token
      tools:
        allow: [create_issue, list_issues]
      required: true
```

#### Environment placeholders (#321)

`${VAR}` / `$VAR` placeholders in the MCP connection fields — `url`,
`client_id`, `authorize_url`, `token_url`, and `scopes` — are **expanded
at load** from the process environment (and the `.env` file loaded by
`forge run`), matching the egress-domain expansion semantics. This is how
managed/platform mode ships a server: the generated `forge.yaml` bakes
only the *shape* (literal `url` + placeholders), and the values are
injected into the agent's env at deploy — so rotating a pinned client is
a redeploy, not a rebuild.

```yaml
auth:
  type: oauth
  client_id: ${MCP_LINEAR_CLIENT_ID}
  authorize_url: ${MCP_LINEAR_AUTHORIZE_URL}
  token_url: ${MCP_LINEAR_TOKEN_URL}
  scopes: ["${MCP_LINEAR_SCOPES}"]     # one var carrying "read write" → two scopes
```

- With all of `client_id`/`authorize_url`/`token_url` present after
  expansion, resolution takes the **explicit** branch — no runtime
  discovery or DCR (the managed-mode contract).
- An **unset** variable expands to `""`, so the block reads as
  *unconfigured* and falls to the discovery / fail-closed path — never a
  literal `${…}` dial.
- A `scopes` entry is **split on whitespace** post-expansion, so
  `${MCP_LINEAR_SCOPES}="read write"` becomes `[read, write]`. (The split
  is unconditional — a literal `["read write"]` splits too; OAuth scopes
  are space-delimited by RFC 6749, so a value never has an internal space.)
- `token_env` (for `bearer`/`static`) is **not** expanded — it is the
  *name* of an env var, resolved at runtime.

> ⚠️ **Keep `url` literal.** Although `url` accepts placeholders for
> symmetry, the **build-time egress freeze** reads MCP hosts from the
> config at *build* time — where the deploy env is unset, so a
> `url: ${MCP_URL}` expands to `""`. That empty value fails the
> `url is required for http transport` validation (a loud build error),
> and even if it slipped through it wouldn't be in the frozen
> `egress_allowlist.json` / NetworkPolicy, blocking the host at runtime in
> a container. Managed mode keeps `url` literal and places placeholders
> only on the auth fields — do the same.

### In-cluster MCP with bearer token from a K8s Secret

```yaml
mcp:
  servers:
    - name: internal
      transport: http
      url: http://internal-mcp.default.svc.cluster.local:8080/mcp
      auth:
        type: bearer
        token_env: INTERNAL_MCP_TOKEN
      tools:
        allow: ["*"]
        deny:  [drop_table, truncate_table]
      required: true
```

## Migrating from the deprecated `mcp_call` builtin

If your `forge.yaml` lists `mcp_call` under `tools:`, remove it and
replace with the new block:

```yaml
# Before (no longer works)
tools:
  - name: mcp_call

# After
mcp:
  servers:
    - name: my-mcp
      transport: http
      url: https://...
      tools:
        allow: [...]
```

LLM-side, each tool is now invoked directly by name
(e.g. `linear__create_issue(...)`) instead of through a single
`mcp_call(...)` wrapper. The LLM no longer needs to know the MCP
endpoint URL or RPC shape — Forge handles that.
