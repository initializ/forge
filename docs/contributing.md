# Contributing

## Development Setup

### Prerequisites

- Go 1.25 or later
- `golangci-lint` (for linting)
- `goreleaser` (for releases, optional)

### Clone and Build

```bash
git clone https://github.com/initializ/forge.git
cd forge
make build
```

### Verify

```bash
make vet
make test
```

## Code Organization

```
internal/cmd/        # CLI command definitions (one file per command)
internal/config/     # Configuration parsing
internal/models/     # Data structures (AgentSpec, ToolSpec, etc.)
internal/pipeline/   # Build pipeline engine
internal/build/      # Build stage implementations
internal/plugins/    # Framework plugin system
internal/runtime/    # Agent local runner
internal/runtime/llm # LLM client abstraction
internal/container/  # Container image building
internal/channels/   # Channel adapter system
internal/tools/      # Tool registry and implementations
internal/validate/   # Validation logic
pkg/a2a/             # Shared A2A protocol types
schemas/             # Embedded JSON schemas
templates/           # Go templates for code generation
testdata/            # Test fixtures
```

## How to Add...

### A New Command

1. Create `internal/cmd/yourcommand.go`
2. Define a `cobra.Command` variable
3. Register it in `internal/cmd/root.go`'s `init()` function
4. Add tests in `internal/cmd/yourcommand_test.go`

### A New Build Stage

1. Create `internal/build/yourstage.go`
2. Implement the `pipeline.Stage` interface (`Name()` and `Execute()`)
3. Add the stage to the pipeline in `internal/cmd/build.go`
4. Add tests in `internal/build/yourstage_test.go`

### A New Tool

**Builtin tool:**
1. Create `internal/tools/builtins/your_tool.go`
2. Implement the `tools.Tool` interface
3. Register in `internal/tools/builtins/register.go`'s `RegisterAll()` function

**Custom tool (script-based):**
1. Create `tools/tool_yourname.py` (or `.ts`/`.js`) in the agent project
2. Forge discovers it automatically via `tools.DiscoverTools()`

### A New Channel Adapter

1. Create `internal/channels/yourplatform/yourplatform.go`
2. Implement the `channels.ChannelPlugin` interface
3. Register in `internal/cmd/channel.go`'s `createPlugin()` and `defaultRegistry()`
4. Add config generation in `generateChannelConfig()` and `generateEnvVars()`
5. Add tests

### A New LLM Provider

1. Create `internal/runtime/llm/providers/yourprovider.go`
2. Implement the `llm.Client` interface (`Chat()`, `ChatStream()`, `ModelID()`)
3. Add the provider to the factory in `internal/runtime/llm/providers/factory.go`
4. Add tests

## Testing Guidelines

### Unit Tests

- Every package should have `*_test.go` files
- Test files use the same package name (white-box) or `_test` suffix (black-box)
- Use table-driven tests where appropriate
- Mock external dependencies (HTTP servers, file system, etc.)

### Integration Tests

- Use the `//go:build integration` build tag
- Run with `go test -tags=integration ./...`
- Integration tests may use test fixtures from `testdata/`
- No external services required (use `httptest` for mock servers)

### Test Fixtures

Test fixtures live in `testdata/` at the project root:
- `forge-valid.yaml` — Full valid forge.yaml
- `forge-minimal.yaml` — Bare-minimum valid config
- `forge-invalid.yaml` — Invalid config for error testing
- `agentspec-valid.json` — Valid AgentSpec JSON
- `agentspec-invalid.json` — Invalid AgentSpec for error testing
- `tool-schema.json` — Minimal tool input schema

### Running Tests

```bash
# All unit tests
make test

# Integration tests
make test-integration

# Coverage report
make cover

# Specific package
go test -v ./internal/pipeline/...
```

## Code Style

- Run `go fmt` on all files
- Run `go vet` before committing
- Run `golangci-lint run ./...` for additional checks
- Keep functions focused and small
- Use meaningful variable names
- Add comments for non-obvious logic only

## PR Process

1. Create a feature branch from `develop`
2. Make your changes with tests
3. Ensure all checks pass: `make vet && make test && make fmt`
4. Push and open a pull request against `develop`
5. PRs require passing CI checks before merge

## Release Process

Releases are automated via GoReleaser:

1. Ensure `develop` is stable and all tests pass
2. Merge `develop` into `main`
3. Tag the release: `git tag v0.1.0`
4. Push the tag: `git push origin v0.1.0`
5. GitHub Actions runs GoReleaser to build and publish binaries
