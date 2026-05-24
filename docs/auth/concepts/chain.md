---
title: "Auth Provider Chain Semantics"
description: "First-match-wins, fail-closed, and the rules every provider follows."
order: 2
---

Forge's auth chain composes multiple providers in a single `auth.providers`
list. Understanding the chain rules is the difference between a chain that
authenticates safely and one that silently downgrades to the most permissive
provider on the list.

## The rules

Each provider's `Verify` returns one of four signals:

| Return | Meaning | Chain behavior |
|---|---|---|
| `Identity, nil` | Token accepted | Stops; chain returns this Identity |
| `nil, ErrTokenNotForMe` | "Not my format" | Continues to next provider |
| `nil, ErrTokenRejected` | "My format, but denied" | **Stops; chain returns 401** |
| `nil, ErrInvalidToken` | "Malformed" | **Stops; chain returns 401** |
| `nil, ErrProviderUnavailable` | "Can't reach my IdP" | **Stops; chain returns 401** |
| `nil, any other error` | Transport / infrastructure | **Stops; chain returns 401** |

The critical rule is the **no-fall-through on rejection**: if provider A
returns `ErrTokenRejected`, the chain does NOT try provider B. Falling
through would let an attacker downgrade their authentication — present a
malformed JWT and hope to be authenticated as an opaque-token user instead.

## First-match-wins ordering

Providers are tried top-to-bottom. Whoever **claims** the request first
(returns anything except `ErrTokenNotForMe`) decides the outcome.

Practically: put more specific providers earlier. For Forge instances that
run channel adapters (Slack, Telegram), the `static_token` loopback secret
is auto-prepended to the chain — it always matches first for self-issued
loopback calls, regardless of what's configured in `forge.yaml`.

## TUI wizard ordering

The `forge init` wizard runs the **Auth step before the Egress step** —
once you pick a provider, the egress review automatically includes the
outbound hosts it needs (STS endpoint for `aws_sigv4`, AAD authority for
`azure_ad`, IAP JWKS host for `gcp_iap`, OIDC issuer host for `oidc`).
Operators see and confirm those hosts alongside provider / channel / tool /
skill hosts in a single review screen — no need to add auth hosts to
`egress_hosts` by hand after the wizard.

## Non-Bearer formats (v0.11.0)

As of v0.11.0 the middleware consults the chain **even when no Bearer token
was extracted** — required for `aws_sigv4` (which reads `Authorization:
AWS4-HMAC-SHA256 …`) and `gcp_iap` (which reads `X-Goog-Iap-Jwt-Assertion`).

When the request carries no auth-shaped headers at all, the audit reason
code is still `missing_token` rather than `not_for_me` — operators can still
distinguish "client didn't auth" from "client tried a format we don't speak."

## Example: mixed AWS + OIDC chain

```yaml
auth:
  required: true
  providers:
    # Internal CI/automation calls — Sigv4-signed under an IAM role:
    - type: aws_sigv4
      settings:
        region: us-east-1
        allowed_principals:
          - "arn:aws:sts::123456789012:assumed-role/ci-deploy/*"
    # Human users — Keycloak Bearer JWT:
    - type: oidc
      settings:
        issuer: https://keycloak.example.com/realms/forge
        audience: api://forge
```

A Sigv4-signed request matches `aws_sigv4` and never reaches `oidc`. A Bearer
JWT request returns `ErrTokenNotForMe` from `aws_sigv4` (no `AWS4-HMAC-SHA256`
prefix), then matches `oidc`. A garbage Bearer ("Bearer foobar") returns
`ErrTokenNotForMe` from both providers and the middleware emits
`auth_fail{reason: "not_for_me"}` with a 401.
