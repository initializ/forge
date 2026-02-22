---
name: github
description: GitHub integration skill
metadata:
  forge:
    requires:
      bins:
        - gh
      env:
        required:
          - GH_TOKEN
        one_of: []
        optional: []
---
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
