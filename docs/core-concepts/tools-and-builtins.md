---
title: "Tools & Builtins"
description: "Built-in tools, adapter tools, and the pluggable tool system."
order: 3
---

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
| `http_request` | Make HTTP requests (GET, POST, PUT, DELETE). Strips credentials on cross-origin redirects |
| `json_parse` | Parse and query JSON data |
| `csv_parse` | Parse CSV data into structured records |
| `datetime_now` | Get current date and time |
| `uuid_generate` | Generate UUID v4 identifiers |
| `math_calculate` | Evaluate mathematical expressions |
| `web_search` | Search the web for quick lookups and recent information |
| `file_create` | Create a downloadable file, written to the agent's `.forge/files/` directory |
| `read_skill` | Load full instructions for an available skill on demand |
| `memory_search` | Search long-term memory (when enabled) |
| `memory_get` | Read memory files (when enabled) |
| `context_expand` | Retrieve the original content behind a `<<ctxzip:...>>` compression marker (when [compression](context-compression.md) is enabled) |
| `cli_execute` | Execute pre-approved CLI binaries |
| `schedule_set` | Create or update a recurring cron schedule |
| `schedule_list` | List all active and inactive schedules |
| `schedule_delete` | Remove an LLM-created schedule |
| `schedule_history` | View execution history for scheduled tasks |

Register all builtins with `builtins.RegisterAll(registry)`.

## File & Search Tools

Every Forge agent gets a file read/write/edit/patch surface plus search, registered by default and confined to the agent's working directory (`WorkDir`) via a `PathValidator`. All resolved paths are confined within the working directory, preventing directory-traversal attacks.

| Tool | Description |
|------|-------------|
| `file_read` | Read file contents with optional line offset/limit, or list directory entries |
| `file_write` | Create or overwrite files in the working directory |
| `file_edit` | Edit files by exact string matching with unified diff output |
| `file_patch` | Batch file operations (add, update, delete, move) in a single call |
| `glob_search` | Find files by glob pattern (e.g., `**/*.go`), sorted by modification time |
| `grep_search` | Search file contents with regex; uses `rg` if available, falls back to Go |
| `directory_tree` | Display tree-formatted directory listing (default max depth: 3) |

