# Packaging & Deployment

> Part of [Forge Documentation](../README.md)

Forge agents can be packaged as container images and deployed to Docker, Kubernetes, or air-gapped environments.

## Building Container Images

```bash
# Build a container image (auto-detects Docker/Podman/Buildah)
forge package

# Production build (rejects dev tools and dev-open egress)
forge package --prod

# Build and push to registry
forge package --registry ghcr.io/myorg --push

# Generate docker-compose with channel sidecars
forge package --with-channels

# Export for Initializ Command platform
forge export --pretty --include-schemas
```

`forge package` generates a Dockerfile, Kubernetes manifests, and NetworkPolicy. Use `--prod` to strip dev tools and enforce strict egress. Use `--verify` to smoke-test the built container.

## Production Build Checks

Production builds (`--prod`) enforce:

- No `dev-open` egress mode
- No dev-only tools (`local_shell`, `local_file_browser`)
- Secret provider chain must include `env` (not just `encrypted-file`)
- `.dockerignore` must exist if a Dockerfile is generated

## Docker Compose

```bash
forge package --with-channels
```

This generates a `docker-compose.yaml` with:
- An `agent` service running the A2A server
- Adapter services (e.g., `slack-adapter`, `telegram-adapter`) connecting to the agent

## Kubernetes

Every `forge build` generates container-ready artifacts:

| Artifact | Purpose |
|----------|---------|
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

## Command Platform Export

For Initializ Command integration, export the agent spec:

```bash
# Export with embedded schemas
forge export --pretty --include-schemas

# Simulate Command import
forge export --simulate-import
```

See [Command Integration](command-integration.md) for the full integration guide.

---
← [Dashboard](dashboard.md) | [Back to README](../README.md) | [Plugins](plugins.md) →
