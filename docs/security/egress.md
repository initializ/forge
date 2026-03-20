# Egress Security

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
   - Deduplicate and sort

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
```

The `allow_private_ips` field controls whether RFC 1918 addresses are allowed through the SafeDialer. When omitted, it defaults to `true` inside containers (detected via `KUBERNETES_SERVICE_HOST` or `/.dockerenv`) and `false` otherwise. Cloud metadata (`169.254.169.254`) is always blocked.

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
{"event":"egress_allowed","domain":"api.tavily.com","mode":"allowlist"}
{"event":"egress_blocked","domain":"evil.com","mode":"allowlist"}
{"event":"egress_allowed","domain":"api.tavily.com","mode":"allowlist","source":"proxy"}
```

Events without `"source"` come from the in-process enforcer; events with `"source": "proxy"` come from the subprocess proxy.

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

---
← [Security Overview](overview.md) | [Back to README](../../README.md) | [Secrets](secrets.md) →
