#!/bin/bash
# Build tree-shaken YAML-only Monaco editor bundle.
# Produces editor.js, editor.worker.js, editor.css.
# Run once; outputs are committed to git.
set -e

# Resolve destination to absolute path before changing dirs
DEST="$(cd "$(dirname "${1:-.}")" && pwd)/$(basename "${1:-.}")"
mkdir -p "$DEST"

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
cd "$WORK"

# Install only what we need
npm init -y > /dev/null 2>&1
npm install monaco-editor@0.52.0 esbuild@0.24.0 > /dev/null 2>&1

# Entry point: editor core + YAML language only
cat > entry.js << 'EOF'
import * as monaco from 'monaco-editor/esm/vs/editor/editor.api';
import 'monaco-editor/esm/vs/basic-languages/yaml/yaml.contribution';
window.monaco = monaco;
EOF

# Bundle editor (IIFE so monaco is globally available)
npx esbuild entry.js \
  --bundle --format=iife --global-name=__monaco \
  --outfile=editor.js --minify \
  --loader:.ttf=dataurl

# Bundle worker separately
npx esbuild node_modules/monaco-editor/esm/vs/editor/editor.worker.js \
  --bundle --format=iife --outfile=editor.worker.js --minify

# Use the pre-built CSS from monaco-editor (complete editor styles)
cp node_modules/monaco-editor/min/vs/editor/editor.main.css editor.css

# Copy to destination
cp editor.js editor.worker.js editor.css "$DEST/"

echo "Built Monaco YAML-only bundle in $DEST/"
