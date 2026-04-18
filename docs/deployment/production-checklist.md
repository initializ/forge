---
title: "Production Checklist"
description: "Production build checks and deployment best practices."
order: 3
---

## Production Build Checks

Production builds (`--prod`) enforce:

- No `dev-open` egress mode
- No dev-only tools (`local_shell`, `local_file_browser`)
- Secret provider chain must include `env` (not just `encrypted-file`)
- `.dockerignore` must exist if a Dockerfile is generated

## Command Platform Export

For Initializ Command integration, export the agent spec:

```bash
# Export with embedded schemas
forge export --pretty --include-schemas

# Simulate Command import
forge export --simulate-import
```

See [Command Integration](command-integration.md) for the full integration guide.
