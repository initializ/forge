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

`token_env` is the name of an environment variable; the variable's
value is read at runtime — never stored in `forge.yaml`.

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
- **Confidential clients are not supported.** Forge registers a public
  (PKCE) client and sends no `client_secret`. If a server insists on
  issuing a confidential client, login fails closed — configure
  `client_id`/`authorize_url`/`token_url` explicitly for that server.

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
