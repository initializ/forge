---
name: code-review
category: dev
tags:
  - code-review
  - diff
  - pull-request
  - quality
  - security
description: AI-powered code review for diffs and individual files using LLM analysis
metadata:
  forge:
    requires:
      bins:
        - curl
        - jq
        - git
      env:
        required: []
        one_of:
          - ANTHROPIC_API_KEY
          - OPENAI_API_KEY
        optional:
          - REVIEW_MODEL
          - REVIEW_MAX_DIFF_BYTES
          - GH_TOKEN
          - FORGE_REVIEW_STANDARDS_DIR
    egress_domains:
      - api.anthropic.com
      - api.openai.com
    timeout_hint: 120
---

# Code Review Skill

AI-powered code review that analyzes diffs and individual files for bugs, security issues, style violations, and improvement opportunities. Supports both local git diffs and GitHub pull requests.

## Authentication

Set at least one LLM API key:

```bash
# Option A: Anthropic (preferred)
export ANTHROPIC_API_KEY="sk-ant-..."

# Option B: OpenAI
export OPENAI_API_KEY="sk-..."

# Optional: override the model (defaults: claude-sonnet-4-20250514 / gpt-4o)
export REVIEW_MODEL="claude-sonnet-4-20250514"
```

For GitHub PR review, also set:

```bash
export GH_TOKEN="ghp_..."
```

The token needs **read-only** access:

| Scope (classic PAT) | Fine-grained permission | Why |
|----------------------|------------------------|-----|
| `repo` (or `public_repo` for public repos) | Contents: Read | Fetch PR diff and file contents |

This skill only reads diffs — it never posts comments, applies labels, or merges. For write operations, see the `code-review-github` skill.

## Environment Variables

| Variable | Required | Description |
|----------|----------|-------------|
| ANTHROPIC_API_KEY | one-of | Anthropic API key (preferred) |
| OPENAI_API_KEY | one-of | OpenAI API key (fallback) |
| REVIEW_MODEL | no | Override LLM model name |
| REVIEW_MAX_DIFF_BYTES | no | Max diff size before truncation (default: 100000) |
| GH_TOKEN | no | GitHub token for PR diffs — read-only access is sufficient |
| FORGE_REVIEW_STANDARDS_DIR | no | Path to `.forge-review/standards/` directory for custom rules |

## Tool: code_review_diff

Review a code diff for bugs, security issues, and improvements. Accepts either a GitHub PR URL or a local git base ref.

**Default behavior:** When the user says "review my local changes" or "review changes in <path>" WITHOUT specifying a base branch, default to `base_ref: "HEAD"`. This reviews uncommitted and untracked files — the user's work-in-progress. Do NOT ask the user for a base ref in this case. Only use `main`/`develop` as `base_ref` when the user explicitly says "against main" or "against develop".

**Input:**

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| pr_url | string | no | GitHub PR URL (e.g., `https://github.com/owner/repo/pull/123`) |
| base_ref | string | no | Git ref to compare from. Default: `HEAD` (uncommitted changes). Use `main` only if user says "against main" |
| repo_path | string | no | Absolute path to the local git repository. Required when using `base_ref` |
| focus | string | no | Review focus: `bugs`, `security`, `style`, `all`. Default: `all` |
| extra_context | string | no | Additional context or instructions for the reviewer |

One of `pr_url` or `base_ref` is required. When using `base_ref`, `repo_path` must point to the user's local repository (scripts run in the agent's directory, not the user's project).

**Output:** JSON object with structured review findings.

### Examples

User says → tool input mapping:

| User request | Tool input |
|-------------|------------|
| "Review code changes against main in ~/myproject" | `{"base_ref": "main", "repo_path": "~/myproject"}` |
| "Review my changes since develop in /opt/app" | `{"base_ref": "develop", "repo_path": "/opt/app"}` |
| "Review the last 3 commits in ~/myproject for security issues" | `{"base_ref": "HEAD~3", "repo_path": "~/myproject", "focus": "security"}` |
| "Review changes on the feature/auth branch in ~/myproject" | `{"base_ref": "main", "repo_path": "~/myproject"}` |
| "Review my local changes in ~/myproject" | `{"base_ref": "HEAD", "repo_path": "~/myproject"}` |
| "Review my uncommitted changes in ~/myproject" | `{"base_ref": "HEAD", "repo_path": "~/myproject"}` |
| "Review this PR https://github.com/org/repo/pull/42" | `{"pr_url": "https://github.com/org/repo/pull/42"}` |
| "Security audit on PR 99 in org/repo" | `{"pr_url": "https://github.com/org/repo/pull/99", "focus": "security"}` |

