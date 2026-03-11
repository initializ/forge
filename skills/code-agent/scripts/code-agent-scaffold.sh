#!/usr/bin/env bash
# code-agent-scaffold.sh — Scaffold a project with a known-good skeleton.
# Usage: ./code-agent-scaffold.sh '{"project_name": "my-app", "framework": "react"}'
#
# Supported frameworks: react, vue, vanilla, node, python, golang, spring-boot
# Requires: jq
set -euo pipefail

# --- Read input ---
INPUT="${1:-}"
if [ -z "$INPUT" ]; then
  echo '{"error": "usage: code-agent-scaffold.sh {\"project_name\": \"...\", \"framework\": \"react\"}"}' >&2
  exit 1
fi
if ! echo "$INPUT" | jq empty 2>/dev/null; then
  echo '{"error": "invalid JSON input"}' >&2
  exit 1
fi

PROJECT_NAME=$(echo "$INPUT" | jq -r '.project_name // empty')
FRAMEWORK=$(echo "$INPUT" | jq -r '.framework // empty')
TITLE=$(echo "$INPUT" | jq -r '.title // empty')

if [ -z "$PROJECT_NAME" ]; then
  echo '{"error": "project_name is required"}' >&2
  exit 1
fi
if [ -z "$FRAMEWORK" ]; then
  echo '{"error": "framework is required. Options: react, vue, vanilla, node, python, golang, spring-boot"}' >&2
  exit 1
fi
if [ -z "$TITLE" ]; then
  TITLE="$PROJECT_NAME"
fi

# --- Resolve output directory within workspace/ ---
OUTPUT_DIR="$(pwd)/workspace/$PROJECT_NAME"

if [ -d "$OUTPUT_DIR" ] && [ "$(ls -A "$OUTPUT_DIR" 2>/dev/null)" ]; then
  FORCE=$(echo "$INPUT" | jq -r '.force // false')
  if [ "$FORCE" != "true" ]; then
    echo '{"error": "project directory already exists and is not empty; set force: true to overwrite"}' >&2
    exit 1
  fi
fi

mkdir -p "$OUTPUT_DIR"

# Track created files
CREATED_FILES=()
write_file() {
  local relpath="$1"
  local content="$2"
  local fullpath="$OUTPUT_DIR/$relpath"
  mkdir -p "$(dirname "$fullpath")"
  echo "$content" > "$fullpath"
  CREATED_FILES+=("$relpath")
}

# --- Framework templates ---

scaffold_react() {
  write_file "package.json" "{
  \"name\": \"$PROJECT_NAME\",
  \"private\": true,
  \"version\": \"0.1.0\",
  \"type\": \"module\",
  \"scripts\": {
    \"dev\": \"vite --open\",
    \"build\": \"vite build\",
    \"preview\": \"vite preview\"
  },
  \"dependencies\": {
    \"react\": \"^19.0.0\",
    \"react-dom\": \"^19.0.0\"
  },
  \"devDependencies\": {
    \"@vitejs/plugin-react\": \"^4.4.0\",
    \"vite\": \"^6.0.0\"
  }
}"

  write_file "vite.config.js" "import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    port: 3000,
    open: true,
  },
})"

  write_file "index.html" "<!DOCTYPE html>
<html lang=\"en\">
  <head>
    <meta charset=\"UTF-8\" />
    <meta name=\"viewport\" content=\"width=device-width, initial-scale=1.0\" />
    <title>$TITLE</title>
    <script src=\"https://cdn.tailwindcss.com\"></script>
  </head>
  <body class=\"bg-zinc-950 text-zinc-200 antialiased\">
    <div id=\"root\"></div>
    <script type=\"module\" src=\"/src/main.jsx\"></script>
  </body>
</html>"

  write_file "src/main.jsx" "import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { App } from './App.jsx'

