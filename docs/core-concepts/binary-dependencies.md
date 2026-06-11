---
title: "Binary Dependencies"
description: "How forge build resolves, installs, and places skill-declared binaries in the runtime container image."
order: 8
---

## Binary Dependencies

Skills declare the binaries they need (`curl`, `gh`, `kubectl`, …) in their `SKILL.md` frontmatter. `forge build` resolves each one against a layered set of sources, classifies it by install method, and emits the right Dockerfile instructions so the binary lands at a path the runtime can call. This page documents the resolution pipeline and the four ways to add a binary.

For the SKILL.md frontmatter contract itself, see [SKILL.md Format](skill-md-format.md). For the build pipeline that consumes this resolution, see [Architecture](how-forge-works.md).

## Sources, in priority order

The classifier walks four sources for each declared binary and takes the **first hit**. Implemented in `forge-core/packaging/bin_classifier.go`.

| Priority | Source | Where it's declared | Use case |
|---|---|---|---|
| 0 | **Local file override** | `forge build --local-bin <name>=/abs/path` flag, or `package.bin_overrides.<name>.local: /abs/path` in `forge.yaml` | Pinning to an internal build, dev iteration, air-gapped installs |
| 1 | **Skill-local override** | `SKILL.md` frontmatter — set `url:`, `run:`, `apt:`, `apk:` on the bin entry itself | Skill needs a bin not in the registry; install metadata travels with the skill |
| 2 | **`forge.yaml` override** | `package.bin_overrides.<name>` with `apt:`, `apk:`, `url:`, `run:`, `dest:`, `chmod:` | Project-level repinning across all skills (e.g. one internal mirror for `kubectl`) |
| 3 | **Registry lookup** | `forge-skills/registry/image-registry.yaml` — match by binary name | The 70+ pre-vetted bins shipped with `forge` |
| 4 | **Fallback** | None — assumes the apt/apk package name equals the binary name | Emits a build-time warning; works for common Debian package names |

The registry is the embedded YAML compiled into the `forge` binary. It groups bins by category: core CLI tools (`jq`, `curl`, `git`, `tar`, …), cloud CLIs (`kubectl`, `gh`, `aws`, `gcloud`, `az`, `terraform`, …), databases (`psql`, `mysql`, `redis-cli`, …), languages and runtimes (`node`, `go`, `bun`, `deno`, …), networking (`httpie`, `nmap`, `dig`, …), and heavy/companion-image bins (`playwright`, `chromium`, …).

## Install methods

The classifier returns one of six install methods per binary. Each routes through a different Dockerfile slot. See `forge-core/packaging/dockerfile_generator.go` for the emitter.

| Method | Where it runs in the Dockerfile | When the classifier picks it |
|---|---|---|
| `apt` | Application stage: `RUN apt-get install -y --no-install-recommends <pkg>` | Debian/Ubuntu, registry entry has `apt:`, or the fallback heuristic |
| `apk` | Application stage: `RUN apk add --no-cache <pkg>` | Alpine, registry entry has `apk:` |
| `direct-URL` | Bins stage download + per-binary `COPY --from=bins <abs> <abs>` into app stage | Registry entry has `url:`, no `run:` block |
| `custom-run` | Bins stage executes a script of `RUN <line>` directives + per-binary `COPY --from=bins` into app stage | Registry entry has `run:` (multi-step install — tar/unzip/configure) |
| `image-copy` | Companion `FROM <upstream> AS bin-<name>` stage + per-binary `COPY --from=bin-<name>` directly into app stage | Registry entry has `heavy: true` + `image:` (browsers, ML frameworks) |
| `local-file` | Application stage: `COPY .local-bins/<name> <dest>` + `RUN chmod` | Set via `--local-bin` flag or `package.bin_overrides.<name>.local` |

