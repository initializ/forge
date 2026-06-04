# A2A Agent Card

Every Forge agent publishes an Agent Card per the [Agent2Agent (A2A) Protocol](https://github.com/google/a2a-spec) so peer agents, orchestrators (initializ platform, custom registries), and A2A-aware tooling can discover its identity, capabilities, and authentication shape via a single `GET`.

The card is JSON, conforms to **A2A 0.3.0**, and lives at the spec-canonical path.

```
GET http://<host>:<port>/.well-known/agent-card.json
```

The legacy path `GET /.well-known/agent.json` is still served and returns the same body, but emits a `Deprecation: true` response header per [RFC 8594](https://datatracker.ietf.org/doc/html/rfc8594) and a `Link` header pointing at the canonical path. The legacy alias will be removed one release after this change ships.

Both paths are public — `DefaultSkipPaths` exempts them from the auth chain.

## Card shape

A Forge agent's card always contains every field A2A 0.3.0 marks as required:

| Field | Source | Notes |
|---|---|---|
| `name` | `forge.yaml` `agent_id` (or `agentspec.Name`) | Required. |
| `description` | `agentspec.Description` | Optional but Forge populates. |
| `url` | `http://<host>:<port>` of the running A2A server | Required. |
| `version` | `forge.yaml` `version` (or `agentspec.Version`) | Required. Defaults to `0.0.0` when not set. |
| `protocolVersion` | Pinned at build time | Always `0.3.0`. Bumping is a deliberate PR. |
| `defaultInputModes` | Forge default | `["text/plain", "application/json"]`. |
| `defaultOutputModes` | Forge default | `["text/plain", "application/json"]`. |
| `skills` | `agentspec.A2A.Skills` (build-time SKILL.md mapping) + builtin tools | A2A `AgentSkill` objects; see below. |
| `capabilities` | `agentspec.A2A.Capabilities` | `streaming`, `pushNotifications`, `stateTransitionHistory`. |
| `securitySchemes` | Derived from `auth.providers` | See *Security* below. |
| `security` | Derived from `auth.providers` | First-match-wins → OR-list per A2A semantics. |

Forge-internal fields (egress allowlist, denied tools, trust hints, guardrails) are intentionally **not** serialized into the Agent Card. The card is a public discovery surface; those fields are runtime contracts that stay inside Forge.

## AgentSkill mapping

The SKILL.md frontmatter maps to A2A `AgentSkill` objects with no information loss for spec-defined fields:

| `SKILL.md` frontmatter | A2A `AgentSkill` field |
|---|---|
| `name` | `id` and `name` |
| (display name from frontmatter, if any) | `name` (overrides `id`-derived name) |
| `description` | `description` |
| `category` | `tags[0]` (so clients can group) |
| `tags` | `tags[]` (appended, case-insensitive dedup) |

A2A 0.3.0 makes `tags` **required** — Forge falls back to `["skill"]` (or `["tool"]` for builtin tools surfaced as skills) when neither category nor tags are supplied, so the field is always non-empty.

`examples`, `inputModes`, `outputModes` are spec-optional and currently not populated from SKILL.md. A future SKILL.md schema bump can add `examples:` and `modes:` blocks; the types already accept them.

### Where the skill list comes from

Forge walks SKILL.md frontmatter in **two places** — and both apply the same mapping rules above:

1. **`forge build`** — the `generate-agentspec` stage discovers `skills/*.md`, `skills/*/SKILL.md`, and the main agent skill (default `SKILL.md`, or `skills.path` from `forge.yaml`), parses each, and writes the result into `agent.json` under `a2a.skills`. This is what initializ-side registries and any consumer reading the raw `agent.json` will see.
2. **`forge run` / `forge dev`** — the runner does the same walk at agent startup and appends discovered skills onto the card. Pre-existing skills (from `agent.json`'s `a2a.skills`) take precedence; the runtime enrichment only fills gaps. This means agents started directly from source — before `forge build` runs — still publish the right skill set.

Both paths sort the discovered skills deterministically by ID so the resulting card bytes are stable across rebuilds + restarts (and so the `agent_card_published` audit event's sha256 hash is meaningful).

The card is fixed at agent startup (matches the binary's embedded skills + build artifact + runtime SKILL.md walk). Hot-reload via the file watcher re-runs the walk, rebuilds the card, and re-emits the `agent_card_published` audit event.

## Security schemes

When `forge.yaml` declares an `auth:` chain, every provider becomes one entry in `securitySchemes` and one entry in `security`. The mapping mirrors the auth middleware's actual acceptance rules:

| `auth.providers[].type` | A2A scheme | Notes |
|---|---|---|
| `static_token` | `http` + `bearer` | Shared-secret token in `Authorization`. |
| `http_verifier` | `http` + `bearer` | Opaque bearer; external verifier validates. |
| `oidc` | `openIdConnect` | `openIdConnectUrl` derived from `issuer`. |
| `azure_ad` | `openIdConnect` | `openIdConnectUrl` is the AAD per-tenant well-known. |
| `gcp_iap` | `apiKey` in `header` | `X-Goog-Iap-Jwt-Assertion`. |
| `aws_sigv4` | `http` + `bearer` (custom `bearerFormat: "forge-aws-v1"`) | Pre-signed STS URL wrapped in a Bearer. |

Schemes Forge doesn't have a well-defined mapping for emit nothing in the card — the auth chain still enforces them; the card just doesn't advertise the credential shape. Operators with a hand-wired scheme set on the card before runtime invocation are preserved verbatim (the deriver is additive).

The `security` array carries one OR-entry per scheme, matching Forge's first-match-wins chain semantics: presenting any one configured credential satisfies the requirement.

## Audit event on publish

Each time Forge finalizes an Agent Card (startup + file-watcher hot-reload), the runtime emits one `agent_card_published` audit event to the audit logger:

```json
{
  "event": "agent_card_published",
  "fields": {
    "name":             "weather-agent",
    "version":          "0.4.2",
    "protocol_version": "0.3.0",
    "url":              "http://localhost:8080",
    "skill_count":      7,
    "capabilities":     {"streaming": true, "push_notifications": false, "state_transition_history": false},
    "security_schemes": ["static_token", "oidc"],
    "card_size_bytes":  3471,
    "card_sha256":      "3a8c…"
  }
}
```

The event carries identity + size metadata + a sha256 of the JSON-encoded card so audit consumers can detect config drift across deploys. Full payload bytes are intentionally NOT emitted — the same discipline every other Forge audit event respects.

## `forge dev` vs deployed parity

The card builder uses the same code path in both environments:

- `forge dev` / `forge run` from source — `AgentCardFromConfig(cfg, baseURL)` populates the core fields from `forge.yaml`; the runner then walks SKILL.md files in the workdir and appends discovered skills via `enrichAgentCardWithSkills`.
- After `forge build` produces `.forge-output/agent.json` — `AgentCardFromSpec(spec, baseURL)` populates from the spec (which already carries `a2a.skills` populated at build time); the runner's enrichment then appends any SKILL.md skills not already represented (no-op when the build artifact is complete).

Both paths apply the same `PopulateSecuritySchemes` deriver and emit the same `agent_card_published` event. **The card's JSON shape and skill list are identical in both environments** — the only difference is whether `agent.json` exists on disk; the runtime guarantees parity by walking SKILL.md regardless.
