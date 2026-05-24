---
title: "Authentication Providers"
description: "Pluggable auth provider chain that gates Forge's /tasks endpoint — OIDC, AWS Sigv4, GCP IAP, Azure AD, and local-only static_token."
order: 6
---

Forge's `a2a` HTTP server (the `/tasks` endpoint and friends) requires every
caller to authenticate through a pluggable provider chain configured in
`forge.yaml`. Each provider recognizes one token shape; the chain tries them
in order, first match wins, and the result lands in `Identity` for the
audit log and any downstream authz hook.

## Provider matrix

| Provider | Use case | Token format | Verifies against | Phase |
|---|---|---|---|---|
| `static_token` | Local dev, channel-adapter loopback | Shared secret | constant-time SHA-256 compare | 1 |
| `oidc` | Any IdP with OIDC discovery (Keycloak, Auth0, Okta, Google) | `Authorization: Bearer <jwt>` | Issuer's JWKS (TTL-cached, with backoff + stale-grace) | 1 |
| `http_verifier` | Custom verifier endpoint you operate | Opaque token | Your own `/verify` HTTP service | 1 |
| `aws_sigv4` | AWS-IAM-based callers (Lambda, EC2, EKS, IAM users) | `Authorization: Bearer forge-aws-v1.<base64-url>` | AWS STS `GetCallerIdentity` (pre-signed URL pattern) | 2 (v0.11.0) |
| `gcp_iap` | Forge behind GCP HTTPS LB + IAP | `X-Goog-Iap-Jwt-Assertion: <jwt>` | IAP's hardcoded JWKS at `www.gstatic.com` | 2 (v0.11.0) |
| `azure_ad` | Microsoft Entra ID tokens | `Authorization: Bearer <aad-jwt>` | AAD JWKS via composed `oidc` provider + tenant gate | 2 (v0.11.0) |

Forge holds **no IdP secrets**. All providers verify a caller-minted
credential against a third party (STS / GCP JWKS / AAD JWKS / your own
`/verify`), then stamp an `Identity` from what the verifier returned.

## Chain semantics

Each `Verify` returns one of:

| Return | Meaning | Chain behavior |
|---|---|---|
| `Identity, nil` | Token accepted | Stops; chain returns this Identity |
| `nil, ErrTokenNotForMe` | "Not my format" | Continues to next provider |
| `nil, ErrTokenRejected` | "My format, but denied" | **Stops; 401** |
| `nil, ErrInvalidToken` | "Malformed" | **Stops; 401** |
| `nil, ErrProviderUnavailable` | "Can't reach my IdP" | **Stops; 401** (fail-closed) |

The critical rule is **no fall-through on rejection**: if provider A
returns `ErrTokenRejected`, the chain does NOT try provider B. Otherwise
an attacker could downgrade by presenting a malformed token of type A and
hoping to be authenticated as type B.

### Loopback `static_token` is auto-prepended

Forge writes a random token to `.forge/runtime.token` (mode `0600`) on
startup and auto-prepends a `static_token` provider for it to the chain.
This is how channel adapters (Slack, Telegram, MS Teams) and the local
Web UI authenticate without you configuring anything. Anyone with read
access to `.forge/runtime.token` can call the a2a server. Treat that
file like an SSH key.

### Non-Bearer auth headers (Phase 2)

The middleware consults the chain **even when no `Authorization: Bearer`
was extracted**, provided a non-Bearer auth header is present
(`X-Goog-Iap-Jwt-Assertion`). When there are no auth-shaped headers at
all, the audit reason stays `missing_token` rather than widening to
`not_for_me` — operators can still distinguish "client didn't auth" from
"client tried a format we don't speak."

## `forge.yaml` schema

```yaml
auth:
  required: true                     # 401 every unauthenticated request
  providers:
    - type: oidc | aws_sigv4 | gcp_iap | azure_ad | http_verifier | static_token
      settings:
        # provider-specific keys (see per-provider sections below)
```

Per-provider settings are validated by `forge validate`. Unknown keys
produce a warning (typo detection); the Web UI's `/api/create` endpoint
additionally filters to a closed-key whitelist before scaffolding so
malicious POST payloads can't drop arbitrary keys into `forge.yaml`.

---

## `oidc` — Generic OIDC issuer

