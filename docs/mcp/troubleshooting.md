---
title: "MCP — Troubleshooting"
description: "Common Phase 1 failure modes mapped to fixes."
order: 5
---

Walk the table top-to-bottom — most failures fall in the first few
rows.

## Reachability and config

| Symptom                                                                      | Likely cause                                                              | Fix                                                              |
|------------------------------------------------------------------------------|---------------------------------------------------------------------------|------------------------------------------------------------------|
| `forge validate` rejects `transport: stdio`                                  | Stdio is on the roadmap; Phase 1 supports HTTP only                       | Wait for the deferred stdio strategy or run an HTTP MCP          |
| `forge validate` says `at least one of allow or deny must be set`            | Default-deny — explicit opt-in required                                   | Add `tools: { allow: ["*"] }` or list specific tool names        |
| `forge mcp list` shows `failed` with `transport unavailable: dial`           | Server URL unreachable; DNS or firewall                                   | `curl <url>` from the same network; check `forge.yaml` URL       |
| `forge mcp list` shows `failed` with `protocol version mismatch`             | Server announces a different MCP `protocolVersion` than the pinned one    | Update the MCP server or wait for a Forge release pinning newer  |

## OAuth

| Symptom                                                                      | Likely cause                                            | Fix                                                            |
|------------------------------------------------------------------------------|---------------------------------------------------------|----------------------------------------------------------------|
| `forge mcp login` opens browser but `state mismatch` after consent           | Stale callback or malicious redirect attempt            | Re-run; if persistent, check for browser extensions injecting URLs |
| At runtime: `mcp: oauth token revoked`                                       | Refresh token denied — usually expired or user revoked  | `forge mcp login <name>` again; re-bundle the Secret           |
| At runtime: `mcp: no stored token for "<name>" — run 'forge mcp login ...'`  | Token store empty inside the pod                        | Mount the Secret correctly; set `HOME` so the store path resolves |

## Tool name conflicts

| Symptom                                                                      | Likely cause                                            | Fix                                                            |
|------------------------------------------------------------------------------|---------------------------------------------------------|----------------------------------------------------------------|
| `tool name "foo__bar" contains '__' which is reserved for MCP namespacing`   | A builtin / skill tool tried to use the `__` separator  | Rename the tool; `__` is reserved for `<server>__<tool>`        |
| `mcp_tool_conflict` in audit log                                             | Two MCP tools with the same name registered             | The second registration is rejected; check `forge mcp list`     |

## Egress / NetworkPolicy

| Symptom                                                                      | Likely cause                                            | Fix                                                            |
|------------------------------------------------------------------------------|---------------------------------------------------------|----------------------------------------------------------------|
| Locally the agent works; in K8s it fails with `transport unavailable`        | NetworkPolicy egress doesn't include the MCP server      | Rebuild — `egress_stage.go` merges `MCPDomains` automatically; re-apply manifests |
| OAuth refresh fails with `transport: dial` from the pod                      | `token_url` host blocked by NetworkPolicy                | The build merges this too — verify the latest `egress_allowlist.json` |

## Runtime behavior

| Symptom                                                                      | Likely cause                                            | Fix                                                            |
|------------------------------------------------------------------------------|---------------------------------------------------------|----------------------------------------------------------------|
| Required server fails → pod exits with non-zero                              | This is the intended behavior; K8s CrashLoopBackOff is the signal | Fix the upstream MCP, then redeploy                       |
| Required=false server logs a warning; agent has no tools from it             | Operator marked the server optional; agent continues without it | Set `required: true` if the agent cannot run without those tools |
| Tool calls return `[truncated]` suffix                                       | Tool result exceeded `MaxResultChars` (default 64 KiB) | Reduce the upstream response or request smaller chunks         |

## Where to look next

- Audit log search:
  `grep -E 'mcp_server_failed|mcp_token_refresh.*"ok":false' <stderr>`
- Per-server probe: `forge mcp test <name>`
- Egress allowlist contents: `cat .forge-output/compiled/egress_allowlist.json | jq '.entries[] | select(.source | startswith("mcp:"))'`
