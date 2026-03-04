---
name: codegen-html
icon: 🌐
category: developer
tags:
  - code-generation
  - frontend
  - html
  - preact
  - ui
  - zero-dependency
description: Scaffold and iterate on standalone Preact + HTM applications with zero build dependencies
metadata:
  forge:
    requires:
      bins:
        - jq
      env:
        required: []
        one_of: []
        optional: []
    egress_domains:
      - cdn.tailwindcss.com
      - esm.sh
    timeout_hint: 60
---

# Codegen HTML Skill

Scaffold and iteratively build standalone HTML applications using Preact + HTM via CDN. Zero local dependencies — no Node.js, no npm, no build step. Just open the HTML file in a browser.

## Quick Start

```bash
# Scaffold a single-file app
./scripts/codegen-html-scaffold.sh '{"project_name": "my-app", "output_dir": "/tmp/my-app", "mode": "single-file"}'

# Scaffold a multi-file app
./scripts/codegen-html-scaffold.sh '{"project_name": "my-app", "output_dir": "/tmp/my-app", "mode": "multi-file"}'

# Read a file or list the project
./scripts/codegen-html-read.sh '{"project_dir": "/tmp/my-app", "file_path": "."}'

# Write/update a file
./scripts/codegen-html-write.sh '{"project_dir": "/tmp/my-app", "file_path": "index.html", "content": "..."}'
```

## CRITICAL: Scaffold Conventions (DO NOT VIOLATE)

These rules prevent runtime errors:

1. **Use `class` (not `className`)** — HTM maps directly to DOM attributes
2. **Use Tailwind CSS utility classes** for all styling (loaded via CDN in `index.html`)
3. **Do NOT create `<style>` blocks or `.css` files** — use Tailwind classes instead
4. **Keep Preact/HTM imports unchanged** — do not change CDN URLs or versions
5. **Use named exports** for components: `export function ComponentName() {}`

## Code Style Guide

- Use **Preact** with **HTM** (tagged template literals instead of JSX)
- Use `class` (not `className`) — HTM maps directly to DOM attributes
- Template literal syntax: `` html`<div class="app">${content}</div>` ``
- Use `useState`, `useEffect`, `useRef` from Preact hooks
- Components are plain functions returning `` html`...` `` template literals
- All imports via CDN (`esm.sh`), pinned to specific versions
- Use **Tailwind CSS utility classes** — the CDN is pre-loaded in `index.html`
- Forge dark theme colors: `bg-zinc-950` (bg), `bg-zinc-900` (cards), `border-zinc-800` (borders), `text-zinc-200` (text), `text-zinc-400` (muted), `bg-indigo-500` (accent)

## Tailwind CSS Reference

Tailwind is loaded via CDN. Use utility classes directly in `class` attributes:

- **Layout:** `flex`, `grid`, `gap-4`, `max-w-4xl`, `mx-auto`, `px-6`, `py-12`
- **Colors:** `bg-zinc-950`, `bg-zinc-900`, `text-zinc-200`, `text-zinc-400`, `bg-indigo-500`
- **Borders:** `border`, `border-zinc-800`, `rounded-lg`, `shadow-lg`
- **Typography:** `text-3xl`, `font-bold`, `text-center`, `font-mono`
- **Interactive:** `hover:bg-indigo-400`, `transition-colors`, `cursor-pointer`

Do NOT write custom CSS when Tailwind utilities exist.

## Safety Constraints

- Output directory must be under `$HOME` or `/tmp`
- Non-empty directories require `force: true` to overwrite
- Path traversal (`..`, absolute paths) is rejected in read/write operations
- No local tooling required — files can be opened directly in a browser

## Iteration Workflow

1. **Scaffold** the project with `codegen_html_scaffold`
2. **Read** files to understand current state with `codegen_html_read`
3. **Write** updated files with `codegen_html_write`
4. Repeat steps 2-3 to iterate on the UI

After scaffolding, the user can simply open `index.html` in a browser. No install or build step needed.

## When to Use

Use `codegen-html` when the user wants:
- A quick UI prototype with no setup
- A single HTML file they can share or open directly
- Zero-dependency frontend (no Node.js required)
- Simple interactive apps, dashboards, or demos

For full React apps with build tooling and npm packages, use `codegen-react` instead.

## Tool: codegen_html_scaffold

Create a new Preact + HTM project with Forge-themed dark UI.

**Input:**

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| project_name | string | yes | Name for the project |
| output_dir | string | yes | Absolute path for the project directory |
| title | string | no | Page title. Default: project_name |
| mode | string | no | `single-file` (one index.html) or `multi-file` (separate JS/CSS). Default: `single-file` |
| force | boolean | no | Overwrite non-empty directory. Default: false |

**Output:** JSON object with status, output_dir, mode, and list of files created.

### Response Format

```json
{
  "status": "created",
  "output_dir": "/tmp/my-app",
  "project_name": "my-app",
  "mode": "single-file",
  "files": ["index.html"]
}
```

## Tool: codegen_html_read

Read a file or list the project directory.

**Input:**

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| project_dir | string | yes | Absolute path to the project directory |
| file_path | string | yes | Relative path to read, or `"."` for directory listing |

**Output:** JSON object with path, content (or listing), size, and modified timestamp.

### Response Format (file)

```json
{
  "path": "index.html",
  "content": "<!DOCTYPE html>...",
  "size": 2048,
  "modified": "2025-01-15T10:30:00Z"
}
```

### Response Format (directory listing)

```json
{
  "path": ".",
  "type": "directory",
  "files": ["index.html", "app.js", "styles.css"]
}
```

## Tool: codegen_html_write

Write or update a file in the project.

**Input:**

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| project_dir | string | yes | Absolute path to the project directory |
| file_path | string | yes | Relative path to write |
| content | string | yes | Complete file content |

**Output:** JSON object with path, action (created/updated), and size.

### Response Format

```json
{
  "path": "index.html",
  "action": "updated",
  "size": 2150
}
```