The workhorse provider — any IdP with an OIDC discovery doc and JWKS.

```yaml
auth:
  required: true
  providers:
    - type: oidc
      settings:
        issuer:    https://login.example.com/auth/realms/forge   # required
        audience:  api://forge                                    # required
        client_id: my-spa                                         # optional azp fallback
        jwks_url:  https://...                                    # optional — overrides discovery
        jwks_cache_ttl: 1h
        clock_skew: 30s
        claim_map:                                                # remap claim names
          groups: roles
```

- Algorithm whitelist: `RS256`, `RS384`, `RS512`, `PS256`, `PS384`, `PS512`, `ES256`, `ES384`, `ES512`. `none` and HMAC are rejected before key lookup.
- JWKS is TTL-cached with backoff + stale-grace — token verification keeps working through brief JWKS outages.
- Issuer trailing-slash normalization handles the Auth0/Okta disagreement (`https://x/` vs `https://x`).

---

## `aws_sigv4` — AWS IAM via pre-signed STS URL

Authenticates callers by their AWS-IAM identity. Same pattern as
[`aws-iam-authenticator`](https://github.com/kubernetes-sigs/aws-iam-authenticator)
for EKS: caller pre-signs a `GetCallerIdentity` URL with their AWS SDK
and sends it as a Bearer token; Forge invokes that URL, STS validates
the signature against its own host and returns the canonical ARN.

```yaml
auth:
  required: true
  providers:
    - type: aws_sigv4
      settings:
        region: us-east-1                                # required
        audience: api://forge                            # informational; in audit Claims
        allowed_accounts: ["412664885516"]               # ergonomic: "anyone in these accounts"
        allowed_principals:                              # explicit globs (path.Match syntax)
          - "arn:aws:sts::412664885516:assumed-role/ci-deploy/*"
        identity_cache_ttl: 60s
        max_token_expires: 15m                           # caps caller's X-Amz-Expires claim
        clock_skew: 5m
```

### Wire format

```
Authorization: Bearer forge-aws-v1.<base64url-of-presigned-sts-url>
```

The base64-decoded payload is a complete pre-signed URL of the form:

```
https://sts.<region>.amazonaws.com/
  ?Action=GetCallerIdentity
  &Version=2011-06-15
  &X-Amz-Algorithm=AWS4-HMAC-SHA256
  &X-Amz-Credential=<AKID>/<YYYYMMDD>/<region>/sts/aws4_request
  &X-Amz-Date=<YYYYMMDDTHHMMSSZ>
  &X-Amz-Expires=<seconds, max 900>
  &X-Amz-SignedHeaders=host
  &X-Amz-Signature=<hex>
```

### Client side (3 lines)

```python
import boto3, base64, requests
from botocore.auth import SigV4QueryAuth
from botocore.awsrequest import AWSRequest

creds = boto3.Session().get_credentials().get_frozen_credentials()
req = AWSRequest(method="GET",
                 url="https://sts.us-east-1.amazonaws.com/?Action=GetCallerIdentity&Version=2011-06-15")
SigV4QueryAuth(creds, "sts", "us-east-1", expires=900).add_auth(req)
token = "forge-aws-v1." + base64.urlsafe_b64encode(req.url.encode()).rstrip(b"=").decode()

requests.post(forge_url, headers={"Authorization": f"Bearer {token}"}, data=msg)
```

> `boto3.client('sts').generate_presigned_url('get_caller_identity', ...)`
> does **not** work — it signs as if the request were a POST, STS rejects
> the GET. Use the lower-level `SigV4QueryAuth` shown above. Same quirk
> `aws-iam-authenticator` works around internally.

Reference client ships in [`scripts/forge-aws-sign.py`](../../scripts/forge-aws-sign.py).

### `allowed_accounts` — "anyone in this account"

The ergonomic shortcut for whole-account trust. Each 12-digit account ID
expands internally to the canonical glob set covering every STS identity
shape (IAM users, IAM roles, STS assumed-roles incl. SSO, federated
users). Composable with `allowed_principals`.

### Org-wide trust without enumerating accounts

There's no STS API to ask "is account X in Org Y?" — AWS deliberately
doesn't expose that. Two production paths:

1. **AWS IAM Identity Center (SSO).** Every user's session is already an
   assumed-role under `AWSReservedSSO_*`. Use a glob:
   ```yaml
   allowed_principals:
     - "arn:aws:sts::ACCT:assumed-role/AWSReservedSSO_*/*"
   ```
   Org membership is enforced by Identity Center at sign-in time.

2. **Entry role with `aws:PrincipalOrgID` condition.** Customer creates
   one IAM role in one account with a trust policy that allows anyone in
   their Org to assume it. Forge's allowlist contains just that one
   assumed-role ARN. The Org-membership check happens at AWS IAM, not in
   Forge.

### Security model

- **No secret keys on Forge.** STS validates signatures.
- **SSRF guard.** Pre-signed URL host must be `sts.<configured-region>.amazonaws.com` exactly; userinfo (`user:pass@`) and foreign hosts are rejected at parse time.
- **No HTTP redirects.** `CheckRedirect` is pinned to `ErrUseLastResponse` so a redirect off `sts.…` (e.g. MITM, TLS-inspecting proxy) can't substitute attacker bytes for the STS response.
- **Freshness gate.** Tokens claiming `X-Amz-Expires > 15min` are rejected; tokens whose `X-Amz-Date + Expires` window has lapsed (with 5min clock skew) are rejected. Bounds stolen-token replay independent of STS's own enforcement.
- **Cache bucketing on `hash(AKID, YYYYMMDD)`** — bounds stolen-key replay to one day worst-case.
- **No `aws-sdk-go-v2` dependency.** STS RPC is ~80 LOC of hand-rolled HTTP + XML.

### Audit log shape

```json
{ "event": "auth_verify",
  "fields": {
    "provider":    "aws_sigv4",
    "user_id":     "arn:aws:sts::123456789012:assumed-role/ci-deploy/i-0abc",
    "org_id":      "123456789012",
    "token_kind":  "sigv4"
  }
}
```

---

## `gcp_iap` — GCP Identity-Aware Proxy

Verifies the JWT IAP forwards as `X-Goog-Iap-Jwt-Assertion` when Forge
sits behind a GCP HTTPS Load Balancer with IAP enabled.

```yaml
auth:
  required: true
  providers:
    - type: gcp_iap
      settings:
        audience: /projects/12345678/global/backendServices/9876543210
```

`audience` is the backend service ID — find it in
**GCP Console → Security → IAP → Backend Services → Signed Header JWT Audience**.

### Security model

- **Hardcoded JWKS host** (`www.gstatic.com/iap/verify/public_key-jwk`). Operators cannot override — eliminates the "trust attacker's JWKS" failure mode.
- **ES256-only.** Any other alg rejected before key lookup.
- **JWKS merge-on-success.** A partial-but-valid JWKS response can't drop kids the stale-grace contract assumes are kept.
- **No HTTP redirects.** Same `ErrUseLastResponse` pin as `aws_sigv4`.
- **No GCP SDK dependency.**

Sub `email` / `hd` (Workspace domain) flow through to `Identity.Claims`
for downstream policy.

---

## `azure_ad` — Microsoft Entra ID

Composes the Phase 1 `oidc` provider for signature verification; layers
AAD-specific concerns on top.

### Single-tenant (the safe default)

```yaml
auth:
  required: true
  providers:
    - type: azure_ad
      settings:
        tenant_id: 00000000-1111-2222-3333-444444444444
        audience:  api://forge
        groups_mode: claim                              # or "graph"
```

`tid` claim must equal `tenant_id`; iss is double-checked via OIDC.

### Multi-tenant with explicit allowlist

```yaml
auth:
  required: true
  providers:
    - type: azure_ad
      settings:
        audience: api://forge
        allow_multi_tenant: true
        allowed_tenants:                                # case-insensitive GUID match
          - "00000000-1111-2222-3333-444444444444"
          - "55555555-6666-7777-8888-999999999999"
```

### Multi-tenant "any tenant globally" (high-risk)

```yaml
auth:
  required: true
  providers:
    - type: azure_ad
      settings:
        audience: api://forge
        allow_multi_tenant: true
        # allowed_tenants intentionally omitted
```

`forge validate` emits a warning so this trade-off is loud, not silent.

### Groups overage (graph mode)

When `groups_mode: graph` and the JWT's `groups` claim is empty (AAD
truncates at ~200 groups), Forge calls Microsoft Graph
`/me/transitiveMemberOf` using the **caller's** Bearer to fetch the
full list. Forge holds no Graph credentials of its own. Soft-fails on
Graph 5xx (returns Identity with empty Groups rather than blocking
prod traffic).

