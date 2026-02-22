# Command Platform Integration Guide

## Overview

forge-core (`github.com/initializ/forge/forge-core`) is a pure Go library that Command imports to compile, validate, and run Forge agents. It has zero CLI, Docker, or Kubernetes dependencies.

## Shared Runtime Base Image Pattern

Unlike `forge build` which generates per-agent Dockerfiles with language-specific base images, Command uses a **shared runtime base image**:

1. **No per-agent container builds**: Command does not run `forge build` or generate Dockerfiles. Instead, it imports agents via their AgentSpec JSON.

2. **Shared base image**: Command maintains a single runtime base image that includes the Forge agent runtime, common language runtimes (Python, Node.js, Go), and the A2A server. Agents are loaded as configuration, not baked into containers.

3. **Agent loading flow**:
   ```
   AgentSpec JSON → forgecore.Compile() → Runtime configuration
                  → forgecore.NewRuntime() → LLM executor with injected tools
   ```

4. **Why this matters**: Per-agent containers add build time, registry storage, and deployment complexity. The shared base image approach means new agents can be deployed in seconds by loading their spec, rather than minutes of container building.

## Importing forge-core

```go
import (
    forgecore "github.com/initializ/forge/forge-core"
    "github.com/initializ/forge/forge-core/types"
    "github.com/initializ/forge/forge-core/agentspec"
    "github.com/initializ/forge/forge-core/skills"
    "github.com/initializ/forge/forge-core/llm"
    "github.com/initializ/forge/forge-core/runtime"
    "github.com/initializ/forge/forge-core/security"
    "github.com/initializ/forge/forge-core/tools"
    "github.com/initializ/forge/forge-core/validate"
)
```

## Compile API

Compile transforms a `ForgeConfig` into a fully-resolved `AgentSpec`:

```go
result, err := forgecore.Compile(forgecore.CompileRequest{
    Config:       cfg,           // *types.ForgeConfig
    PluginConfig: pluginCfg,     // optional *plugins.AgentConfig
    SkillEntries: skillEntries,  // optional []skills.SkillEntry
})
// result.Spec           — *agentspec.AgentSpec
// result.CompiledSkills — *skills.CompiledSkills (nil if no skills)
// result.EgressConfig   — *security.EgressConfig
// result.Allowlist      — []byte (JSON)
```

## Validate API

```go
// Validate forge.yaml config
valResult := forgecore.ValidateConfig(cfg)
if !valResult.IsValid() {
    // handle errors
}

// Validate agent.json against JSON schema
schemaErrs, err := forgecore.ValidateAgentSpec(jsonData)

// Check Command platform compatibility
compatResult := forgecore.ValidateCommandCompat(spec)

// Simulate what Command's import API would produce
simResult := forgecore.SimulateImport(spec)
```

## Runtime API

Create an LLM executor with full dependency injection:

```go
executor := forgecore.NewRuntime(forgecore.RuntimeConfig{
    LLMClient:     myLLMClient,    // llm.Client interface
    Tools:         toolRegistry,   // runtime.ToolExecutor interface
    Hooks:         hookRegistry,   // *runtime.HookRegistry (optional)
    SystemPrompt:  "You are ...",
    MaxIterations: 10,
    Guardrails:    guardrailEngine, // *runtime.GuardrailEngine (optional)
    Logger:        logger,          // runtime.Logger (optional)
})

// Execute a task
resp, err := executor.Execute(ctx, task, message)
```

## Override Patterns

### Model Override

Command controls which LLM provider and model each agent uses:

```go
// Create your own LLM client with desired provider/model
client, _ := providers.NewClient("anthropic", llm.ClientConfig{
    APIKey: os.Getenv("ANTHROPIC_API_KEY"),
    Model:  "claude-sonnet-4-20250514",
})
```

### Tool Restriction

Command can restrict which tools are available:

```go
// Register only approved tools
reg := tools.NewRegistry()
builtins.RegisterAll(reg)

// Filter to allowed tools only
filtered := reg.Filter([]string{"http_request", "json_parse"})
```

### Egress Tightening

Command applies organization-level egress policies:

```go
egressCfg, _ := security.Resolve(
    "strict",           // profile
    "allowlist",        // mode
    orgAllowedDomains,  // organization-level domain allowlist
    toolNames,
    capabilities,
)
```

### Skill Gating

Skills are compiled from `[]SkillEntry`, allowing Command to filter or augment:

```go
// Filter skills before compilation
var approved []skills.SkillEntry
for _, entry := range allEntries {
    if isApproved(entry.Name) {
        approved = append(approved, entry)
    }
}
result, _ := forgecore.Compile(forgecore.CompileRequest{
    Config:       cfg,
    SkillEntries: approved,
})
```

## API Stability Contract

### Versioning

forge-core follows semantic versioning:
- **Major version** (v1 -> v2): Breaking changes to public API signatures or behavior
- **Minor version** (v1.0 -> v1.1): New features, backward-compatible
- **Patch version** (v1.0.0 -> v1.0.1): Bug fixes only

### Stability Guarantees

The following are considered stable and will not change without a major version bump:

| API | Stability |
|-----|-----------|
| `forgecore.Compile()` signature and return types | Stable |
| `forgecore.ValidateConfig()` | Stable |
| `forgecore.ValidateAgentSpec()` | Stable |
| `forgecore.ValidateCommandCompat()` | Stable |
| `forgecore.SimulateImport()` | Stable |
| `forgecore.NewRuntime()` | Stable |
| `types.ForgeConfig` struct fields | Stable |
| `agentspec.AgentSpec` struct fields | Stable |
| `llm.Client` interface | Stable |
| `runtime.ToolExecutor` interface | Stable |
| `runtime.Logger` interface | Stable |
| `security.EgressConfig` struct | Stable |
| `skills.SkillEntry` struct | Stable |
| `tools.Registry` public methods | Stable |

### Supported Versions

| Field | Supported Values |
|-------|-----------------|
| `forge_version` | `1.0`, `1.1` |
| `tool_interface_version` | `1.0` |
| `skills_spec_version` | `agentskills-v1` |

### Deprecation Policy

- Deprecated APIs will be marked with `// Deprecated:` comments
- Deprecated APIs will continue to work for at least one minor version
- Removal of deprecated APIs requires a major version bump
