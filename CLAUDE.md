# Forge - CLAUDE.md

## Project Structure

Multi-module Go workspace with three modules:
- `forge-core/` — Core library (registry, tools, security, channels, LLM)
- `forge-cli/` — CLI commands, TUI wizard, runtime
- `forge-plugins/` — Channel plugins (telegram, slack), markdown converter

## Pre-Commit Requirements

**Always run before committing:**

```sh
# Format all modules
gofmt -w forge-core/ forge-cli/ forge-plugins/

# Lint all modules
golangci-lint run ./forge-core/...
golangci-lint run ./forge-cli/...
golangci-lint run ./forge-plugins/...
```

Fix any lint errors and formatting issues before creating commits.

## Testing

Run tests for affected modules before committing:

```sh
cd forge-core && go test ./...
cd forge-cli && go test ./...
cd forge-plugins && go test ./...
```
