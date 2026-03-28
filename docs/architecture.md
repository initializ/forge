# Architecture

> Part of [Forge Documentation](../README.md)

Forge is a portable runtime for building and running secure AI agents from simple skill definitions.

## At a Glance

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

1. You write a `SKILL.md` that describes what the agent can do
2. Forge parses the skill definitions and optional YAML frontmatter (binary deps, env vars)
3. The build pipeline discovers tools, resolves egress domains, and compiles an `AgentSpec`
4. Security policies (egress allowlists, capability bundles) are applied
5. Build artifacts are checksummed and optionally signed (Ed25519)
6. At runtime, encrypted secrets are decrypted and the LLM-powered tool-calling loop executes with session persistence, memory, and a cron scheduler for recurring tasks

## Module Architecture

Forge is organized as a Go workspace with five modules:

```
go.work
├── forge-core/       Embeddable library
├── forge-cli/        CLI frontend
├── forge-plugins/    Channel plugin implementations
├── forge-ui/         Local web dashboard
└── forge-skills/     Skill system (registry, parser, compiler)
```

### forge-core — Library

Pure Go library with no CLI dependencies. Provides the compiler, validator, runtime engine, LLM providers, tool/plugin/channel interfaces, A2A protocol types, and security subsystem. External consumers access the library through the `forgecore` package.

### forge-cli — CLI Frontend

Command-line application built on top of forge-core. Includes Cobra commands, build pipeline stages, container builders, framework plugins (CrewAI, LangChain, custom), A2A dev server, and init templates.

### forge-plugins — Channel Plugins

Messaging platform integrations that implement the `channels.ChannelPlugin` interface from forge-core. Ships Slack, Telegram, and markdown formatting plugins.

### forge-ui — Web Dashboard

Local web dashboard for managing agents from the browser. Single Go module embedded into the `forge` binary. See [Dashboard](dashboard.md) for details.

### forge-skills — Skill System

Skill system including the embedded and local skill registries, SKILL.md parser, skill compiler, requirement aggregation, security analyzer, binary/env resolver, and skill signing/verification.

## Package Map

### forge-core

| Package | Responsibility | Key Types |
|---------|---------------|-----------|
| `forgecore` | Public API entry point | `Compile`, `ValidateConfig`, `ValidateAgentSpec`, `NewRuntime` |
| `a2a` | A2A protocol types | `Task`, `Message`, `TaskStatus`, `Part` |
| `agentspec` | AgentSpec definitions and schema validation | `AgentSpec` |
| `channels` | Channel adapter plugin interface | `ChannelPlugin`, `ChannelConfig`, `ChannelEvent`, `EventHandler` |
| `compiler` | AgentSpec compilation and plugin config merging | `CompileRequest`, `CompileResult` |
| `export` | Agent export functionality | — |
| `llm` | LLM client interface and message types | `Client`, `ChatRequest`, `ChatResponse`, `StreamDelta` |
| `llm/providers` | LLM provider implementations | OpenAI, Anthropic, Ollama |
| `pipeline` | Build pipeline context and orchestration | `Pipeline`, `Stage`, `BuildContext` |
| `plugins` | Plugin and framework plugin interfaces | `Plugin`, `FrameworkPlugin`, `AgentConfig`, `FrameworkRegistry` |
| `registry` | Embedded skill registry | — |
| `runtime` | LLM agent loop, executor, hooks, memory, guardrails | `AgentExecutor`, `LLMExecutor`, `ToolExecutor` |
| `schemas` | Embedded JSON schemas | `agentspec.v1.0.schema.json` |
| `security` | Egress allowlist, security policies, network policies | `EgressConfig`, `Resolve`, `GenerateAllowlistJSON` |
| `skills` | Skill parsing, compilation, requirements resolution | `CompiledSkills`, `Compile`, `WriteArtifacts` |
| `tools` | Tool plugin system and executor | `Tool`, `Registry`, `CommandExecutor` |
| `tools/adapters` | Tool adapters | Webhook, MCP, OpenAPI |
| `tools/builtins` | Built-in tools | `http_request`, `json_parse`, `csv_parse`, `datetime_now`, `uuid_generate`, `math_calculate`, `web_search` |
| `types` | ForgeConfig type definitions | `ForgeConfig`, `ModelRef`, `ToolRef` |
| `util` | Utility functions | Slug generation |
| `validate` | Config and schema validation | `ValidationResult`, `ValidateForgeConfig`, `ImportSimResult` |

