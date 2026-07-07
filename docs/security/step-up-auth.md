# Step-up authorization (governance R4b)

Forge can require a fresher, higher-assurance authentication for
specific tool calls. When a caller presents an authentication that
doesn't meet the tool's declared assurance level, Forge returns an
[RFC 9470](https://www.rfc-editor.org/rfc/rfc9470) step-up challenge
so the client can re-authenticate at the required level and retry.

This closes the R4b gap in the governance framework — pre-check,
the policy engine could ALLOW or DENY per action, but had no third
option to demand a higher-assurance authentication.

## How it works

1. Operator declares per-tool assurance requirements in
   `forge.yaml security.step_up.tools`.
2. On every `BeforeToolExec`, Forge:
   - Looks up the required `acr` for the tool.
   - Reads the caller's identity from context (populated by the
     auth middleware at the request boundary).
   - Checks the identity's `acr` claim against the requirement.
3. On a mismatch, the runtime:
   - Aborts the tool call.
   - Emits an `auth_step_up_required` audit event.
   - Returns HTTP 401 with:
     ```
     WWW-Authenticate: Bearer error="step_up_required",
                              acr_values="acr:mfa"
     ```
4. The caller's SDK sees the challenge, prompts the user for the
   higher-assurance authentication (MFA / hardware key / etc.),
   and retries with a new bearer token whose `acr` claim now meets
   the requirement.
5. Auth middleware validates the token, the retry passes step-up,
   the tool runs.

## Configuration

```yaml
security:
  step_up:
    enabled: true

    # Per-tool required acr values. Tools not listed have no
    # step-up requirement.
    tools:
      cli_execute: acr:mfa
      http_request: acr:mfa

    # Optional: ordered assurance hierarchy, lowest first. When
    # present, comparison is "index-of-presented >= index-of-required".
    # A caller with a stronger acr satisfies a weaker requirement.
    # Absent → strict-equal comparison.
    acr_hierarchy:
      - acr:password
      - acr:mfa
      - acr:hardware
```

**Default is off.** When `enabled: false` (or absent), no step-up
hook fires and no config is validated.

**Fail-loud on config errors**: enabling with an empty `tools` map,
or listing a tool whose required acr isn't in the hierarchy (typo),
fails startup rather than at first tool call.

## ACR claim shape

Forge reads the `acr` claim from the identity's `Claims` map — the
raw JWT payload that the OIDC / bearer verifier populated. Values
are opaque strings; Forge doesn't interpret them beyond matching
against the configured requirements. Typical shapes:

- `"acr:password"`, `"acr:mfa"`, `"acr:hardware"` — internal
  convention (as used in these docs).
- `"urn:mace:incommon:iap:silver"` — InCommon.
- `"0"` / `"1"` / `"2"` — SAML AuthContextClass loa values.
- `"phr"` / `"phrh"` — pre-registered OIDC classes.

Coordinate values with your IdP: whatever it stamps on the token,
that's what Forge sees.

## RFC 9470 challenge shape

The response follows [RFC 9470 §3](https://www.rfc-editor.org/rfc/rfc9470#name-authentication-challenges-c):

```
HTTP/1.1 401 Unauthorized
Content-Type: application/json
WWW-Authenticate: Bearer error="step_up_required", acr_values="acr:mfa"

{
  "error": "step_up_required",
  "tool": "cli_execute",
  "required_acr": "acr:mfa",
  "reason": "no acr claim presented"
}
```

The `error` parameter and `acr_values` parameter are the two RFC
9470 params SDKs key off. The JSON body carries the same shape as
the `auth_step_up_required` audit event so operators debugging a
challenge can grep for the tool + acr pair in both places.

## Audit event

```json
{
  "event": "auth_step_up_required",
  "task_id": "task-abc",
  "correlation_id": "req-xyz",
  "fields": {
    "tool": "cli_execute",
    "required_acr": "acr:mfa",
    "presented_acr": "",
    "reason": "no acr claim presented"
  }
}
```

`presented_acr` is omitted when the caller had no `acr` claim at
all. Payload never carries token bytes or full claim maps.

## Threat model + limits

**Solves**:
- Escalation attacks where a caller with a low-assurance session
  tries to invoke a high-assurance tool.
- Post-hoc audit of "which action required step-up and did the
  caller satisfy it?" via `auth_step_up_required` events.

**Does NOT solve**:
- **Confused-deputy**: a legitimate high-assurance session invoked
  by an attacker on the same client machine. Step-up doesn't
  authenticate the *human* on the retry, only the token.
- **acr claim tampering**: if the IdP mints a token with a spoofed
  acr claim, Forge trusts it. Protection lives at the IdP layer.
- **Cross-service step-up state**: Forge does not maintain a
  step-up-completed marker across separate tool calls. Every call
  is evaluated against the current identity's acr; if the required
  acr expires or a lower-acr session is presented, step-up fires
  again.

## Current coverage

The 401 challenge is emitted from:

- `POST /tasks/send` REST handler ✅

Not yet:
- JSON-RPC over HTTP — the error surfaces in the JSON-RPC error
  body but does not translate to an HTTP 401 with the challenge
  header. Follow-up to plumb the typed error through the dispatcher.
- SSE `/tasks/sendSubscribe` — the challenge fires mid-stream, so
  it lands as a task-failed status event rather than an HTTP
  response header. Follow-up to design the SSE analogue.

For deployments where JSON-RPC or SSE are the primary surface, use
`security.step_up` alongside a strict `guardrails` deny so the
tool never runs even when the challenge isn't the transport-native
signal.

## Combining with other governance controls

- **R3 intent alignment (#208)**: fires BEFORE step-up. A tool
  call the alignment engine denies never reaches the step-up
  check (correct — the operator's intent policy says the caller
  shouldn't do this at all).
- **R9 JIT credentials (#215)**: step-up validates the caller's
  authentication assurance; JIT credentials mint the tool's
  scope-down credentials. Both fire per tool call — step-up first
  (fails fast if the caller can't authenticate) then JIT mint (only
  if step-up admits).
- **R5 hash chain (#212) + R6 signing (#213)**: the
  `auth_step_up_required` event participates in both — chained
  under prev_hash, signed with the audit kid.
