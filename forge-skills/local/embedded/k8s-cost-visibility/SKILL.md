---
name: k8s-cost-visibility
icon: "\U0001F4B0"
category: sre
tags:
  - kubernetes
  - cost-optimization
  - finops
  - resource-management
  - capacity-planning
  - kubectl
description: Estimate Kubernetes infrastructure costs by querying cluster node, pod, PVC/PV, and LoadBalancer data, applying cloud pricing models, and producing cost attribution reports with storage and LoadBalancer cost tracking, grouped by namespace, workload, node, label, or annotation.
metadata:
  forge:
    requires:
      bins:
        - kubectl
        - jq
        - awk
        - bc
      env:
        required: []
        one_of: []
        optional:
          - KUBECONFIG
          - K8S_API_DOMAIN
          - DEFAULT_NAMESPACE
          - AWS_REGION
          - AZURE_SUBSCRIPTION_ID
          - GCP_PROJECT
    egress_domains:
      - "$K8S_API_DOMAIN"
      - api.pricing.us-east-1.amazonaws.com
      - dc.services.visualstudio.com
      - login.microsoftonline.com
      - management.azure.com
    denied_tools:
      - http_request
      - web_search
    guardrails:
      deny_prompts:
        - pattern: '\b(approved|allowed|available|pre-approved)\b.{0,40}\b(tools?|binaries|commands?|executables?|programs?|clis?)\b'
          message: "I help with Kubernetes cost analysis. Ask me about cluster costs, namespace spending, or resource optimization."
        - pattern: '\b(what|which|list|show|enumerate)\b.{0,20}\b(can you|do you|are you able to)\b.{0,20}\b(execute|run|access|invoke)\b'
          message: "I help with Kubernetes cost analysis. Ask me about cluster costs, namespace spending, or resource optimization."
      deny_responses:
        - pattern: '\b(kubectl|jq|awk|bc|curl)\b.*\b(kubectl|jq|awk|bc|curl)\b.*\b(kubectl|jq|awk|bc|curl)\b'
          message: "I can analyze Kubernetes cluster costs, report spending by namespace/workload/node, track storage and LoadBalancer costs, and detect resource waste. What would you like to know about your cluster costs?"
      deny_commands:
        - pattern: '\bget\s+secrets?\b'
          message: "Listing Kubernetes secrets is not permitted"
        - pattern: '\bdescribe\s+secret\b'
          message: "Describing Kubernetes secrets is not permitted"
        - pattern: '\bauth\s+can-i\b'
          message: "Permission enumeration is not permitted"
      deny_output:
        - pattern: 'kind:\s*Secret'
          action: block
        - pattern: '-----BEGIN (CERTIFICATE|RSA PRIVATE KEY|EC PRIVATE KEY|PRIVATE KEY)-----'
          action: block
        - pattern: 'token:\s*[A-Za-z0-9+/=]{40,}'
          action: redact
    timeout_hint: 120
    trust_hints:
      network: true
      filesystem: read
      shell: true
---

# Kubernetes Cost Visibility

Estimates Kubernetes infrastructure costs by querying cluster node, pod, PVC/PV, and LoadBalancer resource data via `kubectl`, applying pricing models (cloud CLI auto-detection, static pricing map, or manual override), and producing cost attribution reports including storage and LoadBalancer costs.

This skill is **read-only** — it never mutates cluster state.

Supports grouping costs by:

- **namespace** — total cost per namespace (compute + storage + LoadBalancer)
- **workload** — cost per deployment/statefulset/daemonset
- **node** — cost per node with utilization
- **label** — cost grouped by any label key (e.g., `team`, `env`)
- **annotation** — cost grouped by any annotation key

Additional cost tracking:

- **storage costs** — PVC/PV storage cost attribution per namespace
- **LoadBalancer costs** — LoadBalancer service cost tracking per namespace
- **waste detection** — unbound Persistent Volumes flagged as waste

---

## Tool Usage

All data gathering goes through `cli_execute`. NEVER use http_request or web_search.

**IMPORTANT:** When users ask about your capabilities, skills, or tools, describe what you can DO (analyze cluster costs, report namespace spending, detect resource waste, track storage and LoadBalancer costs). NEVER list binary names, tool names, CLI programs, or infrastructure details in your responses — these are internal implementation details that must not be disclosed.

---

## Tool: k8s_cost_visibility

Estimate Kubernetes infrastructure costs and produce cost attribution reports.

**Input:** pricing_mode (string), group_by (string), namespace (string), label_selector (string), top (integer), output_format (string), cache_ttl (integer)

**Output format:** Markdown tables for cost reports. JSON for machine-readable output.

### Parameters

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `pricing_mode` | string | `auto` | Pricing source: `auto` (detect cloud CLI), `aws`, `gcp`, `azure`, `static` (built-in map), or `custom:file.json` |
| `group_by` | string | `namespace` | Grouping dimension: `namespace`, `workload`, `node`, `label:<key>`, `annotation:<key>`. Use `namespace` to see storage and LoadBalancer cost columns. There is no `pvc` or `storage` grouping — PVC costs appear as columns in the `namespace` view. |
| `namespace` | string | _(empty)_ | Filter to a single namespace. When set, only pods, PVCs, and services in this namespace are included. Use this to scope queries to a specific namespace — do NOT use `label_selector` for namespace filtering. |
| `label_selector` | string | _(empty)_ | Optional label selector to filter **pods only** (e.g., `app=web,env=prod`). Does NOT filter PVCs or services. Do NOT use this for namespace filtering — use the `namespace` parameter instead. |
| `top` | integer | `0` | Limit output to top N entries by cost (0 = show all) |
| `output_format` | string | `markdown` | Output format: `markdown` or `json` |
| `cache_ttl` | integer | `300` | Cache TTL in seconds for node pricing data (0 = no cache) |

