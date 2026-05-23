---
title: "Authentication Providers"
description: "Pluggable auth provider chain — pick the formats Forge accepts on /tasks."
order: 1
---

Forge's `/tasks` endpoint requires every request to authenticate through a
chain of pluggable auth providers configured in `forge.yaml`. Each provider
recognizes one token shape; the chain tries them in order, first match wins.

## Provider Matrix

| Provider | Use case | Token format | Phase |
|---|---|---|---|
| [`static_token`](./providers/static_token.md) | Local dev, loopback channel adapters | Shared secret | 1 |
| [`oidc`](./providers/oidc.md) | Any IdP with OIDC discovery (Keycloak, Okta, Auth0, Google) | Bearer JWT | 1 |
| [`http_verifier`](./providers/http_verifier.md) | Custom verifier endpoint | Opaque | 1 |
| [`aws_sigv4`](./providers/aws_sigv4.md) | AWS-IAM-based callers (Lambda, EC2, EKS, IAM users) | Sigv4 (`Authorization`) | **2 (v0.11.0)** |
| [`gcp_iap`](./providers/gcp_iap.md) | Behind GCP HTTPS Load Balancer + IAP | IAP JWT (`X-Goog-Iap-Jwt-Assertion`) | **2 (v0.11.0)** |
| [`azure_ad`](./providers/azure_ad.md) | Microsoft Entra ID tokens | Bearer JWT | **2 (v0.11.0)** |

## Phase 2 — Cloud-native providers (v0.11.0)

Three new providers consume the identities customers already have in their
cloud — no parallel IdP required:

- **AWS IAM** → callers sign requests with their existing IAM keys/role
  (IRSA, instance profile, Lambda role, `aws configure`). Forge verifies via
  STS `GetCallerIdentity` — zero secrets, no token endpoint to host.
- **GCP IAP** → Forge sitting behind a GCP HTTPS LB+IAP consumes the JWT IAP
  forwards on every authenticated request.
- **Azure AD** → Entra tokens with tenant lock-in and optional Microsoft
  Graph group enrichment.

## Chain semantics

Read [concepts/chain](./concepts/chain.md) for the full rules. In short:

- Providers are tried in `forge.yaml` order.
- A provider returns one of: an `Identity` (chain stops, request authorized);
  `ErrTokenNotForMe` (try next provider); or any other error (chain
  rejects — **does not** fall through to a more permissive provider).
- The first-match-wins rule prevents downgrade attacks: an attacker who
  presents a malformed token of type A doesn't get a chance to be authenticated
  as type B.

## Auditing

Every accepted request emits an `auth_verify` event with the provider's name
and the principal's identifiers. Every rejection emits `auth_fail` with a
stable reason code (`missing_token`, `not_for_me`, `rejected`, `invalid`,
`provider_unavailable`). Pipe both into your SIEM — see each provider's
"Audit log" section for the exact shape.