### Security model

- **Composition over inheritance.** No JWT verify or JWKS code in `azure_ad/` — all crypto lives in `oidc`.
- **Tenant gate.** Single-tenant: `tid == tenant_id`. Multi-tenant + allowlist: `tid ∈ allowed_tenants`. Multi-tenant + empty: no tid check (high-risk, warned).
- **Internal `skip_issuer_check` flag** carries `yaml:"-"` — unreachable from `forge.yaml`, only set by this package when `allow_multi_tenant=true`.
- **No HTTP redirects** on Graph client. Graph nextLink scheme + host both validated to prevent Bearer-downgrade via `https→http` same-host redirects.

---

## `static_token` — Shared secret

Loopback / dev use. Provider does constant-time SHA-256 comparison so
length-leak / timing attacks are blocked.

```yaml
auth:
  required: true
  providers:
    - type: static_token
      settings:
        token_env: FORGE_AUTH_TOKEN                     # prefer env over literal
```

`token:` (literal value in YAML) is also accepted but produces a warning.

---

## `http_verifier` — External `/verify` endpoint

Legacy / custom — you operate the verifier; Forge POSTs the token to it.

```yaml
auth:
  required: true
  providers:
    - type: http_verifier
      settings:
        url:         https://auth.example.com/verify
        default_org: acme
        timeout:     10s
```

