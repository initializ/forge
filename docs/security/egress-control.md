---
title: "Egress Control"
description: "Layered egress security controls for restricting outbound network access."
order: 2
---

Forge provides layered egress security controls that restrict which external domains an agent can access — at both build time and runtime.

## Overview

Egress security operates at two levels:

1. **Build time** — Generates allowlist artifacts and Kubernetes NetworkPolicy manifests for container-level enforcement
2. **Runtime** — An in-process `EgressEnforcer` (Go `http.RoundTripper`) validates every outbound HTTP request, and a local `EgressProxy` enforces the same rules on subprocess HTTP traffic (skill scripts, `cli_execute`)

The system resolves allowed domains from three sources:
1. **Explicit domains** — Listed in `forge.yaml`
2. **Tool domains** — Inferred from registered tools
3. **Capability bundles** — Pre-defined domain sets for common services

## Profiles

Profiles set the overall security posture. Defined in `forge-core/security/types.go`:

| Profile | Description | Default Mode |
|---------|-------------|-------------|
| `strict` | Maximum restriction, deny by default | `deny-all` |
| `standard` | Balanced, allow known domains | `allowlist` |
| `permissive` | Minimal restriction for development | `dev-open` |

Default profile: `strict`. Default mode: `deny-all`.

## Modes

Modes control egress behavior within a profile:

| Mode | Behavior |
|------|----------|
| `deny-all` | No outbound network access (localhost always allowed) |
| `allowlist` | Only explicitly allowed domains (exact + wildcard) |
| `dev-open` | Unrestricted outbound access (development only) |

## Domain Matching

Domain matching is handled by `DomainMatcher` (`forge-core/security/domain_matcher.go`), shared by both the in-process enforcer and the subprocess proxy:

- **Exact match**: `api.openai.com` matches `api.openai.com`
- **Wildcard match**: `*.github.com` matches `api.github.com` but not `github.com`
- **Case insensitive**: `API.OpenAI.COM` matches `api.openai.com`
- **Localhost bypass**: `127.0.0.1`, `::1`, and `localhost` are always allowed in all modes

## IP Validation

All egress paths validate hostnames against non-standard IP formats before domain matching. The IP validator (`forge-core/security/ip_validator.go`) rejects SSRF bypass vectors:

| Blocked Format | Example | Reason |
|---------------|---------|--------|
| Octal | `0177.0.0.1` | Resolves to `127.0.0.1` in some parsers |
| Hexadecimal | `0x7f000001` | Resolves to `127.0.0.1` in some parsers |
| Packed decimal | `2130706433` | Resolves to `127.0.0.1` in some parsers |
| Leading zeros | `127.0.0.01` | Ambiguous parsing across languages |
| IPv6 transition (NAT64) | `64:ff9b::10.0.0.1` | Embeds private IPv4 in IPv6 |
| IPv6 transition (6to4) | `2002:0a00:0001::` | Embeds private IPv4 in IPv6 |
| IPv6 transition (Teredo) | `2001:0000:...` | Embeds XOR'd IPv4 in IPv6 |

The `ValidateHostIP()` function is called early in both the EgressEnforcer and EgressProxy before any domain matching occurs.

## Safe Dialer (DNS Rebinding Protection)

The `SafeDialer` (`forge-core/security/safe_dialer.go`) prevents DNS rebinding and TOCTOU attacks by validating resolved IPs before connecting:

1. Resolves hostname to IP addresses via DNS
2. Validates **all** resolved IPs against blocked CIDR ranges
3. Dials the first safe IP directly (bypasses re-resolution)

Blocked IP ranges depend on the `allowPrivateIPs` setting:

