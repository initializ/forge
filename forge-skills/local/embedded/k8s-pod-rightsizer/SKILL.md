---
name: k8s-pod-rightsizer
category: sre
tags:
  - kubernetes
  - rightsizing
  - cost-optimization
  - resource-management
  - prometheus
  - capacity-planning
  - kubectl
description: Analyze Kubernetes workload metrics and produce policy-constrained CPU/memory rightsizing recommendations with optional patch generation and rollback-safe apply.
metadata:
  forge:
    requires:
      bins:
        - bash
        - kubectl
        - jq
        - curl
      env:
        required: []
        one_of: []
        optional:
          - KUBECONFIG
          - K8S_API_DOMAIN
          - PROMETHEUS_URL
          - PROMETHEUS_TOKEN
          - POLICY_FILE
          - DEFAULT_NAMESPACE
    egress_domains:
      - "$K8S_API_DOMAIN"
      - "$PROMETHEUS_URL"
    denied_tools:
      - http_request
      - web_search
    timeout_hint: 300
    trust_hints:
      network: true
      filesystem: read
      shell: true
---

# Kubernetes Pod Rightsizer

Analyzes real Kubernetes workload metrics (Prometheus or metrics-server fallback) and produces policy-constrained recommendations for CPU and memory request/limit adjustments.

Supports three modes:

- **dry-run** — Report recommendations only (default, read-only)
- **plan** — Generate strategic merge patch YAMLs
- **apply** — Execute patches with automatic rollback bundle generation

This skill uses deterministic formulas, never LLM-based guessing.

---

## Tool Usage

This skill uses `cli_execute` with `kubectl` and `curl` commands.
NEVER use http_request or web_search to interact with Kubernetes or Prometheus.
All cluster operations MUST go through kubectl or the rightsizer script via cli_execute.

---

## Applying Patches

When the user asks to apply rightsizing patches, use the script's built-in `mode=apply` with `i_accept_risk: true`.

**NEVER** manually run `kubectl apply -f <file>` — the script's apply mode provides:
- Automatic rollback bundle generation (backup of current specs)
- Strategic merge patches via `kubectl patch`
- Rollout verification after each patch
- Action logging

**Correct workflow:**
1. First run with `mode=dry-run` to show recommendations
2. If user confirms, run with `mode=apply` and `i_accept_risk: true`
3. Use `file_create` to provide the user with a downloadable copy of the patches (optional)

**Example:**
- User: "apply the rightsizing patches" → `{"namespace": "prod", "mode": "apply", "i_accept_risk": true}`

---

## Tool: k8s_pod_rightsizer

Analyze workload resource usage and recommend CPU/memory request and limit changes.

**Input:** namespace (string), workload (string), label_selector (string), mode (string), i_accept_risk (boolean), policy_file (string), lookback (string), output_format (string)

**Output format:** Markdown tables for recommendations. YAML code blocks for patches. JSON for machine-readable output.

### CRITICAL: Mode Field Rules

`mode` controls the **action**, NOT the analysis filter. There are ONLY three valid values:

| mode | Purpose |
|------|---------|
| `dry-run` | Analyze and report recommendations (default) |
| `plan` | Generate patch YAMLs |
| `apply` | Execute patches (requires `i_accept_risk: true`) |

**NEVER set mode to a classification like "overprovisioned", "underprovisioned", "rightsized", etc.** These are OUTPUT classifications the tool produces, not input modes.

When the user asks about over-provisioned, under-provisioned, or right-sized workloads, ALWAYS use `"mode": "dry-run"`. The output will include a `classification` field for each workload (e.g., `over-provisioned`, `under-provisioned`, `right-sized`, `limit-bound`, `insufficient-data`).

Examples:
- "which workloads are over-provisioned?" → `{"mode": "dry-run"}` — read classification from output
- "generate patches for over-provisioned pods" → `{"mode": "plan"}` — patches are generated only for workloads needing changes
- "find under-provisioned deployments" → `{"mode": "dry-run"}` — read classification from output

---

