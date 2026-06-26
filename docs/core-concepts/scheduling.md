---
title: "Scheduling"
description: "Built-in cron scheduler for recurring agent tasks."
order: 8
---

Forge includes a built-in cron scheduler for recurring tasks, configurable in `forge.yaml` or created dynamically by the agent at runtime.

## Configuration

```yaml
schedules:
  - id: daily-report
    cron: "@daily"
    task: "Generate and send the daily status report"
    skill: "tavily-research"           # optional: invoke a specific skill
    channel: telegram                  # optional: deliver results to a channel
    channel_target: "-100123456"       # optional: destination chat/channel ID
```

## Cron Expressions

| Format | Example | Description |
|--------|---------|-------------|
| 5-field standard | `*/15 * * * *` | Every 15 minutes |
| Aliases | `@hourly`, `@daily`, `@weekly`, `@monthly` | Common intervals |
| Intervals | `@every 5m`, `@every 1h30m` | Duration-based (minimum 1 minute) |

## Schedule Tools

The agent has four built-in tools for managing schedules at runtime:

| Tool | Description |
|------|-------------|
| `schedule_set` | Create or update a recurring schedule |
| `schedule_list` | List all active and inactive schedules |
| `schedule_delete` | Remove a schedule (LLM-created only; YAML-defined cannot be deleted) |
| `schedule_history` | View execution history for scheduled tasks |

Schedules can also be managed via the CLI:

```bash
# List all schedules
forge schedule list
```

## Channel Delivery

When a schedule includes `channel` and `channel_target`, the agent's response is automatically delivered to the specified channel after each execution. When schedules are created from channel conversations (Slack, Telegram), the channel context is automatically available so the agent can capture the delivery target.

## Scheduler backend

Forge picks one of two scheduler backends at startup based on the `scheduler` block in `forge.yaml` and whether the process is running inside a Kubernetes pod (issue #162).

| Backend | When used | Persistence | Timing |
|---------|-----------|-------------|--------|
| `file` | Outside a Kubernetes pod | `<WorkDir>/.forge/memory/SCHEDULES.md` | 30s in-process goroutine ticker |
| `kubernetes` | Inside a pod with `scheduler.backend: auto` or `kubernetes` | K8s `CronJob` resources (etcd) | Cluster's CronJob controller |

```yaml
scheduler:
  backend: auto              # auto (default) | file | kubernetes
  kubernetes:
    namespace: ""            # defaults to the agent pod's own namespace
    service_url: ""          # in-cluster URL CronJob trigger pods POST to; auto-derived to http://<agent_id>.<namespace>.svc:<port>/ when empty (issue #179)
    allow_dynamic: false     # whether schedule_set can create CronJobs at runtime
    trigger_image: ""        # default: curlimages/curl:8.10.1
    auth_secret_name: ""     # default: <agent_id>-internal-token
```

`auto` resolves to `kubernetes` when `/var/run/secrets/kubernetes.io/serviceaccount/token` is present; otherwise `file`. The escape hatch `FORGE_IN_CLUSTER=true|false` overrides for testing.

When `forge package` runs with `schedules[]` populated, it emits one `cronjob-<id>.yaml` per entry plus a credential-less Secret template plus a Role/RoleBinding into the `k8s/` output directory — see [Scheduler — Kubernetes](../deployment/scheduler-kubernetes.md) for the full deploy playbook, including the token-provisioning workflow that pairs with [`forge auth secret-yaml`](../reference/cli-reference.md#forge-auth).

## Execution Details

- **File backend tick interval**: 30 seconds. The Kubernetes backend delegates timing to the cluster's CronJob controller — no in-process ticker.
- **Overlap prevention**: File backend skips a fire when the previous run is still in flight. The Kubernetes backend sets `concurrencyPolicy: Forbid` on each CronJob — the K8s-native equivalent.
- **Persistence (file mode)**: `<WorkDir>/.forge/memory/SCHEDULES.md`. LLM-created schedules survive restarts only when this path is mounted (PVC in containers).
- **Persistence (Kubernetes mode)**: CronJob resources in etcd — durable across pod restarts without a PVC.
- **History**: File backend keeps the last 50 executions per schedule. Kubernetes backend defers to the audit stream's `schedule_complete` events.
- **Audit events**: `schedule_fire`, `schedule_complete`, `schedule_skip`, `schedule_modify`.

## Tracing

When tracing is enabled, the **file backend** opens a `schedule.fire` span around each dispatch. The dispatcher's downstream `agent.execute` (and the LLM / tool subtree below it) nest under this span, so an operator filtering on `forge.schedule.id` in their trace browser sees every span the scheduled job produced as one tree instead of an orphaned `agent.execute` root.

| Attribute | Source |
|---|---|
| `forge.schedule.id` | `Schedule.ID` |
| `forge.schedule.cron` | `Schedule.Cron` — e.g. `@hourly`, `*/5 * * * *` |
| `forge.schedule.source` | `yaml` (from `forge.yaml schedules[]`) or `llm` (added at runtime via `schedule_create`) |

Span Status is set to `Error` when the dispatch callback returns an error so error-rate dashboards work uniformly across the Forge span families. See [Observability — Tracing › `schedule.fire`](../core-concepts/observability-tracing.md#schedulefire) for the full hierarchy.

**Kubernetes backend** is **out of scope for v1** — the trigger Pod is a separate curl-based Pod, so plumbing `traceparent` into the dispatch requires injecting it into the rendered CronJob YAML at `forge package` time. Tracked as a follow-up.