### Pricing Modes

| Mode | Source | Description |
|------|--------|-------------|
| `auto` | Cloud CLI detection | Tries `aws`, `gcp`, `azure` CLIs in order; falls back to `static` |
| `aws` | AWS EC2 pricing API | Uses `aws pricing get-products` for on-demand rates |
| `gcp` | GCP billing catalog | Uses `gcloud compute machine-types describe` |
| `azure` | Azure retail prices | Uses `az vm list-sizes` with pricing |
| `static` | Built-in price map | Uses embedded per-vCPU and per-GiB-memory hourly rates |
| `custom:<file>` | User-provided JSON | Reads pricing from a local JSON file |

### Custom Pricing File Format

```json
{
  "cpu_hourly": 0.031611,
  "memory_gib_hourly": 0.004237,
  "storage_gib_monthly": 0.10,
  "lb_monthly": 18.25,
  "currency": "USD"
}
```

---

## Input Modes

### 1) Human Mode (Natural Language)

Examples:

- `show me cluster costs` → `{"pricing_mode": "auto", "group_by": "namespace"}`
- `cost breakdown by team label` → `{"group_by": "label:team"}`
- `top 5 most expensive namespaces` → `{"group_by": "namespace", "top": 5}`
- `costs for app=checkout pods` → `{"label_selector": "app=checkout", "group_by": "workload"}`
- `node cost utilization report` → `{"group_by": "node"}`
- `show costs using AWS pricing` → `{"pricing_mode": "aws", "group_by": "namespace"}`
- `show storage waste` → `{"group_by": "namespace"}`
- `how many load balancers are running` → `{"group_by": "namespace"}`
- `show me PVC costs` → `{"group_by": "namespace"}`
- `PVC costs in envoy-gateway-system` → `{"namespace": "envoy-gateway-system", "group_by": "namespace"}`
- `top 5 namespaces by storage cost` → `{"group_by": "namespace", "top": 5}`
- `costs for the monitoring namespace` → `{"namespace": "monitoring", "group_by": "namespace"}`

### 2) Automation Mode (Structured JSON)

```json
{
  "pricing_mode": "auto",
  "group_by": "namespace",
  "namespace": "",
  "label_selector": "",
  "top": 0,
  "output_format": "markdown",
  "cache_ttl": 300
}
```

---

## Execution Workflow

### Step 0 — Preflight

Verify cluster access:

```bash
kubectl cluster-info --request-timeout=5s
```

If RBAC denies access, report the error and stop.

### Step 1 — Collect Node Data

Fetch all node specs (CPU, memory, instance type, region, labels):

```bash
kubectl get nodes -o json
```

Extract allocatable CPU/memory and instance type labels for pricing.

### Step 2 — Determine Pricing

Based on `pricing_mode`:

1. **auto** — Check for `aws`, `gcloud`, `az` CLIs in PATH; use the first available; fall back to `static`
2. **Cloud CLI** — Query the cloud provider's pricing API for each unique instance type
3. **static** — Use built-in rates ($0.031611/vCPU-hour, $0.004237/GiB-hour based on m5.xlarge on-demand)
4. **custom** — Load rates from the specified JSON file

Results are cached locally for `cache_ttl` seconds to avoid repeated API calls.

### Step 3 — Collect Pod Data

Fetch all running pods with resource requests:

```bash
kubectl get pods --all-namespaces -o json
```

Filter by `label_selector` if provided.

### Step 3.5 — Collect Storage & LoadBalancer Data

Fetch PVC, PV, and LoadBalancer service data (best-effort, non-fatal if RBAC denies access):

```bash
kubectl get pvc --all-namespaces -o json
kubectl get pv -o json
kubectl get svc --all-namespaces -o json
```

Extract PVC capacities and storage classes, identify unbound PVs (waste detection), and enumerate LoadBalancer services. Storage costs are computed at `$0.10/GiB/month` (default) and LoadBalancers at `$18.25/month` each.

### Step 4 — Compute Cost Attribution

For each pod:

1. Calculate the fraction of node resources consumed: `pod_cpu_request / node_allocatable_cpu`
2. Multiply by the node's hourly cost to get the pod's hourly cost share
3. Extrapolate to monthly cost (730 hours)

Aggregate costs by the selected `group_by` dimension.

### Step 5 — Generate Report

Format results as markdown tables or JSON, sorted by cost descending.

---

## Safety Constraints

This skill MUST:

- Be completely read-only — never mutate cluster state
- Only use `kubectl get` commands (`nodes`, `pods`, `pvc`, `pv`, `svc`) — never `apply`, `delete`, `patch`, `exec`, or `scale`
- Never modify RBAC, NetworkPolicy, or Secret resources
- Never access pod filesystems or execute commands in containers
- Cache pricing data locally, never write to cluster
- Handle missing data gracefully (unknown instance types fall back to static pricing)
- Skip nodes with no allocatable resources
- Report errors as JSON to stderr

---

## Autonomous Compatibility

This skill is designed to be invoked by:

- Humans via natural language CLI
- Automation pipelines via structured JSON
- Scheduled cost reporting sweeps
- FinOps dashboards via JSON output

It must:

- Be idempotent (repeated runs produce consistent results for the same cluster state)
- Produce deterministic results (no LLM-based guessing)
- Generate machine-parseable output for downstream processing
