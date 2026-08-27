# Forge GenAI traces → OpenTelemetry Collector → Datadog (US1)

```
Forge agent ──OTLP/http :4318──▶ otel-collector (contrib + datadog exporter) ──▶ Datadog US1
   (collector host auto-allowlisted at `forge build`)         (DD_API_KEY from Secret)
```

Two ways to run the collector:

- **`helm/` — production daemonset (recommended).** One collector pod per node
  via the official OpenTelemetry Collector Helm chart. Node-local routing,
  k8sattributes enrichment + RBAC, `GOMEMLIMIT` tuning — all handled by the chart.
- **`otel-collector-datadog.yaml` — plain manifests (no Helm).** A single
  Deployment + Service. Simplest to read; not node-local, no autoscaling.

Shared by both: `datadog-secret.yaml` (API-key Secret) and
`forge-tracing.snippet.yaml` (the `forge.yaml` block). The Forge-side config is
**identical** for both — the collector is reachable at the same stable DNS name.

---

## Recommended: Helm daemonset

```bash
# One-shot: creates the namespace + Secret, adds the repo, installs the chart.
DD_API_KEY=<your-datadog-api-key> ./helm/install.sh
```

Or step by step:

```bash
helm repo add open-telemetry https://open-telemetry.github.io/opentelemetry-helm-charts
helm repo update
kubectl create namespace monitoring
kubectl create secret generic datadog-secret -n monitoring \
  --from-literal=DD_API_KEY=<your-datadog-api-key>
helm upgrade --install otel-collector open-telemetry/opentelemetry-collector \
  --namespace monitoring --values helm/values.yaml
kubectl -n monitoring rollout status daemonset/otel-collector
```

Why these values (`helm/values.yaml`):

- **`image.repository: otel/opentelemetry-collector-contrib`** — the Datadog
  exporter and `datadog` connector ship **only** in `-contrib`. The `-k8s` /
  core images crashloop with `unknown type: datadog`. `command.name` is set to
  `otelcol-contrib` to match the contrib binary.
- **`mode: daemonset` + `service.internalTrafficPolicy: Local`** — each agent's
  OTLP goes to the collector pod on its **own node**, while keeping a **stable
  Service DNS name**. That stable name is what makes the daemonset work with
  Forge: `forge build` allowlists the collector **host** for egress, and a
  dynamic node IP (hostPort / `HOST_IP` downward-API) could not be allowlisted
  at build time — it would be blocked by the egress enforcer.
- **`presets.kubernetesAttributes.enabled: true`** — adds the `k8sattributes`
  processor (with a node-local pod filter) plus the ServiceAccount + ClusterRole
  it needs, so every span is tagged with pod / namespace / deployment / node.
- **`datadog/connector`** — computes APM trace stats (hits/errors/latency) so
  the APM UI shows service/resource stats for the gen_ai spans.

## Point Forge at it

Merge `forge-tracing.snippet.yaml` into your agent's `forge.yaml`, then:

```bash
forge build && forge package   # collector host auto-added to egress allowlist + NetworkPolicy
```

Endpoint is `http://otel-collector.monitoring.svc.cluster.local:4318/v1/traces`
for **both** the Helm and plain-manifest paths. `service.name` in Datadog =
your `agent_id`.

## Verify

```bash
kubectl -n monitoring rollout status daemonset/otel-collector
kubectl -n monitoring logs -l app.kubernetes.io/name=opentelemetry-collector | grep -iE 'datadog|Everything is ready'
```

In Datadog: **APM → Traces**, filter `service:<agent_id>`. Spans include
`agent.execute`, `llm.completion`, `tool.<name>`, with `gen_ai.*` tags
(`gen_ai.provider.name`, `gen_ai.request.model`, `gen_ai.usage.input_tokens`,
`gen_ai.tool.name`, `gen_ai.tool.type`, …).

---

## Alternative: plain manifests (no Helm)

```bash
kubectl create secret generic datadog-secret -n monitoring \
  --from-literal=DD_API_KEY=<your-datadog-api-key>   # create ns first if needed
kubectl apply -f otel-collector-datadog.yaml
```

Single Deployment + Service, same collector pipeline. Use this if you don't run
Helm; prefer the daemonset for production.

---

## Production notes / gotchas

- **Tail-based sampling needs a gateway, not a daemonset.** A daemonset pod sees
  only the spans from apps on its node, so it cannot make a whole-trace sampling
  decision. Forge's default is **head** sampling at the source
  (`sampler: parentbased_always_on`), which is correct for a daemonset. If you
  want tail sampling (e.g. keep-on-error), run a **second** collector as a
  `mode: deployment` gateway with the `tail_sampling` processor and point the
  daemonset's exporter at it instead of Datadog.
- **Collector → Datadog egress.** The collector needs outbound 443 to
  `*.datadoghq.com`. This is OUTSIDE Forge's egress control (separate workload).
  On a default-deny-egress cluster, add a NetworkPolicy for the collector.
- **Cross-namespace reachability.** The agent's namespace must reach `monitoring`
  on 4318. Forge's generated NetworkPolicy allows the agent's egress; ensure no
  ingress NetworkPolicy on `monitoring` blocks it.
- **TLS.** The in-cluster hop is plaintext OTLP (`http://…:4318`). For `https://`
  you must terminate TLS on the collector's OTLP receiver and switch the
  forge.yaml endpoint accordingly.
- **Image version.** `helm/values.yaml` pins a placeholder contrib tag — bump it
  to the latest contrib release you have validated.
- **LLM Observability.** These land in Datadog **APM** with `gen_ai.*` as span
  tags. Datadog's native OTel-GenAI → LLM-Observability mapping is still
  evolving; APM span search on `gen_ai.*` works today.
