---
title: "Observability — Tracing"
description: "OpenTelemetry distributed tracing across A2A → executor → LLM → tool — config, propagation, audit cross-link, and build-time egress."
order: 9
---

OpenTelemetry tracing in Forge is off by default. When enabled, every inbound A2A request becomes one trace whose span tree covers the dispatcher, the agent execution loop, every LLM completion, every tool call, and every outbound HTTP request. Trace context propagates across multi-hop A2A flows, audit events carry the active span's `trace_id` + `span_id`, and the OTLP collector host is auto-allowlisted at build time so deployments need no second egress edit.

> **Status:** shipped as OTel Tracing v1 — initiative tracking issue #108, delivered across phases #101–#107 (PRs #122–#128).

## Quick start

```yaml
# forge.yaml
observability:
  tracing:
    enabled: true
    endpoint: https://otel-collector.monitoring.svc.cluster.local:4318/v1/traces
    sampler: parentbased_always_on
```

Run:

```bash
forge run                    # tracing on, defaults applied
forge build && forge package # collector host auto-added to egress allowlist
kubectl apply -f ...
```

Spans arrive at the collector. The agent's `agent_id` is the `service.name` your trace backend groups by.

## forge.yaml schema

```yaml
observability:
  tracing:
    enabled: true                           # off by default
    endpoint: https://collector:4318/v1/traces
    protocol: http/protobuf                  # or "grpc"
    sampler: parentbased_always_on           # standard OTEL_TRACES_SAMPLER name
    sampler_ratio: 1.0                       # used by *traceidratio* samplers
    timeout: 10s                             # per-request exporter timeout
    service_name: my-agent                   # default: agent_id
    headers:                                  # OTLP request headers (auth tokens etc.)
      x-tenant: demo
    resource_attrs:                          # extra OTel resource attributes
      deployment.environment: prod
    redact: true                             # scrub vendor secret tokens when capture_content is on
    capture_content: false                   # opt-in: stamp prompt/completion/tool I/O on spans
```

