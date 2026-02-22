# Skills

Skills are a progressive disclosure mechanism for defining agent capabilities in a structured, human-readable format. They compile into container artifacts during `forge build`.

## Overview

Skills bridge the gap between high-level capability descriptions and the tool-calling system. A `skills.md` file in your project root defines what the agent can do, and Forge compiles these into JSON artifacts and prompt text for the container.

## SKILL.md Format

Skills are defined in a Markdown file (default: `skills.md`). The file supports optional YAML frontmatter and two body formats.

### YAML Frontmatter

Skills can declare metadata and requirements in a YAML frontmatter block delimited by `---`:

```markdown
---
name: weather
description: Weather data skill
metadata:
  forge:
    requires:
      bins:
        - curl
      env:
        required: []
        one_of: []
        optional: []
---
## Tool: weather_current
Get current weather for a location.
```

The `metadata.forge.requires` block declares:
- **`bins`** — Binary dependencies that must be in `$PATH` at runtime
- **`env.required`** — Environment variables that must be set
- **`env.one_of`** — At least one of these environment variables must be set
- **`env.optional`** — Optional environment variables for extended functionality

Frontmatter is parsed by `ParseWithMetadata()` in `forge-core/skills/parser.go` and feeds into the compilation pipeline. The `SkillMetadata` and `SkillRequirements` types are defined in `forge-core/skills/types.go`.

### Tool Heading Format (recommended)

```markdown
## Tool: web_search
Search the web for current information and return relevant results.

**Input:** query: string, max_results: int
**Output:** results: []string

## Tool: summarize
Summarize long text into a concise paragraph.

**Input:** text: string, max_length: int
**Output:** summary: string
```

Each `## Tool:` heading starts a new skill entry. Paragraph text becomes the description. `**Input:**` and `**Output:**` lines set the input/output specifications.

### Legacy List Format

```markdown
# Agent Skills

- translate
- summarize
- classify
```

Single-word list items (no spaces, max 64 characters) create name-only skill entries. This format is simpler but provides less metadata.

## Compilation Pipeline

The skill compilation pipeline has three stages:

1. **Parse** (`internal/plugins/skills/parser.go`) — Reads `skills.md` and extracts `SkillEntry` values with name, description, input spec, and output spec. When YAML frontmatter is present, `ParseWithMetadata()` (`forge-core/skills/parser.go`) additionally extracts `SkillMetadata` and `SkillRequirements` (binary deps, env vars).

2. **Compile** (`internal/skills/compiler.go`) — Converts entries into `CompiledSkills` with:
   - A JSON-serializable skill list
   - A human-readable prompt catalog
   - Version identifier (`agentskills-v1`)

3. **Write Artifacts** — Outputs to the build directory:
   - `compiled/skills/skills.json` — Machine-readable skill definitions
   - `compiled/prompt.txt` — LLM-readable skill catalog

## Build Stage Integration

The `SkillsStage` (`internal/build/skills_stage.go`) runs as part of the build pipeline:

1. Resolves the skills file path (default: `skills.md` in work directory)
2. Skips silently if the file doesn't exist
3. Parses, compiles, and writes artifacts
4. Updates the `AgentSpec` with `skills_spec_version` and `forge_skills_ext_version`
5. Records generated files in the build manifest

## Prompt-Only vs Tool-Bearing Skills

- **Prompt-only skills** (legacy format) provide names only. They appear in the prompt catalog but have no structured input/output.
- **Tool-bearing skills** (heading format) include full specifications that can be used for validation and documentation.

## Configuration

In `forge.yaml`:

```yaml
skills:
  path: skills.md  # default, can be customized
```

## CLI Workflow

```bash
# Initialize a project with skills support
forge init my-agent --from-skills

# Build compiles skills automatically
forge build
```

## Related Files

- `internal/plugins/skills/parser.go` — SKILL.md parser
- `internal/skills/compiler.go` — Skill compilation and artifact generation
- `internal/build/skills_stage.go` — Build pipeline integration
