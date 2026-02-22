# Architecture

## Overview

Forge is a portable runtime for building and running secure AI agents from simple skill definitions. The core data flow is:

```
SKILL.md → Parse → Discover tools/requirements → Compile AgentSpec → Apply security → Run LLM loop
```

Skill definitions and `forge.yaml` configuration are compiled into a canonical `AgentSpec`, security policies are applied, and the resulting agent can be run locally, packaged into a container, or served over the A2A protocol.

## Module Architecture

Forge is organized as a Go workspace with three modules:

```
go.work
├── forge-core/     Embeddable library
├── forge-cli/      CLI frontend
└── forge-plugins/  Channel plugin implementations
```

### forge-core — Library

Pure Go library with no CLI dependencies. Provides the compiler, validator, runtime engine, LLM providers, tool/plugin/channel interfaces, A2A protocol types, and security subsystem. External consumers access the library through the `forgecore` package.

### forge-cli — CLI Frontend

Command-line application built on top of forge-core. Includes Cobra commands, build pipeline stages, container builders, framework plugins (CrewAI, LangChain, custom), A2A dev server, and init templates.

### forge-plugins — Channel Plugins

Messaging platform integrations that implement the `channels.ChannelPlugin` interface from forge-core. Ships Slack, Telegram, and markdown formatting plugins.

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
| `cmd` | CLI command implementations | `init`, `build`, `run`, `validate`, `package`, `export`, `tool`, `channel`, `skills` |
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

## Schema Validation

AgentSpec JSON is validated against `schemas/agentspec.v1.0.schema.json` (JSON Schema draft-07) using the `gojsonschema` library. The schema is embedded in the binary via `go:embed` in `forge-core/schemas/`.

Validation checks include:
- `agent_id` matches pattern `^[a-z0-9-]+$`
- `version` matches semver pattern
- Required fields: `forge_version`, `agent_id`, `version`, `name`
- Nested object schemas for runtime, tools, policy_scaffold, identity, a2a, model

## Template System

Templates use Go's `text/template` package and are embedded via `go:embed` in `forge-cli/templates/`. Templates are used for:

- **Build output** — Dockerfile, Kubernetes manifests
- **Init scaffolding** — forge.yaml, agent entrypoints, tool examples, .gitignore
- **Framework wrappers** — A2A wrappers for CrewAI and LangChain

## Runtime Architecture

The local runner (`forge run`) orchestrates:

1. **Executor selection** — `LLMExecutor` (custom with LLM) lives in forge-core; `SubprocessExecutor`, `MockExecutor`, `StubExecutor` live in `forge-cli/runtime`
2. **A2A server** — JSON-RPC 2.0 HTTP server handling `tasks/send`, `tasks/get`, `tasks/cancel` (in `forge-cli/server`)
3. **Guardrail engine** — Optional inbound/outbound message checking (in `forge-core/runtime`)
4. **Channel adapters** — Optional Slack/Telegram bridges forwarding events to the A2A server (in `forge-plugins/channels`)

## Egress Security

Build-time egress controls generate allowlist artifacts and Kubernetes NetworkPolicy manifests. The resolver in `forge-core/security` combines explicit domains, tool-inferred domains, and capability bundles. See [docs/security-egress.md](security-egress.md) for details.