### forge-cli

| Package | Responsibility | Key Types |
|---------|---------------|-----------|
| `cmd/forge` | Main entry point | — |
| `cmd` | CLI command implementations | `init`, `build`, `run`, `validate`, `package`, `export`, `tool`, `channel`, `skills`, `serve`, `schedule`, `secret`, `key`, `ui` |
| `config` | ForgeConfig loading and YAML parsing | — |
| `build` | Build pipeline stage implementations | `FrameworkAdapterStage`, `AgentSpecStage`, `ToolsStage`, `SkillsStage`, `EgressStage`, etc. |
| `container` | Container image builders | `DockerBuilder`, `PodmanBuilder`, `BuildahBuilder` |
| `plugins` | Framework plugin registry | — |
| `plugins/crewai` | CrewAI framework adapter | — |
| `plugins/langchain` | LangChain framework adapter | — |
| `plugins/custom` | Custom framework plugin | — |
| `runtime` | CLI-specific runtime (subprocess, watchers, stubs, mocks) | — |
| `server` | A2A HTTP server implementation | — |
| `channels` | Channel configuration and routing | — |
| `skills` | Skill file loading and writing | — |
| `tools` | Tool discovery and execution | — |
| `tools/devtools` | Dev-only tools | `local_shell`, `local_file_browser` |
| `templates` | Embedded templates for init wizard | — |

### forge-plugins

| Package | Responsibility |
|---------|---------------|
| `channels` | Channel plugin package root |
| `channels/slack` | Slack channel adapter (Socket Mode) |
| `channels/telegram` | Telegram channel adapter (polling) |
| `channels/markdown` | Markdown formatting helper |

### forge-skills

| Package | Responsibility |
|---------|---------------|
| `contract` | Skill types, registry interface, filtering |
| `local` | Embedded + local skill registries |
| `parser` | SKILL.md parser (frontmatter + body extraction) |
| `compiler` | Skill compiler (prompt generation) |
| `requirements` | Requirement aggregation and derivation |
| `analyzer` | Security audit for skills |
| `resolver` | Binary and env var resolution |
| `trust` | Skill signing and verification |

## Key Interfaces

### `forgecore` Public API

The `forgecore` package exposes the top-level library surface:

```go
func Compile(req CompileRequest) (*CompileResult, error)
func ValidateConfig(cfg *types.ForgeConfig) *validate.ValidationResult
func ValidateAgentSpec(jsonData []byte) ([]string, error)
func ValidateCommandCompat(spec *agentspec.AgentSpec) *validate.ValidationResult
func SimulateImport(spec *agentspec.AgentSpec) *validate.ImportSimResult
func NewRuntime(cfg RuntimeConfig) *runtime.LLMExecutor
```

### `runtime.AgentExecutor`

Core execution interface for running agents. Implemented by `LLMExecutor` in forge-core.

```go
type AgentExecutor interface {
    Execute(ctx context.Context, task *a2a.Task, msg *a2a.Message) (*a2a.Message, error)
    ExecuteStream(ctx context.Context, task *a2a.Task, msg *a2a.Message) (<-chan *a2a.Message, error)
    Close() error
}
```

### `llm.Client`

Provider-agnostic LLM client. Implementations: OpenAI, Anthropic, Ollama (in `llm/providers`).

```go
type Client interface {
    Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error)
    ChatStream(ctx context.Context, req *ChatRequest) (<-chan StreamDelta, error)
    ModelID() string
}
```

### `tools.Tool`

Agent tool with name, schema, and execution. Categories: builtin, adapter, dev, custom.

