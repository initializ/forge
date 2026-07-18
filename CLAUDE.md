# Forge - CLAUDE.md

## Project Structure

Multi-module Go workspace with three modules:
- `forge-core/` — Core library (registry, tools, security, channels, LLM)
- `forge-cli/` — CLI commands, TUI wizard, runtime
- `forge-plugins/` — Channel plugins (telegram, slack), markdown converter

## Platform-integration contracts (used by initializ AIP)

- **Tenancy headers**: every forge→platform HTTP callout (admission, remote session store, MCP platform token resolver) MUST send `Org-Id` + `Workspace-Id` from `FORGE_ORG_ID`/`FORGE_WORKSPACE_ID` env alongside `Authorization: Bearer ${FORGE_PLATFORM_TOKEN}` — the platform verifies a PER-ORG HS256 token and needs Org-Id to pick the signing secret BEFORE it can validate the bearer (omitting it → 401 "missing org-id header"). Pattern lives in `admission_loader.go` / `remote_session_store.go` / `mcp/platform_token.go`.
- **MCP auth types** (`auth.type`): `oauth`/`bearer`/`static` + managed `platform` (agent-principal token from `ForgeConfig.Platform.token_endpoint`) and `user` (delegated, lazy — cannot be `required:true`). The platform owns the op→identity split; forge only reads per-server config.
- **DEFER approval is FULLY BUILT & enforced** (`forge-core/security/deferpolicy/`, Slack Block-Kit delivery in `forge-plugins/channels/slack/approvals.go`): `security.defer.tools.<toolName>{to: channel:slack:#x, approvers, timeout}` — keyed by the RUNTIME tool name, which for MCP is **`<server>__<op>`**. Guardrails `approvalGates` are parsed but NOT enforced — use `security.defer.*`.
- **MCP OAuth discovery/DCR** (`forge-core/mcp/oauth_discovery.go`, #316/#320): RFC 9728→8414→7591. `wellKnown()` must INSERT the well-known segment for path-qualified issuers (Atlassian `auth.atlassian.com/<id>`) — replacing the path resolves the wrong AS.

## Pre-Commit Requirements

**Always run before committing:**

```sh
# Format all modules
gofmt -w forge-core/ forge-cli/ forge-plugins/

# Lint all modules
golangci-lint run ./forge-core/...
golangci-lint run ./forge-cli/...
golangci-lint run ./forge-plugins/...
```

Fix any lint errors and formatting issues before creating commits.

## Testing

Run tests for affected modules before committing:

```sh
cd forge-core && go test ./...
cd forge-cli && go test ./...
cd forge-plugins && go test ./...
```