createRoot(document.getElementById('root')).render(
  <StrictMode>
    <App />
  </StrictMode>
)"

  write_file "src/App.jsx" "import { useState } from 'react'

export function App() {
  const [count, setCount] = useState(0)

  return (
    <div className=\"min-h-screen bg-zinc-950 text-zinc-200\">
      <div className=\"max-w-4xl mx-auto px-6 py-12\">
        <h1 className=\"text-3xl font-bold text-white mb-8 text-center\">$TITLE</h1>
        <div className=\"bg-zinc-900 border border-zinc-800 rounded-lg p-8 text-center shadow-lg\">
          <button
            onClick={() => setCount(c => c + 1)}
            className=\"bg-indigo-500 hover:bg-indigo-400 text-white font-semibold px-6 py-3 rounded-lg transition-colors\"
          >
            Count: {count}
          </button>
          <p className=\"text-zinc-400 mt-4\">
            Edit <code className=\"bg-zinc-950 px-1.5 py-0.5 rounded text-sm font-mono\">src/App.jsx</code> and save to see changes.
          </p>
        </div>
      </div>
    </div>
  )
}"

  write_file ".gitignore" "node_modules
dist
.env
*.log"
}

scaffold_vue() {
  write_file "package.json" "{
  \"name\": \"$PROJECT_NAME\",
  \"private\": true,
  \"version\": \"0.1.0\",
  \"type\": \"module\",
  \"scripts\": {
    \"dev\": \"vite --open\",
    \"build\": \"vite build\",
    \"preview\": \"vite preview\"
  },
  \"dependencies\": {
    \"vue\": \"^3.5.0\"
  },
  \"devDependencies\": {
    \"@vitejs/plugin-vue\": \"^5.2.0\",
    \"vite\": \"^6.0.0\"
  }
}"

  write_file "vite.config.js" "import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue()],
  server: {
    port: 3000,
    open: true,
  },
})"

  write_file "index.html" "<!DOCTYPE html>
<html lang=\"en\">
  <head>
    <meta charset=\"UTF-8\" />
    <meta name=\"viewport\" content=\"width=device-width, initial-scale=1.0\" />
    <title>$TITLE</title>
    <script src=\"https://cdn.tailwindcss.com\"></script>
  </head>
  <body class=\"bg-zinc-950 text-zinc-200 antialiased\">
    <div id=\"app\"></div>
    <script type=\"module\" src=\"/src/main.js\"></script>
  </body>
</html>"

  write_file "src/main.js" "import { createApp } from 'vue'
import App from './App.vue'

createApp(App).mount('#app')"

  write_file "src/App.vue" "<script setup>
import { ref } from 'vue'

const count = ref(0)
</script>

<template>
  <div class=\"min-h-screen bg-zinc-950 text-zinc-200\">
    <div class=\"max-w-4xl mx-auto px-6 py-12\">
      <h1 class=\"text-3xl font-bold text-white mb-8 text-center\">$TITLE</h1>
      <div class=\"bg-zinc-900 border border-zinc-800 rounded-lg p-8 text-center shadow-lg\">
        <button
          @click=\"count++\"
          class=\"bg-indigo-500 hover:bg-indigo-400 text-white font-semibold px-6 py-3 rounded-lg transition-colors\"
        >
          Count: {{ count }}
        </button>
        <p class=\"text-zinc-400 mt-4\">
          Edit <code class=\"bg-zinc-950 px-1.5 py-0.5 rounded text-sm font-mono\">src/App.vue</code> and save to see changes.
        </p>
      </div>
    </div>
  </div>
</template>"

  write_file ".gitignore" "node_modules
dist
.env
*.log"
}

scaffold_vanilla() {
  write_file "package.json" "{
  \"name\": \"$PROJECT_NAME\",
  \"private\": true,
  \"version\": \"0.1.0\",
  \"type\": \"module\",
  \"scripts\": {
    \"dev\": \"vite --open\",
    \"build\": \"vite build\",
    \"preview\": \"vite preview\"
  },
  \"devDependencies\": {
    \"vite\": \"^6.0.0\"
  }
}"

  write_file "vite.config.js" "import { defineConfig } from 'vite'

