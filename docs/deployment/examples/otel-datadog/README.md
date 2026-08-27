# Forge GenAI traces → OpenTelemetry Collector → Datadog (US1)

```
Forge agent ──OTLP/http :4318──▶ otel-collector (contrib + datadog exporter) ──▶ Datadog US1
   (collector host auto-allowlisted at `forge build`)      (DD_API_KEY from Secret)
```

Files:
- `otel-collector-datadog.yaml` — namespace `monitoring`, collector ConfigMap, Deployment, Service.
- `datadog-secret.yaml` — template only; prefer the imperative `kubectl create secret` below.
- `forge-tracing.snippet.yaml` — the `observability.tracing` block for your agent's `forge.yaml`.

## 1. Create the Datadog API-key Secret

```bash
kubectl create secret generic datadog-secret \
  --namespace monitoring \
  --from-literal=DD_API_KEY=<your-datadog-api-key>
```
(The `monitoring` namespace is created by the main manifest; if applying the
Secret first, `kubectl create namespace monitoring` beforehand.)

## 2. Deploy the collector

```bash
kubectl apply -f otel-collector-datadog.yaml
kubectl -n monitoring rollout status deploy/otel-collector
```

The Service resolves to `otel-collector.monitoring.svc.cluster.local` — the
hostname Forge's docs default to.

## 3. Point Forge at it

Merge `forge-tracing.snippet.yaml` into your agent's `forge.yaml`, then:

```bash
forge build && forge package   # collector host auto-added to egress allowlist + NetworkPolicy
```

Forge wraps the OTLP exporter in its egress enforcer, so the collector host is
allowlisted automatically — no manual egress edit. `service.name` in Datadog =
your `agent_id`.

## 4. Verify

```bash
# Collector healthy + datadog exporter started
kubectl -n monitoring logs deploy/otel-collector | grep -iE 'datadog|everything is ready'

# See spans flow (temporary): set exporters.debug verbosity: detailed and add
# `debug` to the traces/export pipeline, re-apply, then:
kubectl -n monitoring logs -f deploy/otel-collector | grep -iE 'gen_ai|llm.completion|tool\.'
```

In Datadog: **APM → Traces**, filter `service:<agent_id>`. You should see traces
whose spans include `agent.execute`, `llm.completion`, and `tool.<name>`, with
`gen_ai.*` attributes as span tags (`gen_ai.provider.name`, `gen_ai.request.model`,
`gen_ai.usage.input_tokens`, `gen_ai.tool.name`, `gen_ai.tool.type`, …).

## Notes / gotchas

- **TLS.** The in-cluster hop is plaintext OTLP (`http://…:4318`). Forge's docs
  show an `https://` example — that requires terminating TLS on the collector
  receiver (`tls:` under `otlp.protocols.http`) and switching the forge.yaml
  endpoint to `https://`. Plaintext in-cluster is fine and is the default here;
  bound it with a NetworkPolicy / service mesh if your posture needs it.
- **Collector → Datadog egress.** The collector pod needs outbound 443 to
  Datadog's intake (`*.datadoghq.com`). This is OUTSIDE Forge's egress control
  (separate deployment). If your cluster has default-deny egress NetworkPolicies,
  add one allowing the collector egress to 443 (DNS + `api.datadoghq.com`).
- **Cross-namespace reachability.** The agent (its namespace) must be able to
  reach the `monitoring` namespace on 4318. Forge's generated NetworkPolicy
  allows the agent's egress; make sure no ingress NetworkPolicy on `monitoring`
  blocks it.
- **Image version.** `otel/opentelemetry-collector-contrib:0.119.0` is a
  placeholder — pin to the latest contrib release you've validated.
- **APM stats / connector.** The `datadog/connector` produces APM trace metrics
  (hits/errors/latency). Remove it and simplify to `otlp → batch → datadog` if
  you only want raw traces without APM service stats.
- **LLM Observability.** These land in Datadog **APM** with `gen_ai.*` as span
  tags. Datadog's LLM Observability product ingests via its own SDK/OTel mapping
  that is still evolving — APM span search on `gen_ai.*` works today; native
  LLM-Obs surfacing may need additional Datadog-side config.
- **k8sattributes (optional).** To tag spans with pod/deployment/namespace, add
  the `k8sattributes` processor — it needs a ServiceAccount + ClusterRole/Binding
  (get/list/watch on pods, replicasets). Left out here to keep the deploy RBAC-free.