| CIDR | Always Blocked | Blocked when `allowPrivateIPs=false` |
|------|---------------|--------------------------------------|
| `169.254.169.254/32` (cloud metadata) | Yes | Yes |
| `127.0.0.0/8` (loopback) | Yes | Yes |
| `::1/128` (IPv6 loopback) | Yes | Yes |
| `0.0.0.0/8` | Yes | Yes |
| `10.0.0.0/8` (RFC 1918) | — | Yes |
| `172.16.0.0/12` (RFC 1918) | — | Yes |
| `192.168.0.0/16` (RFC 1918) | — | Yes |
| `169.254.0.0/16` (link-local) | — | Yes |
| `100.64.0.0/10` (CGNAT) | — | Yes |
| `fc00::/7` (IPv6 ULA) | — | Yes |
| `fe80::/10` (IPv6 link-local) | — | Yes |

Both the EgressEnforcer and EgressProxy use `SafeTransport` (an `http.Transport` wired to the SafeDialer) for non-localhost connections.

## Container-Aware Private IP Handling

In container and Kubernetes environments, pods communicate via service DNS names that resolve to RFC 1918 addresses (e.g., `10.96.x.x`). Blocking these would break inter-service communication.

The `allowPrivateIPs` setting is resolved with this precedence:

1. **Explicit config** — `egress.allow_private_ips` in `forge.yaml`
2. **Auto-detect** — `true` if `InContainer()` detects Docker/Kubernetes
3. **Default** — `false` (block all private IPs)

| Scenario | `allowPrivateIPs` | RFC 1918 | Cloud Metadata | Loopback |
|----------|-------------------|----------|----------------|----------|
| Local dev | `false` | Blocked | Blocked | Allowed (localhost bypass) |
| Docker Desktop | `true` (auto) | Allowed | **Blocked** | Allowed (localhost bypass) |
| Kubernetes | `true` (auto) | Allowed | **Blocked** | Allowed (localhost bypass) |

Cloud metadata (`169.254.169.254`) is **always** blocked regardless of the `allowPrivateIPs` setting.

### Narrow Private-CIDR Allowlist (issue #337 preparation)

`allow_private_ips: true` opens **all** RFC 1918 / link-local / CGNAT / ULA ranges at once — the right choice for a Kubernetes pod that talks to any service in the mesh, but too wide for an agent that only needs to reach one internal database.

`allowed_private_cidrs` narrows that: when `allow_private_ips` is false (or unset), IPs falling inside any listed CIDR bypass the private-block, and everything else private stays blocked.

```yaml
egress:
  mode: allowlist
  allow_private_ips: false                # keep RFC 1918 blocked wholesale
  allowed_private_cidrs:                  # ...but open these two slices
    - 10.20.0.0/16                        # internal databases
    - 172.16.42.0/24                      # message brokers
```

Invariants (checked by tests):

- **Always-blocked ranges (cloud metadata `169.254.169.254`, loopback, `0.0.0.0/8`) stay blocked** even if an operator lists a CIDR that would otherwise contain them. No allowlist punches a hole in these.
- **`allow_private_ips: true` supersedes** the CIDR list — the boolean means "all private allowed" and the list is redundant. A warning in that case is a good future addition, but the effective policy is safe (superset).
- **Invalid CIDR strings fail at config-load time**, not at first dial. `Resolve()` validates each entry via `net.ParseCIDR`; bare IPs (missing `/mask`) are rejected because the config intent is range-level exemption. **Non-canonical entries with host bits set** (e.g. `10.20.0.5/16`) are also rejected — Go's `net.ParseCIDR` silently masks those to the network (widening from a single host to a /16), which is the security-wrong direction. Write `10.20.0.5/32` (single host) or `10.20.0.0/16` (range) — never the ambiguous mix.
- **Both `forge build` and `forge run` validate the CIDR strings** — a typo trips the build, not just the first launch.
- CIDRs are consulted by both the in-process `EgressEnforcer` (Go clients) and the subprocess `EgressProxy` (skill scripts) — one policy, two enforcement paths.

Companion feature for #337: raw-TCP egress (databases, message brokers) requires this narrow-private mechanism to be reachable in the first place. Landing the CIDR allowlist first (this PR) unblocks the SOCKS5-based TCP path (follow-up PR) without opening RFC 1918 wholesale.

## Runtime Egress Enforcer

The `EgressEnforcer` (`forge-core/security/egress_enforcer.go`) is an `http.RoundTripper` that wraps a `SafeTransport`. Every outbound HTTP request from in-process Go code (builtins like `http_request`, `web_search`, LLM API calls) passes through it.

