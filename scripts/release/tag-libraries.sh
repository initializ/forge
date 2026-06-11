#!/usr/bin/env bash
#
# tag-libraries.sh — push path-prefixed Git tags for the three library
# modules (`forge-skills`, `forge-core`, `forge-plugins`) so external
# consumers (e.g. the Initializ platform) can `go get
# github.com/initializ/forge/<module>@<version>`.
#
# Why this exists: in development, the library modules cross-reference
# each other via `replace ../<path>` directives in their go.mod files
# (and `v0.0.0` placeholder require lines). That's how a `go.work`
# workspace handles the multi-module repo locally. When an external
# consumer fetches a tagged version via the Go module proxy, the
# `replace` directive points at a non-existent local path and the
# `v0.0.0` require can't resolve. The dependency graph (forge-skills <-
# forge-core <- forge-plugins) needs to be expressed with real version
# constraints in the published `go.mod` files.
#
# Strategy: leave main alone. For each release, build an *ephemeral*
# git commit on top of the binary-release tag (`vX.Y.Z`) whose tree
# carries `go.mod` files rewritten with `replace` directives dropped
# and `require` lines bumped to the new version. Path-prefixed tags
# (`forge-core/vX.Y.Z`, `forge-plugins/vX.Y.Z`) point at this
# ephemeral commit; `forge-skills/vX.Y.Z` points at the original
# binary-tag commit (it has no internal deps so no rewrite needed).
# Main never sees the rewritten go.mod files — workspace-mode dev keeps
# working unchanged.
#
# Usage:
#   scripts/release/tag-libraries.sh v0.15.0
#   scripts/release/tag-libraries.sh v0.15.0 --dry-run
#   scripts/release/tag-libraries.sh v0.15.0 --no-push   # tag locally, don't push
#
# Assumptions:
#   - The script is invoked from the repo root.
#   - The binary-release tag `vX.Y.Z` already exists locally (annotated
#     or lightweight). The release.yaml workflow ensures this — it's
#     the trigger.
#   - `go` is on PATH (used for `go mod edit`).
#   - For push mode: the caller has `git push` credentials.

set -euo pipefail

usage() {
    cat <<EOF
Usage: $0 <version> [--dry-run] [--no-push]

Arguments:
  <version>     The release version, e.g. v0.15.0. Must match a tag that
                already exists in this repo.

Flags:
  --dry-run     Show what would happen without creating any commits, tags,
                or pushes.
  --no-push     Create the tags locally but don't push them to origin.

Library modules tagged (in dependency order):
  forge-skills/<version>      no internal deps; tags at the binary-release commit
  forge-core/<version>        depends on forge-skills; tags at an ephemeral commit
  forge-plugins/<version>     depends on forge-core;   tags at an ephemeral commit
EOF
    exit 1
}

# ─── arg parsing ───────────────────────────────────────────────────
if [[ $# -lt 1 ]]; then
    usage
fi

VERSION="$1"
shift

DRY_RUN=0
PUSH=1
for arg in "$@"; do
    case "$arg" in
        --dry-run) DRY_RUN=1 ;;
        --no-push) PUSH=0 ;;
        *) echo "unknown flag: $arg" >&2; usage ;;
    esac
done

