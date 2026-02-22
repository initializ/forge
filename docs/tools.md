# Tools

Tools are capabilities that an LLM agent can invoke during execution. Forge provides a pluggable tool system with built-in tools, adapter tools, development tools, and custom tools.

## Tool Categories

| Category | Code | Description |
|----------|------|-------------|
| **Builtin** | `builtin` | Core tools shipped with Forge (A) |
| **Adapter** | `adapter` | External service integrations via webhook, MCP, or OpenAPI (B) |
| **Dev** | `dev` | Development-only tools, filtered in production builds (C) |
| **Custom** | `custom` | User-defined tools discovered from the project |

## Tool Interface

All tools implement the `tools.Tool` interface defined in `internal/tools/tool.go`:

```go
type Tool interface {
    Name() string
    Description() string
    Category() Category
    InputSchema() json.RawMessage
    Execute(ctx context.Context, args json.RawMessage) (string, error)
}
```

## Built-in Tools

Located in `internal/tools/builtins/`:

| Tool | Description |
|------|-------------|
| `web_search` | Search the web using Perplexity API |
| `http_request` | Make HTTP requests (GET, POST, etc.) |
| `json_parse` | Parse and query JSON data |
| `csv_parse` | Parse CSV data into structured records |
| `datetime_now` | Get current date and time |
| `uuid_generate` | Generate UUID v4 identifiers |
| `math_calculate` | Evaluate mathematical expressions |

Register all builtins with `builtins.RegisterAll(registry)`.

## Adapter Tools

Located in `internal/tools/adapters/`:

| Adapter | Description |
|---------|-------------|
| `webhook` | Invoke external HTTP endpoints as tools |
| `mcp` | Connect to Model Context Protocol servers |
| `openapi` | Auto-generate tools from OpenAPI specifications |

Adapter tools bridge external services into the agent's tool set.

## Development Tools

Located in `internal/tools/devtools/`:

Development tools (`local_shell`, `local_file_browser`, `debug_console`, `test_runner`) are available during `forge run --dev` but are **automatically filtered out** in production builds by the `ToolFilterStage`.

## Writing a Custom Tool

Custom tools are discovered from the project directory. Create a Python or TypeScript file with a docstring schema:

```python
"""
Tool: my_custom_tool
Description: Does something useful.

Input:
  query (str): The search query.
  limit (int): Maximum results.

Output:
  results (list): The search results.
"""

import json
import sys

def execute(args: dict) -> str:
    query = args.get("query", "")
    return json.dumps({"results": [f"Result for: {query}"]})

if __name__ == "__main__":
    input_data = json.loads(sys.stdin.read())
    print(execute(input_data))
```

## Tool Discovery

The tool discovery system (`internal/tools/discovery.go`) scans project directories for custom tool files. It recognizes:

- Python files with docstring schemas
- TypeScript files with JSDoc schemas
- Tool configuration in `forge.yaml`

## Tool Registry

The `tools.Registry` (`internal/tools/registry.go`) is a thread-safe tool registry that:

- Prevents duplicate registrations
- Provides `Execute(name, args)` and `ToolDefinitions()` methods
- Satisfies the `engine.ToolExecutor` interface via structural typing

## CLI Commands

```bash
# List all registered tools
forge tool list

# Show details for a specific tool
forge tool describe web_search
```

## Build Pipeline

The `ToolFilterStage` (`internal/build/tool_filter_stage.go`) runs during `forge build`:

1. Annotates each tool with its category (builtin, adapter, dev, custom)
2. Sets `tool_interface_version` to `"1.0"` on the AgentSpec
3. In production mode (`--prod`), removes all dev-category tools
4. Counts tools per category for the build manifest

## Related Files

- `internal/tools/tool.go` — Tool interface and category constants
- `internal/tools/registry.go` — Thread-safe tool registry
- `internal/tools/builtins/` — Built-in tool implementations
- `internal/tools/adapters/` — Adapter tool implementations
- `internal/tools/devtools/` — Development tools
- `internal/tools/discovery.go` — Tool discovery from project files
- `internal/build/tool_filter_stage.go` — Build-time tool filtering
