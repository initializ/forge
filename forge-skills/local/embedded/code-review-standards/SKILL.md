---
name: code-review-standards
icon: 📏
category: developer
tags:
  - code-review
  - standards
  - configuration
  - linting
  - quality
description: Discover and apply organization coding standards from .forge-review/ configuration
metadata:
  forge:
    requires:
      bins: []
      env:
        required: []
        one_of: []
        optional:
          - FORGE_REVIEW_STANDARDS_DIR
    egress_domains: []
---

# Code Review Standards

Teaches the agent to discover and apply organization-specific coding standards stored in a `.forge-review/` directory in the target repository. This skill provides configuration templates and standard rule files that teams can customize.

## Overview

The `.forge-review/` directory is a convention for storing code review configuration alongside the codebase. When present, the `code-review` skill automatically loads these standards and applies them during reviews.

This skill:
- Documents the `.forge-review/` directory structure and file formats
- Provides templates for bootstrapping review standards in new repositories
- Teaches the agent how to discover and interpret standards files

## Directory Structure

```
.forge-review/
├── config.yaml                  # Review configuration
├── .forge-review-ignore         # Files/patterns to skip during review
└── standards/                   # Coding standards (one file per topic)
    ├── general.md               # General coding rules
    ├── security.md              # Security rules
    ├── go.md                    # Go-specific rules
    ├── python.md                # Python-specific rules
    ├── typescript.md            # TypeScript rules
    └── testing.md               # Testing rules
```

## Tool: review_standards_init

Initialize a `.forge-review/` directory in the current repository with default templates.

This is a binary-backed tool that uses filesystem operations (mkdir, write) to scaffold the standards directory. The agent reads templates bundled with this skill and writes them to the target repository.

**Input:**

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| target_dir | string | no | Target directory (default: current working directory) |
| languages | array | no | Language-specific standards to include: `go`, `python`, `typescript`. Default: all |
| overwrite | boolean | no | Overwrite existing files. Default: false |

**Output:** List of created files and a summary of the initialized configuration.

### Detection Heuristics

The agent selects this tool when it detects:
- Requests to "set up code review", "initialize review standards", "configure review"
- Questions about `.forge-review/` directory structure
- Requests to create or customize coding standards

## Tool: review_standards_check

Check whether the current repository has a `.forge-review/` configuration and report its status.

**Input:**

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| target_dir | string | no | Directory to check (default: current working directory) |

**Output:** JSON object with configuration status, found files, and any validation issues.

### Detection Heuristics

The agent selects this tool when it detects:
- Questions about whether review standards are configured
- Requests to validate review configuration
- Troubleshooting review behavior

## Configuration Reference

### config.yaml

```yaml
# .forge-review/config.yaml
version: "1"

# Default review settings
review:
  # Focus areas to always include (bugs, security, style, performance, maintainability)
  default_focus:
    - bugs
    - security

  # Severity threshold: only report findings at or above this level
  # Options: nitpick, info, warning, error
  min_severity: info

  # Maximum findings to report per review
  max_findings: 50

# Language-specific settings
languages:
  go:
    standards_file: standards/go.md
    enabled: true
  python:
    standards_file: standards/python.md
    enabled: true
  typescript:
    standards_file: standards/typescript.md
    enabled: true

# File patterns to always skip (supplements .forge-review-ignore)
skip_patterns:
  - "vendor/**"
  - "node_modules/**"
  - "*.generated.go"
  - "*.min.js"
  - "**/*.pb.go"
```

### .forge-review-ignore

Gitignore-style patterns for files that should be skipped during review:

```
# Dependencies
vendor/
node_modules/

# Generated code
*.generated.go
*.pb.go
*_gen.go

# Build artifacts
dist/
build/
*.min.js
*.min.css

# Test fixtures
testdata/
**/fixtures/**

# Documentation
*.md
LICENSE
```

### Standards Files

Standards files are markdown documents with rules organized by category. Each rule should be clear and actionable. The `code-review` skill injects these into the LLM prompt during review.

Format:

```markdown
# Category Name

## Rule Title

Description of the rule and why it matters.

**Good:**
\`\`\`go
// example of correct code
\`\`\`

**Bad:**
\`\`\`go
// example of incorrect code
\`\`\`
```

## Templates

This skill bundles template files in the `templates/` directory. Use `review_standards_init` to copy them into a target repository, or browse them directly for reference.

Available templates:
- `templates/.forge-review/config.yaml` — Default configuration
- `templates/.forge-review/.forge-review-ignore` — Default ignore patterns
- `templates/.forge-review/standards/general.md` — General coding standards
- `templates/.forge-review/standards/security.md` — Security review rules
- `templates/.forge-review/standards/go.md` — Go-specific standards
- `templates/.forge-review/standards/python.md` — Python-specific standards
- `templates/.forge-review/standards/typescript.md` — TypeScript standards
- `templates/.forge-review/standards/testing.md` — Testing standards

## Safety Constraints

- This skill only reads and writes configuration files in `.forge-review/`
- It never modifies source code
- Standards files are only used as additional context for LLM-based review; they do not block merges or fail CI on their own
- Overwrite protection is on by default (`overwrite: false`)
