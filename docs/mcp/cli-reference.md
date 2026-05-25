---
title: "MCP — CLI Reference"
description: "Every flag of every forge mcp subcommand."
order: 3
---

```
forge mcp list
forge mcp test    <name>  [--call <tool> --args '<json>' --timeout <dur>]
forge mcp login   <name>
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

Flags:
- `--call <tool>` — also invoke this tool after listing.
- `--args '<json>'` — JSON arguments for `--call`. Default `{}`.
- `--timeout <duration>` — per-RPC timeout. Default 10s.

Examples:

```sh
forge mcp test linear
forge mcp test linear --call list_issues --args '{"first":5}'
```

## `forge mcp login <name>`

Runs the OAuth 2.1 PKCE flow against the named server. Opens a
`127.0.0.1` loopback listener on a random port, opens the operator's
browser at the configured `authorize_url`, exchanges the returned
code for tokens, and persists them encrypted at
`~/.forge/credentials/mcp_<name>.json`.

Requires `auth.type: oauth` in the server's config. Fails fast for
bearer / static / no-auth servers.

For Kubernetes deployments:

```sh
# On your laptop:
forge mcp login linear

# Bundle the credentials file into a Secret:
kubectl create secret generic mcp-tokens \
  --from-file=mcp_linear.json=$HOME/.forge/credentials/mcp_linear.json

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

Deletes the stored OAuth token for the server. Idempotent.

```sh
forge mcp logout linear
```