`file_read` / `file_write` / `file_edit` / `file_patch` are registered for general agents by the runtime (#268); previously they were reachable only when a skill wired them up. They give a general agent a real file-editing surface — not just `file_create` + search.

### General file tools vs. the code-agent skill

There are two distinct file surfaces, and they do **not** collide:

| Surface | Tools | Scope | When |
|---------|-------|-------|------|
| General builtins (#268) | `file_read` / `file_write` / `file_edit` / `file_patch` | `WorkDir` | Every agent, **except** when the code-agent skill is active |
| Code-agent skill | `code_agent_read` / `code_agent_write` / `code_agent_run` (from the skill's `SKILL.md`) | the skill's `project_dir` | Only when the `code-agent` skill is active |

When the `code-agent` skill is active, the general `file_*` builtins are **skipped** — the skill's project-scoped `code_agent_*` tools are the specialized file surface (skill tools win), so the LLM never sees two overlapping file surfaces. Search tools (`grep_search` / `glob_search` / `directory_tree`) are registered in both cases, scoped to `workspace/` under the code-agent skill and to `WorkDir` otherwise.

### Registration Groups

These tools are constructed and registered in layered groups (`forge-core/tools/builtins/register.go`), so the runtime and skills can request only the capabilities they need:

| Group | Tools | Purpose |
|-------|-------|---------|
| `FileTools` | `file_read`, `file_write`, `file_edit`, `file_patch` | General file surface — registered for non-code-agent agents (#268) |
| `CodeAgentSearchTools` | `grep_search`, `glob_search`, `directory_tree` | Read-only exploration — registered for every Forge agent |
| `CodeAgentReadTools` | `file_read` + search tools | Safe reading |
| `CodeAgentWriteTools` | `file_write`, `file_edit`, `file_patch` | Modification |
| `CodeAgentTools` | All read + write tools | Full code-agent capability |

### Path Validation

All file tools use `PathValidator` (from `pathutil.go`):

- All resolved paths must stay within the configured `workDir`
- Directory traversal via `..` is caught after symlink resolution
- Standard directories are excluded from search: `.git`, `node_modules`, `vendor`, `__pycache__`, `.venv`, `dist`, `build`

## Adapter Tools

| Adapter | Description |
|---------|-------------|
| `webhook_call` | POST JSON payloads to webhook URLs. Strips credentials on cross-origin redirects |
| `openapi_call` | Call OpenAPI-described endpoints |

Adapter tools bridge external services into the agent's tool set.

> **MCP tools** are not listed in this table. Configure MCP servers
> under the top-level `mcp:` block in `forge.yaml`; each server's
> discovered tools are registered as namespaced `<server>__<tool>`
> entries automatically. See [docs/mcp/](../mcp/index.md) for the
> configuration reference. The previous `mcp_call` adapter tool
> was removed in v0.12.0 — the new block is strictly more capable.

## Web Search Providers

The `web_search` tool supports two providers:

| Provider | API Key Env Var | Endpoint |
|----------|----------------|----------|
| Tavily (recommended) | `TAVILY_API_KEY` | `api.tavily.com/search` |
| Perplexity | `PERPLEXITY_API_KEY` | `api.perplexity.ai/chat/completions` |

Provider selection: `WEB_SEARCH_PROVIDER` env var, or auto-detect from available API keys (Tavily first).

## CLI Execute

The `cli_execute` tool provides security-hardened command execution with 13 security layers:

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
| 1 | **Shell denylist** | Shell interpreters (`bash`, `sh`, `zsh`, `dash`, `ksh`, `csh`, `tcsh`, `fish`) are filtered out at construction time and unconditionally blocked at execution — they defeat the no-shell design |
| 2 | **Binary allowlist** | Only pre-approved binaries can execute |
| 3 | **Binary resolution** | Binaries are resolved to absolute paths via `exec.LookPath` at startup |
| 4 | **Argument validation** | Rejects arguments containing `$(`, backticks, newlines, or `file://` URLs |
| 5 | **File protocol blocking** | Arguments containing `file://` (case-insensitive) are blocked to prevent filesystem traversal via `curl file:///etc/passwd` (see [File Protocol Blocking](../security/guardrails.md#file-protocol-blocking)) |
| 6 | **Path confinement** | Path arguments inside `$HOME` but outside `workDir` are blocked (see [Path Containment](../security/guardrails.md#path-containment)) |
| 7 | **Timeout** | Configurable per-command timeout (default: 120s) |
| 8 | **No shell** | Uses `exec.CommandContext` directly — no shell expansion |
| 9 | **Working directory** | `cmd.Dir` set to `workDir` so relative paths resolve within the agent directory |
| 10 | **Environment isolation** | Only `PATH`, `HOME`, `LANG`, explicit passthrough vars, proxy vars, `OPENAI_ORG_ID` (when set), `GH_CONFIG_DIR` (auto-set to real `~/.config/gh` **only for `gh`**), and `KUBECONFIG`/`NO_PROXY` (**only for `kubectl`/`helm`** — see below). `HOME` is overridden to `workDir` to prevent `~` expansion from reaching the real home directory |
| 11 | **Output limits** | Configurable max output size (default: 1MB) to prevent memory exhaustion |
| 12 | **Skill guardrails** | Skill-declared `deny_commands` and `deny_output` patterns block/redact command inputs and outputs (see [Skill Guardrails](../security/guardrails.md#skill-guardrails)) |
| 13 | **Custom tool entrypoint validation** | Custom tool entrypoints are validated: rejects empty, absolute, or `..`-containing paths; resolves symlinks and verifies the target stays within the project directory and is a regular file |

### KUBECONFIG and NO_PROXY Scoping

When `HOME` is overridden to `workDir`, `kubectl` and `helm` lose access to `~/.kube/config`. For these two binaries only, `cli_execute` auto-sets:

| Env Var | Value | Purpose |
|---------|-------|---------|
| `KUBECONFIG` | Explicit `KUBECONFIG` if set, else `<real-home>/.kube/config` | Passes through the active kubeconfig |
| `NO_PROXY` | K8s API server hostname(s) | Bypasses the egress proxy for cluster connections |

If `KUBECONFIG` is explicitly set in the environment (e.g., via `docker run -e KUBECONFIG=...` or after [KUBECONFIG materialization](runtime-engine.md#kubeconfig-materialization)), that value is passed through directly. Otherwise, `cli_execute` falls back to the real `~/.kube/config`. `NO_PROXY` is extracted from the kubeconfig's `clusters[].cluster.server` field. Other binaries do not receive these variables.

## File Create

The `file_create` tool generates downloadable files that are both written to disk and uploaded to the user's channel (Slack/Telegram).

| Field | Description |
|-------|-------------|
| `filename` | Name with extension (e.g., `patches.yaml`, `report.json`) |
| `content` | Full file content as text |

**Output JSON** includes `filename`, `content`, `mime_type`, and `path`. The `path` field contains the absolute disk location, allowing other tools (e.g., `kubectl apply -f <path>`) to reference the file.

**File location:** Files are written to the agent's `.forge/files/` directory (under `WorkDir`). The runtime injects this path via `FilesDir` in the executor context. When running outside the full runtime (e.g., tests), falls back to `$TMPDIR/forge-files/`.

**Allowed extensions:**

| Extension | MIME Type |
|-----------|-----------|
| `.md` | `text/markdown` |
| `.json` | `application/json` |
| `.yaml`, `.yml` | `text/yaml` |
| `.txt`, `.log` | `text/plain` |
| `.csv` | `text/csv` |
| `.sh` | `text/x-shellscript` |
| `.xml` | `text/xml` |
| `.html` | `text/html` |
| `.py` | `text/x-python` |
| `.ts` | `text/typescript` |

Filenames with path separators (`/`, `\`) or traversal patterns (`..`) are rejected.

## Memory Tools

When [long-term memory](memory-system.md) is enabled, two additional tools are registered:

- **`memory_search`** — Hybrid vector + keyword search across stored memory files
- **`memory_get`** — Read specific memory files by path

These tools allow the agent to recall information from previous sessions.

## Context Expansion Tool

When [context compression](context-compression.md) is enabled, the `context_expand` tool is registered. Compressed tool outputs carry inline `<<ctxzip:HASH note>>` markers; the model calls `context_expand` with the hash to retrieve the offloaded original from the local store. The tool tolerates imperfect input — a whole marker pasted as the hash, or a truncated hash that uniquely prefixes a recently emitted one — and a miss (expired/evicted entry) returns guidance to re-run the producing tool rather than an error.

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

Custom tools can also be added by placing scripts in a `tools/` directory in your project. TypeScript tools run via `npx --no-install ts-node` to prevent automatic package downloads.

### Custom Tool Entrypoint Validation

Custom tool entrypoints are validated at registration time:

- Empty or absolute paths are rejected
- Paths containing `..` after `filepath.Clean` are rejected
- Symlinks are resolved and the target must remain within the project directory
- The entrypoint must be a regular file (not a directory or device)

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
