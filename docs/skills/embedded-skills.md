---
title: "Embedded Skills"
description: "Built-in skills that ship with Forge: GitHub, Tavily, Kubernetes, codegen, and more."
order: 1
---

## Built-in Skills

| Skill | Icon | Category | Description | Scripts |
|-------|------|----------|-------------|---------|
| `github` | 🐙 | developer | Clone repos, create issues/PRs, query GitHub API, and manage git workflows | `github-clone.sh`, `github-checkout.sh`, `github-commit.sh`, `github-push.sh`, `github-create-pr.sh`, `github-status.sh`, `github-list-prs.sh`, `github-get-user.sh`, `github-list-stargazers.sh`, `github-list-forks.sh`, `github-pr-author-profiles.sh`, `github-stargazer-profiles.sh` |
| `code-agent` | 🤖 | developer | Autonomous code generation, modification, and project scaffolding | — (builtin tools) |
| `weather` | 🌤️ | utilities | Get weather data for a location | — (binary-backed) |
| `tavily-search` | 🔍 | research | Search the web using Tavily AI search API | `tavily-search.sh` |
| `tavily-research` | 🔬 | research | Deep multi-source research via Tavily API | `tavily-research.sh`, `tavily-research-poll.sh` |
| `k8s-incident-triage` | ☸️ | sre | Read-only Kubernetes incident triage using kubectl | — (binary-backed) |
| `k8s-cost-visibility` | 💰 | sre | Estimate K8s infrastructure costs (compute, storage, LoadBalancer) with cost attribution reports | `k8s-cost-visibility.sh` |
| `k8s-pod-rightsizer` | ⚖️ | sre | Analyze workload metrics and produce CPU/memory rightsizing recommendations | — (binary-backed) |
| `code-review` | 🔎 | developer | AI-powered code review for diffs and files (supports Anthropic API, OpenAI Chat Completions, and OpenAI Responses/Codex API with streaming) | `code-review-diff.sh`, `code-review-file.sh` |
| `code-review-standards` | 📏 | developer | Initialize and manage code review standards | — (template-based) |
| `code-review-github` | 🐙 | developer | Post code review results to GitHub PRs | — (binary-backed) |
| `codegen-react` | ⚛️ | developer | Scaffold and iterate on Vite + React apps | `codegen-react-scaffold.sh`, `codegen-react-read.sh`, `codegen-react-write.sh`, `codegen-react-run.sh` |
| `codegen-html` | 🌐 | developer | Scaffold standalone Preact + HTM apps (zero dependencies) | `codegen-html-scaffold.sh`, `codegen-html-read.sh`, `codegen-html-write.sh` |

### Tavily Research Skill

The `tavily-research` skill demonstrates the **async two-tool pattern** for long-running operations:

```bash
forge skills add tavily-research
```

This registers two tools:

| Tool | Purpose | Behavior |
|------|---------|----------|
| `tavily_research` | Submit a research query | Returns immediately with a `request_id` |
| `tavily_research_poll` | Wait for results | Polls internally for up to ~5 minutes, returns complete report |

The LLM uses them in sequence: submit the research request, inform the user that research is in progress, then call the poll tool which handles all waiting internally. The complete report (1000-3000 words with sources) is returned to the LLM and delivered to the user.

**Research models:**

| Model | Speed | Use Case |
|-------|-------|----------|
| `mini` | ~30s | Quick overviews, simple topics |
| `pro` | ~300s | Comprehensive analysis, complex topics |
| `auto` | Varies | Let the API choose based on query complexity |

Requires: `curl`, `jq`, `TAVILY_API_KEY` environment variable.

### Kubernetes Incident Triage Skill

The `k8s-incident-triage` skill performs read-only triage of Kubernetes workloads using `kubectl`:

```bash
forge skills add k8s-incident-triage
```

This registers a single tool:

| Tool | Purpose | Behavior |
|------|---------|----------|
| `k8s_triage` | Diagnose unhealthy workloads, pods, or namespaces | Runs read-only kubectl commands, produces a structured triage report |

The skill accepts two input modes:

- **Human mode** — natural language like `"triage payments-prod"` or `"why are pods pending in checkout-prod?"`
- **Automation mode** — structured JSON with namespace, workload, pod, and diagnostic options

**Triage process:**

1. Verify cluster access (kubectl version, cluster-info)
2. Fast health snapshot (pods, deployments, statefulsets)
3. Events timeline (FailedScheduling, probe failures, evictions)
4. Describe pods & workloads (container state, restart counts, probes)
5. Node diagnostics (optional — NotReady, memory/disk pressure)
6. Logs (optional — with previous container logs for CrashLoopBackOff)
7. Metrics (optional — via metrics-server)

**Detection heuristics** classify issues into: CrashLoop, OOMKilled, Image Pull Failure, Scheduling Constraint, Probe Failure, PVC/Volume Failure, Node Pressure/Eviction, Rollout Stuck. Each finding includes a hypothesis, evidence, confidence score (0.0-1.0), and recommended next commands.

