---
title: "Ship to Production"
description: "The full pipeline: init, skills, secrets, validate, build, package, deploy."
order: 3
---

Once [`forge try`](quick-start.md) has shown you a working agent, this is the
path to a deployable one you own: scaffold it, give it skills and secrets,
validate, build, and package it into an egress-enforced container for your own
cluster. Each step is independently runnable.

## 1. Scaffold

```bash
forge init my-agent
cd my-agent
```

The interactive wizard configures the model provider, validates the API key,
optionally connects a channel (Slack / Telegram), picks skills, and sets the
egress allowlist. For scripted setups, use flags with `--non-interactive`:

```bash
forge init my-agent --model-provider anthropic --non-interactive
```

`forge try --keep` writes the same layout to `./forge-quickstart`, so you can
graduate the demo agent instead of starting from scratch.

## 2. Add skills

Skills are the agent's capabilities. Install from the registry or write your own:

```bash
forge skills add tavily-research        # registry skill
# or author skills/<name>/SKILL.md by hand
```

See [Your First Skill](your-first-skill.md) for the SKILL.md format.

## 3. Configure secrets

Secrets are encrypted at rest (AES-256-GCM), per-agent:

```bash
forge secret set ANTHROPIC_API_KEY sk-...
forge secret set SLACK_BOT_TOKEN xoxb-...
```

## 4. Validate

Catch config, egress, and policy problems before building:

```bash
forge validate
```

## 5. Run locally

```bash
forge run                 # A2A server on :8080
forge serve               # long-running service
forge run --with slack    # attach a channel
```

Tail the audit log while it runs to see tool calls, egress decisions, and
guardrail blocks, the same stream `forge try` renders inline.

## 6. Build

Compile the agent and its dependencies into a runnable artifact. Build-time
egress allowlisting and Ed25519 artifact signing happen here:

```bash
forge build
```

## 7. Package and deploy

Produce an egress-enforced container image and the deployment manifests for your
cluster:

```bash
forge package
```

The generated image enforces the outbound domain allowlist at runtime (including
subprocess HTTP via the local egress proxy), so the deployed agent has the same
network posture you validated locally. Deploy it with your own pipeline.

## Related

- [Quick Start](quick-start.md) — the 60-second `forge try` on-ramp.
- [Your First Skill](your-first-skill.md) — author a SKILL.md.
- [Installation](installation.md) — every install method.
