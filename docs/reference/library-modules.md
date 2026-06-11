---
title: "Library Modules"
description: "Import forge-core, forge-skills, and forge-plugins as Go library packages — for embedding Forge in a host application."
order: 12
---

## Library Modules

Forge ships as a single CLI binary for most users (`forge build`, `forge run`, `forge package`), but the underlying Go modules are also published independently so a host platform can embed Forge's agent runtime as a library — `go get github.com/initializ/forge/forge-core@<version>` instead of shelling out to `forge run`.

This page documents:
- the three importable modules and their roles
- the path-prefixed Git tag scheme used to release them
- the release pipeline that produces those tags
- the cross-module dependency graph and what's in `go.mod` at a tagged version vs in the monorepo workspace

For the CLI workflow, see [CLI Reference](cli-reference.md). For embedding patterns, see the host platform's own integration docs.

## Importable modules

| Module | Import path | Role | Internal deps |
|---|---|---|---|
| **`forge-skills`** | `github.com/initializ/forge/forge-skills` | SKILL.md parser, skill registry, bin classifier, security policy analyzer. The library piece any skill-aware tool needs. | none |
| **`forge-core`** | `github.com/initializ/forge/forge-core` | Runtime engine (LLM loop, guardrails, audit, hooks), tool registry, MCP client, channel interfaces, A2A types, security subsystem (auth chain, egress enforcer, platform policy), memory, scheduler, secrets, validation. | `forge-skills` |
| **`forge-plugins`** | `github.com/initializ/forge/forge-plugins` | Channel adapters (Slack, Telegram, MS Teams), Markdown converter. Optional — only needed if you want Forge's bundled channel adapters in your host. | `forge-core` |

`forge-cli` and `forge-ui` are CLI / web-UI consumers and are **not** published as library modules. Their internals are subject to change without notice.

## Releasing — path-prefixed tags