## Input Modes

### 1) Human Mode (Natural Language)

Input is a plain string.

Examples:

- `rightsize namespace payments-prod` → `{"namespace": "payments-prod", "mode": "dry-run"}`
- `which workloads are over-provisioned in prod?` → `{"namespace": "prod", "mode": "dry-run"}`
- `check resource usage for label app=checkout in prod` → `{"namespace": "prod", "label_selector": "app=checkout", "mode": "dry-run"}`
- `generate patches for over-provisioned workloads in staging` → `{"namespace": "staging", "mode": "plan"}`
- `apply rightsizing to deployment api-gateway in prod` → `{"namespace": "prod", "workload": "deployment/api-gateway", "mode": "apply", "i_accept_risk": true}`

Behavior:

- Parse namespace, workload, or selector intent.
- If namespace omitted, use `$DEFAULT_NAMESPACE` if set.
- Default mode is `dry-run`. ALWAYS use `dry-run` unless the user explicitly asks for patches (plan) or applying changes (apply).
- Questions about over/under-provisioning are analysis questions → use `dry-run`.
- Never require the user to remember JSON fields.

---

### 2) Automation Mode (Structured JSON)

Input JSON schema:

```json
{
  "namespace": "payments-prod",
  "workload": "deployment/payments-api",
  "label_selector": "",
  "mode": "dry-run",
  "i_accept_risk": false,
  "policy_file": "",
  "lookback": "24h",
  "output_format": "markdown"
}
```

Rules:

- `namespace` is required (or `$DEFAULT_NAMESPACE` must be set).
- `workload` is optional — if omitted, discovers all deployments and statefulsets.
- `label_selector` is optional — filters discovered workloads.
- `mode` must be one of: `dry-run`, `plan`, `apply`.
- `i_accept_risk` must be `true` for `apply` mode.
- `output_format`: `markdown` (default), `json`, or `yaml`.

---

## Execution Workflow

### Step 0 — Preconditions

Verify cluster access:

```bash
kubectl cluster-info --request-timeout=5s
```

If RBAC denies access, report the error and stop.

Check Prometheus availability if `$PROMETHEUS_URL` is set:

```bash
curl -s "$PROMETHEUS_URL/api/v1/status/buildinfo"
```

Fall back to metrics-server if Prometheus is unavailable.

---

### Step 1 — Discover Workloads

If a specific workload is provided, validate it exists:

```bash
kubectl get <kind> <name> -n <namespace> -o json
```

Otherwise, discover all deployments and statefulsets:

```bash
kubectl get deploy,sts -n <namespace> -o json
```

Filter by `label_selector` if provided. Skip `kube-system` unless explicitly targeted. Extract container resource specs for each workload.

---

### Step 2 — Collect Metrics

**Prometheus (preferred):**

Query p95 CPU and memory usage over the lookback window:

```promql
quantile_over_time(0.95, rate(container_cpu_usage_seconds_total{namespace="NS",pod=~"WORKLOAD.*",container!="POD"}[5m])[LOOKBACK:1m])
```

```promql
quantile_over_time(0.95, container_memory_working_set_bytes{namespace="NS",pod=~"WORKLOAD.*",container!="POD"}[LOOKBACK])
```

Also collect throttle ratios and OOM kill counts.

**Metrics-server fallback:**

```bash
kubectl top pod -n <namespace> --containers
```

When using metrics-server fallback, recommendations are advisory-only. Apply mode is blocked.

---

### Step 3 — Compute Recommendations

All computations use deterministic formulas:

- **Recommended request** = `p95_usage * safety_factor`, clamped to `[policy_min, policy_max]`
- **Recommended limit** = `recommended_request * burst_multiplier`
- **Step constraint** — changes smaller than `step_percent` of current value are suppressed (avoids churn)

CPU values are rounded to nearest 10m. Memory values are rounded to nearest MiB.

---

### Step 4 — Generate Report

Output format depends on `output_format` parameter:

