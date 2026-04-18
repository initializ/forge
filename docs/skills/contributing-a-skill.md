---
title: "Contributing a Skill"
description: "Contribute a skill to the Forge embedded skill registry."
order: 4
---

## Contributing to the Skill Registry

Forge ships with an embedded skill registry. You can contribute new skills by submitting a pull request to the [forge repository](https://github.com/initializ/forge).

### Skill Structure

Each skill lives in its own directory under `forge-skills/local/embedded/`:

```
forge-skills/local/embedded/
  your-skill/
    SKILL.md           # Required: skill definition
    scripts/           # Optional: script-backed tools
      your-tool.sh
```

### Requirements

1. **Valid SKILL.md** — Must include all required frontmatter fields: `name`, `icon`, `category`, `tags`, `description`
2. **Category and tags** — Must be lowercase kebab-case
3. **Tool definitions** — Use `## Tool: tool_name` sections with Input/Output specifications
4. **Binary dependencies** — Declare all required binaries in `metadata.forge.requires.bins`
5. **Environment variables** — Declare required and optional env vars
6. **Egress domains** — Declare all external domains the skill accesses

### Validation

Before submitting:

```bash
# Validate skill requirements
forge skills validate

# Run security audit
forge skills audit --embedded

# Test the skill
forge run
```

### Signing

Skills can be signed for integrity verification:

```bash
# Generate a signing key (if you don't have one)
forge skills keygen

# Sign the skill
forge skills sign

# Generate trust report
forge skills trust-report
```

### Submission

1. Fork the [forge repository](https://github.com/initializ/forge)
2. Add your skill under `forge-skills/local/embedded/your-skill/`
3. Run `forge skills validate` and `forge skills audit --embedded`
4. Submit a pull request with a description of the skill's purpose and requirements
