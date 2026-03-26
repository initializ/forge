---
name: github
icon: 🐙
category: developer
tags:
  - github
  - issues
  - pull-requests
  - repositories
  - git
  - stargazers
  - forks
  - users
description: Create issues, PRs, clone repos, and manage git workflows
metadata:
  forge:
    workflow_phase: finalize
    requires:
      bins:
        - gh
        - git
        - jq
      env:
        required: []
        one_of: []
        optional:
          - GH_TOKEN
    egress_domains:
      - api.github.com
      - github.com
---
## System Prompt

You have access to GitHub and git tools. You MUST use these tools for all git and GitHub operations. Do NOT use cli_execute or bash to run git commands directly.

**When asked to clone, checkout, or work with a GitHub repository, ALWAYS start by calling `github_clone`.** This is the ONLY way to clone repositories. Do NOT attempt to use cli_execute, bash, or any other tool to run `git clone` directly.

The `repo` parameter accepts any of these formats:
- `owner/repo` (e.g. `initializ-mk/openclaw`)
- SSH URL: `git@github.com:owner/repo.git`
- HTTPS URL: `https://github.com/owner/repo.git`

**Required workflow for code changes:**

1. `github_clone` — clone the repository (auto-creates a feature branch)
2. Explore: use `directory_tree`, `grep_search`, `glob_search`, `code_agent_read` to understand the codebase
3. Edit: use `code_agent_edit` or `code_agent_write` to make changes
4. `github_status` — review what changed before committing
5. `github_commit` — stage and commit changes
6. `github_push` — push the feature branch to remote
7. `github_create_pr` — create a pull request

**File path convention:**
- `github_clone` clones repos into `workspace/`. The returned `project_dir` (e.g. `openclaw`) is the directory name inside `workspace/`.
- ALL tools that accept `project_dir` (github tools, code-agent tools) accept BOTH `openclaw` and `workspace/openclaw` — the `workspace/` prefix is stripped automatically.
- For `directory_tree`, `grep_search`, `glob_search` use `workspace/<project_dir>` as the `path` (e.g. `workspace/openclaw`).

**You MUST complete the entire workflow — do NOT stop after exploring.**
When asked to fix a bug or make changes, you must: explore → understand → edit → commit → push → create PR. Do NOT stop after step 2 to report findings. Complete ALL steps in ONE session. Only stop early if you genuinely cannot determine what to change.

**Exploration strategy — bug fixes:**
1. `directory_tree` to understand project structure.
2. `grep_search` for the error message, config key, or symptom from the bug description.
3. **Trace to the origin:** follow the error/value through call sites until you find where it is first produced or validated. Do not stop at the first file that mentions the symptom.
4. **Read what you will change:** before editing a function, read its implementation. Before replacing a function call, read both the old and new function to confirm the new one handles the same inputs correctly.
5. **Find a working reference:** if similar functionality works elsewhere in the codebase (e.g., another provider, another endpoint), read how it handles the same input. Replicate that approach, not a different one.
6. Form your hypothesis with evidence, then edit.
7. **Verify your fix:** after editing, trace the specific failing input through your new code path. Read the functions your new code calls and confirm they handle the input type that was failing (e.g., objects, not just strings). If your fix adds types but doesn't change runtime behavior, it is wrong.

**Exploration strategy — features and refactors:**
1. `directory_tree` to understand project structure.
2. `grep_search` for existing patterns similar to what you need to add (2-3 searches).
3. Read the file(s) where you will add or modify code.
4. Follow existing conventions, then edit immediately.

**Do NOT:**
- Edit test files first — always fix the source code first, then update tests to match
- Read files unrelated to the error path or the code you plan to change
- Pattern-match on function names without reading their implementations
- Replace a function call with another without verifying both handle the same input types (e.g., objects vs strings)
- Keep searching after you have traced the error to its origin or found the insertion point
- Consider a fix complete without tracing the failing input through the new code to confirm it reaches the correct code path

**Branch safety rules:**
- All work happens on feature branches — never on main/master.
- `github_clone` automatically creates a feature branch after cloning.
- `github_commit`, `github_push`, and `github_checkout` refuse to operate on main/master.
- Always use `github_status` before committing to review what changed.

