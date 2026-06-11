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
   | `forge-skills/registry/image-registry.yaml`, `forge-core/packaging/` | `docs/core-concepts/binary-dependencies.md` — refresh the registry contents, install methods, or Dockerfile shape sections; keep the source-file list at the bottom in sync with the actual files touched |
   | `forge-cli/templates/Dockerfile.tmpl`, `forge-cli/build/dockerfile_stage.go` | `docs/core-concepts/binary-dependencies.md` (image-shape section), `docs/deployment/docker.md` |
   | `forge-core/types/` / `forge.yaml` | `docs/reference/forge-yaml-schema.md` |
   | `CHANGELOG.md` | (rendered into release notes; no per-doc sync needed) |
   | Any of `forge-core/**`, `forge-cli/**`, `forge-ui/**`, `forge-plugins/**`, `forge-skills/**`, `docs/**`, `CHANGELOG.md`, `forge.yaml` schema | `.claude/skills/forge.md` — refresh the affected section(s) of the comprehensive knowledge skill. Sweep the specific section that maps to the changed area; don't rewrite the whole file. Keep the table-of-contents anchors in sync with the section headings. |
   | `forge-ui/skill_builder_context.go` (specifically the `skillBuilderSystemPrompt` constant) | `.claude/skills/forge-skill-builder.md` — re-port verbatim. The body must remain byte-identical to the Go constant apart from un-doing the Go string-concatenation escapes (`` ` + "```" + ` `` → triple-backticks, etc.). Do not edit either side without updating the other. |

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
- When `.claude/skills/forge.md` is updated, the table of contents at the top must stay synchronized with the section anchors below it. When `.claude/skills/forge-skill-builder.md` is updated, the body must remain byte-identical to `forge-ui/skill_builder_context.go`'s `skillBuilderSystemPrompt` constant (apart from un-doing the Go string-concatenation escapes — the only difference is markdown rendering).