Same wire format as the pre-Phase-1 `--auth-url` flag.

---

## Egress allowlist auto-extension

Configuring an auth provider automatically adds the hosts it needs to
the egress allowlist:

| Provider | Host(s) auto-added |
|---|---|
| `oidc` | `<issuer-host>`, `<jwks_url-host>` if explicit |
| `http_verifier` | `<url-host>` |
| `aws_sigv4` | `sts.<region>.amazonaws.com` |
| `gcp_iap` | `www.gstatic.com` |
| `azure_ad` | `login.microsoftonline.com` (+ `graph.microsoft.com` when `groups_mode: graph`) |

`forge init`'s wizard runs the Auth step before the Egress step, so
operators see the full outbound surface for review in one screen.

## Wizard / CLI

`forge init` interactive TUI: pick auth type → enter region / audience /
tenant / etc. → done. Non-interactive equivalent via flags:

```bash
forge init --non-interactive \
  --name my-agent \
  --model-provider ollama \
  --auth=aws_sigv4 \
  --auth-aws-region=us-east-1 \
  --auth-aws-audience=api://forge \
  --auth-aws-allowed-account=412664885516
```

See [CLI Reference](../reference/cli-reference.md) for the full flag set.

---

## Mesh patterns (agent-to-agent)

When an agent calls another agent, the receiver's auth provider gates
the call the same way it would for a human or CI. Two common patterns:

**Single-account "fleet" model.** Every agent runs as a workload in
one dedicated AWS account with its own IAM role; every agent's
`forge.yaml` has `allowed_accounts: [<FLEET_ACCT>]`. Trust boundary =
the account. Onboarding a new agent = create one IAM role; no other
agent's config changes.

**Per-pair allowlist.** Sensitive agents (touching money, PII, customer
data) override the broad account allowlist with explicit
`allowed_principals` patterns for the specific calling agents allowed.

See [Audit Logging](audit-logging.md) for how to grep `user_id` across
audit events to map the actual call graph.

---

## Related Documentation

| Document | Description |
|----------|-------------|
| [Audit Logging](audit-logging.md) | `auth_verify` / `auth_fail` event shape, reason codes, `token_kind` values |
| [Egress Security](egress-control.md) | Auth-host auto-allowlist and how it composes with operator-set domains |
| [Trust Model](trust-model.md) | Caller → Forge trust boundary; what Forge does and doesn't trust |
| [forge.yaml Schema](../reference/forge-yaml-schema.md) | Full YAML reference including `auth:` block |
| [CLI Reference](../reference/cli-reference.md) | `forge init` auth flags |
| [Web Dashboard](../reference/web-dashboard.md) | Auth provider options in the create flow |
