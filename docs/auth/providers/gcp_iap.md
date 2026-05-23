---
title: "gcp_iap — GCP Identity-Aware Proxy"
description: "Verify the JWT IAP forwards on every authenticated request."
order: 11
---

The `gcp_iap` provider authenticates requests that come through GCP's
Identity-Aware Proxy. IAP terminates user authentication at the HTTPS Load
Balancer and forwards a signed JWT in the `X-Goog-Iap-Jwt-Assertion` header.
Forge verifies that JWT against IAP's well-known JWKS and stamps an Identity
from the `sub` + `email` claims.

## When to use it

Forge is deployed behind a GCP HTTPS Load Balancer with IAP enabled, and you
want the IAP-authenticated user (Google account / Workspace identity) to
flow through to Forge.

## Prerequisites

- [ ] Forge is reachable through a GCP HTTPS LB.
- [ ] IAP is enabled on the LB's backend service.
- [ ] Forge can reach `www.gstatic.com` (auto-added to the Phase 2 egress
  allowlist).
- [ ] You know the backend service's "Signed Header JWT Audience" string.

### Finding the audience

The audience is the GCP backend service ID:

```
GCP Console → Security → Identity-Aware Proxy
  → Backend Services tab
  → click your backend
  → "Signed Header JWT Audience" field
```

Format: `/projects/PROJECT_NUMBER/global/backendServices/BACKEND_ID`.

## forge.yaml

```yaml
auth:
  required: true
  providers:
    - type: gcp_iap
      settings:
        audience: /projects/12345678/global/backendServices/9876543210
```

## Configuration reference

| Field | Required | Default | Description |
|---|---|---|---|
| `audience` | yes | — | GCP backend service ID. Must match exactly. |
| `jwks_refresh_ttl` | no | `1h` | How long a cached JWKS is reused before refresh. |
| `http_timeout` | no | `5s` | JWKS fetch timeout. |

The IAP **issuer** (`https://cloud.google.com/iap`) and **JWKS URL**
(`https://www.gstatic.com/iap/verify/public_key-jwk`) are **hardcoded**.
They are the only stable contract GCP exposes; an override knob would be a
footgun (someone could be tricked into trusting an attacker's JWKS).

## Audit log

```json
{ "event": "auth_verify",
  "fields": {
    "provider":    "gcp_iap",
    "user_id":     "1234567890",          // claims.sub
    "email":       "alice@example.com",   // claims.email
    "token_kind":  "jwt"
  }
}
```

The Workspace domain claim (`hd`) is included in `Identity.Claims` for
downstream policy decisions.

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| `auth_fail reason=not_for_me` | Request didn't come through IAP — no `X-Goog-Iap-Jwt-Assertion` header | Hit Forge through the GCP LB, not the backend directly |
| `auth_fail reason=rejected` + "iss" mismatch | Token is a regular Google OAuth token, not an IAP assertion | Confirm IAP is enabled; route the request through the LB |
| `auth_fail reason=rejected` + "aud" | `audience` in forge.yaml doesn't match the backend service ID | Re-check the audience string in the GCP console |
| `auth_fail reason=invalid` + "alg" | Token signed with something other than ES256 | Not an IAP token; check origin |
| `auth_fail reason=provider_unavailable` | `www.gstatic.com` unreachable | Check egress allowlist, network |

## Security model

- **ES256 algorithm whitelist** rejected any other signing method *before*
  key lookup — algorithm-confusion attacks can't reach the JWKS.
- **JWKS host hardcoded** so a malicious config can't redirect verification
  to an attacker-controlled key server.
- **Stale-grace cache** — during a JWKS endpoint outage, the previously
  cached keys remain valid. IAP rotates keys on the order of weeks; freshness
  matters less than availability.

## Limitations

- No per-email / per-domain allowlist in Forge — use **GCP IAM Conditions**
  on the backend service for that. IAP can grant or deny access at the LB
  layer; Forge sees only requests IAP already approved.
- Single audience per provider entry.
