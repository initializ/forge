---
title: "Model Context Protocol (MCP)"
description: "Pluggable MCP client — connect Forge agents to any HTTP MCP server."
order: 1
---

Forge agents can act as **MCP clients**, consuming tools from any
spec-compliant Model Context Protocol server (Linear's hosted MCP,
Notion, GitHub, Atlassian, or your own). Discovered tools are
registered as namespaced `<server>__<tool>` first-class tools that
flow through the existing LLM executor — no custom code in the
agent loop.

## Phase 1 scope (v0.12.0)

Phase 1 ships HTTP transport only, declared servers, and the four
CLI subcommands operators need to run and debug an MCP setup.

| Capability                                        | Phase 1 |
|---------------------------------------------------|:-------:|
| HTTP MCP servers in `forge.yaml`                  |   ✅    |
| OAuth 2.1 PKCE + bearer + static auth             |   ✅    |
| Per-server lifecycle, parallel startup, backoff   |   ✅    |
| Namespaced tool registration (`<srv>__<tool>`)    |   ✅    |
| Egress allowlist auto-merge                       |   ✅    |
| Audit events (`mcp_*`)                            |   ✅    |
| `forge mcp list/test/login/logout`                |   ✅    |
| **Stdio MCP servers**                             | 🛣 roadmap |
| MCP resources / prompts / sampling primitives     | 🛣 roadmap |

## Stdio servers — roadmap

The majority of community MCP servers (Notion, Linear community,
Atlassian, GitHub local, the modelcontextprotocol/servers reference
set) ship stdio-only today. Phase 1 does **not** support stdio: the
Forge runtime never spawns subprocesses for MCP, and `transport:
stdio` is rejected at `forge validate` time.

The deferred strategy is captured in the recommendations doc; the
shortlist of options under consideration:

- **Project-based mcp-proxy.** Per-agent bridge pod, declared in
  `forge.yaml`, scoped to the MCP servers that agent needs.
- **Provider-based mcp-proxy.** One bridge pod per provider (Notion,
  Jira, …) shared across agents in a namespace.
- **MCP gateway.** Platform-level shared service with central auth,
  audit, and a tool catalog.

Pick will follow real-world usage of Phase 1.

## Quick start

A minimal `forge.yaml` with one hosted MCP server:

```yaml
agent_id: my-agent
version: 0.1.0
framework: forge

mcp:
  servers:
    - name: linear
      transport: http
      url: https://mcp.linear.app/sse
      auth:
        type: oauth
        client_id: ${LINEAR_OAUTH_CLIENT_ID}
        scopes: [read, write]
        authorize_url: https://linear.app/oauth/authorize
        token_url: https://api.linear.app/oauth/token
      tools:
        allow: [create_issue, list_issues, update_issue]
```

Workflow:

```sh
forge mcp login linear                  # one-time, on laptop
forge mcp list                          # verify state == ready
forge mcp test linear                   # print discovered tools
forge run                               # tools available as linear__create_issue, etc.
```

## Related docs

- [Configuration reference](./configuration.md) — every `mcp:` field
- [CLI reference](./cli-reference.md) — every flag of every subcommand
- [Audit events](./audit-events.md) — the seven event types and their fields
- [Troubleshooting](./troubleshooting.md) — reason codes → fixes
- [Delegated consent (platform integration)](./delegated-consent.md) — deploy `type: user` servers and drive the auth-required gate
