---
title: "Your First Skill"
description: "Create your first agent skill with the SKILL.md format."
order: 3
---

Skills bridge the gap between high-level capability descriptions and the tool-calling system. Each skill lives in its own subdirectory under `skills/` with a `SKILL.md` file that defines what the agent can do.

## Create Your First Skill

### 1. Initialize a project

```bash
forge init my-agent
cd my-agent
```

### 2. Create a skill directory

```bash
mkdir -p skills/weather
```

### 3. Write the SKILL.md

Create `skills/weather/SKILL.md`:

```markdown
---
name: weather
icon: 🌤️
category: utilities
tags:
  - weather
  - forecast
description: Weather data skill
---
## Tool: weather_current

Get current weather for a location.

**Input:** location (string) - City name or coordinates
**Output:** Current temperature, conditions, humidity, and wind speed
```

### 4. Build and run

```bash
forge build
forge run
```

Your agent now has a `weather_current` tool it can invoke. The skill's instructions are injected into the LLM system prompt automatically.

### 5. Add from the registry

Instead of writing skills from scratch, you can add pre-built skills:

```bash
# Browse available skills
forge skills list

# Add a skill from the registry
forge skills add tavily-research

# Validate requirements
forge skills validate
```

See [SKILL.md Format](/docs/core-concepts/skill-md-format) for the complete format reference and [Embedded Skills](/docs/skills/embedded-skills) for all built-in skills.
