# Scheduling (Cron)

> Part of [Forge Documentation](../README.md)

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

## Execution Details

- **Tick interval**: 30 seconds
- **Overlap prevention**: A schedule won't fire again if its previous run is still in progress
- **Persistence**: Schedules are stored in `.forge/memory/SCHEDULES.md` and survive restarts
- **History**: The last 50 executions are recorded with status, duration, and correlation IDs
- **Audit events**: `schedule_fire`, `schedule_complete`, `schedule_skip`, `schedule_modify`

---
← [Guardrails](security/guardrails.md) | [Back to README](../README.md) | [Hooks](hooks.md) →
