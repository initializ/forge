# Changelog

## Unreleased

### Fixed

- **`forge init` Custom provider now produces a runnable agent (issue #83).**
  Picking the **Custom** provider in `forge init` (or the Web UI wizard)
  previously wrote `provider: custom` to `forge.yaml` plus
  `MODEL_BASE_URL` / `MODEL_API_KEY` env vars, neither of which the runtime
  understood — agents fell back to `StubExecutor` and every task failed
  with `agent execution not configured for framework "forge"`. Scaffold
  now normalizes Custom → `provider: openai` + `OPENAI_BASE_URL` /
  `OPENAI_API_KEY`, matching the OpenAI-compatible code path the runtime
  resolver already supports. Affects both TUI and Web UI flows.
- **OAuth-credentials path no longer silently overrides
  `OPENAI_BASE_URL` (issue #83).** When the runtime or skill builder
  found stored ChatGPT OAuth credentials AND no `OPENAI_API_KEY`, it
  ignored an explicitly-set `OPENAI_BASE_URL` and routed traffic to
  `chatgpt.com/backend-api/codex` — manifesting as a 400 from ChatGPT
  rejecting the operator's model name. Both `forge run` and `forge ui`
  now refuse this combination with a clear error explaining what to set.

### Migration

- If you have `provider: custom` in a checked-in `forge.yaml` from an
  earlier `forge init` run, change it to `provider: openai` and rename
  the `.env` keys from `MODEL_BASE_URL` / `MODEL_API_KEY` to
  `OPENAI_BASE_URL` / `OPENAI_API_KEY`. No new `forge init` is required.

## v0.12.0 — Phase 1: MCP integration (HTTP transport) — in progress

### Added

- **Model Context Protocol (MCP) HTTP client support.** Configure servers
  under a new `mcp:` block in `forge.yaml`; discovered tools are
  registered as namespaced `<server>__<tool>` first-class tools that
  flow through the existing LLM executor.
- **`forge mcp` subcommands:**
  - `forge mcp list` — show every configured server, its state, and
    the number of tools it exposes after filtering.
  - `forge mcp test <name>` — connect, list tools, optionally call one
    with `--call <tool> --args '<json>'`.
  - `forge mcp login <name>` — laptop-time OAuth 2.1 PKCE flow.
  - `forge mcp logout <name>` — remove stored OAuth tokens.
- **OAuth 2.1 PKCE** for hosted MCP servers (Linear, Notion, Atlassian,
  GitHub hosted MCP, etc.). Tokens persist via the existing
  AES-256-GCM keyring at `~/.forge/credentials/mcp_<name>.json`
  (encrypted when `FORGE_PASSPHRASE` is set).
- **Audit events** (NDJSON to stderr, no byte payload ever):
  `mcp_server_started`, `mcp_server_failed`, `mcp_server_degraded`,
  `mcp_tool_call`, `mcp_tool_result`, `mcp_tool_conflict`,
  `mcp_token_refresh`.
- **Egress integration.** MCP server hosts auto-merged into the egress
  allowlist (mirroring `auth_domains`) so an HTTP MCP call cannot
  silently be blocked at runtime.
- **Tool namespacing.** `tools.Registry.Register` rejects names
  containing `__` unless the tool implements the new
  `tools.MCPSource` marker interface, preventing builtins from
  shadowing MCP-namespaced tools.

### Removed

- **`mcp_call` adapter tool removed.** Superseded by the new `mcp:`
  configuration block in `forge.yaml`, which exposes each MCP
  server's tools as first-class namespaced tools — strictly better UX
  for the LLM than a single meta-tool. See `docs/mcp/index.md` for
  the migration path.

### Notes

- **Phase 1 supports HTTP transport only.** Stdio MCP servers (Notion,
  Linear community, Atlassian, the modelcontextprotocol/servers
  reference set) are on the roadmap. `transport: stdio` is rejected at
  `forge validate` time with the message
  `"stdio is on the roadmap; Phase 1 supports HTTP transport only"`.
- **MCP protocol version pinned to `2025-06-18`**. Handshake hard-fails
  on mismatch — version negotiation is intentionally absent.
- **OAuth callback** runs on a `127.0.0.1` loopback listener; it is a
  laptop-time operation. For K8s deployments, run
  `forge mcp login <name>` locally, then mount the resulting
  credentials file as a Secret and point `MCP_TOKEN_STORE_PATH` at it.
- **No new top-level dependencies** — JSON Schema validation reuses
  the existing `xeipuuv/gojsonschema` already in `go.mod`.

---

## v0.11.0 — Phase 2: cloud-native auth providers (in progress)

### Added

- **`aws_sigv4` auth provider.** Authenticate AWS-IAM callers by reflecting
  their Sigv4 signature to AWS STS `GetCallerIdentity`. No `aws-sdk-go-v2`
  dependency.
- **`gcp_iap` auth provider.** Verify the JWT IAP forwards as
  `X-Goog-Iap-Jwt-Assertion` when Forge sits behind a GCP HTTPS Load
  Balancer with IAP enabled.
- **`azure_ad` auth provider.** Verify Microsoft Entra ID Bearer tokens
  with tenant lock-in and optional Microsoft Graph group enrichment.
- Non-interactive `forge init` flags for the three new providers:
  `--auth-aws-region`, `--auth-aws-allowed-principal` (repeatable),
  `--auth-gcp-iap-audience`, `--auth-azure-tenant`,
  `--auth-azure-multi-tenant`, `--auth-azure-groups-mode`.
- Web UI exposes the three new types via the `/api/wizard-meta` endpoint;
  server-side validation rejects malformed payloads before scaffold.
- `egress_hosts` automatically extended for each new provider
  (`sts.<region>.amazonaws.com`, `www.gstatic.com`,
  `login.microsoftonline.com`, `graph.microsoft.com` when applicable).

### Changed

- Middleware now consults the auth chain **even when no Bearer token is
  extracted**, so non-Bearer formats (Sigv4 `Authorization`, IAP
  `X-Goog-Iap-Jwt-Assertion`) can be recognized. Existing Bearer + JWT
  flows are unchanged.
- `auth.HeadersFromRequest` widened with `X-Goog-Iap-Jwt-Assertion`
  for `gcp_iap`. Providers that don't consume this header are unaffected.
- `auth.TokenKind` recognizes the `forge-aws-v1.` Bearer prefix and
  returns `"sigv4"`. The audit `token_kind` field now has five possible
  values: `empty`, `opaque`, `jwt`, `sigv4`, `iap_jwt`.
- `validate.ValidateAuthConfig` admits the three new provider types and
  enforces their per-type required keys (`aws_sigv4.region`,
  `gcp_iap.audience`, `azure_ad.audience`, `azure_ad.tenant_id`-unless-
  multi-tenant, `azure_ad.groups_mode` whitelist).

### Notes for upgraders

- **No forge.yaml changes are required** for callers continuing to use
  Phase 1 providers (`static_token`, `oidc`, `http_verifier`). Phase 1
  test suite passes without modification.
- If you wrote a custom provider that inspects headers, the `Headers`
  map now contains additional keys. Existing keys are unchanged.
- The `oidc` package gained an internal `SkipIssuerCheck` field carrying
  `yaml:"-"` — it cannot be set via `forge.yaml` and is reachable only
  from Go callers (currently only `azure_ad` multi-tenant). Operators see
  no change.

### `allowed_accounts` shortcut for whole-account trust

For "any IAM principal in these AWS accounts" without writing
glob patterns:

```yaml
auth:
  providers:
    - type: aws_sigv4
      settings:
        region: us-east-1
        allowed_accounts: ["412664885516", "109887654321"]
```

Internally expands to the canonical glob set covering all identity
shapes (IAM users, IAM roles, STS assumed-roles, federated users)
for each account. Composes with `allowed_principals` — you can list
specific roles AND whole accounts in the same provider entry.

For AWS-Org-wide trust without enumerating accounts, use AWS IAM
Identity Center (SSO) — SSO permission sets gate Org membership at
sign-in, and you can match Identity Center-assumed roles with the
existing `allowed_principals` globs.

### `azure_ad.allowed_tenants` — explicit allowlist for multi-tenant mode

```yaml
auth:
  providers:
    - type: azure_ad
      settings:
        audience: api://forge
        allow_multi_tenant: true
        allowed_tenants:
          - "00000000-1111-2222-3333-444444444444"   # partner A
          - "55555555-6666-7777-8888-999999999999"   # partner B
```

When `allow_multi_tenant: true`, the `tid` claim must be in
`allowed_tenants` (case-insensitive GUID match). Empty list +
multi-tenant remains the documented "any tenant globally" mode for
back-compat, but `forge validate` now emits a warning when the list
is empty to make the trade-off explicit. Non-interactive flag:
`--auth-azure-allowed-tenant` (repeatable).

### TUI wizard supports Phase 2 providers

`forge init`'s TUI picker now includes `AWS Sigv4 (IAM)`,
`GCP Identity-Aware Proxy`, and `Azure AD / Entra ID` entries with
step-by-step input flows. AAD is single-tenant in the TUI;
multi-tenant remains a deliberate YAML edit (security default).

### Client experience for `aws_sigv4`

The client side is a Bearer token with a 3-line mint:

```python
import boto3, base64
url   = boto3.client('sts', region_name='us-east-1').generate_presigned_url(
            'get_caller_identity', ExpiresIn=900)
token = 'forge-aws-v1.' + base64.urlsafe_b64encode(url.encode()).rstrip(b'=').decode()

requests.post(forge_url, headers={'Authorization': f'Bearer {token}'}, data=msg)
```

Pattern is identical to `aws-iam-authenticator` for EKS. Reference client
in `scripts/forge-aws-sign.py` — use it directly or as a template for
Go / Java / Node clients. Wire format is documented in the package
docstring of `forge-core/auth/providers/aws_sigv4/provider.go`.

### Known deferred work

- (none for Phase 2)
