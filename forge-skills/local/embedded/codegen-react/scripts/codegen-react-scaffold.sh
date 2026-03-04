#!/usr/bin/env bash
# codegen-react-scaffold.sh — Scaffold a Vite + React project with Forge dark theme.
# Usage: ./codegen-react-scaffold.sh '{"project_name": "my-app", "output_dir": "/tmp/my-app"}'
#
# Requires: node, npx, jq
set -euo pipefail

# --- Read input ---
INPUT="${1:-}"
if [ -z "$INPUT" ]; then
  echo '{"error": "usage: codegen-react-scaffold.sh {\"project_name\": \"...\", \"output_dir\": \"...\"}"}' >&2
  exit 1
fi

# Validate JSON
if ! echo "$INPUT" | jq empty 2>/dev/null; then
  echo '{"error": "invalid JSON input"}' >&2
  exit 1
fi

# --- Extract fields ---
PROJECT_NAME=$(echo "$INPUT" | jq -r '.project_name // empty')
OUTPUT_DIR=$(echo "$INPUT" | jq -r '.output_dir // empty')
TITLE=$(echo "$INPUT" | jq -r '.title // empty')
FORCE=$(echo "$INPUT" | jq -r '.force // false')

if [ -z "$PROJECT_NAME" ]; then
  echo '{"error": "project_name is required"}' >&2
  exit 1
fi
if [ -z "$OUTPUT_DIR" ]; then
  echo '{"error": "output_dir is required"}' >&2
  exit 1
fi

# Default title to project name
if [ -z "$TITLE" ]; then
  TITLE="$PROJECT_NAME"
fi

# --- Safety: output_dir must be under $HOME or /tmp ---
RESOLVED_DIR=$(cd "$(dirname "$OUTPUT_DIR")" 2>/dev/null && pwd)/$(basename "$OUTPUT_DIR") 2>/dev/null || RESOLVED_DIR="$OUTPUT_DIR"
case "$RESOLVED_DIR" in
  "$HOME"/*|/tmp/*)
    ;;
  *)
    echo '{"error": "output_dir must be under $HOME or /tmp"}' >&2
    exit 1
    ;;
esac

# --- Check non-empty directory ---
if [ -d "$OUTPUT_DIR" ] && [ "$(ls -A "$OUTPUT_DIR" 2>/dev/null)" ]; then
  if [ "$FORCE" != "true" ]; then
    echo '{"error": "output_dir is not empty; set force: true to overwrite"}' >&2
    exit 1
  fi
fi

# --- Create project structure ---
mkdir -p "$OUTPUT_DIR/src"

# package.json
cat > "$OUTPUT_DIR/package.json" << 'EOF'
{
  "name": "PLACEHOLDER_PROJECT_NAME",
  "private": true,
  "version": "0.1.0",
  "type": "module",
  "scripts": {
    "dev": "vite",
    "build": "vite build",
    "preview": "vite preview"
  },
  "dependencies": {
    "react": "^19.0.0",
    "react-dom": "^19.0.0"
  },
  "devDependencies": {
    "@vitejs/plugin-react": "^4.4.0",
    "vite": "^6.0.0"
  }
}
EOF
sed "s|PLACEHOLDER_PROJECT_NAME|${PROJECT_NAME}|g" "$OUTPUT_DIR/package.json" > "$OUTPUT_DIR/package.json.tmp" && mv "$OUTPUT_DIR/package.json.tmp" "$OUTPUT_DIR/package.json"

# vite.config.js
cat > "$OUTPUT_DIR/vite.config.js" << 'EOF'
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    port: 3000,
    open: true,
  },
})
EOF

# index.html
cat > "$OUTPUT_DIR/index.html" << 'EOF'
<!DOCTYPE html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <title>PLACEHOLDER_TITLE</title>
    <script src="https://cdn.tailwindcss.com"></script>
  </head>
  <body class="bg-zinc-950 text-zinc-200 antialiased">
    <div id="root"></div>
    <script type="module" src="/src/main.jsx"></script>
  </body>
</html>
EOF
sed "s|PLACEHOLDER_TITLE|${TITLE}|g" "$OUTPUT_DIR/index.html" > "$OUTPUT_DIR/index.html.tmp" && mv "$OUTPUT_DIR/index.html.tmp" "$OUTPUT_DIR/index.html"

# src/main.jsx
cat > "$OUTPUT_DIR/src/main.jsx" << 'EOF'
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { App } from './App.jsx'
import './App.css'

createRoot(document.getElementById('root')).render(
  <StrictMode>
    <App />
  </StrictMode>
)
EOF

# src/App.jsx
cat > "$OUTPUT_DIR/src/App.jsx" << 'EOF'
import { useState } from 'react'

export function App() {
  const [count, setCount] = useState(0)

  return (
    <div className="min-h-screen bg-zinc-950 text-zinc-200">
      <div className="max-w-4xl mx-auto px-6 py-12">
        <header className="text-center mb-8">
          <h1 className="text-3xl font-bold text-white">PLACEHOLDER_TITLE</h1>
        </header>
        <main className="flex flex-col items-center gap-6">
          <div className="bg-zinc-900 border border-zinc-800 rounded-lg p-8 text-center shadow-lg">
            <button
              onClick={() => setCount(c => c + 1)}
              className="bg-indigo-500 hover:bg-indigo-400 text-white font-semibold px-6 py-3 rounded-lg transition-colors"
            >
              Count: {count}
            </button>
            <p className="text-zinc-400 mt-4">
              Edit <code className="bg-zinc-950 px-1.5 py-0.5 rounded text-sm font-mono">src/App.jsx</code> and save to see changes.
            </p>
          </div>
        </main>
      </div>
    </div>
  )
}
EOF
sed "s|PLACEHOLDER_TITLE|${TITLE}|g" "$OUTPUT_DIR/src/App.jsx" > "$OUTPUT_DIR/src/App.jsx.tmp" && mv "$OUTPUT_DIR/src/App.jsx.tmp" "$OUTPUT_DIR/src/App.jsx"

# src/App.css
cat > "$OUTPUT_DIR/src/App.css" << 'EOF'
/*
 * Tailwind CSS is loaded via CDN in index.html — use utility classes for styling.
 * Add custom styles here only when Tailwind classes are insufficient.
 */
EOF

# .gitignore
cat > "$OUTPUT_DIR/.gitignore" << 'EOF'
node_modules
dist
.env
.env.local
*.log
EOF

# --- Output result ---
echo "{
  \"status\": \"created\",
  \"output_dir\": \"$OUTPUT_DIR\",
  \"project_name\": \"$PROJECT_NAME\",
  \"files\": [
    \"package.json\",
    \"vite.config.js\",
    \"index.html\",
    \"src/main.jsx\",
    \"src/App.jsx\",
    \"src/App.css\",
    \".gitignore\"
  ]
}" | jq .