```go
enforcer := security.NewEgressEnforcer(nil, security.ModeAllowlist, allowedDomains, false)
client := &http.Client{Transport: enforcer}
```

Request validation order:
1. Reject non-standard IP formats (`ValidateHostIP`)
2. Allow localhost (bypass SafeTransport, use `http.DefaultTransport`)
3. Check domain against allowlist (`DomainMatcher.IsAllowed`)
4. Forward via `SafeTransport` (post-DNS IP validation)

Blocked requests return: `egress blocked: domain "X" not in allowlist (mode=allowlist)`

The enforcer fires an `OnAttempt` callback for every request, enabling audit logging with domain, mode, and allow/deny decision.

## Subprocess Egress Proxy

Skill scripts and `cli_execute` subprocesses bypass the Go-level `EgressEnforcer` because they use external tools like `curl` or `wget`. The `EgressProxy` (`forge-core/security/egress_proxy.go`) closes this gap.

### How it works

```
┌─────────────────────────────────────────────────────┐
│                   forge run                         │
│                                                     │
│  In-process HTTP ──→ EgressEnforcer (RoundTripper)  │
│                                                     │
│  Subprocesses ──→ HTTP_PROXY ──→ EgressProxy        │
│  (curl, wget,       127.0.0.1:<port>  (validates    │
│   python, etc.)                        domains)     │
└─────────────────────────────────────────────────────┘
```

1. Before tool registration, Forge starts a local HTTP/HTTPS forward proxy on `127.0.0.1:0` (random port)
2. `HTTP_PROXY`, `HTTPS_PROXY`, `http_proxy`, and `https_proxy` env vars are injected into every subprocess
3. The proxy validates each request's destination hostname against the same `DomainMatcher`
4. Allowed requests are forwarded; blocked requests receive `403 Forbidden`

### Properties

| Property | Detail |
|----------|--------|
| **Binding** | `127.0.0.1:0` — localhost only, random port, never exposed externally |
| **Lifecycle** | Per `Runner.Run()` — starts before tool registration, shuts down on context cancellation |
| **Isolation** | Multiple `forge run` instances each get their own proxy on different ports |
| **HTTP requests** | Reads `req.URL.Host`, checks `DomainMatcher.IsAllowed()`, forwards or returns `403` |
| **HTTPS CONNECT** | Parses host from `CONNECT host:port`, validates domain, blind-relays bytes (no MITM/decryption) |
| **Env vars** | Sets both uppercase and lowercase forms to cover all HTTP client libraries |
| **Audit** | Emits same `egress_allowed`/`egress_blocked` audit events with `"source": "proxy"` |

### When the proxy is skipped

- **Container environments**: When `KUBERNETES_SERVICE_HOST` is set or `/.dockerenv` exists, Kubernetes `NetworkPolicy` handles egress enforcement instead
- **`dev-open` mode**: No restrictions needed, proxy would be a transparent passthrough

Container detection is handled by `InContainer()` in `forge-core/security/container.go`.

## Capability Bundles

Capability bundles (`forge-core/security/capabilities.go`) map service names to their required domains:

| Capability | Domains |
|-----------|---------|
| `slack` | `slack.com`, `hooks.slack.com`, `api.slack.com` |
| `telegram` | `api.telegram.org` |

Specify capabilities in `forge.yaml` to automatically include their domains.

## Tool Domain Inference

The tool domain inference system (`forge-core/security/tool_domains.go`) maps tool names to known required domains:

| Tool | Inferred Domains |
|------|-----------------|
| `web_search` / `web-search` | `api.tavily.com`, `api.perplexity.ai` |
| `github_api` | `api.github.com`, `github.com` |
| `slack_notify` | `slack.com`, `hooks.slack.com` |
| `openai_completion` | `api.openai.com` |
| `anthropic_api` | `api.anthropic.com` |
| `huggingface_api` | `api-inference.huggingface.co`, `huggingface.co` |
| `google_vertex` | `us-central1-aiplatform.googleapis.com` |
| `sendgrid_email` | `api.sendgrid.com` |
| `twilio_sms` | `api.twilio.com` |
| `aws_bedrock` | `bedrock-runtime.us-east-1.amazonaws.com` |
| `azure_openai` | `openai.azure.com` |
| `tavily_research` | `api.tavily.com` |
| `tavily_search` | `api.tavily.com` |