**Safety:** This skill is strictly read-only. It never executes `apply`, `patch`, `delete`, `exec`, `port-forward`, `scale`, or `rollout restart`. It never prints Secret values.

Requires: `kubectl`, optional `KUBECONFIG`, `K8S_API_DOMAIN`, `DEFAULT_NAMESPACE` environment variables.

### Kubernetes Pod Rightsizer Skill

The `k8s-pod-rightsizer` skill analyzes real workload metrics (Prometheus or metrics-server fallback) and produces policy-constrained CPU/memory rightsizing recommendations:

```bash
forge skills add k8s-pod-rightsizer
```

This skill operates in three modes:

| Mode | Purpose | Mutates Cluster |
|------|---------|-----------------|
| `dry-run` | Report recommendations only (default) | No |
| `plan` | Generate strategic merge patch YAMLs | No |
| `apply` | Execute patches with rollback bundle | Yes (requires `i_accept_risk: true`) |

**Key features:**

- Deterministic formulas — no LLM-based guessing for recommendations
- Policy model with per-namespace and per-workload overrides (safety factors, min/max bounds, step constraints)
- Prometheus p95 metrics with metrics-server fallback
- Automatic rollback bundle generation in apply mode
- Workload classification: over-provisioned, under-provisioned, right-sized, limit-bound, insufficient-data

**Apply workflow:** The skill's built-in `mode=apply` handles rollback bundles, strategic merge patches via `kubectl patch`, and rollout verification. Do not manually run `kubectl apply -f` — use `mode=apply` with `i_accept_risk: true` instead.

Requires: `bash`, `kubectl`, `jq`, `curl`. Optional: `KUBECONFIG`, `K8S_API_DOMAIN`, `PROMETHEUS_URL`, `PROMETHEUS_TOKEN`, `POLICY_FILE`, `DEFAULT_NAMESPACE`.

### Kubernetes Cost Visibility Skill

The `k8s-cost-visibility` skill estimates Kubernetes infrastructure costs by querying cluster node, pod, PVC/PV, and LoadBalancer data via `kubectl`, applying cloud pricing models, and producing cost attribution reports:

```bash
forge skills add k8s-cost-visibility
```

This registers a single tool:

| Tool | Purpose | Behavior |
|------|---------|----------|
| `k8s_cost_visibility` | Estimate cluster costs and produce attribution reports | Queries nodes, pods, PVCs, PVs, and services; applies pricing; returns cost breakdown |

**Cost dimensions tracked:**

| Dimension | Source | Default Rate |
|-----------|--------|-------------|
| Compute (CPU + memory) | Node instance types, pod resource requests | Auto-detected from cloud CLI or $0.031611/vCPU-hr |
| Storage (PVC/PV) | PVC capacities, storage classes | $0.10/GiB/month |
| LoadBalancer | Services with `type: LoadBalancer` | $18.25/month each |
| Waste | Unbound Persistent Volumes | Flagged with estimated monthly waste |

**Grouping modes:** `namespace` (includes storage + LB columns), `workload`, `node`, `label:<key>`, `annotation:<key>`.

**Pricing modes:** `auto` (detect cloud CLI), `aws`, `gcp`, `azure`, `static` (built-in rates), `custom:<file.json>` (user-provided rates).

**Safety:** This skill is strictly read-only. It only uses `kubectl get` commands (nodes, pods, pvc, pv, svc) — never `apply`, `delete`, `patch`, `exec`, or `scale`.

Requires: `kubectl`, `jq`, `awk`, `bc`. Optional: `KUBECONFIG`, `K8S_API_DOMAIN`, `DEFAULT_NAMESPACE`, `AWS_REGION`, `AZURE_SUBSCRIPTION_ID`, `GCP_PROJECT`.

### Codegen React Skill

The `codegen-react` skill scaffolds and iterates on **Vite + React** applications with Tailwind CSS:

```bash
forge skills add codegen-react
```

This registers four tools:

| Tool | Purpose | Behavior |
|------|---------|----------|
| `codegen_react_scaffold` | Create a new project | Generates package.json, Vite config, React components with Tailwind CSS and Forge dark theme |
| `codegen_react_run` | Start the dev server | Runs `npm install` + `npm run dev`, auto-opens browser, returns server URL and PID |
| `codegen_react_read` | Read project files | Returns file content or directory listing (excludes `node_modules/`, `.git/`) |
| `codegen_react_write` | Write/update files | Creates or updates files with path traversal prevention; Vite hot-reloads automatically |

**Iteration workflow:**

1. Scaffold the project with `codegen_react_scaffold`
2. Start the dev server with `codegen_react_run` — installs deps, opens browser
3. Read/write files with `codegen_react_read` / `codegen_react_write` — Vite hot-reloads on save
4. Repeat step 3 to iterate on the UI

**Scaffold output:** `package.json` (React 19, Vite 6), `vite.config.js`, `index.html` (with Tailwind CDN), `src/main.jsx`, `src/App.jsx` (Tailwind utility classes), `src/App.css`, `.gitignore`.

