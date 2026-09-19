# initializ-deploy.yaml — deploying agents to the initializ platform

`initializ-deploy.yaml` is the platform deploy manifest. It lives in the agent's git repo (next
to `forge.yaml` for forge agents) and is consumed in CI by:

```sh
initializ agent deploy -f initializ-deploy.yaml --image "$IMAGE" --wait
```

The manifest is an UPSERT keyed on `(workspace, name)`. Its schema is owned by the `initializ` CLI
(`internal/deployspec`). Always emit `apiVersion: initializ.ai/v1` and `kind: AgentDeploy`.

## Detecting the agent type (investigate before you generate)

Before generating a spec, investigate the project to infer the type (use the
`initializ_detect_agent` tool), then **confirm with the user**:

- **forge.yaml** present → **forge**.
- **package.json** with a Claude Agent SDK dependency (`@anthropic-ai/claude-agent-sdk`,
  `@anthropic-ai/sdk`, `@initializ/a2a-kit`) or a `strands` dependency → **claude-agent** / **strands**,
  language **node**.
- **requirements.txt** / **pyproject.toml** with `claude-agent-sdk` / `anthropic` or `strands` /
  `strands-agents` → **claude-agent** / **strands**, language **python**.

The language (node vs python) doesn't change the `agent.type` in the manifest (the governed image
determines the runtime) — it's used to confirm understanding with the user. Always confirm the type
(and node/python for non-forge) before calling the generator.

## Agent types

`agent.type` selects the runtime. Supported today:

- **forge** (default) — a forge agent. Reads `forge.yaml` + `SKILL.md` + `.forge-output`. The
  model/provider, egress, channels and skills all come from `forge.yaml`, NOT this manifest. The
  manifest must NOT set `model`, `a2a`, `http`, `storage`, or `initContainers` (the platform rejects
  them for forge). `agent.name` is optional — it defaults to `forge.yaml`'s `agent_id`. **A forge
  agent is ALWAYS exposed over A2A** — the platform auto-wires it, so you never declare `a2a` in the
  spec.
- **claude-agent** — a governed Claude Agent SDK image. No `forge.yaml`. `agent.name` and
  `model.provider` are REQUIRED. Choose ONE exposure mode (see below). May also use `storage`,
  `initContainers`, `ingress`.
- **strands** — a governed AWS Strands image. Structurally identical to claude-agent (same required
  and allowed fields, same two exposure modes).

Planned (not yet accepted by the platform): **langchain**, **google-adk**.

## Exposure (claude-agent / strands): A2A or an HTTP invoke endpoint

A governed non-forge agent is exposed exactly ONE of two ways — they are mutually exclusive:

- **A2A** (the default) — Agent2Agent. The governed loader serves the Agent Card
  (`/.well-known/agent-card.json`) + JSON-RPC. Fields: `a2a.enabled: true`, optional `a2a.port`
  (default 9090), `a2a.name` (default `agent.name`), `a2a.description`, `a2a.auth` (`""` | `bearer` |
  `none`; `bearer` requires the platform token).
- **HTTP invoke** — a plain-HTTP endpoint its own server serves (a webhook / manual-trigger route).
  Fields: `http.path` (must start `/`, e.g. `/invocations`), `http.method` (default POST),
  `http.input` (a JSON-Schema object for the request body; the console renders a typed form).

