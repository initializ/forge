---
title: "azure_ad — Microsoft Entra ID"
description: "Verify Entra ID tokens with tenant lock-in and optional Graph enrichment."
order: 12
---

The `azure_ad` provider authenticates Microsoft Entra ID (formerly Azure AD)
Bearer tokens. It composes the Phase 1 `oidc` provider for signature
verification and standard claim checks, then layers AAD-specific concerns
on top:

- Tenant lock-in via the `tid` claim
- Optional Microsoft Graph group enrichment when the JWT's `groups` claim
  overflows (AAD truncates at ~200 groups)
- Single-tenant vs. multi-tenant operation

## When to use it

Your callers — humans, CI, or Azure-hosted workloads — already have Entra
identities. AAD-specific quirks (`tid`, app roles, groups overage) are
handled correctly.

## Prerequisites

- [ ] An Entra tenant exists and you know its GUID.
- [ ] An app registration exists in the tenant.
- [ ] App registration → **Expose an API** → Application ID URI is set
  (e.g. `api://forge`).
- [ ] Forge can reach `login.microsoftonline.com`.
- [ ] *(For graph mode only)* Forge can reach `graph.microsoft.com`, the
  app registration has the **delegated** Graph permission
  `GroupMember.Read.All`, and an admin has **granted consent**.

### App registration walkthrough

```
Entra admin center → App registrations → New registration
  Name:                 Forge
  Supported accounts:   Single tenant (unless you need multi-tenant)
  Redirect URI:         (leave empty — Forge doesn't initiate flows)

→ Expose an API:
  Application ID URI:   api://forge
  Add a scope:          user_impersonation
  Who can consent:      Admins and users

→ API permissions (ONLY if groups_mode=graph):
  Add a permission → Microsoft Graph → Delegated → GroupMember.Read.All
  Click "Grant admin consent"
```

## forge.yaml — single-tenant, claim mode (typical)

```yaml
auth:
  required: true
  providers:
    - type: azure_ad
      settings:
        tenant_id:    00000000-1111-2222-3333-444444444444
        audience:     api://forge
        groups_mode:  claim   # default; reads `groups`/`roles` from JWT
```

## forge.yaml — multi-tenant

> **WARNING:** Multi-tenant means **any** Entra tenant in the world can
> present tokens to Forge. Without an authz layer (Phase 4+ ABAC), this is
> effectively wide-open auth. Pair it with claim-based policy in your
> handler middleware, or stick to single-tenant.

```yaml
auth:
  required: true
  providers:
    - type: azure_ad
      settings:
        audience:            api://forge
        allow_multi_tenant:  true
```

## forge.yaml — graph mode (groups overage)

When a user is in more than ~200 groups, AAD truncates the JWT `groups`
claim and includes `_claim_names` indicating overflow. The default `claim`
mode would see empty groups in that case — switch to `graph` mode to query
Microsoft Graph for the full list:

```yaml
auth:
  required: true
  providers:
    - type: azure_ad
      settings:
        tenant_id:    00000000-...
        audience:     api://forge
        groups_mode:  graph
```

The caller's Bearer token is **reflected** to Graph; Forge holds no Graph
credentials of its own. The user's `GroupMember.Read.All` delegated
permission is what authorizes the Graph read.

## Configuration reference

| Field | Required | Default | Description |
|---|---|---|---|
| `audience` | yes | — | Application ID URI from the app registration. |
| `tenant_id` | required unless `allow_multi_tenant=true` | — | Entra tenant GUID. |
| `allow_multi_tenant` | no | `false` | Accept tokens from any tenant. **High risk** without authz. |
| `groups_mode` | no | `claim` | `claim` (use JWT groups) or `graph` (enrich via Graph on empty groups). |
| `graph_timeout` | no | `5s` | Per-Graph-call timeout. |
| `jwks_cache_ttl` | no | `1h` | JWKS cache age before refresh. |

## Acquiring tokens for testing

```bash
# From a logged-in az CLI:
az account get-access-token --resource api://forge \
  --query accessToken -o tsv > /tmp/aad.tok

curl -H "Authorization: Bearer $(cat /tmp/aad.tok)" \
  http://localhost:9999/tasks -d '{}'
```

## Audit log

```json
{ "event": "auth_verify",
  "fields": {
    "provider":   "azure_ad",
    "user_id":    "alice@contoso.onmicrosoft.com",
    "org_id":     "00000000-1111-2222-3333-444444444444",   // tid
    "token_kind": "jwt",
    "groups":     ["group-guid-1", "group-guid-2"]
  }
}
```

Note `provider: "azure_ad"`, not `"oidc"` — even though OIDC did the
signature work, the audit name reflects the operator's configuration.

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| `auth_fail reason=rejected` + "tid mismatch" | Token from a different Entra tenant | Re-sign in to the right tenant, or set `allow_multi_tenant: true` if intentional |
| `auth_fail reason=rejected` + "aud" | `audience` in forge.yaml doesn't match the app registration's Application ID URI | Make both match exactly |
| Identity has empty `groups` despite user being in groups | Groups overage (>200 groups) | Switch to `groups_mode: graph` and grant `GroupMember.Read.All` admin consent |
| Graph enrichment soft-fails (auth OK, groups empty) | Graph 5xx or network failure | Check `graph.microsoft.com` reachability |
| `auth_fail reason=invalid` + "missing tid" | Token is not an AAD token (maybe a guest/personal Microsoft account?) | Confirm token issuer; only AAD work/school accounts have `tid` |

## Security model

- **Composes** the `oidc` provider — no JWT/JWKS code lives in this package.
  All signature verification, algorithm-whitelisting, and key rotation
  reuse the Phase 1 OIDC implementation.
- **Tenant gate before Graph** — for single-tenant configs, the `tid` check
  runs before any Graph call. Different tenant = different security domain.
- **Caller's token reflected to Graph** — Forge never holds Graph
  credentials. The user's delegated permission is the only authority.
- **Soft-fail on Graph 5xx** — Identity still returned with empty groups
  rather than blocking prod traffic on a transient Graph outage.
- **Internal `skip_issuer_check`** flag on the composed OIDC is exposed
  ONLY when `allow_multi_tenant=true`, and the field is `yaml:"-"` so it
  cannot be set via `forge.yaml` — preventing an operator from accidentally
  disabling issuer validation.

## Limitations

- No SAML — Phase 5+.
- No `client_credentials` flow for workload-to-Forge with no user context.
  If running in AWS, use [`aws_sigv4`](./aws_sigv4.md); otherwise use an
  [`oidc`](./oidc.md) entry pointed at your workload identity's issuer.
- "Multi-tenant" means **any** tenant — there's no allowlist of tenants.
  For "this set of tenants," configure multiple `azure_ad` entries.
