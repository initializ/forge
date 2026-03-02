# Forge — Secure, Portable AI Agent Runtime

Build, run, and deploy AI agents from a single `SKILL.md` file.
Secure by default. Runs anywhere — local, container, cloud, air-gapped.

## Why Forge?

- **60-second setup** — `forge init` wizard configures provider, keys, channels, and skills
- **Secure by default** — outbound-only connections, egress allowlists, encrypted secrets, no public listeners
- **Portable** — same agent runs locally, in Docker, Kubernetes, or inside [Initializ Command](https://initializ.ai)
- **Observable** — structured NDJSON audit logs with correlation IDs for every action
- **Extensible** — add skills, tools, channels, and LLM providers without changing core code

## Quick Start

```bash
# Install
brew install initializ/tap/forge          # or download binary from GitHub Releases

# Create and run an agent
forge init my-agent && cd my-agent && forge run

# Connect to Slack
forge run --with slack
```

See [Quick Start](docs/quickstart.md) for the full walkthrough, or [Installation](docs/installation.md) for all methods.

## How It Works

```
SKILL.md --> Parse --> Discover tools/requirements --> Compile AgentSpec
                                                            |
                                                            v
                                                    Apply security policy
                                                            |
                                                            v
                                                    Run LLM agent loop
                                               (tool calling + memory + cron)
```

You write a `SKILL.md`. Forge compiles it into a secure, runnable agent with egress controls, encrypted secrets, and audit logging.

## Key Features

| Feature | Description |
|---------|-------------|
| Atomic Skills | `SKILL.md`-based agent definitions with YAML frontmatter |
| Egress Security | Runtime + build-time domain allowlists with subprocess proxy |
| Channel Connectors | Slack (Socket Mode), Telegram (polling) — outbound-only |
| Cron Scheduling | Recurring tasks with channel delivery |
| Memory | Session persistence + long-term vector search |
| LLM Fallbacks | Multi-provider with automatic failover |
| Web Dashboard | `forge ui` for browser-based agent management |
| Build Signing | Ed25519 artifact signing & verification |
| Air-Gap Ready | Runs with local models, no cloud required |

## Documentation

### Getting Started

| Document | Description |
|----------|-------------|
| [Quick Start](docs/quickstart.md) | Get an agent running in 60 seconds |
| [Installation](docs/installation.md) | Homebrew, binary, and Windows install |
| [Architecture](docs/architecture.md) | System design, module layout, and data flows |

### Core Concepts

| Document | Description |
|----------|-------------|
| [Skills](docs/skills.md) | Skill definitions, registry, and compilation |
| [Tools](docs/tools.md) | Built-in tools, adapters, and custom tools |
| [Runtime](docs/runtime.md) | LLM providers, fallback chains, running modes |
| [Memory](docs/memory.md) | Session persistence and long-term memory |
| [Channels](docs/channels.md) | Slack and Telegram adapter setup |
| [Scheduling](docs/scheduling.md) | Cron configuration and schedule tools |

### Security

| Document | Description |
|----------|-------------|
| [Security Overview](docs/security/overview.md) | Complete security architecture |
| [Egress Security](docs/security/egress.md) | Egress enforcement deep dive |
| [Secrets](docs/security/secrets.md) | Encrypted secret management |
| [Build Signing](docs/security/signing.md) | Ed25519 signing and verification |
| [Guardrails](docs/security/guardrails.md) | Content filtering and PII detection |

### Operations

| Document | Description |
|----------|-------------|
| [Commands](docs/commands.md) | Full CLI reference |
| [Configuration](docs/configuration.md) | `forge.yaml` schema and environment variables |
| [Dashboard](docs/dashboard.md) | Web UI features and architecture |
| [Deployment](docs/deployment.md) | Container packaging, Kubernetes, air-gap |
| [Hooks](docs/hooks.md) | Agent loop hook system |
| [Plugins](docs/plugins.md) | Framework plugin system |
| [Command Integration](docs/command-integration.md) | Initializ Command platform guide |

## Philosophy

Running agents that do real work requires **atomicity** (explicit skills, defined tools, declared dependencies), **security** (restricted egress, encrypted secrets, audit trails), and **portability** (runs locally, in containers, in Kubernetes, in cloud — same agent, anywhere).

> Real agent systems require atomicity, security, and portability. Forge provides those building blocks.

## Contributing

We welcome contributions! See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup, how to add skills/tools/channels, and the PR process.

Please read our [Code of Conduct](CODE_OF_CONDUCT.md) before participating.

## License

See [LICENSE](LICENSE) for details.
