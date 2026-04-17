# Packaging & Deployment

> Part of [Forge Documentation](../README.md)

Forge agents can be packaged as container images and deployed to Docker, Kubernetes, or air-gapped environments.

## Pre-built Docker Image

Forge publishes multi-architecture Docker images (linux/amd64, linux/arm64) to GitHub Container Registry on every release:

```bash
# Pull the latest release
docker pull ghcr.io/initializ/forge:latest

# Pin to a specific version
docker pull ghcr.io/initializ/forge:v1.2.3

# Run with your agent directory mounted
docker run -v /path/to/agent:/home/forge/agent -w /home/forge/agent \
  -e OPENAI_API_KEY=sk-... \
  ghcr.io/initializ/forge:latest run --host 0.0.0.0
```

Tags follow the pattern `v1.2.3`, `v1.2`, `v1`, and `latest`.

The image is built from a multi-stage Dockerfile in the repository root — `golang:1.25-alpine` for the build stage (static binary, `CGO_ENABLED=0`) and `alpine:3.21` for the runtime with `ca-certificates`, `git`, and `tzdata`. The container runs as a non-root `forge` user.

## Building Agent Container Images

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