**Important:**
- Pass `repo_path` exactly as the user provides it. The script handles `~` expansion internally — do NOT try to resolve `~` yourself (you do not know the user's home directory). For example, if the user says `~/myproject`, pass `"repo_path": "~/myproject"`.
- For local reviews, `repo_path` is required because the script runs in the agent's working directory, not the user's project directory.
- `base_ref` is the starting point to compare FROM. It is NOT the branch being reviewed — it is the base that changes are measured against. The diff includes both committed and uncommitted (working tree) changes relative to `base_ref`.
- `base_ref` can be a branch name (`main`, `develop`), a commit SHA, or a relative ref (`HEAD~3`). The script uses `git merge-base` to find the fork point, so only changes on the current branch are reviewed.
- When a branch has many commits (e.g., merged PRs), use `HEAD~N` to narrow the review scope to the most recent N commits. For example, `HEAD~5` reviews only the last 5 commits.
- When the user says "review changes on branch X" or "review branch X", they mean: review the changes that branch X introduces. Use `main` (or the repo's default branch) as `base_ref` — NOT the branch name itself.
- When the user says "review my uncommitted changes" or "review my local changes", use `HEAD` as `base_ref` to show only unstaged/staged changes.

### Pre-Flight Scope Check (IMPORTANT)

Before calling `code_review_diff` with a `base_ref`, the agent MUST run `cli_execute` to check the diff scope:

```bash
cd <repo_path> && git log --oneline <base_ref>..HEAD | head -20
```

This shows how many commits will be reviewed. If there are more than ~5 commits:
1. Tell the user: "There are N commits between `<base_ref>` and HEAD. This includes: [list first few commit subjects]."
2. Ask: "Would you like me to review all N commits, or narrow the scope? For example, I can review just the last 3 commits with `HEAD~3`."
3. Only call `code_review_diff` after the user confirms the scope.

This prevents reviewing unrelated changes from older commits or merged PRs on long-lived branches.

### Detection Heuristics

The agent selects this tool when it detects:
- Requests mentioning "review", "diff", "PR", "pull request", "changes"
- A directory path or repository path in the user's message
- GitHub PR URLs pasted in conversation
- Requests to check code quality, find bugs, or audit changes

### Response Format

```json
{
  "summary": "Brief overall assessment",
  "risk_level": "low|medium|high|critical",
  "findings": [
    {
      "file": "path/to/file.go",
      "line": 42,
      "severity": "error|warning|info|nitpick",
      "category": "bug|security|style|performance|maintainability",
      "title": "Short finding title",
      "description": "Detailed explanation of the issue",
      "suggestion": "Suggested fix or improvement"
    }
  ],
  "stats": {
    "files_reviewed": 5,
    "total_findings": 3,
    "by_severity": {"error": 1, "warning": 1, "nitpick": 1}
  }
}
```

### Tips

- Use `focus: security` for security-focused audits
- Use `base_ref: main` to review all uncommitted changes against main
- Large diffs are automatically truncated at `REVIEW_MAX_DIFF_BYTES` (default 100KB)
- Set `FORGE_REVIEW_STANDARDS_DIR` to apply org-specific coding standards

## Tool: code_review_file

Deep review of a single file with full context (not just diff). Useful for thorough analysis of critical files.

**Input:**

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| file_path | string | yes | Path to the file to review (relative to repo root, or absolute path) |
| repo_path | string | no | Absolute path to the local repository. Required for local file review (scripts run in the agent's directory, not the user's project) |
| pr_url | string | no | If set, fetches the file from the PR head branch |
| focus | string | no | Review focus: `bugs`, `security`, `style`, `all`. Default: `all` |
| extra_context | string | no | Additional context or instructions for the reviewer |

**Output:** Same JSON structure as `code_review_diff`.

### Examples

User says → tool input mapping:

| User request | Tool input |
|-------------|------------|
| "Review the file src/server.go in ~/myproject" | `{"file_path": "src/server.go", "repo_path": "~/myproject"}` |
| "Security audit on cmd/main.go in /opt/app" | `{"file_path": "cmd/main.go", "repo_path": "/opt/app", "focus": "security"}` |
| "Review auth.py from PR #42 in org/repo" | `{"file_path": "auth.py", "pr_url": "https://github.com/org/repo/pull/42"}` |

**Important:** For local file review, `repo_path` is required. `file_path` is relative to the repo root. Resolve `~` to the absolute home directory path.

### Detection Heuristics

The agent selects this tool when it detects:
- Requests to "review this file", "audit file", "check file for issues"
- A specific file path mentioned with a repository/directory path
- Single-file deep review requests (as opposed to diff-level review)

### Tips

- Use this for critical files that need thorough review beyond just changes
- Combine with `code_review_diff` for comprehensive PR review: diff-level first, then deep-dive on flagged files
- Works with any text file: source code, configs, scripts, IaC templates

## Safety Constraints

- Never executes code from the diff or reviewed files
- API keys are passed via environment variables, never logged or included in output
- Diff content is sent to the configured LLM API for analysis (respects `egress_domains`)
- Large diffs are truncated, not streamed, to prevent excessive API costs
- No filesystem modifications: read-only analysis only