- **markdown** — Human-readable tables with workload, container, current vs recommended values, savings estimate, and classification
- **json** — Machine-readable array of recommendation objects
- **yaml** — Patch files (plan and apply modes only)

---

### Step 5 — Apply (if mode=apply)

1. Generate rollback bundle (backup of current resource specs)
2. Show diff preview of all patches
3. Apply strategic merge patches via `kubectl patch`
4. Verify rollout status after each patch
5. Log all actions to `run.log` in the rollback bundle

---

## Policy Model

Policy files define constraints for rightsizing recommendations. Use `$POLICY_FILE` or `--policy-file` to specify.

### Example Policy

```json
{
  "defaults": {
    "cpu_safety_factor": 1.25,
    "memory_safety_factor": 1.35,
    "cpu_burst_multiplier": 2.0,
    "memory_burst_multiplier": 1.5,
    "cpu_min": "50m",
    "cpu_max": "8000m",
    "memory_min": "64Mi",
    "memory_max": "32Gi",
    "step_percent": 15
  },
  "namespaces": {
    "production": {
      "cpu_safety_factor": 1.4,
      "memory_safety_factor": 1.5,
      "step_percent": 20
    }
  },
  "workloads": {
    "production/payments-api": {
      "cpu_min": "500m",
      "memory_min": "512Mi"
    }
  }
}
```

### Field Reference

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `cpu_safety_factor` | float | 1.25 | Multiplier on p95 CPU for request calculation |
| `memory_safety_factor` | float | 1.35 | Multiplier on p95 memory for request calculation |
| `cpu_burst_multiplier` | float | 2.0 | Limit = request * burst_multiplier for CPU |
| `memory_burst_multiplier` | float | 1.5 | Limit = request * burst_multiplier for memory |
| `cpu_min` | string | 10m | Floor for CPU request recommendations |
| `cpu_max` | string | 8000m | Ceiling for CPU request recommendations |
| `memory_min` | string | 32Mi | Floor for memory request recommendations |
| `memory_max` | string | 32Gi | Ceiling for memory request recommendations |
| `step_percent` | int | 15 | Minimum change percentage to trigger a recommendation |

### Precedence

Policy values resolve in 3 levels (highest priority first):

1. **Workload override** — `workloads["namespace/name"]`
2. **Namespace override** — `namespaces["namespace"]`
3. **Defaults** — `defaults`

Values merge via overlay: workload overrides namespace, which overrides defaults.

---

## Metrics Strategy

### Prometheus (Preferred)

When `$PROMETHEUS_URL` is set, the skill queries Prometheus for high-fidelity metrics:

| Metric | PromQL Pattern |
|--------|---------------|
| p95 CPU | `quantile_over_time(0.95, rate(container_cpu_usage_seconds_total{...}[5m])[LOOKBACK:1m])` |
| p95 Memory | `quantile_over_time(0.95, container_memory_working_set_bytes{...}[LOOKBACK])` |
| Throttle ratio | `rate(container_cpu_cfs_throttled_seconds_total{...}[LOOKBACK]) / rate(container_cpu_cfs_periods_total{...}[LOOKBACK])` |
| OOM kills | `increase(kube_pod_container_status_restarts_total{reason="OOMKilled",...}[LOOKBACK])` |

Authentication via `$PROMETHEUS_TOKEN` (Bearer token) if set.

### Metrics-Server Fallback

When Prometheus is unavailable, falls back to:

```bash
kubectl top pod -n <namespace> --containers
```

Limitations:

- Point-in-time snapshot only (no percentile data)
- Recommendations are advisory-only
- Apply mode is blocked
- Step constraint is doubled (30% minimum change)

---

## Decision Engine

All computations are deterministic and performed via `jq` arithmetic.

### Request Calculation

```
raw_request = p95_usage * safety_factor
clamped_request = clamp(raw_request, policy_min, policy_max)
recommended_request = round(clamped_request)
```

### Limit Calculation

```
recommended_limit = recommended_request * burst_multiplier
clamped_limit = clamp(recommended_limit, recommended_request, policy_max)
```

