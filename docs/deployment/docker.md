---
title: "Docker"
description: "Package and deploy Forge agents as Docker containers."
order: 1
---

Forge agents can be packaged as container images and deployed to Docker.

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

The Dockerfile's binary install pipeline — what gets pulled from where, how to add a new binary, and what ends up in the runtime image — is documented in [Binary Dependencies](../core-concepts/binary-dependencies.md).

## Docker Compose

```bash
forge package --with-channels
```

This generates a `docker-compose.yaml` with:
- An `agent` service running the A2A server
- Adapter services (e.g., `slack-adapter`, `telegram-adapter`) connecting to the agent
