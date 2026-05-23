# Changelog

## v0.11.0 — Phase 2: cloud-native auth providers (in progress)

### Added

- **`aws_sigv4` auth provider.** Authenticate AWS-IAM callers by reflecting
  their Sigv4 signature to AWS STS `GetCallerIdentity`. No `aws-sdk-go-v2`
  dependency. See [`docs/auth/providers/aws_sigv4.md`](docs/auth/providers/aws_sigv4.md).
- **`gcp_iap` auth provider.** Verify the JWT IAP forwards as
  `X-Goog-Iap-Jwt-Assertion` when Forge sits behind a GCP HTTPS Load
  Balancer with IAP enabled. See [`docs/auth/providers/gcp_iap.md`](docs/auth/providers/gcp_iap.md).
- **`azure_ad` auth provider.** Verify Microsoft Entra ID Bearer tokens
  with tenant lock-in and optional Microsoft Graph group enrichment.
  See [`docs/auth/providers/azure_ad.md`](docs/auth/providers/azure_ad.md).
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
- `auth.HeadersFromRequest` widened with `Authorization`,
  `X-Goog-Iap-Jwt-Assertion`, `X-Amz-Date`, and `X-Amz-Security-Token`.
  Providers that don't consume these keys are unaffected.
- `auth.TokenKind` recognizes the `AWS4-HMAC-SHA256` prefix and returns
  `"sigv4"`. The audit `token_kind` field now has five possible values:
  `empty`, `opaque`, `jwt`, `sigv4`, `iap_jwt`.
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

### Known deferred work

- The Bubble Tea TUI wizard (`forge init`) does not yet have step-by-step
  input flows for the three new providers. The non-interactive flag path
  is the production-critical surface; operators using the TUI can pick
  "Custom" and edit `forge.yaml` directly until the TUI follow-up lands.