### Step Constraint

A recommendation is only emitted if:

```
abs(recommended - current) / current >= step_percent / 100
```

This prevents churn from minor fluctuations.

### Rounding

- CPU: rounded to nearest 10m (e.g., 137m → 140m)
- Memory: rounded to nearest MiB (e.g., 127.3Mi → 128Mi)

---

## Detection Heuristics

Each container is classified into one of these patterns:

| Pattern | Condition |
|---------|-----------|
| **Over-provisioned CPU** | CPU request > p95 CPU * safety_factor * 2 |
| **Under-provisioned CPU** | CPU request < p95 CPU * 0.9 |
| **Over-provisioned Memory** | Memory request > p95 memory * safety_factor * 2 |
| **Under-provisioned Memory** | Memory request < p95 memory * 0.9 |
| **Limit-bound (throttled)** | Throttle ratio > 0.1 or OOM kills > 0 |
| **Right-sized** | Within step_percent of recommended values |
| **Insufficient data** | Fewer than 10 data points in lookback window |

---

## Output Formats

### Markdown Report (default)

```markdown
| Workload | Container | Resource | Current | Recommended | Change | Classification |
|----------|-----------|----------|---------|-------------|--------|----------------|
| deploy/api | app | CPU req | 1000m | 400m | -60% | Over-provisioned |
| deploy/api | app | CPU lim | 2000m | 800m | -60% | Over-provisioned |
| deploy/api | app | Mem req | 2Gi | 1Gi | -50% | Over-provisioned |
| deploy/api | app | Mem lim | 4Gi | 1536Mi | -63% | Over-provisioned |
```

### JSON Output

```json
[
  {
    "workload": "deployment/api",
    "container": "app",
    "cpu_request": {"current": "1000m", "recommended": "400m", "change_percent": -60},
    "cpu_limit": {"current": "2000m", "recommended": "800m", "change_percent": -60},
    "memory_request": {"current": "2Gi", "recommended": "1Gi", "change_percent": -50},
    "memory_limit": {"current": "4Gi", "recommended": "1536Mi", "change_percent": -63},
    "classification": "over-provisioned"
  }
]
```

### Patch YAMLs (plan/apply modes)

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: payments-prod
spec:
  template:
    spec:
      containers:
        - name: app
          resources:
            requests:
              cpu: "400m"
              memory: "1Gi"
            limits:
              cpu: "800m"
              memory: "1536Mi"
```

---

## Rollback

When `mode=apply`, a rollback bundle is generated before any patches are applied:

```
rollback-<timestamp>/
  backup-<workload>.json    # Current resource specs
  patch-<workload>.json     # Applied patches
  rollback-<workload>.sh    # kubectl patch commands to restore
  run.log                   # Timestamped action log
```

To roll back:

```bash
bash rollback-<timestamp>/rollback-<workload>.sh
```

---

## Safety Constraints

This skill MUST:

- Default to `dry-run` mode — never mutate without explicit mode selection.
- Require `i_accept_risk: true` for `apply` mode.
- Generate rollback bundles before applying any patch.
- Never delete workloads, pods, namespaces, or any Kubernetes resource.
- Never modify RBAC, NetworkPolicy, or Secret resources.
- Never scale replicas.
- Only patch `spec.template.spec.containers[].resources`.
- Block `apply` mode when using metrics-server fallback (insufficient data fidelity).
- Validate all policy values before use.
- Cap lookback window at 30 days.
- Skip `kube-system` namespace unless explicitly targeted.
- Respect step constraints to avoid recommendation churn.
- Log all mutations to the rollback bundle run.log.

---

## Autonomous Compatibility

This skill is designed to be invoked by:

- Humans via natural language CLI
- Automation pipelines via structured JSON
- Scheduled cost-optimization sweeps

It must:

- Be idempotent (repeated runs produce the same recommendations for the same data)
- Produce deterministic results (no LLM-based guessing)
- Be scope-limited (operates only on specified namespace/workload)
- Generate machine-parseable output for downstream processing
