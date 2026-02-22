# Framework Plugins

## Overview

Forge uses a plugin system to support multiple AI agent frameworks. Each framework plugin adapts a specific framework (CrewAI, LangChain, or custom) to the Forge build pipeline, handling project detection, configuration extraction, and A2A wrapper generation.

## Supported Frameworks

| Framework | Plugin | Languages | Wrapper |
|-----------|--------|-----------|---------|
| CrewAI | `crewai.Plugin` | Python | `crewai_wrapper.py` |
| LangChain | `langchain.Plugin` | Python | `langchain_wrapper.py` |
| Custom | `custom.Plugin` | Python, TypeScript, Go | None (agent is the wrapper) |

## Plugin Interface

Every framework plugin implements `plugins.FrameworkPlugin`:

```go
type FrameworkPlugin interface {
    // Name returns the framework name (e.g. "crewai", "langchain", "custom").
    Name() string

    // DetectProject checks if a directory contains this framework's project.
    DetectProject(dir string) (bool, error)

    // ExtractAgentConfig reads framework-specific files and returns
    // an intermediate AgentConfig representation.
    ExtractAgentConfig(dir string) (*AgentConfig, error)

    // GenerateWrapper produces an A2A wrapper script for the framework.
    // Returns nil if no wrapper is needed.
    GenerateWrapper(config *AgentConfig) ([]byte, error)

    // RuntimeDependencies returns pip/npm packages the framework needs at runtime.
    RuntimeDependencies() []string
}
```

## How Plugins Work in the Build Pipeline

1. **Detection** — The `FrameworkAdapterStage` checks the `framework` field in `forge.yaml`. If set, it looks up the plugin by name. Otherwise, it calls `DetectProject()` on each registered plugin to auto-detect the framework.

2. **Extraction** — `ExtractAgentConfig()` reads framework-specific source files (e.g., CrewAI agent definitions, LangChain chain configurations) and produces an `AgentConfig` struct:

   ```go
   type AgentConfig struct {
       Name        string
       Description string
       Tools       []ToolDefinition
       Identity    *IdentityConfig
       Model       *PluginModelConfig
       Extra       map[string]any
   }
   ```

3. **Wrapper Generation** — `GenerateWrapper()` produces an A2A-compliant HTTP server wrapper that launches the framework agent and translates between A2A protocol messages and framework-native calls.

4. **Output** — The wrapper is written to the build output directory and referenced in the Dockerfile entrypoint.

## Writing a Custom Plugin

To add support for a new framework:

1. Create a new package under `internal/plugins/yourframework/`.

2. Implement the `FrameworkPlugin` interface:

   ```go
   package yourframework

   import "github.com/initializ/forge/internal/plugins"

   type Plugin struct{}

   func (p *Plugin) Name() string { return "yourframework" }

   func (p *Plugin) DetectProject(dir string) (bool, error) {
       // Check for framework-specific files
       // e.g., a config file or specific imports
       return false, nil
   }

   func (p *Plugin) ExtractAgentConfig(dir string) (*plugins.AgentConfig, error) {
       // Parse source files and extract agent configuration
       return &plugins.AgentConfig{
           Name:        "my-agent",
           Description: "Agent built with YourFramework",
       }, nil
   }

   func (p *Plugin) GenerateWrapper(config *plugins.AgentConfig) ([]byte, error) {
       // Generate A2A wrapper code or return nil if not needed
       return nil, nil
   }

   func (p *Plugin) RuntimeDependencies() []string {
       return []string{"yourframework>=1.0"}
   }
   ```

3. Register the plugin in `internal/cmd/build.go`:

   ```go
   reg.Register(&yourframework.Plugin{})
   ```

4. Add a wrapper template in `templates/` if needed.

## Plugin Configuration

The `AgentConfig` struct is the intermediate representation passed between the framework plugin and subsequent build stages:

```go
type AgentConfig struct {
    Name        string              // Agent display name
    Description string              // Agent description
    Tools       []ToolDefinition    // Tools discovered from source
    Identity    *IdentityConfig     // Agent identity (role, goal, backstory)
    Model       *PluginModelConfig  // Model info from source
    Extra       map[string]any      // Framework-specific extra data
}
```

The `FrameworkAdapterStage` stores this in `BuildContext.PluginConfig`, making it available to all subsequent stages.

## Hook System

Forge also has a general-purpose plugin hook system for extending the build lifecycle:

```go
type Plugin interface {
    Name() string
    Version() string
    Init(config map[string]any) error
    Hooks() []HookPoint
    Execute(ctx context.Context, hook HookPoint, data map[string]any) error
}
```

Available hook points: `pre-build`, `post-build`, `pre-push`, `post-push`.