## Allowlist Resolution

The resolver (`forge-core/security/resolver.go`) combines all domain sources:

1. Validate profile and mode
2. For `deny-all`: return empty config (no domains allowed)
3. For `dev-open`: return unrestricted config (all domains allowed)
4. For `allowlist`:
   - Start with explicit domains from `forge.yaml`
   - Add tool-inferred domains
   - Add capability bundle domains
   - **Add auth-provider-derived domains** (see below)
   - Deduplicate and sort

### Auth-provider domain auto-extension

Configuring an `auth.providers[]` entry adds the host(s) the provider needs
to reach to the allowlist automatically — operators don't have to remember
to add `sts.us-east-1.amazonaws.com` themselves when they configure
`aws_sigv4`. The `security.AuthDomains` helper centralizes the mapping:

| Provider | Host(s) added |
|---|---|
| `oidc` | host of `issuer` URL, host of explicit `jwks_url` if set |
| `http_verifier` | host of `url` |
| `aws_sigv4` | `sts.<region>.amazonaws.com` (+ test-mode `sts_endpoint` override host) |
| `gcp_iap` | `www.gstatic.com` (hardcoded, IAP JWKS lives there) |
| `azure_ad` | `login.microsoftonline.com` (+ `graph.microsoft.com` when `groups_mode: graph`) |

`forge init`'s wizard runs the Auth step **before** Egress so the operator
sees the full outbound surface for review in a single screen. See
[Authentication](authentication.md) for the per-provider auth model.

### MCP server domain auto-extension

Configuring an `mcp.servers[]` entry with `transport: http` adds the host
of the server `url` to the allowlist automatically — operators don't have
to remember to add `mcp.linear.app` when they configure a Linear MCP
server. See `security.MCPDomains` for the canonical mapping.

### OTel collector domain auto-extension (OTel v1, #107)

Configuring `observability.tracing` with an endpoint adds the
collector's hostname to the allowlist automatically. Without this, a
deployment with tracing enabled in `forge.yaml` would ship a
NetworkPolicy that blocks OTLP traffic — spans accumulate in the
batch processor and drop on shutdown timeout, leaving the operator
with an inexplicably empty trace backend.

| Source field | Host added |
|---|---|
| `observability.tracing.endpoint` (when `enabled: true`) | bare hostname (port stripped) |

The auto-merge fires at **both** build time (`forge package` →
`egress_allowlist.json` → generated NetworkPolicy) **and** runtime
(`forge run` dev mode), so dev and prod behave identically. Disabled
tracing produces no entry — turning tracing off in yaml does NOT
leave a stale entry punched through. Malformed endpoints are silently
skipped: the build never blocks on telemetry config. See
[Observability — Tracing](../core-concepts/observability-tracing.md)
for the full reference.

## Build Artifacts

The `EgressStage` generates:

### `egress_allowlist.json`

```json
{
  "profile": "standard",
  "mode": "allowlist",
  "allowed_domains": ["api.example.com"],
  "tool_domains": ["api.tavily.com"],
  "all_domains": ["api.example.com", "api.tavily.com"]
}
```

Empty arrays are always `[]`, never `null`.

### Kubernetes `network-policy.yaml`

Generated by `GenerateK8sNetworkPolicy()`:

- **deny-all**: Empty egress rules (`egress: []`)
- **allowlist**: Allows ports 80/443 with domain annotations
- **dev-open**: Allows ports 80/443 without restrictions

The NetworkPolicy uses pod selector `app: <agent-id>` and includes domain annotations for external DNS-based policy controllers.

## Configuration

In `forge.yaml`:

```yaml
egress:
  profile: standard
  mode: allowlist
  allowed_domains:
    - api.example.com
    - "*.github.com"
    - hooks.slack.com
  capabilities:
    - slack
    - telegram
  allow_private_ips: false          # default: auto-detect from container env
  allowed_private_cidrs:            # narrow private-IP allowlist (see above)
    - 10.20.0.0/16
```

The `allow_private_ips` field controls whether RFC 1918 addresses are allowed through the SafeDialer. When omitted, it defaults to `true` inside containers (detected via `KUBERNETES_SERVICE_HOST` or `/.dockerenv`) and `false` otherwise. Cloud metadata (`169.254.169.254`) is always blocked.

`allowed_private_cidrs` (added in issue #337) is a narrower alternative: it lets you reach a specific slice of the private-IP space (e.g. only the internal-database subnet) without opening RFC 1918 wholesale. See "Narrow Private-CIDR Allowlist" above.

## Production vs Development

| Setting | Production | Development |
|---------|-----------|-------------|
| Profile | `strict` or `standard` | `permissive` |
| Mode | `deny-all` or `allowlist` | `dev-open` |
| Dev tools | Filtered out | Included |
| Network policy | Enforced | Not generated |
| Egress proxy | Active (allowlist/deny-all) | Skipped (dev-open) |
| Container egress | NetworkPolicy enforced | Proxy enforced locally |

## Audit Events

Both the enforcer and proxy emit structured audit events:

```json
{"event":"egress_allowed","correlation_id":"a1b2c3d4","task_id":"task-1","fields":{"domain":"api.tavily.com","mode":"allowlist"}}
{"event":"egress_blocked","correlation_id":"a1b2c3d4","task_id":"task-1","fields":{"domain":"evil.com","mode":"allowlist"}}
{"event":"egress_allowed","correlation_id":"a1b2c3d4","task_id":"task-1","fields":{"domain":"api.tavily.com","mode":"allowlist","source":"proxy"}}
```

Events without `"source"` come from the in-process enforcer; events with `"source": "proxy"` come from the subprocess proxy. Both carry `correlation_id` (the invocation ID) and `task_id`. The in-process enforcer reads them from the request context; the proxy recovers them from the `Proxy-Authorization` credentials the subprocess replays — the runner stamps the task/invocation IDs into the injected `HTTP_PROXY` URL as userinfo, and standard HTTP clients echo that back as a Basic proxy-auth header on every request and `CONNECT`. A binary that ignores proxy credentials is still enforced and audited, but its proxy events omit the identity fields (issue #338).

## Related Files

| File | Purpose |
|------|---------|
| `forge-core/security/types.go` | Profile and mode types, `EgressConfig` |
| `forge-core/security/ip_validator.go` | Strict IP parsing, CIDR blocking, IPv6 transition detection |
| `forge-core/security/safe_dialer.go` | Post-DNS-resolution IP validation, `SafeTransport` |
| `forge-core/security/domain_matcher.go` | `DomainMatcher` — shared exact/wildcard matching logic |
| `forge-core/security/egress_enforcer.go` | `EgressEnforcer` — in-process `http.RoundTripper` |
| `forge-core/security/egress_proxy.go` | `EgressProxy` — localhost HTTP/HTTPS forward proxy |
| `forge-core/security/redirect.go` | Cross-origin redirect credential stripping |
| `forge-core/security/container.go` | `InContainer()` — Docker/Kubernetes detection |
| `forge-core/security/resolver.go` | Allowlist resolution logic |
| `forge-core/security/capabilities.go` | Capability bundle definitions |
| `forge-core/security/tool_domains.go` | Tool domain inference |
| `forge-core/security/allowlist.go` | JSON allowlist generation |
| `forge-core/security/network_policy.go` | K8s NetworkPolicy generation |
| `forge-cli/tools/exec.go` | `SkillCommandExecutor` — proxy env injection for skill scripts |
| `forge-cli/tools/cli_execute.go` | `CLIExecuteTool` — proxy env injection for CLI binaries |
| `forge-cli/runtime/runner.go` | Proxy lifecycle management in `Run()` |
