---
title: "aws_sigv4 — AWS IAM"
description: "Authenticate AWS-IAM callers via STS GetCallerIdentity reflection."
order: 10
---

The `aws_sigv4` provider authenticates requests signed with AWS Signature
Version 4. Forge reflects the caller's signature to AWS STS
`GetCallerIdentity`; STS validates it on Forge's behalf and returns the
canonical ARN/Account/UserID. **Forge never possesses the caller's secret
key.** Same pattern as `aws-iam-authenticator` (the EKS auth bridge).

## When to use it

Use this when your callers (Lambda, EC2, EKS workloads, developer laptops
with `aws configure`/`aws sso login`) already have AWS credentials. No
separate token issuer needed — IAM is the identity.

## Prerequisites

- [ ] Forge can reach `sts.<region>.amazonaws.com` (auto-added to the
  Phase 2 egress allowlist).
- [ ] At least one IAM identity exists in the target AWS account.
- [ ] You've decided which principals to allow (optional but recommended
  for production).
- [ ] **No additional IAM permissions required.** `GetCallerIdentity` is
  implicit for every IAM principal on itself.

## forge.yaml

```yaml
auth:
  required: true
  providers:
    - type: aws_sigv4
      settings:
        region: us-east-1
        audience: api://forge        # informational; emitted in audit
        allowed_principals:
          # NOTE: STS returns assumed-role ARNs, NOT IAM role ARNs.
          # Match against arn:aws:sts::, not arn:aws:iam::.
          - "arn:aws:sts::123456789012:assumed-role/ci-deploy/*"
          - "arn:aws:sts::123456789012:assumed-role/forge-runner/*"
        identity_cache_ttl: 60s
```

## Configuration reference

| Field | Required | Default | Description |
|---|---|---|---|
| `region` | yes | — | AWS region for the STS endpoint. Sigv4 signatures must scope to this region. |
| `audience` | no | — | Informational string; emitted in `Identity.Claims.audience`. STS doesn't enforce it. |
| `allowed_principals` | no | empty (allow any) | List of [shell-style globs](https://pkg.go.dev/path#Match) matched against the **STS-returned ARN**. |
| `identity_cache_ttl` | no | `60s` | How long a verified Identity is reused before re-checking STS. Bounds the stolen-key window. |
| `http_timeout` | no | `5s` | Per-STS-call timeout. |

`allowed_principals` is the authz mechanism. Empty list means "allow any IAM
principal who can sign for this region" — fine for single-tenant dev, never
appropriate for production.

## Worked example — call Forge from a script

```bash
# Make sure your AWS profile points at an identity in allowed_principals:
export AWS_PROFILE=dev

# awscurl handles the Sigv4 signing:
pipx install awscurl

awscurl --service sts --region us-east-1 \
  -X POST -d '{"task":"hello"}' \
  -H "Content-Type: application/json" \
  http://localhost:9999/tasks
```

A 200 response means the chain accepted your IAM identity; check the Forge
log for the matching `auth_verify` event.

## Audit log

Successful auth:

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

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| `auth_fail reason=not_for_me` | `Authorization` header didn't start with `AWS4-HMAC-SHA256` | Caller is signing with Bearer/something else — point them at the right tooling (`awscurl`, AWS SDK) |
| `auth_fail reason=rejected` + STS body mentions signature | Caller signed with wrong creds, expired session, or clock skew | Re-sign with current creds; check system clock; ensure session token is valid |
| `auth_fail reason=rejected` + "ARN ... not in allowed_principals" | Allowlist mismatch | **Common bug:** the pattern must match the STS-returned **assumed-role ARN** (`arn:aws:sts::ACCT:assumed-role/...`), NOT the IAM role ARN (`arn:aws:iam::ACCT:role/...`) |
| `auth_fail reason=rejected` + "service=s3" or "region=eu-west-1" | Caller signed for wrong service or region | Re-sign with `--service sts --region <forge-region>` |
| `auth_fail reason=provider_unavailable` | STS or network is down; egress allowlist missing the STS host | Check `sts.<region>.amazonaws.com` reachability; check `egress_hosts` |

## Security model

- **No secret key on Forge.** STS does the signature math.
- **Cache bucketing on YYYYMMDD + per-AKID** bounds the stolen-key window:
  a leaked AKID is valid until midnight UTC OR `identity_cache_ttl`
  elapses, whichever is sooner.
- **Service/region scope check** rejects Sigv4 signatures meant for S3, EC2,
  or a different region. Prevents cross-service replay.
- **No `aws-sdk-go` dependency.** The STS RPC is ~150 LOC of hand-rolled
  HTTP + XML. Smaller attack surface than a transitive SDK.

## Limitations

- One AWS region per provider entry. Configure multiple entries for
  multi-region.
- Cognito federation / SAML / OIDC — use the [`oidc`](./oidc.md) provider
  pointed at your Cognito user pool's issuer.
- Sigv4 is inbound only — Forge does not Sigv4-sign outbound requests.
