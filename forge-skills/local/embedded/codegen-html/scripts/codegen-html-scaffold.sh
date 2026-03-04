#!/usr/bin/env bash
# codegen-html-scaffold.sh — Scaffold a Preact + HTM project with Forge dark theme.
# Usage: ./codegen-html-scaffold.sh '{"project_name": "my-app", "output_dir": "/tmp/my-app"}'
#
# Requires: jq
set -euo pipefail

# --- Read input ---
INPUT="${1:-}"
if [ -z "$INPUT" ]; then
  echo '{"error": "usage: codegen-html-scaffold.sh {\"project_name\": \"...\", \"output_dir\": \"...\"}"}' >&2
  exit 1
fi

if ! echo "$INPUT" | jq empty 2>/dev/null; then
  echo '{"error": "invalid JSON input"}' >&2
  exit 1
fi

# --- Extract fields ---
PROJECT_NAME=$(echo "$INPUT" | jq -r '.project_name // empty')
OUTPUT_DIR=$(echo "$INPUT" | jq -r '.output_dir // empty')
TITLE=$(echo "$INPUT" | jq -r '.title // empty')
MODE=$(echo "$INPUT" | jq -r '.mode // "single-file"')
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

# Validate mode
case "$MODE" in
  single-file|multi-file) ;;
  *)
    echo '{"error": "mode must be single-file or multi-file"}' >&2
    exit 1
    ;;
esac

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

mkdir -p "$OUTPUT_DIR"

# --- Single-file mode ---
if [ "$MODE" = "single-file" ]; then
  cat > "$OUTPUT_DIR/index.html" << 'HEREDOC'
<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
  <title>PLACEHOLDER_TITLE</title>
  <script src="https://cdn.tailwindcss.com"></script>
</head>
<body class="bg-zinc-950 text-zinc-200 antialiased">
  <div id="app"></div>
  <script type="module">
    import { h, render } from 'https://esm.sh/preact@10.25.4';
    import { useState } from 'https://esm.sh/preact@10.25.4/hooks';
    import htm from 'https://esm.sh/htm@3.1.1';

    const html = htm.bind(h);

    function App() {
      const [count, setCount] = useState(0);

      return html`
        <div class="min-h-screen bg-zinc-950 text-zinc-200">
          <div class="max-w-4xl mx-auto px-6 py-12">
            <header class="text-center mb-8">
              <h1 class="text-3xl font-bold text-white">PLACEHOLDER_TITLE</h1>
            </header>
            <main class="flex flex-col items-center gap-6">
              <div class="bg-zinc-900 border border-zinc-800 rounded-lg p-8 text-center shadow-lg">
                <button
                  onClick=${() => setCount(c => c + 1)}
                  class="bg-indigo-500 hover:bg-indigo-400 text-white font-semibold px-6 py-3 rounded-lg transition-colors"
                >
                  Count: ${count}
                </button>
                <p class="text-zinc-400 mt-4">Edit this file and refresh to see changes.</p>
              </div>
            </main>
          </div>
        </div>
      `;
    }

    render(html`<${App} />`, document.getElementById('app'));
  </script>
</body>
</html>
HEREDOC

  sed "s|PLACEHOLDER_TITLE|${TITLE}|g" "$OUTPUT_DIR/index.html" > "$OUTPUT_DIR/index.html.tmp" && mv "$OUTPUT_DIR/index.html.tmp" "$OUTPUT_DIR/index.html"

  echo "{
    \"status\": \"created\",
    \"output_dir\": \"$OUTPUT_DIR\",
    \"project_name\": \"$PROJECT_NAME\",
    \"mode\": \"single-file\",
    \"files\": [\"index.html\"]
  }" | jq .
  exit 0
fi

# --- Multi-file mode ---
mkdir -p "$OUTPUT_DIR/components"

# index.html
cat > "$OUTPUT_DIR/index.html" << 'HEREDOC'
<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
  <title>PLACEHOLDER_TITLE</title>
  <script src="https://cdn.tailwindcss.com"></script>
</head>
<body class="bg-zinc-950 text-zinc-200 antialiased">
  <div id="app"></div>
  <script type="module" src="app.js"></script>
</body>
</html>
HEREDOC
sed "s|PLACEHOLDER_TITLE|${TITLE}|g" "$OUTPUT_DIR/index.html" > "$OUTPUT_DIR/index.html.tmp" && mv "$OUTPUT_DIR/index.html.tmp" "$OUTPUT_DIR/index.html"

# app.js
cat > "$OUTPUT_DIR/app.js" << 'HEREDOC'
import { h, render } from 'https://esm.sh/preact@10.25.4';
import htm from 'https://esm.sh/htm@3.1.1';
import { Counter } from './components/Counter.js';

const html = htm.bind(h);

function App() {
  return html`
    <div class="min-h-screen bg-zinc-950 text-zinc-200">
      <div class="max-w-4xl mx-auto px-6 py-12">
        <header class="text-center mb-8">
          <h1 class="text-3xl font-bold text-white">PLACEHOLDER_TITLE</h1>
        </header>
        <main class="flex flex-col items-center gap-6">
          <${Counter} />
        </main>
      </div>
    </div>
  `;
}

render(html`<${App} />`, document.getElementById('app'));
HEREDOC
sed "s|PLACEHOLDER_TITLE|${TITLE}|g" "$OUTPUT_DIR/app.js" > "$OUTPUT_DIR/app.js.tmp" && mv "$OUTPUT_DIR/app.js.tmp" "$OUTPUT_DIR/app.js"

# components/Counter.js
cat > "$OUTPUT_DIR/components/Counter.js" << 'HEREDOC'
import { h } from 'https://esm.sh/preact@10.25.4';
import { useState } from 'https://esm.sh/preact@10.25.4/hooks';
import htm from 'https://esm.sh/htm@3.1.1';

const html = htm.bind(h);

export function Counter() {
  const [count, setCount] = useState(0);

  return html`
    <div class="bg-zinc-900 border border-zinc-800 rounded-lg p-8 text-center shadow-lg">
      <button
        onClick=${() => setCount(c => c + 1)}
        class="bg-indigo-500 hover:bg-indigo-400 text-white font-semibold px-6 py-3 rounded-lg transition-colors"
      >
        Count: ${count}
      </button>
      <p class="text-zinc-400 mt-4">
        Edit <code class="bg-zinc-950 px-1.5 py-0.5 rounded text-sm font-mono">components/Counter.js</code> and refresh to see changes.
      </p>
    </div>
  `;
}
HEREDOC

echo "{
  \"status\": \"created\",
  \"output_dir\": \"$OUTPUT_DIR\",
  \"project_name\": \"$PROJECT_NAME\",
  \"mode\": \"multi-file\",
  \"files\": [
    \"index.html\",
    \"app.js\",
    \"components/Counter.js\"
  ]
}" | jq .
