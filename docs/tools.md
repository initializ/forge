# Tools

> Part of [Forge Documentation](../README.md)

Tools are capabilities that an LLM agent can invoke during execution. Forge provides a pluggable tool system with built-in tools, adapter tools, development tools, and custom tools.

## Tool Categories

| Category | Code | Description |
|----------|------|-------------|
| **Builtin** | `builtin` | Core tools shipped with Forge |
| **Adapter** | `adapter` | External service integrations via webhook, MCP, or OpenAPI |
| **Dev** | `dev` | Development-only tools, filtered in production builds |
| **Custom** | `custom` | User-defined tools discovered from the project |

## Built-in Tools

| Tool | Description |
|------|-------------|
| `http_request` | Make HTTP requests (GET, POST, PUT, DELETE) |
| `json_parse` | Parse and query JSON data |
| `csv_parse` | Parse CSV data into structured records |
| `datetime_now` | Get current date and time |
| `uuid_generate` | Generate UUID v4 identifiers |
| `math_calculate` | Evaluate mathematical expressions |
| `web_search` | Search the web for quick lookups and recent information |
| `read_skill` | Load full instructions for an available skill on demand |
| `memory_search` | Search long-term memory (when enabled) |
| `memory_get` | Read memory files (when enabled) |
| `cli_execute` | Execute pre-approved CLI binaries |
| `schedule_set` | Create or update a recurring cron schedule |
| `schedule_list` | List all active and inactive schedules |
| `schedule_delete` | Remove an LLM-created schedule |
| `schedule_history` | View execution history for scheduled tasks |

Register all builtins with `builtins.RegisterAll(registry)`.

## Adapter Tools

| Adapter | Description |
|---------|-------------|
| `mcp_call` | Call tools on MCP servers via JSON-RPC |
| `webhook_call` | POST JSON payloads to webhook URLs |
| `openapi_call` | Call OpenAPI-described endpoints |

Adapter tools bridge external services into the agent's tool set.

## Web Search Providers

The `web_search` tool supports two providers:

| Provider | API Key Env Var | Endpoint |
|----------|----------------|----------|
| Tavily (recommended) | `TAVILY_API_KEY` | `api.tavily.com/search` |
| Perplexity | `PERPLEXITY_API_KEY` | `api.perplexity.ai/chat/completions` |

Provider selection: `WEB_SEARCH_PROVIDER` env var, or auto-detect from available API keys (Tavily first).

## CLI Execute

The `cli_execute` tool provides security-hardened command execution with 7 security layers:

```yaml
tools:
  - name: cli_execute
    config:
      allowed_binaries: ["git", "curl", "jq", "python3"]
      env_passthrough: ["GITHUB_TOKEN"]
      timeout: 120
      max_output_bytes: 1048576
```

| # | Layer | Detail |
|---|-------|--------|
| 1 | **Binary allowlist** | Only pre-approved binaries can execute |
| 2 | **Binary resolution** | Binaries are resolved to absolute paths via `exec.LookPath` at startup |
| 3 | **Argument validation** | Rejects arguments containing `$(`, backticks, or newlines |
| 4 | **Timeout** | Configurable per-command timeout (default: 120s) |
| 5 | **No shell** | Uses `exec.CommandContext` directly — no shell expansion |
| 6 | **Environment isolation** | Only `PATH`, `HOME`, `LANG`, explicit passthrough vars, and proxy vars |
| 7 | **Output limits** | Configurable max output size (default: 1MB) to prevent memory exhaustion |

## Memory Tools

When [long-term memory](memory.md) is enabled, two additional tools are registered:

- **`memory_search`** — Hybrid vector + keyword search across stored memory files
- **`memory_get`** — Read specific memory files by path

These tools allow the agent to recall information from previous sessions.

## Development Tools

Development tools (`local_shell`, `local_file_browser`, `debug_console`, `test_runner`) are available during `forge run --dev` but are **automatically filtered out** in production builds by the `ToolFilterStage`.

## Tool Interface

All tools implement the `tools.Tool` interface:

```go
type Tool interface {
    Name() string
    Description() string
    Category() Category
    InputSchema() json.RawMessage
    Execute(ctx context.Context, args json.RawMessage) (string, error)
}
```

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

Custom tools can also be added by placing scripts in a `tools/` directory in your project.

## Tool Commands

```bash
# List all registered tools
forge tool list

# Show details for a specific tool
forge tool describe web_search
```

## Build Pipeline

The `ToolFilterStage` runs during `forge build`:

1. Annotates each tool with its category (builtin, adapter, dev, custom)
2. Sets `tool_interface_version` to `"1.0"` on the AgentSpec
3. In production mode (`--prod`), removes all dev-category tools
4. Counts tools per category for the build manifest

---
← [Skills](skills.md) | [Back to README](../README.md) | [Runtime](runtime.md) →