Each library module is published as its own Go module, tagged separately, on every Forge release. The tag format follows the [Go multi-module repo convention](https://go.dev/ref/mod#vcs-version):

```
forge-skills/vX.Y.Z
forge-core/vX.Y.Z
forge-plugins/vX.Y.Z
```

The CLI binary continues to use the flat `vX.Y.Z` tag (consumed by goreleaser). All three library tags share the same version as the CLI for a given release — see [Versioning](#versioning).

Consumer side, this means:

```sh
# In the host platform's go.mod:
go get github.com/initializ/forge/forge-core@v0.15.0

# Equivalent to:
require github.com/initializ/forge/forge-core v0.15.0
```

Go's module proxy resolves `forge-core@v0.15.0` to the tag `forge-core/v0.15.0`. The flat `v0.15.0` tag is used by `forge-cli`'s release artifacts but never by `go get` against the library modules.

## How the release pipeline produces those tags

The library tags can't simply point at the same commit as the binary tag, because:

- The `go.mod` files in the **monorepo workspace** use `replace github.com/initializ/forge/forge-skills => ../forge-skills` and `require github.com/initializ/forge/forge-skills v0.0.0` (a placeholder). This is correct for `go.work`-based local development — workspace mode resolves the cross-module references to the local source tree.
- A **published Go module** can't rely on local `replace` paths. An external consumer fetching `forge-core@v0.15.0` would see `replace ../forge-skills` and the build would fail with "path does not exist."

The fix is an **ephemeral release commit**: a single commit, never merged to `main`, whose tree contains `go.mod` files with the `replace` directives dropped and the `require` lines bumped to point at real sibling versions. The library tags reference this commit.

```
main                                          ◀── workspace-mode go.mod files
 │
 ├─ ... (regular commits)
 │
 └─ tagged: v0.15.0  ◀── flat binary-release tag (consumed by goreleaser)
       │
       │   (ephemeral commit, only referenced by tags)
       └── release-libs(go.mod rewrites)  ◀── forge-core/v0.15.0
                                              forge-plugins/v0.15.0

main itself is unchanged after the release. The release commit lives only
as a tag target and never enters the working history.
```

`forge-skills/v0.15.0` points at the binary-release commit directly — `forge-skills` has no internal deps so no `go.mod` rewrite is needed.

The rewrite + tagging is automated by [`scripts/release/tag-libraries.sh`](https://github.com/initializ/forge/blob/main/scripts/release/tag-libraries.sh), called from the `library-tags` job in [`.github/workflows/release.yaml`](https://github.com/initializ/forge/blob/main/.github/workflows/release.yaml).

### What the script does

1. Verifies the binary-release tag (`vX.Y.Z`) exists locally.
2. Creates a temporary git worktree at that tag (detached HEAD).
3. Runs `go mod edit` to:
   - Drop `replace github.com/initializ/forge/forge-skills` from `forge-core/go.mod`
   - Bump `require github.com/initializ/forge/forge-skills` to the new version
   - Drop `replace github.com/initializ/forge/forge-core` from `forge-plugins/go.mod`
   - Bump `require github.com/initializ/forge/forge-core` to the new version
4. Commits the rewritten `go.mod` files (authored by the `forge release bot` identity).
5. Tags `forge-skills/vX.Y.Z` at the original binary-release commit.
6. Tags `forge-core/vX.Y.Z` and `forge-plugins/vX.Y.Z` at the ephemeral commit.
7. Pushes all three tags.

The script supports `--dry-run` (print the actions that would happen) and `--no-push` (tag locally without pushing). See the script header for the full usage.

### Running it manually

If a release tag was pushed without the library tags (e.g. a hotfix where the workflow was skipped), run the script directly from a checkout with push access:

```sh
git fetch --tags
./scripts/release/tag-libraries.sh v0.15.0
```

Refusing to run if any library tag for the version already exists is intentional — re-running on a duplicate is a hard error. Delete the offending tag first (`git tag -d <name>` locally + `git push origin :refs/tags/<name>` remotely) before re-running.

## Versioning

Forge follows **unified versioning** during the v0.x line: every Forge release ships all three library modules at the same version, even when only one of them changed. This keeps the operational model simple and matches how the workspace is developed (changes routinely span modules).

Semantic versioning applies the way you'd expect:

- **v0.x.y** — no API stability promise. Public symbols may change between minor versions. Breaking changes are noted in `CHANGELOG.md`.
- **v1.0 onward** (when adopted) — semver applies to every package not under an `internal/` directory. Breaking changes require a major-version bump per Go module conventions (the import path changes to `github.com/initializ/forge/forge-core/v2`).

If you depend on the library modules from a host platform, **pin to an exact version** in your `go.mod` (`v0.15.0`, not `v0`) until the v1.0 promise is in effect.

## Public API surface

The convention used inside the library modules:

- Anything under a directory named `internal/` is private — Go's tooling enforces that only paths sharing a common ancestor with the `internal/` directory can import from it. The host platform cannot import from `internal/` at all.
- Anything else is considered public and follows the versioning rules above.

Today the library modules **do not yet have `internal/` markers** for the implementation details that aren't intended to be embedder-facing. Tightening that is on the roadmap (issue link TBD). Until then, treat `forge-core`'s subpackages as having an unstable surface unless the [`docs/`](https://github.com/initializ/forge/tree/main/docs) explicitly call them out as supported.

The currently-stable public entry points for embedders are:

- `forge-core/runtime` — `NewLLMExecutor`, `LLMExecutorConfig`, `Execute`, `Hooks`
- `forge-core/llm` — `Client`, `ChatRequest`, `ChatResponse`, the provider abstractions
- `forge-core/tools` — `Registry`, `Tool` interfaces
- `forge-core/a2a` — wire types for the A2A 0.3.0 protocol
- `forge-core/types` — `ForgeConfig` (the `forge.yaml` shape)
- `forge-skills/contract` — `SkillEntry`, `SkillRequirements`, the SKILL.md shape
- `forge-skills/parser` — `ParseFileWithMetadata`
- `forge-skills/registry` — `Default` (the embedded bin registry)
- `forge-skills/analyzer` — `SecurityPolicy`, `AnalyzeSkill*`, `CheckPolicy`

For everything else, look for usage in `forge-cli` (which exercises the same surface as a reference embedder) before depending on it.

## Workspace-mode dev still works the same way

The monorepo continues to use `go.work` with the five-module `use (...)` block. Local development is unchanged:

- `go.mod` files keep their `replace ../forge-skills` and `replace ../forge-core` directives.
- `go build` from any module uses the workspace overrides, not whatever's in `require`.
- The release pipeline only rewrites `go.mod` for the ephemeral release commit; main never sees the rewrite.

What this means in practice: **don't bump the `v0.0.0` placeholder require lines on main**. The release script does that surgically, in the ephemeral commit, only at tag time. Editing them on main breaks workspace-mode dev because the workspace can't resolve `require X v0.15.0` when X is supposed to come from a local path.

If you do need to bump the placeholder (e.g. to test a downstream consumer against an unreleased state), use a feature branch and revert before merging.

## Roadmap

- **API stability markers**: introduce `internal/` directories under `forge-core` and `forge-skills` for the implementation details that are not intended for embedders. Track in a follow-up issue.
- **Smoke-test CI**: a job that, after the release tags are pushed, scaffolds a tiny external module and runs `go get github.com/initializ/forge/forge-core@<just-tagged>` + a "compile a minimal agent" check. Catches `replace`-leak / unresolved-version bugs the same day they happen, not when a consumer hits them.
- **Independent versioning**: opt-in once one of the libraries justifies a release cadence different from the CLI. Today unified versioning is simpler.

## Source files

- `scripts/release/tag-libraries.sh` — the rewrite-and-tag script
- `.github/workflows/release.yaml` — the `library-tags` job that runs the script
- `forge-skills/go.mod`, `forge-core/go.mod`, `forge-plugins/go.mod` — module declarations (with workspace-mode `replace` directives)
- `go.work` — the monorepo workspace declaration
