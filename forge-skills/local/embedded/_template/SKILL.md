---
name: my-skill
# category: ops                        # Optional: sre, research, ops, dev, security, etc.
# tags:                                 # Optional: discovery keywords
#   - example
#   - starter
description: One-line description of what this skill does
metadata:
  forge:
    requires:
      bins:                             # Binaries that must exist in PATH
        - curl
      env:
        required:                       # Env vars that MUST be set
          - MY_API_KEY
        one_of: []                      # At least one of these must be set
        optional: []                    # Nice-to-have env vars
    egress_domains:                     # Network domains this skill may contact
      - api.example.com
    # denied_tools:                     # Tools this skill must NOT use
    #   - http_request
    #   - web_search
    # timeout_hint: 300                 # Suggested timeout in seconds
---

# My Skill

Brief description of the skill's purpose, capabilities, and intended audience.

## Authentication

Describe how credentials are obtained and configured.

```bash
export MY_API_KEY="your-key-here"
```

## Quick Start

### Script-backed execution

Place executable scripts in `scripts/`. The tool name maps to the script:
underscores in the tool name become hyphens in the filename.

Example: tool `my_search` → `scripts/my-search.sh`

```bash
./scripts/my-search.sh '{"query": "hello"}'
```

### Binary-backed execution

If the skill delegates to a compiled binary instead, delete the `scripts/`
directory entirely and document the binary in `requires.bins` above.

## Tool: my_tool

Short description of what this tool does.

**Input:**

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| query | string | yes | The search query |
| max_results | integer | no | Maximum results (1-20). Default: 5 |

**Output:** JSON object with `results` array of `{title, url, content, score}`.

### Response Format

```json
{
  "results": [
    {
      "title": "Example",
      "url": "https://example.com",
      "content": "Relevant snippet...",
      "score": 0.95
    }
  ]
}
```

### Tips

- Tip 1 for effective usage
- Tip 2 for edge cases

## Safety Constraints

Document any safety rules this skill enforces (read-only, no secrets, etc.).
