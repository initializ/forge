# Contributing to Forge

Thank you for your interest in contributing to Forge! This guide covers general development workflow and skill contribution.

## Table of Contents

- [Getting Started](#getting-started)
- [Project Structure](#project-structure)
- [Development Workflow](#development-workflow)
- [Contributing a Skill](#contributing-a-skill)
- [Security Rules](#security-rules)
- [Pull Request Process](#pull-request-process)
- [Code Style](#code-style)

## Getting Started

### Prerequisites

- Go 1.25 or later
- [golangci-lint](https://golangci-lint.run/) v2.10+
- Git

### Clone and build

```bash
git clone https://github.com/initializ/forge.git
cd forge
cd forge-cli && go build ./... && cd ..
```

### Run tests

```bash
cd forge-core && go test ./... && cd ..
cd forge-cli && go test ./... && cd ..
cd forge-plugins && go test ./... && cd ..
cd forge-skills && go test ./... && cd ..
```

## Project Structure

Forge is a multi-module Go workspace:

| Module | Purpose |
|--------|---------|
| `forge-core/` | Core library — registry, tools, security, channels, LLM |
| `forge-cli/` | CLI commands, TUI wizard, runtime |
| `forge-plugins/` | Channel plugins (Telegram, Slack), markdown converter |
| `forge-skills/` | Skill parser, compiler, analyzer, trust, embedded skills |

Skills live in two locations:

- **Embedded skills** — `forge-skills/local/embedded/` (bundled in the binary)
- **Project skills** — `skills/` in the user's working directory

## Development Workflow

1. Fork the repository and clone your fork
2. Create a feature branch from `main`:
   ```bash
   git checkout -b feature/my-feature main
   ```
3. Make your changes in the relevant module(s)
4. Format and lint:
   ```bash
   gofmt -w forge-core/ forge-cli/ forge-plugins/ forge-skills/
   golangci-lint run ./forge-core/...
   golangci-lint run ./forge-cli/...
   golangci-lint run ./forge-plugins/...
   golangci-lint run ./forge-skills/...
   ```
5. Run tests for affected modules
6. Commit with a clear message and open a pull request against `main`

## Contributing a Skill

### Step 1 — Copy the template

```bash
cp -r forge-skills/local/embedded/_template skills/my-skill
```

### Step 2 — Edit SKILL.md

Open `skills/my-skill/SKILL.md` and fill in:

- **`name`** — Kebab-case identifier matching the directory name
- **`description`** — One-line summary of what the skill does
- **`category`** (optional) — e.g. `sre`, `research`, `ops`, `dev`, `security`
- **`tags`** (optional) — Discovery keywords
- **`requires.bins`** — Binaries that must be in PATH
- **`requires.env`** — Environment variables (required, one_of, optional)
- **`egress_domains`** — Network domains the skill contacts (supports `$VAR` substitution)
- **`denied_tools`** (optional) — Tools the skill must NOT use
- **`timeout_hint`** (optional) — Suggested timeout in seconds

Add `## Tool: tool_name` sections documenting each tool with input/output tables.

If your skill is script-backed, add executable scripts to `scripts/`. Tool name underscores become hyphens in the filename: tool `my_search` maps to `scripts/my-search.sh`.

If your skill is binary-backed, delete the `scripts/` directory and list the binary in `requires.bins`.

### Step 3 — Validate

```bash
forge skills validate
forge skills audit
```

Fix any errors or warnings before submitting.

### Step 4 — Test

Run your skill locally and verify:

- Tools execute correctly with expected input
- Output matches the documented format
- Error cases are handled gracefully
- Egress domains are accurate and minimal

### Step 5 — Open a PR

Follow the [Pull Request Process](#pull-request-process) below.

## Security Rules

All contributions must follow these security requirements:

1. **Egress allowlist** — Every network domain a skill contacts must be listed in `egress_domains`. No wildcard domains.
2. **Minimal permissions** — Request only the environment variables and binaries actually needed.
3. **No secrets in code** — Never hardcode API keys, tokens, or credentials. Use `requires.env` to declare them.
4. **Read-only by default** — Skills should avoid mutating external state unless explicitly required and documented.
5. **Tool restrictions** — If a skill should not use certain tools (e.g. `http_request` when using `cli_execute`), declare them in `denied_tools`.

Use `forge skills audit` to check your skill against the default security policy. Use `forge skills trust-report <name>` to review full metadata.

## Pull Request Process

1. Branch from `main` — never push directly to `main`
2. Ensure all tests pass for affected modules
3. Run `gofmt` and `golangci-lint` with no errors
4. For skill contributions, include `forge skills validate` and `forge skills audit` output
5. Fill out the PR template completely
6. Request review from a maintainer

### Commit messages

Use clear, descriptive commit messages:

```
Add tavily-research skill with async polling

Fix egress domain validation for env var substitution
```

## Code Style

- Follow standard Go conventions (`gofmt`, `go vet`)
- Use `golangci-lint` with the project configuration
- Keep functions focused and testable
- Add tests for new functionality
- Document exported types and functions