```go
type Tool interface {
    Name() string
    Description() string
    Category() Category
    InputSchema() json.RawMessage
    Execute(ctx context.Context, args json.RawMessage) (string, error)
}
```

### `runtime.ToolExecutor`

Bridge between the LLM agent loop and the tool registry.

```go
type ToolExecutor interface {
    Execute(ctx context.Context, name string, arguments json.RawMessage) (string, error)
    ToolDefinitions() []llm.ToolDefinition
}
```

### `channels.ChannelPlugin`

Channel adapter for messaging platforms. Implementations: Slack, Telegram (in `forge-plugins/channels`).

```go
type ChannelPlugin interface {
    Name() string
    Init(cfg ChannelConfig) error
    Start(ctx context.Context, handler EventHandler) error
    Stop() error
    NormalizeEvent(raw []byte) (*ChannelEvent, error)
    SendResponse(event *ChannelEvent, response *a2a.Message) error
}
```

### `pipeline.Stage`

Single unit of work in the build pipeline. Receives a `BuildContext` carrying all state.

```go
type Stage interface {
    Name() string
    Execute(ctx context.Context, bc *BuildContext) error
}
```

### `plugins.FrameworkPlugin`

Framework adapter for the build pipeline. Implementations: CrewAI, LangChain, custom (in `forge-cli/plugins`).

```go
type FrameworkPlugin interface {
    Name() string
    DetectProject(dir string) (bool, error)
    ExtractAgentConfig(dir string) (*AgentConfig, error)
    GenerateWrapper(config *AgentConfig) ([]byte, error)
    RuntimeDependencies() []string
}
```

### `container.Builder`

Container image builder. Implementations: `DockerBuilder`, `PodmanBuilder`, `BuildahBuilder` (in `forge-cli/container`).

## Data Flows

### Compilation Flow

```
forge.yaml
  → config.Load()                         [forge-cli/config]
  → types.ForgeConfig                     [forge-core/types]
  → validate.ValidateForgeConfig()        [forge-core/validate]
  → skills.Compile()                      [forge-core/skills]
  → compiler.Compile()                    [forge-core/compiler]
  → agentspec.AgentSpec + SecurityConfig  [forge-core/agentspec, forge-core/security]
```

Or via the public API:

```
forgecore.Compile(CompileRequest) → CompileResult
```

### Build Pipeline Flow

The build pipeline executes stages sequentially. Each stage lives in `forge-cli/build/` and implements `pipeline.Stage` from forge-core.

| # | Stage | Produces |
|---|-------|----------|
| 1 | **FrameworkAdapterStage** | Detects framework (crewai/langchain/custom), extracts agent config, generates A2A wrapper |
| 2 | **AgentSpecStage** | `agent.json` — canonical AgentSpec from ForgeConfig |
| 3 | **ToolsStage** | Tool schema files from discovered and configured tools |
| 4 | **PolicyStage** | `policy-scaffold.json` — guardrail configuration |
| 5 | **DockerfileStage** | `Dockerfile` — container image definition |
| 6 | **K8sStage** | `deployment.yaml`, `service.yaml`, `network-policy.yaml` |
| 7 | **ValidateStage** | Validates all generated artifacts against schemas |
| 8 | **ManifestStage** | `build-manifest.json` — build metadata and file inventory |
| — | **SkillsStage** | `compiled/skills/skills.json` + `compiled/prompt.txt` — compiled skills |
| — | **EgressStage** | `compiled/egress_allowlist.json` — egress domain allowlist |
| — | **ToolFilterStage** | Annotated + filtered tool list (dev tools removed in prod) |

### Runtime Flow

```
AgentSpec + Tools
  → forgecore.NewRuntime(RuntimeConfig)   [forge-core/forgecore]
  → runtime.LLMExecutor                   [forge-core/runtime]
  → llm.Client (provider selection)       [forge-core/llm/providers]
  → Agent loop: prompt → LLM → tool calls → results → LLM → response
  → a2a.Message                           [forge-core/a2a]
```

The CLI orchestrates the full runtime stack:

