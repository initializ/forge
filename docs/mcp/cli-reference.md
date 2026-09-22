---
title: "MCP — CLI Reference"
description: "Every flag of every forge mcp subcommand."
order: 3
---

```
forge mcp list
forge mcp test    <name>  [--url <url> --token-store-path <dir>
                           --call <tool> --args '<json>' --timeout <dur>]
forge mcp login   <name>  [--url <url> --client-id <id> --scopes a,b
                           --authorize-url <u> --token-url <u> --token-store-path <dir>]
forge mcp logout  <name>
```

## `forge mcp list`

Prints a table of every configured server with a quick reachability
probe. Always exits 0 — one server down is reported in its row, not
as a process error.

```
NAME      TRANSPORT  URL                                  STATE   TOOLS  REASON
linear    http       https://mcp.linear.app/sse           ready   12
notion    http       https://mcp.notion.com/sse           ready   4
internal  http       http://internal.svc.cluster.local    failed  0      mcp: transport unavailable: HTTP 503
```

## `forge mcp test <name>`

Connects to one server, runs `initialize` + `tools/list`, and prints
the discovered tools with truncated input schemas. Exits non-zero on
any failure (useful in CI).

For an `oauth` server this reads the token stored by `forge mcp login`,
honoring the same `mcp.token_store_path` (forge.yaml) / `MCP_TOKEN_STORE_PATH`
(env) override as `login`/`logout` — so a server logged in under a custom
store path resolves correctly. Run `forge mcp login <name>` first;
`test` does not perform interactive login.

Flags:
- `--url <url>` — test **standalone**, without a `forge.yaml` (mirrors
  `mcp login --url`; see below).
- `--token-store-path <dir>` — credential-store dir override (default
  `~/.forge/credentials`; also honored via `MCP_TOKEN_STORE_PATH`).
- `--call <tool>` — also invoke this tool after listing.
- `--args '<json>'` — JSON arguments for `--call`. Default `{}`.
- `--timeout <duration>` — per-RPC timeout. Default 10s.

Examples:

```sh
forge mcp test linear
forge mcp test linear --call list_issues --args '{"first":5}'
```

### Standalone — no `forge.yaml` (`--url`)

For a **non-forge agent** (Strands, Claude, …) with no `forge.yaml`, pass `--url`
to verify a connection you logged into with `mcp login --url`. `test` builds the
server inline (`auth: oauth`) and reuses the token/registration `mcp login` stored
under `<name>` — it does not re-authenticate. With no `forge.yaml` allow-list, all
discovered tools are shown.

```sh
forge mcp login atlassian-read --url https://mcp.example/…    # once
forge mcp test  atlassian-read --url https://mcp.example/…    # list tools
forge mcp test  atlassian-read --url https://mcp.example/… --call search --args '{"query":"…"}'
```

`--url` must use **https** (a loopback host — `localhost`/`127.0.0.1`/`[::1]` — may
use http for a dev IdP): unlike `login`, `test` **replays the stored access token**
as a bearer to `--url`, so a plain-http target would leak it in cleartext. `--url`
is also the token *target* — pass the same server you `mcp login`'d against, since
`<name>` only selects which stored token to send.

`<name>` must match the name used at `mcp login` (it keys the stored token) and be a
slug `^[a-z][a-z0-9-]{0,30}$`. Without `--url`, `test` reads the server from
`forge.yaml` (unchanged).

## `forge mcp login <name>`

Runs the OAuth 2.1 PKCE flow against the named server. Opens a
`127.0.0.1` loopback listener on a random port, opens the operator's
browser at the authorization endpoint, exchanges the returned code for
tokens, and persists them encrypted at
`~/.forge/credentials/mcp_<name>.json`.

**Discovery (#316):** when the server's `auth` block omits
`client_id` / `authorize_url` / `token_url`, login **discovers** them
from the server `url` (RFC 9728 → RFC 8414) and **dynamically registers**
a client (RFC 7591). The discovered endpoints + minted `client_id` are
persisted alongside the token as `mcp_reg_<name>.json` and reused on
refresh — login never re-registers. Explicit config in `forge.yaml`
overrides discovery. See
[configuration.md](configuration.md#oauth-discovery--dynamic-client-registration-316).

### Standalone — no `forge.yaml` (`--url`)

For a **non-forge agent** (Strands, Claude, …) you have no `forge.yaml`. Pass the
connection inline and login skips forge.yaml entirely:

```sh
forge mcp login atlassian-read --url https://mcp.example/…
```

- `--url <url>` — the MCP server endpoint; **switches on standalone mode**. Must be a
  valid `http`/`https` URL (same rule the forge.yaml validator applies); discovery
  (RFC 9728/8414/7591) fills the client + endpoints. Plain `http://` prints a warning
  (the OAuth flow carries tokens).
- `--client-id <id>` — optional; DCR mints one when omitted.
- `--scopes a,b` — optional; discovered scopes are used when omitted.
- `--authorize-url` / `--token-url` — optional explicit endpoints; **must be set
  together** (or both omitted for discovery).
- `--token-store-path <dir>` — override the credential dir (else `MCP_TOKEN_STORE_PATH`,
  else `~/.forge/credentials`).

`<name>` must be a slug matching `^[a-z][a-z0-9-]{0,30}$` (it becomes the
`mcp_<name>.json` store file — the slug rule blocks path traversal on the store).
The stored token is consumable by any runtime — including the initializ Strands
SDK's direct (`auth: bearer`) lane, which reads the same `~/.forge/credentials/mcp_<name>.json`.

Without `--url`, login reads the server from `forge.yaml` (below) — unchanged.

Requires `auth.type: oauth` in the server's config (forge.yaml mode). Fails fast for
bearer / static / no-auth servers, and fails closed with a clear message
if a server advertises no metadata / no registration endpoint and no
`client_id` is configured.

**Agent-principal servers need no login.** A server with
`grant: client_credentials` (2LO — the agent authenticates as itself, #324)
mints its token at runtime; `forge mcp login <name>` on such a server
prints "no login needed" and exits successfully.

For Kubernetes deployments:

```sh
# On your laptop:
forge mcp login linear

# Bundle the credentials into a Secret. For a discovery-based server,
# include the registration record too (mcp_reg_<name>.json) — the
# runtime refresh path needs the discovered token_url + client_id:
kubectl create secret generic mcp-tokens \
  --from-file=mcp_linear.json=$HOME/.forge/credentials/mcp_linear.json \
  --from-file=mcp_reg_linear.json=$HOME/.forge/credentials/mcp_reg_linear.json

# In the pod spec:
volumeMounts:
  - name: mcp-tokens
    mountPath: /etc/forge/credentials
    readOnly: true
env:
  - name: HOME
    value: /etc/forge          # so llm/oauth.LoadCredentials finds the file
```

## `forge mcp logout <name>`

Deletes the stored OAuth token **and** the discovery/registration record
(`mcp_reg_<name>.json`) for the server. Idempotent. Clearing the
registration is the recovery path when an authorization server revokes
the dynamically-registered client: the next `forge mcp login` re-discovers
and re-registers from scratch.

```sh
forge mcp logout linear
```
