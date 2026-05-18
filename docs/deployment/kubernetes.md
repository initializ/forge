---
title: "Kubernetes"
description: "Deploy Forge agents to Kubernetes with generated manifests and NetworkPolicy."
order: 2
---

## Kubernetes

Every `forge build` generates container-ready artifacts:

| Artifact | Purpose |
|----------|---------|
| `guardrails.json` | Guardrail policy config (copied from project root if present) |
| `Dockerfile` | Container image with minimal attack surface |
| `deployment.yaml` | Kubernetes Deployment manifest |
| `service.yaml` | Kubernetes Service manifest |
| `secrets.yaml` | Kubernetes Secret with one empty entry per required env var |
| `network-policy.yaml` | NetworkPolicy restricting pod egress to allowed domains |
| `egress_allowlist.json` | Machine-readable domain allowlist |
| `checksums.json` | SHA-256 checksums + Ed25519 signature |

## Env Var Injection

`deployment.yaml` wires each required env var to a `secretKeyRef` against the agent's `<agent_id>-secrets` Secret. The required set is the union of:

- **Skill env vars** — `metadata.forge.requires.env.required` from every `SKILL.md`.
- **Channel env vars** — every `_env`-suffixed setting in each `<channel>-config.yaml` referenced by `channels:` in `forge.yaml`. For example, `bot_token_env: SLACK_BOT_TOKEN` in `slack-config.yaml` adds `SLACK_BOT_TOKEN` to the required set.

The same canonical source feeds `docker-compose.yaml` when `forge package --with-channels` is used, so the two output paths produce a consistent set.

Adding a new channel env var requires zero edits to the build pipeline — append a new `_env`-suffixed setting to the channel YAML and the next `forge build` picks it up. To wire the secret values into the cluster, populate `secrets.yaml` (or replace it with a sealed-secret / ExternalSecret) before applying.

```yaml
# slack-config.yaml — operator adds a per-project override
adapter: slack
settings:
  app_token_env: SLACK_APP_TOKEN
  bot_token_env: SLACK_BOT_TOKEN
  custom_env: MY_PROJECT_SLACK_OVERRIDE   # ← appears in secrets.yaml + deployment.yaml
```

A channel listed in `forge.yaml` whose `<channel>-config.yaml` is missing produces a build warning, not an error — the manifest is generated without that channel's env vars.

## Air-Gap Deployments

Forge can run entirely offline with local models:

1. Use `ollama` as the LLM provider with a locally-hosted model
2. Set egress mode to `deny-all` to block all outbound traffic
3. Pre-install all binary dependencies in the container image
4. Use environment variables for secrets (no passphrase prompting needed)

```yaml
model:
  provider: ollama
  name: llama3
egress:
  mode: deny-all
```