export default defineConfig({
  server: {
    port: 3000,
    open: true,
  },
})"

  write_file "index.html" "<!DOCTYPE html>
<html lang=\"en\">
  <head>
    <meta charset=\"UTF-8\" />
    <meta name=\"viewport\" content=\"width=device-width, initial-scale=1.0\" />
    <title>$TITLE</title>
    <script src=\"https://cdn.tailwindcss.com\"></script>
  </head>
  <body class=\"bg-zinc-950 text-zinc-200 antialiased\">
    <div id=\"app\" class=\"min-h-screen\">
      <div class=\"max-w-4xl mx-auto px-6 py-12\">
        <h1 class=\"text-3xl font-bold text-white mb-8 text-center\">$TITLE</h1>
        <div id=\"content\" class=\"bg-zinc-900 border border-zinc-800 rounded-lg p-8 text-center shadow-lg\">
          <button id=\"counter\" class=\"bg-indigo-500 hover:bg-indigo-400 text-white font-semibold px-6 py-3 rounded-lg transition-colors\">
            Count: 0
          </button>
          <p class=\"text-zinc-400 mt-4\">
            Edit <code class=\"bg-zinc-950 px-1.5 py-0.5 rounded text-sm font-mono\">src/main.js</code> and save to see changes.
          </p>
        </div>
      </div>
    </div>
    <script type=\"module\" src=\"/src/main.js\"></script>
  </body>
</html>"

  write_file "src/main.js" "let count = 0