**Pagination:**
For tools that return lists (`github_list_prs`, `github_list_stargazers`, `github_list_forks`, `github_pr_author_profiles`, `github_stargazer_profiles`), use `page` (1-based) and `per_page` (default 30, max 100) parameters. The response includes a `pagination` object with `has_next_page` — if true, increment `page` to fetch the next batch.

## Tool: github_clone

Clone a GitHub repository and create a feature branch.

**Input:** repo (string: owner/repo, SSH URL, or HTTPS URL), branch (string, optional: branch name — auto-generated if omitted), project_dir (string, optional: directory name — defaults to repo name)
**Output:** `{status, repo, branch, project_dir}`

## Tool: github_status

Show git status for a cloned project.

**Input:** project_dir (string: project directory name)
**Output:** `{branch, modified[], staged[], untracked[], ahead, behind}`

## Tool: github_commit

Stage and commit changes on a feature branch. Refuses to commit on main/master.

**Input:** project_dir (string), message (string: commit message), files (string[], optional: specific files to stage — stages all if omitted)
**Output:** `{sha, branch, files_changed}`

## Tool: github_push

Push a feature branch to the remote. Refuses to push main/master.

**Input:** project_dir (string), branch (string, optional: defaults to current branch)
**Output:** `{status, branch, sha, remote}`

## Tool: github_checkout

Switch to or create a branch. Refuses to switch to main/master.

**Input:** project_dir (string), branch (string: target branch name), create (boolean, optional: create new branch — default false)
**Output:** `{status, branch}`

## Tool: github_create_issue

Create a GitHub issue.

**Input:** repo (string), title (string), body (string)
**Output:** Issue URL

## Tool: github_list_issues

List open issues for a repository.

**Input:** repo (string), state (string: open/closed)
**Output:** List of issues with number, title, and state

## Tool: github_create_pr

Create a pull request.

**Input:** repo (string), title (string), body (string), head (string), base (string)
**Output:** Pull request URL

## Tool: github_list_prs

List pull requests for a repository with pagination.

**Input:** repo (string: owner/repo, SSH URL, or HTTPS URL), state (string: open/closed/all, default: open), page (int, default: 1), per_page (int, default: 30, max: 100)
**Output:** `{repo, state, pull_requests: [{number, title, state, user, created_at, updated_at, head_ref, base_ref, url}], pagination: {page, per_page, count, has_next_page}}`

## Tool: github_get_user

Get a GitHub user's public profile information.

**Input:** username (string: GitHub username)
**Output:** `{login, name, email, bio, company, location, blog, public_repos, followers, following, created_at, url}`

## Tool: github_list_stargazers

List stargazers (users who starred) for a repository with pagination.

**Input:** repo (string: owner/repo, SSH URL, or HTTPS URL), page (int, default: 1), per_page (int, default: 30, max: 100)
**Output:** `{repo, stargazers: [{login, url}], pagination: {page, per_page, count, has_next_page}}`

## Tool: github_list_forks

List forks of a repository with pagination.

**Input:** repo (string: owner/repo, SSH URL, or HTTPS URL), sort (string: newest/oldest/stargazers, default: newest), page (int, default: 1), per_page (int, default: 30, max: 100)
**Output:** `{repo, forks: [{full_name, owner, created_at, updated_at, stargazers_count, url}], pagination: {page, per_page, count, has_next_page}}`

## Tool: github_pr_author_profiles

List PR authors and fetch their full profiles (compound 2-step tool). First fetches PRs, then fetches the profile of each unique author.

**Input:** repo (string: owner/repo, SSH URL, or HTTPS URL), state (string: open/closed/all, default: open), page (int, default: 1), per_page (int, default: 30, max: 100)
**Output:** `{repo, state, profiles: [{login, name, email, bio, company, location, blog, public_repos, followers, following, created_at, url, pr_count}], total_prs_scanned, unique_authors, pagination: {page, per_page, count, has_next_page}}`

## Tool: github_stargazer_profiles

List stargazers and fetch their full profiles (compound 2-step tool). First fetches stargazers, then fetches the profile of each unique user.

**Input:** repo (string: owner/repo, SSH URL, or HTTPS URL), page (int, default: 1), per_page (int, default: 30, max: 100)
**Output:** `{repo, profiles: [{login, name, email, bio, company, location, blog, public_repos, followers, following, created_at, url}], total_stargazers_scanned, unique_users, pagination: {page, per_page, count, has_next_page}}`
