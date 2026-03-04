---
name: code-review-github
category: developer
tags:
  - code-review
  - github
  - pull-request
  - ci
  - automation
description: GitHub PR workflow orchestration for code review — list PRs, post comments, apply labels, and guarded auto-merge
metadata:
  forge:
    requires:
      bins:
        - gh
        - jq
      env:
        required:
          - GH_TOKEN
        one_of: []
        optional:
          - REVIEW_AUTO_MERGE
          - REVIEW_LABEL_PREFIX
    egress_domains:
      - api.github.com
      - github.com
    denied_tools:
      - http_request
      - web_search
---

# Code Review GitHub Integration

Orchestrates GitHub PR workflows for AI code review. Provides tools to list pull requests, post inline review comments, apply labels based on review findings, and (with explicit opt-in) auto-merge clean PRs.

All GitHub API interactions go through the `gh` CLI. Direct HTTP requests and web searches are denied to ensure all operations are auditable and respect GitHub's rate limiting.

## Authentication

```bash
export GH_TOKEN="ghp_..."
```

The token needs **read-write** access since this skill posts comments, applies labels, and can merge PRs:

| Scope (classic PAT) | Fine-grained permission | Why |
|----------------------|------------------------|-----|
| `repo` | Pull requests: Read & Write | List PRs, post review comments, merge |
| `repo` | Contents: Read | Read PR diff and file contents |
| `repo` | Issues: Write | Apply and remove labels |
| `read:org` (optional) | Organization: Read | Org-level label policies |

**Minimum fine-grained token permissions:** `pull_requests: write`, `contents: read`, `issues: write` on the target repository.

For read-only review without GitHub interaction, use the `code-review` skill instead — it only needs read access.

## Environment Variables

| Variable | Required | Description |
|----------|----------|-------------|
| GH_TOKEN | yes | GitHub personal access token or fine-grained token |
| REVIEW_AUTO_MERGE | no | Set to `true` to enable guarded auto-merge. Default: disabled |
| REVIEW_LABEL_PREFIX | no | Prefix for review labels (default: `review/`). Labels: `review/approved`, `review/needs-changes`, `review/security-concern` |

## Tool: review_github_list_prs

List open pull requests for a repository, optionally filtered by author or label.

**Input:**

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| repo | string | yes | Repository in `owner/repo` format |
| author | string | no | Filter by PR author username |
| label | string | no | Filter by label |
| limit | integer | no | Maximum PRs to return (default: 10, max: 100) |

**Output:** JSON array of pull requests with `number`, `title`, `author`, `url`, `labels`, `created_at`, `updated_at`, `mergeable_state`.

### Detection Heuristics

The agent selects this tool when it detects:
- Requests to "list PRs", "show open pull requests", "what PRs need review"
- Requests to find PRs by a specific author or with specific labels

## Tool: review_github_post_comments

Post inline review comments on a GitHub pull request based on code review findings.

**Input:**

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| repo | string | yes | Repository in `owner/repo` format |
| pr_number | integer | yes | Pull request number |
| findings | array | yes | Array of findings from `code_review_diff` output |
| submit_review | boolean | no | Submit as a formal review (not just individual comments). Default: true |
| review_event | string | no | Review event type: `COMMENT`, `APPROVE`, `REQUEST_CHANGES`. Default: `COMMENT` |

Each finding in the array should have: `file`, `line`, `severity`, `title`, `description`, `suggestion`.

**Output:** JSON object with `comments_posted`, `review_url`, and any `errors` for comments that failed.

### Detection Heuristics

The agent selects this tool when it detects:
- Requests to "post review comments", "comment on the PR", "submit review"
- After running `code_review_diff`, user asks to post findings to GitHub

### Comment Format

Comments are posted as inline review comments with this format:

```
**[severity] category: title**

description

💡 **Suggestion:** suggestion
```

## Tool: review_github_apply_labels

Apply review-status labels to a pull request based on review findings.

**Input:**

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| repo | string | yes | Repository in `owner/repo` format |
| pr_number | integer | yes | Pull request number |
| risk_level | string | yes | Overall risk level from review: `low`, `medium`, `high`, `critical` |
| has_security_findings | boolean | no | Whether security issues were found. Default: false |

**Output:** JSON object with `labels_applied` and `labels_removed`.

### Label Mapping

| Risk Level | Label Applied |
|------------|---------------|
| low | `review/approved` |
| medium | `review/needs-changes` |
| high | `review/needs-changes` |
| critical | `review/security-concern` |

If `has_security_findings` is true, `review/security-concern` is always applied regardless of risk level.

Previous review labels (with the configured prefix) are removed before applying new ones.

### Detection Heuristics

The agent selects this tool when it detects:
- Requests to "label the PR", "apply review labels", "mark PR as approved"
- After completing a review, user asks to update PR status

## Tool: review_github_auto_merge

Merge a pull request after verifying safety conditions. Requires explicit opt-in via `REVIEW_AUTO_MERGE=true`.

**Input:**

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| repo | string | yes | Repository in `owner/repo` format |
| pr_number | integer | yes | Pull request number |
| merge_method | string | no | Merge method: `merge`, `squash`, `rebase`. Default: `squash` |
| require_clean_review | boolean | no | Require no error-severity findings before merging. Default: true |
| review_result | object | no | Review result from `code_review_diff` to verify cleanliness |

**Output:** JSON object with `merged`, `sha`, `message`, or `blocked_reason` if merge was prevented.

### Detection Heuristics

The agent selects this tool when it detects:
- Requests to "merge the PR", "auto-merge if clean"
- Requests to "merge after review passes"

### Safety Guards

This tool enforces multiple safety layers:

1. **Env var gate:** `REVIEW_AUTO_MERGE` must be explicitly set to `true`. If unset or any other value, merge is blocked with a clear message.
2. **Clean review required:** By default, the PR must have no `error`-severity findings. Override with `require_clean_review: false`.
3. **CI checks:** All required status checks must pass (enforced by GitHub's branch protection).
4. **No dismissed reviews:** The tool never dismisses existing review requests or approvals.
5. **Merge conflicts:** GitHub API rejects merges with conflicts — the tool surfaces this clearly.

## Workflow: Full PR Review Cycle

A typical end-to-end review workflow using all three code-review skills:

1. **List PRs** → `review_github_list_prs` to find PRs needing review
2. **Run review** → `code_review_diff` with the PR URL to generate findings
3. **Post comments** → `review_github_post_comments` to post findings as inline comments
4. **Apply labels** → `review_github_apply_labels` to set status labels
5. **Auto-merge** (optional) → `review_github_auto_merge` if review is clean and opt-in is enabled

## Safety Constraints

This skill MUST:

- Never merge without `REVIEW_AUTO_MERGE=true` explicitly set
- Never dismiss existing reviews or review requests
- Never force-push or modify PR branch contents
- Never delete branches (leave that to GitHub's auto-delete setting)
- Never bypass branch protection rules
- Never approve its own PRs (if the token belongs to the PR author)
- Post comments as the authenticated user — never impersonate
- All GitHub operations go through `gh` CLI — `http_request` and `web_search` tools are denied
- Rate-limit awareness: back off on 403/429 responses from GitHub API

## Autonomous Compatibility

This skill is designed to work in automated pipelines:

- All inputs and outputs are structured JSON
- Error states return JSON with `error` field and descriptive messages
- Idempotent: re-running label application or comment posting produces consistent results
- Labels are prefix-scoped to avoid conflicting with other label systems
