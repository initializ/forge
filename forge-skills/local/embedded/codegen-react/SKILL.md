---
name: codegen-react
icon: ⚛️
category: developer
tags:
  - code-generation
  - frontend
  - react
  - vite
  - ui
description: Scaffold and iterate on Vite + React applications
metadata:
  forge:
    requires:
      bins:
        - node
        - npx
        - jq
      env:
        required: []
        one_of: []
        optional: []
    egress_domains:
      - registry.npmjs.org
      - cdn.jsdelivr.net
      - cdn.tailwindcss.com
    timeout_hint: 120
---

# Codegen React Skill

Scaffold and iteratively build Vite + React applications. Creates a complete project structure with React 19, Vite 6, and a Forge-themed dark UI out of the box.

## Quick Start

```bash
# Scaffold a new project
./scripts/codegen-react-scaffold.sh '{"project_name": "my-app", "output_dir": "/tmp/my-app"}'

# Install deps and start dev server (opens browser)
./scripts/codegen-react-run.sh '{"project_dir": "/tmp/my-app"}'

# Read a file or list the project
./scripts/codegen-react-read.sh '{"project_dir": "/tmp/my-app", "file_path": "src/App.jsx"}'

# Write/update a file (Vite hot-reloads automatically)
./scripts/codegen-react-write.sh '{"project_dir": "/tmp/my-app", "file_path": "src/App.jsx", "content": "..."}'
```

## CRITICAL: Scaffold Conventions (DO NOT VIOLATE)

These rules prevent build errors. Violating them **will** break the app:

1. **NEVER modify `src/main.jsx`** — it is the entry point and must not be changed
2. **ALWAYS use named exports**: `export function ComponentName() {}`, NEVER `export default`
3. **NEVER create or import `index.css`** — it does not exist and will cause a build error
4. **Use Tailwind CSS utility classes** for all styling (loaded via CDN in `index.html`)
5. **Only modify `src/App.jsx`** and create new component files under `src/`
6. `src/App.css` exists for custom styles but prefer Tailwind classes

## Code Style Guide

- Use **functional components** with hooks (`useState`, `useEffect`, `useRef`, etc.)
- Use **named exports** for components: `export function App() {}`
- Keep components in separate files under `src/`
- Use **Tailwind CSS utility classes** — the CDN is pre-loaded in `index.html`
- Forge dark theme colors: `bg-zinc-950` (bg), `bg-zinc-900` (cards), `border-zinc-800` (borders), `text-zinc-200` (text), `text-zinc-400` (muted), `bg-indigo-500` (accent)
- Prefer `const` over `let`; never use `var`

## Tailwind CSS Reference

Tailwind is loaded via CDN. Use utility classes directly in JSX `className` attributes:

- **Layout:** `flex`, `grid`, `gap-4`, `max-w-4xl`, `mx-auto`, `px-6`, `py-12`
- **Colors:** `bg-zinc-950`, `bg-zinc-900`, `text-zinc-200`, `text-zinc-400`, `bg-indigo-500`
- **Borders:** `border`, `border-zinc-800`, `rounded-lg`, `shadow-lg`
- **Typography:** `text-3xl`, `font-bold`, `text-center`, `font-mono`
- **Interactive:** `hover:bg-indigo-400`, `transition-colors`, `cursor-pointer`

Do NOT write custom CSS classes when Tailwind utilities exist. Do NOT create new `.css` files.

## Safety Constraints

- Output directory must be under `$HOME` or `/tmp`
- Non-empty directories require `force: true` to overwrite
- Path traversal (`..`, absolute paths) is rejected in read/write operations
- No network calls during scaffold (all files generated locally)

## Iteration Workflow

1. **Scaffold** the project with `codegen_react_scaffold`
2. **Run** the dev server with `codegen_react_run` — installs deps and opens the browser
3. **Read** files to understand current state with `codegen_react_read`
4. **Write** updated files with `codegen_react_write` — Vite hot-reloads automatically
5. Repeat steps 3-4 to iterate on the UI

## When to Use

Use `codegen-react` when the user wants:
- A full React application with build tooling (Vite)
- Component-based architecture with JSX
- Hot module replacement during development
- npm package ecosystem access

For simpler needs (single HTML file, no build step), use `codegen-html` instead.

## Tool: codegen_react_scaffold

Create a new Vite + React project with Forge-themed dark UI.

**Input:**

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| project_name | string | yes | Name for the project (used in package.json) |
| output_dir | string | yes | Absolute path for the project directory |
| title | string | no | Page title. Default: project_name |
| force | boolean | no | Overwrite non-empty directory. Default: false |

**Output:** JSON object with status, output_dir, and list of files created.

### Response Format

```json
{
  "status": "created",
  "output_dir": "/tmp/my-app",
  "project_name": "my-app",
  "files": [
    "package.json",
    "vite.config.js",
    "index.html",
    "src/main.jsx",
    "src/App.jsx",
    "src/App.css",
    ".gitignore"
  ]
}
```

## Tool: codegen_react_read

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
  "path": "src/App.jsx",
  "content": "export function App() { ... }",
  "size": 245,
  "modified": "2025-01-15T10:30:00Z"
}
```

### Response Format (directory listing)

```json
{
  "path": ".",
  "type": "directory",
  "files": [
    "package.json",
    "vite.config.js",
    "index.html",
    "src/main.jsx",
    "src/App.jsx",
    "src/App.css"
  ]
}
```

## Tool: codegen_react_write

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
  "path": "src/App.jsx",
  "action": "updated",
  "size": 312
}
```

## Tool: codegen_react_run

Install dependencies and start the Vite dev server. Automatically opens the browser.

Call this tool **after scaffolding** to get the app running. It installs `node_modules` (if not already present) and starts `npm run dev` in the background. Vite hot-reloads on file changes, so subsequent `codegen_react_write` calls update the browser automatically.

**Input:**

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| project_dir | string | yes | Absolute path to the project directory (must contain `package.json`) |

**Output:** JSON object with status, url, pid, and install status.

### Response Format

```json
{
  "status": "running",
  "url": "http://localhost:3000",
  "pid": 12345,
  "project_dir": "/tmp/my-app",
  "install": "installed"
}
```

### Tips

- Call this once after `codegen_react_scaffold` — the server stays running
- After the server is running, just use `codegen_react_write` to update files — Vite hot-reloads automatically
- The `install` field is `"installed"` on first run and `"skipped"` on subsequent runs
- The `pid` can be used to stop the server later with `kill <pid>`