const btn = document.getElementById('counter')
btn.addEventListener('click', () => {
  count++
  btn.textContent = \`Count: \${count}\`
})"

  write_file ".gitignore" "node_modules
dist
.env
*.log"
}

scaffold_node() {
  write_file "package.json" "{
  \"name\": \"$PROJECT_NAME\",
  \"private\": true,
  \"version\": \"0.1.0\",
  \"type\": \"module\",
  \"scripts\": {
    \"dev\": \"node --watch src/server.js\",
    \"start\": \"node src/server.js\"
  },
  \"dependencies\": {
    \"express\": \"^4.21.0\"
  }
}"

  write_file "src/server.js" "import express from 'express'
import { fileURLToPath } from 'url'
import { dirname, join } from 'path'

const __dirname = dirname(fileURLToPath(import.meta.url))
const app = express()
const PORT = process.env.PORT || 3000

app.use(express.json())

// Serve static frontend files from public/ directory
app.use(express.static(join(__dirname, '..', 'public')))

// API routes
app.get('/api/health', (req, res) => {
  res.json({ status: 'healthy', uptime: process.uptime() })
})

// Fallback: serve index.html for any non-API route
app.get('*', (req, res) => {
  res.sendFile(join(__dirname, '..', 'public', 'index.html'))
})

app.listen(PORT, () => {
  console.log(\`Server running at http://localhost:\${PORT}\`)
})"

  write_file "public/index.html" "<!DOCTYPE html>
<html lang=\"en\">
  <head>
    <meta charset=\"UTF-8\" />
    <meta name=\"viewport\" content=\"width=device-width, initial-scale=1.0\" />
    <title>$TITLE</title>
    <script src=\"https://cdn.tailwindcss.com\"></script>
  </head>
  <body class=\"bg-zinc-950 text-zinc-200 antialiased\">
    <div class=\"min-h-screen flex items-center justify-center\">
      <div class=\"max-w-md mx-auto text-center\">
        <h1 class=\"text-3xl font-bold text-white mb-4\">$TITLE</h1>
        <p class=\"text-zinc-400\">Express server is running.</p>
        <p class=\"text-zinc-500 mt-2 text-sm\">API: <code class=\"bg-zinc-900 px-1.5 py-0.5 rounded\">/api/health</code></p>
      </div>
    </div>
    <script type=\"module\" src=\"/app.js\"></script>
  </body>
</html>"

  write_file "public/app.js" "// Frontend JavaScript — fetches from API and updates the UI
console.log('$TITLE frontend loaded')"

  write_file ".gitignore" "node_modules
.env
*.log"
}

scaffold_python() {
  write_file "requirements.txt" "fastapi>=0.115.0
uvicorn[standard]>=0.32.0"

  write_file "main.py" "from pathlib import Path
from fastapi import FastAPI
from fastapi.responses import HTMLResponse, FileResponse
from fastapi.staticfiles import StaticFiles

app = FastAPI(title=\"$TITLE\")

# Serve static frontend files from static/ directory
STATIC_DIR = Path(__file__).parent / \"static\"
if STATIC_DIR.exists():
    app.mount(\"/static\", StaticFiles(directory=str(STATIC_DIR)), name=\"static\")


@app.get(\"/\", response_class=HTMLResponse)
async def root():
    index = STATIC_DIR / \"index.html\"
    if index.exists():
        return index.read_text()
    return '<h1>$TITLE</h1><p>Create static/index.html for the UI</p>'


# API routes — all under /api/
@app.get(\"/api/health\")
async def health():
    return {\"status\": \"healthy\"}


@app.get(\"/docs\")
async def docs_redirect():
    \"\"\"Redirect to auto-generated API docs.\"\"\"
    from fastapi.responses import RedirectResponse
    return RedirectResponse(url=\"/docs\")


if __name__ == \"__main__\":
    import uvicorn
    uvicorn.run(\"main:app\", host=\"0.0.0.0\", port=8000, reload=True)"

  write_file "static/index.html" "<!DOCTYPE html>
<html lang=\"en\">
<head>
    <meta charset=\"UTF-8\">
    <meta name=\"viewport\" content=\"width=device-width, initial-scale=1.0\">
    <title>$TITLE</title>
    <script src=\"https://cdn.tailwindcss.com\"></script>
</head>
<body class=\"bg-zinc-950 text-zinc-200 antialiased\">
    <div class=\"min-h-screen flex items-center justify-center\">
        <div class=\"max-w-md mx-auto text-center\">
            <h1 class=\"text-3xl font-bold text-white mb-4\">$TITLE</h1>
            <p class=\"text-zinc-400\">FastAPI server is running.</p>
            <a href=\"/docs\" class=\"inline-block mt-4 bg-indigo-500 hover:bg-indigo-400 text-white font-semibold px-6 py-3 rounded-lg transition-colors\">
                Open API Docs
            </a>
        </div>
    </div>
    <script type=\"module\" src=\"/static/app.js\"></script>
</body>
</html>"

  write_file "static/app.js" "// Frontend JavaScript — fetches from API and updates the UI
console.log('$TITLE frontend loaded')"

  write_file ".gitignore" "__pycache__
*.pyc
.venv
venv
.env
*.log"
}

scaffold_golang() {
  write_file "go.mod" "module $PROJECT_NAME

go 1.22

require github.com/gin-gonic/gin v1.10.0

require (
	github.com/bytedance/sonic v1.12.6 // indirect
	github.com/bytedance/sonic/loader v0.2.1 // indirect
	github.com/cloudwego/base64x v0.1.4 // indirect
	github.com/cloudwego/iasm v0.2.0 // indirect
	github.com/gabriel-vasile/mimetype v1.4.7 // indirect
	github.com/gin-contrib/sse v0.1.0 // indirect
	github.com/go-playground/locales v0.14.1 // indirect
	github.com/go-playground/universal-translator v0.18.1 // indirect
	github.com/go-playground/validator/v10 v10.23.0 // indirect
	github.com/goccy/go-json v0.10.4 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/klauspost/cpuid/v2 v2.2.9 // indirect
	github.com/leodido/go-urn v1.4.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/pelletier/go-toml/v2 v2.2.3 // indirect
	github.com/twitchyliquid64/golang-asm v0.15.1 // indirect
	github.com/ugorji/go/codec v1.2.12 // indirect
	golang.org/x/arch v0.12.0 // indirect
	golang.org/x/crypto v0.31.0 // indirect
	golang.org/x/net v0.33.0 // indirect
	golang.org/x/sys v0.28.0 // indirect
	golang.org/x/text v0.21.0 // indirect
	google.golang.org/protobuf v1.36.1 // indirect
)"

  write_file "main.go" "package main

import (
	\"net/http\"

	\"github.com/gin-gonic/gin\"
)

func main() {
	r := gin.Default()

	// Serve static frontend files from static/ directory
	r.Static(\"/static\", \"./static\")
	r.StaticFile(\"/\", \"./static/index.html\")

	// API routes — all under /api/
	api := r.Group(\"/api\")
	{
		api.GET(\"/health\", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{\"status\": \"healthy\"})
		})
	}

	r.Run(\":8080\")
}"

  write_file "static/index.html" "<!DOCTYPE html>
<html lang=\"en\">
  <head>
    <meta charset=\"UTF-8\" />
    <meta name=\"viewport\" content=\"width=device-width, initial-scale=1.0\" />
    <title>$TITLE</title>
    <script src=\"https://cdn.tailwindcss.com\"></script>
  </head>
  <body class=\"bg-zinc-950 text-zinc-200 antialiased\">
    <div class=\"min-h-screen flex items-center justify-center\">
      <div class=\"max-w-md mx-auto text-center\">
        <h1 class=\"text-3xl font-bold text-white mb-4\">$TITLE</h1>
        <p class=\"text-zinc-400\">Gin server is running.</p>
        <p class=\"text-zinc-500 mt-2 text-sm\">API: <code class=\"bg-zinc-900 px-1.5 py-0.5 rounded\">/api/health</code></p>
      </div>
    </div>
    <script type=\"module\" src=\"/static/app.js\"></script>
  </body>
</html>"

  write_file "static/app.js" "// Frontend JavaScript — fetches from API and updates the UI
console.log('$TITLE frontend loaded')"

  write_file ".gitignore" "bin/
*.exe
*.test
*.out
.env
vendor/"
}

scaffold_springboot() {
  local GROUP_ID="com.example"
  local ARTIFACT_ID="$PROJECT_NAME"
  local PKG_PATH="com/example/${PROJECT_NAME//-/}"
  local PKG_NAME="com.example.${PROJECT_NAME//-/}"

  write_file "pom.xml" "<?xml version=\"1.0\" encoding=\"UTF-8\"?>
<project xmlns=\"http://maven.apache.org/POM/4.0.0\"
         xmlns:xsi=\"http://www.w3.org/2001/XMLSchema-instance\"
         xsi:schemaLocation=\"http://maven.apache.org/POM/4.0.0 https://maven.apache.org/xsd/maven-4.0.0.xsd\">
    <modelVersion>4.0.0</modelVersion>

    <parent>
        <groupId>org.springframework.boot</groupId>
        <artifactId>spring-boot-starter-parent</artifactId>
        <version>3.4.0</version>
        <relativePath/>
    </parent>

    <groupId>$GROUP_ID</groupId>
    <artifactId>$ARTIFACT_ID</artifactId>
    <version>0.1.0</version>
    <name>$TITLE</name>

    <properties>
        <java.version>21</java.version>
    </properties>

    <dependencies>
        <dependency>
            <groupId>org.springframework.boot</groupId>
            <artifactId>spring-boot-starter-web</artifactId>
        </dependency>
        <dependency>
            <groupId>org.springframework.boot</groupId>
            <artifactId>spring-boot-starter-test</artifactId>
            <scope>test</scope>
        </dependency>
    </dependencies>

    <build>
        <plugins>
            <plugin>
                <groupId>org.springframework.boot</groupId>
                <artifactId>spring-boot-maven-plugin</artifactId>
            </plugin>
        </plugins>
    </build>
</project>"

  write_file "src/main/resources/application.properties" "spring.application.name=$PROJECT_NAME
server.port=8080"

  # Spring Boot automatically serves files from src/main/resources/static/
  write_file "src/main/resources/static/index.html" "<!DOCTYPE html>
<html lang=\"en\">
  <head>
    <meta charset=\"UTF-8\" />
    <meta name=\"viewport\" content=\"width=device-width, initial-scale=1.0\" />
    <title>$TITLE</title>
    <script src=\"https://cdn.tailwindcss.com\"></script>
  </head>
  <body class=\"bg-zinc-950 text-zinc-200 antialiased\">
    <div class=\"min-h-screen flex items-center justify-center\">
      <div class=\"max-w-md mx-auto text-center\">
        <h1 class=\"text-3xl font-bold text-white mb-4\">$TITLE</h1>
        <p class=\"text-zinc-400\">Spring Boot server is running.</p>
        <p class=\"text-zinc-500 mt-2 text-sm\">API: <code class=\"bg-zinc-900 px-1.5 py-0.5 rounded\">/api/health</code></p>
      </div>
    </div>
    <script type=\"module\" src=\"/app.js\"></script>
  </body>
</html>"

  write_file "src/main/resources/static/app.js" "// Frontend JavaScript — fetches from API and updates the UI
console.log('$TITLE frontend loaded')"

  write_file "src/main/java/$PKG_PATH/Application.java" "package $PKG_NAME;

import org.springframework.boot.SpringApplication;
import org.springframework.boot.autoconfigure.SpringBootApplication;

@SpringBootApplication
public class Application {
    public static void main(String[] args) {
        SpringApplication.run(Application.class, args);
    }
}"

  write_file "src/main/java/$PKG_PATH/HelloController.java" "package $PKG_NAME;

import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;

import java.util.Map;

@RestController
@RequestMapping(\"/api\")
public class HelloController {

    @GetMapping(\"/health\")
    public Map<String, String> health() {
        return Map.of(\"status\", \"healthy\");
    }
}"

  write_file ".gitignore" "target/
*.class
*.jar
.env
*.log
.idea/
*.iml"

  # Maven wrapper for ./mvnw support
  write_file ".mvn/wrapper/maven-wrapper.properties" "distributionUrl=https://repo.maven.apache.org/maven2/org/apache/maven/apache-maven/3.9.9/apache-maven-3.9.9-bin.zip
wrapperUrl=https://repo.maven.apache.org/maven2/org/apache/maven/wrapper/maven-wrapper/3.3.2/maven-wrapper-3.3.2.jar"
}

# --- Dispatch ---
case "$FRAMEWORK" in
  react)       scaffold_react ;;
  vue)         scaffold_vue ;;
  vanilla)     scaffold_vanilla ;;
  node)        scaffold_node ;;
  python)      scaffold_python ;;
  golang|go)   scaffold_golang ;;
  spring-boot|springboot|spring) scaffold_springboot ;;
  *)
    echo "{\"error\": \"unknown framework: $FRAMEWORK. Options: react, vue, vanilla, node, python, golang, spring-boot\"}" >&2
    exit 1
    ;;
esac

# --- Output result ---
FILES_JSON=$(printf '%s\n' "${CREATED_FILES[@]}" | jq -R -s 'split("\n") | map(select(length > 0))')
jq -n \
  --arg status "created" \
  --arg project_name "$PROJECT_NAME" \
  --arg framework "$FRAMEWORK" \
  --arg project_dir "$OUTPUT_DIR" \
  --argjson files "$FILES_JSON" \
  '{status: $status, project_name: $project_name, framework: $framework, project_dir: $project_dir, files: $files}'