| Field | Type | Default | Notes |
|---|---|---|---|
| `enabled` | bool | `false` | Off by default per the initiative ruling. |
| `endpoint` | string | — | Required when `enabled: true`. Empty endpoint collapses to "off." |
| `protocol` | string | `http/protobuf` | Or `grpc`. HTTP is recommended (egress enforcer can wrap it; gRPC bypasses). |
| `sampler` | string | `parentbased_always_on` | Standard `OTEL_TRACES_SAMPLER` names — see below. |
| `sampler_ratio` | float | `1.0` | Only applies to `traceidratio` variants. |
| `timeout` | duration | `10s` | Per-request OTLP exporter timeout. |
| `service_name` | string | `agent_id` | `OTEL_SERVICE_NAME` env wins if set. |
| `headers` | map | — | OTLP HTTP/gRPC headers. Env is the preferred path for secrets. |
| `resource_attrs` | map | — | Merged with the auto-stamped `service.*` + `forge.runtime.version`. |
| `redact` | bool | `true` | When `capture_content: true`, scrub vendor secret tokens (Anthropic / OpenAI / GitHub / AWS / Slack / private keys / Telegram) before stamping content attributes. See [Span content capture](#span-content-capture). |
| `capture_content` | bool | `false` | Stamp prompt / completion / tool I/O as span attributes. Off by default; metadata-only spans ship. See [Span content capture](#span-content-capture). |

## Config precedence

Lowest → highest:

1. Defaults
2. `observability.tracing` block in `forge.yaml`
3. `OTEL_*` environment variables (standard SDK names)
4. CLI flags (`--otel-*`)

A set-but-empty env var does **not** wipe a non-empty yaml field. Absence-of-value is "no override," not "unset."

### Environment variables

| Env var | Maps to |
|---|---|
| `OTEL_SDK_DISABLED` | inverted → `enabled` |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | `endpoint` (preferred — signal-specific) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `endpoint` (generic fallback) |
| `OTEL_EXPORTER_OTLP_PROTOCOL` | `protocol` |
| `OTEL_EXPORTER_OTLP_HEADERS` | `headers` (merged with yaml; env wins on key collision) |
| `OTEL_EXPORTER_OTLP_TIMEOUT` | `timeout` (milliseconds) |
| `OTEL_SERVICE_NAME` | `service_name` |
| `OTEL_RESOURCE_ATTRIBUTES` | `resource_attrs` (merged with yaml) |
| `OTEL_TRACES_SAMPLER` | `sampler` |
| `OTEL_TRACES_SAMPLER_ARG` | `sampler_ratio` |

### CLI flags

Each flag detection uses `cmd.Flags().Changed(...)` rather than zero-value sentinels, because every "zero" is a legitimate explicit ask (`--otel-sampler-ratio 0` = drop everything, `--otel-enabled=false` = force off).

| Flag | Type |
|---|---|
| `--otel-enabled` | bool |
| `--otel-endpoint` | string |
| `--otel-protocol` | string |
| `--otel-sampler` | string |
| `--otel-sampler-ratio` | float |
| `--otel-timeout` | duration |
| `--otel-service-name` | string |
| `--otel-capture-content` | bool |
| `--otel-redact` | bool |

## Samplers

The six standard `OTEL_TRACES_SAMPLER` names — Forge maps them to the OTel SDK directly:

| Name | Behavior |
|---|---|
| `always_on` | Sample everything |
| `always_off` | Drop everything |
| `traceidratio` | Sample at `sampler_ratio` (0.0–1.0) by trace id |
| `parentbased_always_on` (default) | Honor upstream sampled flag; sample everything when no parent |
| `parentbased_always_off` | Honor upstream sampled flag; drop everything when no parent |
| `parentbased_traceidratio` | Honor upstream sampled flag; ratio when no parent |

Name parsing is case-insensitive and whitespace-tolerant. Unknown names error loudly at startup with the offending string named — a typo like `parent_based_always_on` is caught immediately rather than silently falling through to a default.

## Span hierarchy

```
a2a.<method>                          [SpanKindServer; dispatcher]
└── agent.execute                     [outer loop; root for the task]
    ├── llm.completion (× N turns)    [per LLM provider call]
    │   └── http.client (× outbound)  [auto via otelhttp on egress transport]
    └── tool.<tool_name> (× M calls)  [per tool invocation]
        └── http.client (if HTTP)
```

### Attribute conventions

Forge mixes OTel GenAI semconv with Forge-specific `forge.*` namespaced attributes. Backends key dashboards by these:

| Attribute | Where it appears | Source |
|---|---|---|
| `forge.a2a.method` | `a2a.<method>` | JSON-RPC method name |
| `forge.workflow.id` / `.stage.id` / `.step.id` | `a2a.<method>` | FWS-2 `X-Workflow-*` headers |
| `forge.task.id` | `agent.execute` | A2A `params.id` |
| `forge.correlation_id` | `agent.execute` | inbound `X-Forge-Correlation-Id` |
| `forge.loop.iteration` | `agent.execute` (set at End) | turn count |
| `forge.task.final_state` | `agent.execute` (set at End) | `completed` / `failed` / `canceled` |
| `gen_ai.system` | `agent.execute`, `llm.completion` | `"anthropic"`, `"openai"`, `"ollama"` |
| `gen_ai.request.model` / `.response.model` | `llm.completion` | provider request/response model |
| `gen_ai.usage.input_tokens` / `.output_tokens` | `llm.completion` | provider usage block |
| `gen_ai.response.finish_reasons` | `llm.completion` | provider stop reason |
| `forge.tool.name` | `tool.<tool_name>` | tool function name |
| `forge.tool.error` | `tool.<tool_name>` | error message on failure |

Tool errors do **not** fail the outer `agent.execute` span — they surface to the LLM as text and the loop continues. The tool span carries the failure detail so operators can pivot from a trace to the specific failed invocation.

### Span content capture

Prompts, completions, tool args, and tool results are **off by default** — Phase 3 spans ship metadata only (provider, model, usage, finish reasons, tool name). Operators who need content attributes for in-trace debugging or supervised-learning corpora opt in via `observability.tracing.capture_content: true` (Phase 3.5 / issue #130).

| `forge.yaml` knob | Span | Attribute keys added when `capture_content: true` |
|---|---|---|
| (always) | `llm.completion` | `gen_ai.system`, `gen_ai.request.model`, `gen_ai.usage.input_tokens`, `gen_ai.usage.output_tokens`, `gen_ai.response.finish_reasons` |
| `capture_content: true` | `llm.completion` | `gen_ai.prompt` (JSON-serialized inbound messages), `gen_ai.completion` (response text) |
| (always) | `tool.<name>` | `forge.tool.name`, `forge.tool.error` (on failure) |
| `capture_content: true` | `tool.<name>` | `forge.tool.args` (raw arguments JSON), `forge.tool.result` (raw output) |

When `capture_content: true` and `redact: true` (the default when capture is on), attribute values pass through a redactor that scrubs the same vendor secret-token shapes the runtime guardrails default rules cover (Anthropic `sk-ant-…`, OpenAI `sk-…`, GitHub `ghp_/gho_/ghs_/github_pat_…`, AWS `AKIA…`, Slack `xoxb-/xoxp-…`, RSA/EC/OPENSSH/PRIVATE key blocks, Telegram bot tokens). Matched values become `[REDACTED]`. Setting `redact: false` is the enterprise raw-capture path — content is stamped verbatim with the byte cap still applied.

Every captured value is byte-capped at **4 KiB** (below the 5 KiB attribute soft-cap most backends apply). When the input exceeds the cap, the value ends with a `…[truncated:N]` marker where `N` is the original byte length. The marker is **byte-identical** to what the audit payload-capture path emits for the same input, so an operator grepping `[truncated:` across span attributes and audit rows sees aligned output.

**Default posture** (no opt-in): the `gen_ai.prompt`, `gen_ai.completion`, `forge.tool.args`, `forge.tool.result` keys are **absent** from spans — not set to empty string. Backends that gate dashboards on "is this key present?" can distinguish "metadata-only by default" from "operator opted in but the field happened to be empty."

## End-to-end propagation (Phase 5)

Forge installs the W3C `tracecontext + baggage` composite propagator on the OTel global at startup. The JSON-RPC dispatcher extracts inbound `traceparent` + `baggage` headers before opening its own span, so multi-hop A2A flows show as one connected trace:

```
orchestrator
    │  traceparent: 00-T-S1-01
    ▼
┌───────────────┐
│  forge agent  │  span_id=S2, parent=S1   (a2a.tasks/send)
│      ▼        │  span_id=S3, parent=S2   (agent.execute)
│      ▼        │  span_id=S4, parent=S3   (llm.completion)
└──────│────────┘
       ▼  traceparent: 00-T-S2-01  ← otelhttp re-injects on outbound
                                      via the egress-enforced transport
┌───────────────┐
│  downstream   │  span_id=S5, parent=S2   (a2a.tasks/send)
│  forge agent  │
└───────────────┘
```

All five spans share `trace_id = T` and chain by `parent_span_id`. The operator sees **one** connected flame graph.

A malformed inbound `traceparent` returns `ctx` unchanged from the propagator — Forge then starts a fresh root rather than carrying a broken context forward.

`baggage` (the other half of the composite) flows through to the handler ctx so application-level identifiers (tenant id, A/B bucket) travel with the trace.

## Audit ↔ trace cross-link (Phase 4)

Audit events emitted via `EmitFromContext` carry the active span's IDs:

```json
{
  "event": "llm_call",
  "task_id": "t-1234",
  "trace_id": "4a8f95a0e1bedda42c9dd5350fb3b33a",
  "span_id":  "ad8b2c91e44f0a72",
  ...
}
```

- **Pivot audit → trace:** paste the `trace_id` into your backend's search box → land on the matching trace tree. Paste the `span_id` → land directly on the `llm.completion` child carrying matching `gen_ai.usage.*` tokens.
- **Pivot trace → audit:** copy the `trace_id` from Tempo / Jaeger / Honeycomb → grep the audit log for the matching row → get the FWS-8 payload metadata the trace doesn't carry.

Both fields use `omitempty`. When tracing is disabled (the default), audit JSON is byte-identical to the pre-Phase-4 shape — backward-compatible by construction. See [Audit Logging](../security/audit-logging.md#trace-cross-link-fws-105) for full schema details.

## Egress-enforced OTLP transport

Forge wraps the OTLP HTTP exporter's transport with the same egress enforcer every other in-process HTTP client uses. The operator's egress allowlist therefore bounds where Forge can send spans — a misconfigured collector URL cannot exfiltrate span content to an unapproved destination.

**`forge package` auto-injects the collector host into the build's egress allowlist** so the generated NetworkPolicy admits OTLP traffic. The same auto-merge fires at `forge run` time so dev mode matches prod. No second egress edit, no NetworkPolicy patch.

```yaml
# forge.yaml — this is sufficient. The collector is added automatically.
egress:
  mode: allowlist
  allowed_domains:
    - api.anthropic.com    # operator-declared
observability:
  tracing:
    enabled: true
    endpoint: https://otel-collector.monitoring.svc.cluster.local:4318/v1/traces
# → all_domains in egress_allowlist.json = [api.anthropic.com, otel-collector.monitoring.svc.cluster.local]
```

Disabled tracing produces no allowlist entry — turning tracing off in yaml does NOT leave a stale entry punched through the NetworkPolicy.

### HTTP vs gRPC

| Protocol | Egress enforcement |
|---|---|
| `http/protobuf` (default) | Enforced via the in-process `SafeTransport` wrap. Recommended. |
| `grpc` | gRPC exporter dials directly; no in-process wrap. Relies on the build-time allowlist + NetworkPolicy. |

## Disabled-path semantics

When tracing is off (default, or `enabled: false`, or empty `endpoint`):

- `forge-core/runtime.Tracer()` returns the no-op tracer; spans are non-recording and near-zero cost.
- `EmitFromContext` does not stamp `trace_id` / `span_id`; audit JSON is byte-identical to pre-Phase-4.
- `OTelDomain` returns nil; no entry in `egress_allowlist.json`.
- `observability.WrapHTTPTransport` is a near pass-through (noop TracerProvider short-circuits span creation).

**Telemetry failures never crash the agent.** A misconfigured endpoint, a malformed traceparent, an unreachable collector — every failure mode falls through to the noop tracer with a warning in the ops log. The cli's resolver is the single place that fails loudly on bad config at startup.

## Verification

Once configured:

```sh
# stub collector that prints every received span
docker run --rm -p 4318:4318 \
  otel/opentelemetry-collector-contrib:latest \
  --config=/dev/stdin <<'YAML'
receivers:
  otlp:
    protocols:
      http: { endpoint: 0.0.0.0:4318 }
exporters:
  debug: { verbosity: detailed }
service:
  pipelines:
    traces:
      receivers: [otlp]
      exporters: [debug]
YAML

# in another terminal
forge run --otel-enabled \
  --otel-endpoint http://localhost:4318/v1/traces \
  --otel-sampler always_on

# fire a task
curl -H "Authorization: Bearer $(cat .forge/runtime.token)" \
     -H "Content-Type: application/json" \
     -d '{"jsonrpc":"2.0","id":1,"method":"tasks/send",
          "params":{"id":"t-1","message":{"role":"user","parts":[{"kind":"text","text":"hi"}]}}}' \
     http://localhost:8080/
```

The collector should print one trace with `a2a.tasks/send` → `agent.execute` → `llm.completion` (× N) → `tool.<name>` (× M). The `agent.execute` span carries `gen_ai.system`, `forge.task.id`, `forge.task.final_state`. Each `llm.completion` carries `gen_ai.usage.input_tokens` / `output_tokens`. Each audit row in stderr now carries `trace_id` / `span_id` matching the spans.

## Cross-references

- [forge.yaml schema](../reference/forge-yaml-schema.md) — the full schema reference including `observability:`.
- [CLI reference](../reference/cli-reference.md#forge-run) — all `--otel-*` flags on `forge run`.
- [Audit logging](../security/audit-logging.md) — schema including `trace_id` / `span_id`, complementary stream posture vs OTel.
- [Egress control](../security/egress-control.md) — auto-merge mechanics including the OTel collector.
- [Workflow correlation](../security/workflow-correlation.md) — FWS-2 `X-Workflow-*` headers surface on the dispatch span.
- [Deployment monitoring](../deployment/monitoring.md) — operator-level integration with Tempo / Jaeger / Honeycomb.