(forge agents don't use either — their A2A exposure is automatic.)

For the governed non-forge runtimes (claude-agent/strands) the LLM gateway, workload identity, PDP
url and audit forwarder are wired SERVER-SIDE from the workspace — the repo carries NO LLM endpoint,
token, or auth type. It declares only the governed image, the model PROVIDER (which managed gateway
to use), and app-level env/egress/sizing.

## Field reference

Top level:
- `apiVersion` — `initializ.ai/v1`
- `kind` — `AgentDeploy`
- `agent.name` — display/lookup name (DNS-1123 label). Upsert key with workspace.
- `agent.type` — `forge` | `claude-agent` | `strands`
- `agent.workspace` — `ws_…` (optional; falls back to `--workspace` / `INITIALIZ_WORKSPACE_ID` / token)
- `agent.tags` — `map[string]string`, stamped as NAMESPACED pod annotations `agent.initializ.ai/<key>`
- `agent.annotations` — RAW verbatim pod annotations (reserved prefixes `kubernetes.io/`, `k8s.io/`,
  `initializ.ai/` are rejected)
- `image` — the image CI built & pushed (overridable with `--image`)
- `model.provider` — `anthropic` | `openai` (non-forge only; required there)
- `model.name` — optional model pin (platform default otherwise)
- `env` — list of `{ name, value, secret, optional }`. `value` supports `${VAR}` interpolation from
  the CI process env (so secret VALUES come from the CI secret store, never the file); `$$` escapes a
  literal `$`. `secret: true` routes the value into the agent's k8s Secret only. `optional: true`
  drops the entry when its `${VAR}` is unset (otherwise an unset ref fails the deploy).
- `resources.replicas`, `resources.requests.{cpu,memory}`, `resources.limits.{cpu,memory}` — k8s
  quantity strings; omitted fields use platform defaults.
- `port` — container port (0..65535); 0 = forge default 8080.
- `egress.additionalDomains` — extra egress domains, merged additively with the platform floor (and,
  for forge, with `forge.yaml` `egress.allowed_domains` + skill `metadata.forge.egress_domains`).
- `deploy.wait` (bool), `deploy.timeout` (e.g. `10m`) — default deploy behavior; flags win.

forge-only:
- `forge.path` — path to `forge.yaml` (default `./forge.yaml`)
- `forge.outputDir` — forge build output (default `./.forge-output`)

non-forge only (claude-agent / strands):
- `a2a.enabled` (bool), `a2a.port` (default 9090), `a2a.name` (default `agent.name`),
  `a2a.description`, `a2a.auth` (`""` | `bearer` | `none`) — expose over Agent2Agent; the governed
  loader serves the Agent Card + JSON-RPC.
- `http.path` (must start `/`), `http.method` (default POST; GET/POST/PUT/PATCH/DELETE),
  `http.input` (a JSON-Schema object) — a plain-HTTP invoke endpoint. **Mutually exclusive with a2a.**
- `storage[]` — `{ name, size (e.g. 20Gi), mountPath, storageClass }`. ANY volume makes the agent a
  StatefulSet.
- `initContainers[]` — `{ name, command[], image }`; require ≥1 storage volume.
- `ingress.reachability` — `cluster` | `private` | `public` (also `ingress.host`, `className`, `tls`).

## Validation rules (the generator mirrors these)

- `agent.type` must be one of `forge`, `claude-agent`, `strands`.
- claude-agent / strands REQUIRE `agent.name` and `model.provider` (anthropic|openai).
- forge must NOT set `model`, `a2a`, `http`, `storage`, or `initContainers`.
- `a2a` and `http` are mutually exclusive.
- `a2a.auth` ∈ `{"", bearer, none}`; `ingress.reachability` ∈ `{"", cluster, private, public}`;
  `http.method` ∈ `{GET, POST, PUT, PATCH, DELETE}`; `port` ∈ `0..65535`; every `env[].name` non-empty.

## Example — forge

```yaml
apiVersion: initializ.ai/v1
kind: AgentDeploy
agent:
  name: support-agent
  type: forge
image: registry.initializ.ai/acme/support-agent:latest
forge:
  path: ./forge.yaml
  outputDir: ./.forge-output
env:
  - name: LOG_LEVEL
    value: info
  - name: OPENAI_API_KEY
    value: ${OPENAI_API_KEY}
    secret: true
resources:
  replicas: 1
  requests: {cpu: 250m, memory: 256Mi}
  limits: {cpu: "1", memory: 1Gi}
egress:
  additionalDomains: [api.stripe.com]
deploy:
  wait: true
  timeout: 10m
```

## Example — claude-agent

```yaml
apiVersion: initializ.ai/v1
kind: AgentDeploy
agent:
  name: code-reviewer
  type: claude-agent
image: registry.initializ.ai/acme/code-reviewer:latest
model:
  provider: anthropic
a2a:
  enabled: true
  auth: bearer
env:
  - name: INITIALIZ_ENFORCEMENT
    value: audit_only
resources:
  replicas: 1
  requests: {cpu: 250m, memory: 512Mi}
deploy:
  wait: true
  timeout: 10m
```

## Example — strands

```yaml
apiVersion: initializ.ai/v1
kind: AgentDeploy
agent:
  name: denyabot
  type: strands
image: reg.io/fanduel/denyabot:latest
model:
  provider: anthropic
  name: claude-sonnet-4-6
port: 8080
http:
  path: /invocations
  method: POST
  input: {type: object, properties: {max_apps: {type: integer}}}
env:
  - name: INITIALIZ_ENFORCEMENT
    value: audit_only
resources:
  replicas: 1
  requests: {cpu: 500m, memory: 1Gi}
```

## Deploy workflow

1. Authenticate the CLI once (usually in CI): `initializ auth login --token-stdin --org org_…`.
2. Build & push the image (forge: `forge build`; governed runtimes: CI builds the governed image).
3. Deploy: `initializ agent deploy -f initializ-deploy.yaml --image "$IMAGE" --wait`.
4. Inspect: `initializ agent list`, `initializ agent get <name>`, `initializ agent logs <name>`.
5. Secrets: `initializ agent secrets set KEY=VALUE --agent <name>`.

Config precedence for the CLI is flag > env (`INITIALIZ_*`) > `~/.initializ/config.yaml`.
