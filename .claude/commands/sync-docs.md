# Sync Documentation

After feature work, update the affected documentation to reflect code changes.

## Steps

1. **Identify changed files** — Run `git diff main --name-only` to find modified Go files.

2. **Map files to docs** — Use this mapping to determine which docs need updates.
   Paths match the actual `docs/` tree (nested under `core-concepts/`, `reference/`,
   `security/`, `deployment/`, `skills/`).

   | Changed path pattern | Affected docs |
   |---------------------|---------------|
   | `forge-core/auth/` | `docs/security/authentication.md`, `docs/security/audit-logging.md` |
   | `forge-core/runtime/` | `docs/core-concepts/runtime-engine.md`, `docs/core-concepts/hooks.md` |
   | `forge-core/security/` | `docs/security/overview.md`, `docs/security/egress-control.md` |
   | `forge-core/tools/` | `docs/core-concepts/tools-and-builtins.md` |
   | `forge-core/llm/` | `docs/core-concepts/runtime-engine.md` |
   | `forge-core/memory/` | `docs/core-concepts/memory-system.md` |
   | `forge-core/scheduler/` | `docs/core-concepts/scheduling.md` |
   | `forge-core/secrets/` | `docs/security/secret-management.md` |
   | `forge-core/channels/` | `docs/core-concepts/channels.md` |
   | `forge-core/validate/` | `docs/reference/forge-yaml-schema.md` |
   | `forge-cli/cmd/` | `docs/reference/cli-reference.md` |
   | `forge-cli/runtime/` | `docs/core-concepts/runtime-engine.md` |
   | `forge-cli/server/` | `docs/core-concepts/how-forge-works.md` |
   | `forge-cli/channels/` | `docs/core-concepts/channels.md` |
   | `forge-cli/tools/` | `docs/core-concepts/tools-and-builtins.md` |
   | `forge-cli/internal/tui/` | `docs/reference/cli-reference.md` (wizard flow) |
   | `forge-plugins/` | `docs/core-concepts/channels.md`, `docs/reference/framework-plugins.md` |
   | `forge-ui/` | `docs/reference/web-dashboard.md` |
   | `forge-skills/` | `docs/skills/writing-custom-skills.md`, `docs/skills/contributing-a-skill.md` |
   | `forge-core/types/` / `forge.yaml` | `docs/reference/forge-yaml-schema.md` |
   | `CHANGELOG.md` | (rendered into release notes; no per-doc sync needed) |

3. **Read the diff** — For each mapped doc, read the relevant `git diff main` output to understand what changed.

4. **Update docs** — For each affected doc:
   - Read the current doc file
   - Identify sections that need updating based on the code changes
   - Edit the doc to reflect new behavior, flags, types, or configuration
   - Preserve the navigation footer and header

5. **Check cross-references** — If you added a new feature/section, ensure:
   - The README.md documentation table links to it (if it's a new doc)
   - Related docs cross-link to it where appropriate
   - Navigation order is still correct

6. **Validate** — Run a quick broken-link check:
   ```bash
   grep -rn '\[.*\](.*\.md)' README.md docs/ | while read line; do
     file=$(echo "$line" | grep -oP '\(.*?\.md\)' | tr -d '()')
     dir=$(dirname "$(echo "$line" | cut -d: -f1)")
     target="$dir/$file"
     [ ! -f "$target" ] && echo "BROKEN: $line"
   done
   ```

## Rules

- One topic per file; split if >300 lines
- Start each doc with a one-sentence summary
- Use tables over bullet lists for comparisons
- Link, don't repeat — cross-reference other docs
- Keep ASCII diagrams (they render everywhere)
- Code examples must be runnable