```
forge run
  → config.Load()                         [forge-cli/config]
  → tools.Discover() + tools.Registry     [forge-cli/tools, forge-core/tools]
  → runtime.LLMExecutor                   [forge-core/runtime]
  → server.A2AServer                      [forge-cli/server]
  → channels.Router (optional)            [forge-cli/channels]
```

## Module Directory Tree

```
forge/
  forge-core/          Core library
    a2a/               A2A protocol types
    llm/               LLM client, fallback chains, OAuth
    memory/            Long-term memory (vector + keyword search)
    runtime/           Agent loop, hooks, compactor, audit logger
    scheduler/         Cron scheduler (parser, tick loop, overlap prevention)
    secrets/           Encrypted secret storage (AES-256-GCM + Argon2id)
    security/          Egress resolver, enforcer, proxy, K8s NetworkPolicy
    tools/             Tool registry, builtins, adapters, skill_tool
    types/             Config types
  forge-cli/           CLI application
    cmd/               CLI commands (init, build, run, serve, schedule, etc.)
    runtime/           Runner, skill registration, scheduler store, subprocess executor
    internal/tui/      Interactive init wizard (Bubbletea)
    tools/             CLI-specific tools (cli_execute, skill executor)
  forge-plugins/       Channel plugins
    telegram/          Telegram adapter (polling, document upload)
    slack/             Slack adapter (Socket Mode, file upload)
    markdown/          Markdown converter, message splitting
  forge-ui/            Local web dashboard
    server.go          HTTP server, routing, CORS
    handlers*.go       REST API (agents, config, wizard, skills)
    process.go         Agent process manager
    discovery.go       Workspace scanner
    sse.go             Real-time event broker
    chat.go            A2A streaming chat proxy
    static/dist/       Embedded SPA (Preact + HTM + Monaco)
  forge-skills/        Skill system
    contract/          Skill types, registry interface, filtering
    local/             Embedded + local skill registries
    parser/            SKILL.md parser (frontmatter + body extraction)
    compiler/          Skill compiler (prompt generation)
    requirements/      Requirement aggregation and derivation
    analyzer/          Security audit for skills
    resolver/          Binary and env var resolution
    trust/             Skill signing and verification
```

## Schema Validation

AgentSpec JSON is validated against `schemas/agentspec.v1.0.schema.json` (JSON Schema draft-07) using the `gojsonschema` library. The schema is embedded in the binary via `go:embed` in `forge-core/schemas/`.

## Egress Security

Egress controls operate at both build time and runtime. Build-time controls generate allowlist artifacts and Kubernetes NetworkPolicy manifests. Runtime controls include:

- **IP Validation** — Rejects non-standard IP formats (octal, hex, packed decimal) and IPv6 transition addresses embedding private IPs
- **SafeDialer** — Validates resolved IPs post-DNS against blocked CIDR ranges before connecting (prevents DNS rebinding)
- **EgressEnforcer** — In-process `http.RoundTripper` backed by `SafeTransport` for domain allowlist enforcement
- **EgressProxy** — Local HTTP/HTTPS forward proxy for subprocess traffic, also backed by `SafeDialer`
- **Redirect credential stripping** — `http_request` and `webhook_call` strip `Authorization`/`Cookie` headers on cross-origin redirects

The A2A server adds:
- **CORS restriction** — Origin allowlist (localhost by default), configurable via flag/env/YAML
- **Security headers** — `X-Content-Type-Options`, `Referrer-Policy`, `X-Frame-Options`, `Content-Security-Policy`
- **Rate limiting** — Per-IP token bucket middleware (read: 60 req/min burst 10, write: 10 req/min burst 3) with 429 responses and `Retry-After` headers; stale visitors evicted automatically
- **Request size limits** — `MaxHeaderBytes` (1 MiB) and `http.MaxBytesReader` (2 MiB) on request bodies; returns 413 on excess

See [Egress Security](security/egress.md) for details.

---
← [Installation](installation.md) | [Back to README](../README.md) | [Skills](skills.md) →
