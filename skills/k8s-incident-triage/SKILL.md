---
name: k8s-incident-triage
category: sre
tags:
  - kubernetes
  - incident-response
  - triage
  - reliability
  - observability
  - kubectl
  - oncall
  - runbooks
description: Read-only Kubernetes incident triage using kubectl. Accepts natural language or structured input. Produces root-cause hypotheses, evidence, and next-step commands.
metadata:
  forge:
    requires:
      bins:
        - kubectl
      env:
        required: []
        one_of: []
        optional:
          - KUBECONFIG
          - K8S_API_DOMAIN
          - DEFAULT_NAMESPACE
          - TRIAGE_MAX_PODS
          - TRIAGE_LOG_LINES
    egress_domains:
      - "$K8S_API_DOMAIN"
    denied_tools:
      - http_request
      - web_search
---

# Kubernetes Incident Triage

Performs read-only triage of Kubernetes workloads and namespaces using `kubectl`.

Supports:
- Natural language input (human mode)
- Structured JSON input (automation mode)

This skill NEVER mutates cluster state.

---

## Tool Usage

This skill uses `cli_execute` with `kubectl` commands exclusively.
NEVER use http_request or web_search to interact with Kubernetes.
All cluster operations MUST go through kubectl via the cli_execute tool.

---

## Tool: k8s_triage

Diagnose unhealthy Kubernetes workloads, pods, or namespaces.

---

## Input Modes

### 1) Human Mode (Natural Language)

Input is a plain string.

Examples:

- `triage payments-prod`
- `triage deployment payments-api in payments-prod`
- `why are pods pending in checkout-prod?`
- `investigate crashloop in payments-prod`
- `triage pod api-7c9f6d7f86-abcde in payments-prod`
- `check rollout of deployment payments-api in prod`

Behavior:

- Parse namespace, workload, pod, or selector intent.
- If namespace omitted, use `$DEFAULT_NAMESPACE` if set.
- If ambiguity exists, default to namespace-level triage.
- Never require the user to remember JSON fields.

---

### 2) Automation Mode (Structured JSON)

Input JSON schema:

{
  "namespace": "payments-prod",
  "workload_kind": "deployment",
  "workload_name": "payments-api",
  "pod_name": null,
  "label_selector": null,
  "include_logs": true,
  "logs_tail_lines": 200,
  "include_previous_logs": true,
  "events_limit": 50,
  "include_node_diagnostics": true,
  "include_metrics": false,
  "output_format": "markdown"
}

Rules:

- `namespace` is required.
- If `pod_name` provided → pod-level triage.
- If workload fields provided → workload-level triage.
- Else → namespace scan.

---

## Triage Process

### Step 0 — Preconditions

Verify cluster access:

kubectl version --client
kubectl cluster-info

If RBAC denies access:

- Continue with allowed operations.
- Explicitly report denied commands in output.

---

### Step 1 — Fast Health Snapshot

Namespace scope:

kubectl get pods -n <ns> -o wide
kubectl get deploy,sts,ds,job,cronjob -n <ns>

Workload scope:

kubectl get <kind> <name> -n <ns>
kubectl rollout status <kind>/<name> -n <ns> --timeout=10s

Select pods in states:

- CrashLoopBackOff
- ImagePullBackOff
- ErrImagePull
- Pending / Unschedulable
- Error
- NotReady
- OOMKilled
- High restart count

Limit deep triage to `$TRIAGE_MAX_PODS` (default 5).

---

### Step 2 — Events Timeline

kubectl get events -n <ns> --sort-by=.lastTimestamp | tail -n <events_limit>

Look for:

- FailedScheduling
- FailedMount
- Unhealthy (probe failures)
- Back-off pulling image
- Evicted
- Node pressure signals

---

### Step 3 — Describe Pods & Workloads

For each selected pod:

kubectl describe pod <pod> -n <ns>

Capture:

- Container state and reason
- Restart count
- Last termination reason
- Probe failures
- Volume mount issues
- Node assignment
- Taints / tolerations
- Affinity constraints

If workload-level triage:

kubectl describe <kind> <name> -n <ns>

---

### Step 4 — Node Diagnostics (Optional)

kubectl get nodes -o wide
kubectl describe node <node>

Check for:

- NotReady
- MemoryPressure
- DiskPressure
- PIDPressure
- Evictions

---

### Step 5 — Logs (Optional)

kubectl logs <pod> -n <ns> --all-containers --tail=<N>

If restart loops and include_previous_logs=true:

kubectl logs <pod> -n <ns> --previous --all-containers --tail=<N>

Rules:

- Prefer previous logs for CrashLoopBackOff.
- Redact obvious sensitive patterns.
- Never print Secret values.

---

### Step 6 — Optional Metrics

If enabled:

kubectl top pods -n <ns>
kubectl top node

Gracefully skip if metrics-server is unavailable.

---

## Detection Heuristics

Classify detected issues into:

- CrashLoop / Application Crash
- OOMKilled / Resource Limits
- Image Pull Failure
- Scheduling Constraint
- Probe Failure
- PVC / Volume Failure
- Node Pressure / Eviction
- Rollout Stuck
- Unknown / Escalation Needed

For each issue provide:

- Hypothesis
- Supporting evidence
- Confidence score (0.0–1.0)
- Recommended next commands

---

## Output Structure

### 1) Incident Summary
- Namespace
- Scope (pod/workload/namespace)
- Affected resources count
- Timestamp

### 2) Top Findings
Concise bullet summary.

### 3) Likely Root Causes (Top 3)
For each:
- Description
- Evidence excerpt
- Confidence
- Recommended actions

### 4) Next Commands
Copy-paste kubectl commands.

### 5) Evidence Appendix
- Events (recent)
- Describe excerpts
- Log excerpts

---

## Safety Constraints

This skill MUST:

- Perform read-only kubectl operations only.
- Never execute:
  - apply
  - patch
  - delete
  - exec
  - port-forward
  - scale
  - rollout restart
- Never print Secret values.
- Avoid dumping full environment variables.

---

## Autonomous Compatibility

This skill is designed to be invoked by:

- k8s_alert_handler (alert-triggered triage)
- k8s_patrol (scheduled patrol scans)
- Humans via natural language CLI

It must:

- Be idempotent
- Produce deterministic fingerprints
- Avoid excessive cluster load
- Limit deep triage scope