if [[ ! "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
    echo "error: version must match vMAJOR.MINOR.PATCH (with optional -prerelease); got: $VERSION" >&2
    exit 1
fi

# ─── helpers ───────────────────────────────────────────────────────
log() { printf "[tag-libraries] %s\n" "$*"; }
run() {
    if [[ $DRY_RUN -eq 1 ]]; then
        printf "[tag-libraries dry-run] %s\n" "$*"
    else
        eval "$@"
    fi
}

# ─── preflight ─────────────────────────────────────────────────────
REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "$REPO_ROOT"

if ! git rev-parse --verify "refs/tags/$VERSION" >/dev/null 2>&1; then
    echo "error: tag $VERSION does not exist locally. Fetch it first or push the binary release tag." >&2
    exit 1
fi

BINARY_TAG_SHA="$(git rev-parse "refs/tags/$VERSION^{commit}")"
log "binary-release tag $VERSION → commit $BINARY_TAG_SHA"

for prefix in forge-skills forge-core forge-plugins; do
    if git rev-parse --verify "refs/tags/$prefix/$VERSION" >/dev/null 2>&1; then
        echo "error: tag $prefix/$VERSION already exists. Delete it before re-running, or pick a different version." >&2
        exit 1
    fi
done

# Make sure we're not on a feature branch with dirty state that would
# get caught up in the worktree we create.
if ! git diff-index --quiet HEAD --; then
    echo "error: working tree has uncommitted changes; commit, stash, or clean before tagging." >&2
    exit 1
fi

# ─── build the ephemeral commit ────────────────────────────────────
# Use a temporary git worktree so we can edit go.mod without touching
# the active checkout (avoids confusing the human running this).
TMP_WORKTREE="$(mktemp -d -t forge-libtag-XXXXXX)"
trap "git worktree remove --force '$TMP_WORKTREE' >/dev/null 2>&1 || true" EXIT

log "preparing ephemeral commit in $TMP_WORKTREE"
run "git worktree add --detach '$TMP_WORKTREE' '$BINARY_TAG_SHA'"

# Rewrite forge-core/go.mod: drop the local-path replace for
# forge-skills, replace the v0.0.0 placeholder require with the real
# version about to be tagged.
log "rewriting forge-core/go.mod (drop replace, require forge-skills@$VERSION)"
run "(cd '$TMP_WORKTREE/forge-core' && \
     go mod edit -dropreplace=github.com/initializ/forge/forge-skills && \
     go mod edit -require=github.com/initializ/forge/forge-skills@$VERSION)"

# Same surgery on forge-plugins/go.mod for forge-core.
log "rewriting forge-plugins/go.mod (drop replace, require forge-core@$VERSION)"
run "(cd '$TMP_WORKTREE/forge-plugins' && \
     go mod edit -dropreplace=github.com/initializ/forge/forge-core && \
     go mod edit -require=github.com/initializ/forge/forge-core@$VERSION)"

# Commit the rewrites in the detached worktree. The commit's parent is
# the binary-release tag so consumers fetching by tag see the full
# history.
log "creating ephemeral release commit"
run "(cd '$TMP_WORKTREE' && \
     git add forge-core/go.mod forge-plugins/go.mod && \
     git -c user.name='forge release bot' -c user.email='release-bot@initializ.ai' \
         commit -m 'release(libs): pin cross-module require lines for $VERSION

Tags forge-core/$VERSION and forge-plugins/$VERSION point at this
commit. forge-skills/$VERSION points at the parent (the $VERSION
binary-release commit). Main is unaffected; the workspace-mode
go.mod files there continue to use replace directives.

Generated by scripts/release/tag-libraries.sh.')"

if [[ $DRY_RUN -eq 1 ]]; then
    log "dry-run: would tag forge-skills/$VERSION at $BINARY_TAG_SHA"
    log "dry-run: would tag forge-core/$VERSION and forge-plugins/$VERSION at the ephemeral commit"
    [[ $PUSH -eq 1 ]] && log "dry-run: would push all three tags to origin"
    exit 0
fi

EPHEMERAL_SHA="$(git -C "$TMP_WORKTREE" rev-parse HEAD)"
log "ephemeral commit SHA: $EPHEMERAL_SHA"

# ─── create the tags ───────────────────────────────────────────────
# forge-skills tags at the binary-release commit — no internal deps,
# no rewrite needed.
log "tagging forge-skills/$VERSION at $BINARY_TAG_SHA"
git tag -a "forge-skills/$VERSION" "$BINARY_TAG_SHA" \
    -m "forge-skills $VERSION

Released as part of forge $VERSION. See CHANGELOG.md for details."

# forge-core + forge-plugins tag at the ephemeral commit.
log "tagging forge-core/$VERSION at $EPHEMERAL_SHA"
git tag -a "forge-core/$VERSION" "$EPHEMERAL_SHA" \
    -m "forge-core $VERSION

Released as part of forge $VERSION. See CHANGELOG.md for details.
Cross-module require pinned to forge-skills@$VERSION."

log "tagging forge-plugins/$VERSION at $EPHEMERAL_SHA"
git tag -a "forge-plugins/$VERSION" "$EPHEMERAL_SHA" \
    -m "forge-plugins $VERSION

Released as part of forge $VERSION. See CHANGELOG.md for details.
Cross-module require pinned to forge-core@$VERSION."

# ─── push ──────────────────────────────────────────────────────────
if [[ $PUSH -eq 1 ]]; then
    log "pushing library tags to origin"
    git push origin \
        "refs/tags/forge-skills/$VERSION" \
        "refs/tags/forge-core/$VERSION" \
        "refs/tags/forge-plugins/$VERSION"
else
    log "skipping push (--no-push). Push manually with:"
    log "  git push origin refs/tags/forge-skills/$VERSION refs/tags/forge-core/$VERSION refs/tags/forge-plugins/$VERSION"
fi

log "done. External consumers can now:"
log "  go get github.com/initializ/forge/forge-skills@$VERSION"
log "  go get github.com/initializ/forge/forge-core@$VERSION"
log "  go get github.com/initializ/forge/forge-plugins@$VERSION"
