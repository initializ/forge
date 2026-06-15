---
title: "Tenancy Stamping"
description: "Stamping org_id and workspace_id on every audit event from env + headers."
order: 9
---

## Tenancy Stamping

For multi-tenant deployments, every Forge audit event can carry top-level `org_id` and `workspace_id` keys so SIEM / audit-warehouse consumers filter by tenancy without joining against `auth_verify` rows. See issue #157.

## Two layers

The same agent process supports both the static-deployment case (one
agent serves one workspace) and the multi-tenant routing case (one
agent serves many workspaces, the orchestrator picks per request).

### Tenancy fields (`org_id` / `workspace_id`)

| Layer | Source | Wins when |
|-------|--------|-----------|
| 1 — Explicit on event | `AuditEvent.OrgID` / `AuditEvent.WorkspaceID` set before emit | Always — caller-owned event takes precedence over every fallback |
| 2 — Per-request override | `X-Forge-Org-ID` / `X-Forge-Workspace-ID` request headers | Inside the request scope when present; ctx falls through to layer 3 otherwise |
| 3 — Deployment-time stamp | `FORGE_ORG_ID` / `FORGE_WORKSPACE_ID` env vars | Whenever the higher layers carry no value |

Each field is resolved independently. A request that overrides only `X-Forge-Org-ID` still lets the env stamp fill in `workspace_id`.

### Entity fields (`entity_id` / `entity_type`) — #164

| Layer | Source | Wins when |
|-------|--------|-----------|
| 1 — Explicit on event | `AuditEvent.EntityID` / `AuditEvent.EntityType` set before emit | Always — caller-owned event takes precedence |
| 2 — Deployment-time stamp | `FORGE_AGENT_ID` env / forge.yaml `agent_id` → `entity_id`; `entity_type` hardcoded to `"agent"` | Whenever the higher layer is empty |

Entity identity has **no per-request header layer** — entity is fixed at process startup. If a deployment needs per-request entity routing, the tenancy layer above already covers that (an agent serving multiple workspaces). Agent identity is the process, by definition.

`entity_type` and `entity_id` match the field names + values the guardrails library writes to its MongoDB `GuardrailAuditEvent` collection (see `EntityType` constants: `agent` / `workflow` / `assistant`). When `FORGE_GUARDRAILS_DB` is set, both streams carry the same `(entity_id, entity_type)` pair and consumers join them 1:1 without translation. Forge only runs agents today, so the value is always `"agent"`; future entity types are an additive value change, not a schema change.

## Static tenancy (one agent per workspace)

The simplest case: deploy one Forge agent into one workspace, declare the tenancy via env, never set headers. Every emitted event — including startup banners — carries the stamp.

```yaml
# Kubernetes deployment fragment
env:
  - name: FORGE_ORG_ID
    value: "org_abc123"
  - name: FORGE_WORKSPACE_ID
    value: "ws_xyz789"
```

The audit stream then looks like:

```json
{"ts":"2026-06-14T10:00:00Z","event":"agent_card_published","schema_version":"1.0","org_id":"org_abc123","workspace_id":"ws_xyz789","fields":{...}}
{"ts":"2026-06-14T10:00:05Z","event":"session_start","schema_version":"1.0","seq":1,"correlation_id":"...","task_id":"...","org_id":"org_abc123","workspace_id":"ws_xyz789"}
{"ts":"2026-06-14T10:00:08Z","event":"llm_call","schema_version":"1.0","seq":2,"correlation_id":"...","task_id":"...","model":"...","provider":"...","org_id":"org_abc123","workspace_id":"ws_xyz789"}
```

SIEM filter: `org_id = "org_abc123" AND workspace_id = "ws_xyz789"`.

## Per-request routing (one agent serves many workspaces)

For deployments where one Forge agent fronts many workspaces and the orchestrator routes per request, set the env vars to a default tenancy (or leave them empty) and have the orchestrator send the override headers on every request:

```sh
curl -X POST https://agent.example.com/ \
  -H 'X-Forge-Org-ID: org_def456' \
  -H 'X-Forge-Workspace-ID: ws_pqr012' \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":"1","method":"tasks/send","params":{...}}'
```

Every audit event emitted during that request carries `"org_id":"org_def456","workspace_id":"ws_pqr012"`. The next request from a different workspace gets its own stamp.

Startup banners (`agent_card_published`, `policy_loaded`, `audit_export_status`) still reflect the env stamp because they have no request context.

## Outbound propagation (agent-to-agent flows)

When one Forge agent calls another (via the egress proxy with explicit propagation), the helper `coreruntime.TenancyContextFromContext(ctx).ApplyToHTTPHeaders(req.Header)` writes both headers onto the outbound request. The downstream agent picks them up at its A2A boundary the same way.

Auto-propagation is NOT built into the egress proxy. The agent only propagates tenancy when it knows the target is a tenancy-aware Forge peer. This mirrors the workflow-header behavior: explicit only, to avoid leaking tenancy to unrelated third-party APIs.

## Backwards compatibility

Both `org_id` and `workspace_id` use `omitempty`. Deployments that set neither env nor header keep emitting the pre-tenancy JSON shape verbatim. Consumers that ignore unknown keys continue to work unchanged. The audit schema version is **not** bumped — additive optional fields are schema-compatible per the documented policy.

## Distinct from auth_verify.fields.org_id

The auth provider chain resolves an `Identity.OrgID` from the inbound bearer token (whatever the issuer claims) and stamps it on `auth_verify.fields.org_id` for back-compat. That value reflects the *user's* org from their identity token.

The top-level `org_id` documented here is the **deployment's** declared tenancy — the operator's explicit assertion of where this agent runs. The two can differ legitimately (federated identity, cross-tenant invocation) and downstream consumers should treat them as independent signals. Both can be present on the same `auth_verify` event.

## See also

- [Audit Logging](audit-logging.md) — full event catalog
- [Workflow correlation IDs](workflow-correlation.md) — the sibling FWS-2 header system (`X-Workflow-*`)
- [Authentication](authentication.md) — where `Identity.OrgID` comes from