**Why apt installs run in the app stage** (issue #149): apt-installed binaries land at `/usr/bin/` on Debian with transitive deps in `/usr/lib/`, `/etc/`. Routing them through a separate bins stage and copying just `/usr/bin/<name>` would break them — dependent libs and config files wouldn't come along. Running the apt install in the application stage lets apt's own dependency resolution pull everything in correctly.

**Why direct-URL / custom-run / image-copy use the bins stage**: these methods produce static, single-file binaries (or self-contained directories). They can be copied with one per-binary `COPY` and don't need package-manager dependency resolution. Keeping the bins stage scoped to these methods means the application image stays small.

## The four ways to add a binary

### 1. Use an existing registry entry

The fastest path. List the bin name in your SKILL.md frontmatter:

```yaml
---
name: my-skill
metadata:
  forge:
    requires:
      bins:
        - jq              # registry → apt: jq, apk: jq
        - curl
        - kubectl         # registry → direct URL download, pinned version
---
```

Discover what's already in the registry by reading `forge-skills/registry/image-registry.yaml`, or run `forge skills add <skill>` to import a vetted skill that already declares its bins.

### 2. Declare an unknown binary inline in SKILL.md

If the bin you need isn't in the registry, give the classifier enough info inline. The mapping form replaces the scalar form:

```yaml
metadata:
  forge:
    requires:
      bins:
        # apt-installable, package name differs from bin name
        - name: my-cli
          apt: my-cli-debian-package
          apk: my-cli-alpine-package

        # Direct URL download (static binary)
        - name: vault
          url: "https://releases.hashicorp.com/vault/1.18.0/vault_1.18.0_linux_amd64.zip"
          dest: /usr/local/bin/vault
          chmod: "0755"

        # Multi-step install (custom RUN script)
        - name: cosign
          run:
            - "curl -fsSL https://github.com/sigstore/cosign/releases/download/v2.4.0/cosign-linux-amd64 -o /usr/local/bin/cosign"
            - "chmod 0755 /usr/local/bin/cosign"
```

This keeps install metadata with the skill that needs it. Same skill works across projects without a forge.yaml change.

### 3. Override a registry entry at the project level

When every skill in your project should use a different install method for the same bin (internal mirror, pinned version, custom build), put it in `forge.yaml`:

```yaml
package:
  bin_overrides:
    kubectl:                                              # repin to internal mirror
      url: "https://internal-mirror.example.com/kubectl-1.30.5-linux-amd64"
      dest: /usr/local/bin/kubectl
      chmod: "0755"

    redis-cli:                                            # use a specific package version
      run:
        - "apk add --no-cache redis-tools=7.2-r0"

    forge:                                                # point at a locally-built binary
      local: ./bin/forge-linux-amd64
```

A `forge.yaml` override beats the registry but loses to a skill-local override (priority 1). See [`forge.yaml` schema](../reference/forge-yaml-schema.md#package) for the full `package.bin_overrides` field reference.

### 4. Use a local binary file (dev / testing / air-gap)

Quickest iteration loop — no `forge.yaml` edit needed:

```sh
forge build --local-bin forge=/Users/you/go/bin/forge \
            --local-bin my-tool=/tmp/my-tool-linux-amd64
```

The file is copied into `.forge-output/.local-bins/` and the Dockerfile emits a `COPY .local-bins/<name> /usr/local/bin/<name>`. Repeatable for multiple bins. See the [`forge build --local-bin` flag](../reference/cli-reference.md#forge-build) reference.

### Adding to the registry permanently

If you maintain a Forge fork or want to upstream a new bin, edit `forge-skills/registry/image-registry.yaml` and submit a PR. The simplest possible entry just maps to apt/apk package names:

```yaml
bins:
  cosign:
    url: "https://github.com/sigstore/cosign/releases/download/v{{.Version}}/cosign-linux-amd64"
    default_version: "2.4.0"
    chmod: "0755"
```

Available fields:

| Field | Purpose |
|---|---|
| `apt` | Debian/Ubuntu package name (defaults to bin name) |
| `apk` | Alpine package name |
| `url` | Direct download URL — supports `{{.Version}}` template |
| `default_version` | Used when the skill doesn't specify `version:` |
| `dest` | Install path — default `/usr/local/bin/<name>` |
| `chmod` | Permission bits — default `"0755"` |
| `heavy` | When `true`, pull from a companion Docker image instead of apt/url |
| `image` | Companion Docker image template (with `heavy: true`) |
| `requires_ubuntu` | Forces Debian/Ubuntu base image; incompatible with Alpine |
| `requires_first` | Other bins that must install first (e.g. `unzip` before `terraform`) |
| `run` | Custom `RUN` lines — replaces apt/url emission entirely; use for multi-step installs |

## Quick decision tree

```
Need a binary in the container?
│
├─ Is it in image-registry.yaml?
│   └─ Yes → list the name in SKILL.md `requires.bins`. Done.
│
├─ Is it a standard apt/apk package whose name matches?
│   └─ Yes → list the name (fallback handles it; expect a "not found in registry" warning).
│
├─ Is it a static binary from upstream?
│   └─ Use `url:` inline in SKILL.md, or add a registry entry.
│
├─ Does install need multiple steps (tar/unzip/configure)?
│   └─ Use `run:` (custom-run) inline or in registry.
│
├─ Is it heavy / shipped as a Docker image (browser, ML model)?
│   └─ Registry-level `heavy: true` + `image: <upstream-image>`.
│
└─ Pinned internal build / local dev / air-gap?
    └─ `package.bin_overrides.<name>.local:` in forge.yaml, or `forge build --local-bin`.
```

## What ends up in the runtime image

After [PR #150](https://github.com/initializ/forge/pull/150) (issue #149), the generated Dockerfile is intent-explicit per binary:

```dockerfile
# --- Binary installation stages (auto-generated) ---
FROM debian:bookworm-slim AS bins
RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates && rm -rf /var/lib/apt/lists/*
RUN curl -fsSL https://github.com/cli/cli/releases/.../gh_2.60.0_linux_amd64.tar.gz | tar xz -C /tmp
RUN mv /tmp/gh_2.60.0_linux_amd64/bin/gh /usr/local/bin/gh
RUN chmod 0755 /usr/local/bin/gh

# --- Application stage ---
FROM debian:bookworm-slim
WORKDIR /app
COPY --from=bins /usr/local/bin/gh /usr/local/bin/gh     # ← per-binary, not /usr/local/bin/
COPY . .
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl git jq && rm -rf /var/lib/apt/lists/*
# ... forge framework install, EXPOSE, ENTRYPOINT
```

Reading conventions:

- The bins stage's apt install is build-time only — its `curl` and `ca-certificates` never reach the application image. They exist to let the bins stage download direct-URL binaries.
- Each binary the application stage needs has its own `COPY` line (per-binary, not wholesale `/usr/local/bin/`). New bins reaching the app stage land as new `COPY` lines, not hidden inside a directory copy.
- The application stage's apt install line carries both `ca-certificates` (always needed for TLS) and every runtime apt package the agent's skills declared.

See [Docker Deployment](../deployment/docker.md) for the operator-facing build / run / push workflow.

## Cross-references

- [SKILL.md Format § Frontmatter](skill-md-format.md#yaml-frontmatter) — the `metadata.forge.requires.bins` block
- [`forge.yaml` schema § `package`](../reference/forge-yaml-schema.md#package) — `bin_overrides` field reference
- [`forge build` § Flags](../reference/cli-reference.md#forge-build) — `--local-bin` flag
- [Contributing a Skill § Binary dependencies](../skills/contributing-a-skill.md) — when bundling skills with the runtime
- [Writing Custom Skills](../skills/writing-custom-skills.md) — end-to-end skill authoring
- [Docker Deployment](../deployment/docker.md) — how the built image is run

## Source files

- `forge-skills/registry/image-registry.yaml` — the embedded binary registry
- `forge-skills/registry/registry.go` — registry loader
- `forge-core/packaging/bin_classifier.go` — source-priority walker + classifier
- `forge-core/packaging/dockerfile_generator.go` — emits Dockerfile fragments per install method
- `forge-cli/templates/Dockerfile.tmpl` — application-stage template (consumes the fragments)
- `forge-cli/build/dockerfile_stage.go` — wires the generator output into the build pipeline
- `forge-skills/contract/types.go` — `BinRequirement` (the SKILL.md frontmatter shape)
- `forge-core/types/config.go` — `BinOverride` (the `forge.yaml` shape)