**Safety:** Output directories must be under `$HOME` or `/tmp`. Path traversal (`..`, absolute paths) is rejected. Non-empty directories require `force: true`.

Requires: `node`, `npx`, `jq`. Egress: `registry.npmjs.org`, `cdn.jsdelivr.net`, `cdn.tailwindcss.com`.

### Codegen HTML Skill

The `codegen-html` skill scaffolds standalone **Preact + HTM** applications with zero local dependencies:

```bash
forge skills add codegen-html
```

This registers three tools:

| Tool | Purpose | Behavior |
|------|---------|----------|
| `codegen_html_scaffold` | Create a new project | Generates HTML with Preact + HTM via CDN and Tailwind CSS; supports single-file and multi-file modes |
| `codegen_html_read` | Read project files | Returns file content or directory listing |
| `codegen_html_write` | Write/update files | Creates or updates files with path traversal prevention |

**Two scaffold modes:**

| Mode | Files | Use Case |
|------|-------|----------|
| `single-file` | One `index.html` with inline JS | Quick prototypes, shareable demos |
| `multi-file` | `index.html`, `app.js`, `components/Counter.js` | Larger apps with component separation |

**Key differences from codegen-react:** No Node.js required. No build step. No `npm install`. Just open `index.html` in a browser. Uses `class` (not `className`) since HTM maps directly to DOM attributes.

**Safety:** Same restrictions as codegen-react — output under `$HOME` or `/tmp`, path traversal prevention, `force: true` for non-empty directories.

Requires: `jq`. Egress: `cdn.tailwindcss.com`, `esm.sh`.

### GitHub Skill

The `github` skill provides a complete git + GitHub workflow through script-backed tools:

```bash
forge skills add github
```

This registers fourteen tools:

| Tool | Purpose |
|------|---------|
| `github_clone` | Clone a repository and create a feature branch |
| `github_checkout` | Switch to or create a branch |
| `github_status` | Show git status for a cloned project |
| `github_commit` | Stage and commit changes |
| `github_push` | Push a feature branch to the remote |
| `github_create_pr` | Create a pull request |
| `github_create_issue` | Create a GitHub issue |
| `github_list_issues` | List open issues for a repository |
| `github_list_prs` | List pull requests with state filter and pagination |
| `github_get_user` | Get a GitHub user's public profile |
| `github_list_stargazers` | List stargazers for a repository with pagination |
| `github_list_forks` | List forks of a repository with pagination |
| `github_pr_author_profiles` | List PR authors and fetch their full profiles (compound 2-step) |
| `github_stargazer_profiles` | List stargazers and fetch their full profiles (compound 2-step) |

**Workflow:** Clone → explore → edit → status → commit → push → create PR. The skill's system prompt enforces this sequence and prevents raw `git` commands via `cli_execute`.

**Pagination:** List tools (`github_list_prs`, `github_list_stargazers`, `github_list_forks`, `github_pr_author_profiles`, `github_stargazer_profiles`) support `page` (1-based) and `per_page` (default 30, max 100) parameters. Responses include `pagination.has_next_page` to indicate more results are available.

**PII exemption:** Profile-returning tools (`github_get_user`, `github_pr_author_profiles`, `github_stargazer_profiles`) are pre-configured in the default policy scaffold's `no_pii` `allow_tools` list, so they can return public profile data (emails, bios) without triggering PII guardrails. See [Per-Tool PII Exemptions](../security/guardrails.md#per-tool-pii-exemptions).

Requires: `gh`, `git`, `jq`. Optional: `GH_TOKEN`. Egress: `api.github.com`, `github.com`.

### Code-Agent Skill

The `code-agent` skill enables autonomous code generation and modification using [builtin code-agent tools](../core-concepts/tools-and-builtins.md#code-agent-tools):

```bash
forge skills add code-agent
```

This registers eight tools:

| Tool | Purpose |
|------|---------|
| `code_agent_scaffold` | Bootstrap a new project (Vite, Express, FastAPI, Go, Spring Boot, etc.) |
| `code_agent_write` | Create or update files |
| `code_agent_edit` | Surgical text replacement in existing files |
| `code_agent_read` | Read a file or list directory contents |
| `code_agent_run` | Install dependencies, start a server, open a browser |
| `grep_search` | Search file contents by regex |
| `glob_search` | Find files by name pattern |
| `directory_tree` | Show project directory tree |

The skill uses **denied tools** (`file_write`, `file_edit`, `file_patch`, `file_read`, `schedule_*`) to ensure the LLM uses the skill's own tool wrappers instead of raw builtins. All file operations are confined to the agent's working directory via `PathValidator`.

Requires: `bash`, `jq`. Egress: `registry.npmjs.org`, `cdn.tailwindcss.com`, `pypi.org`, `files.pythonhosted.org`, `proxy.golang.org`, `sum.golang.org`, `storage.googleapis.com`, `repo.maven.apache.org`, `repo1.maven.org`.
