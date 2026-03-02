# Sync Documentation

After feature work, update the affected documentation to reflect code changes.

## Steps

1. **Identify changed files** — Run `git diff main --name-only` to find modified Go files.

2. **Map files to docs** — Use this mapping to determine which docs need updates:

   | Changed path pattern | Affected docs |
   |---------------------|---------------|
   | `forge-core/runtime/` | `docs/runtime.md`, `docs/hooks.md` |
   | `forge-core/security/` | `docs/security/overview.md`, `docs/security/egress.md` |
   | `forge-core/tools/` | `docs/tools.md` |
   | `forge-core/llm/` | `docs/runtime.md` |
   | `forge-core/memory/` | `docs/memory.md` |
   | `forge-core/scheduler/` | `docs/scheduling.md` |
   | `forge-core/secrets/` | `docs/security/secrets.md` |
   | `forge-core/skills/` | `docs/skills.md` |
   | `forge-core/channels/` | `docs/channels.md` |
   | `forge-cli/cmd/` | `docs/commands.md` |
   | `forge-cli/runtime/` | `docs/runtime.md` |
   | `forge-cli/server/` | `docs/architecture.md` |
   | `forge-cli/channels/` | `docs/channels.md` |
   | `forge-cli/tools/` | `docs/tools.md` |
   | `forge-plugins/` | `docs/channels.md`, `docs/plugins.md` |
   | `forge-ui/` | `docs/dashboard.md` |
   | `forge-skills/` | `docs/skills.md` |
   | `forge.yaml` / `types/` | `docs/configuration.md` |

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
