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
| `network-policy.yaml` | NetworkPolicy restricting pod egress to allowed domains |
| `egress_allowlist.json` | Machine-readable domain allowlist |
| `checksums.json` | SHA-256 checksums + Ed25519 signature |

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
