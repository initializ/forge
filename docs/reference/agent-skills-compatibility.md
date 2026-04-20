---
title: "Agent Skills Compatibility"
description: "Compatibility matrix for skills across agent types and LLM providers."
order: 4
---

## Compatibility Matrix

All embedded skills work with any LLM provider that supports tool calling. Provider-specific requirements are limited to API keys for external services.

### Provider Support

| Feature | OpenAI | Anthropic | Gemini | Ollama |
|---------|--------|-----------|--------|--------|
| Tool calling | ✅ | ✅ | ✅ | ✅ (model-dependent) |
| Script-backed skills | ✅ | ✅ | ✅ | ✅ |
| Binary-backed skills | ✅ | ✅ | ✅ | ✅ |
| Skill guardrails | ✅ | ✅ | ✅ | ✅ |
| OAuth login | ✅ | — | — | — |

### Skill Requirements

| Skill | Required Binaries | Required Env Vars | Egress Domains |
|-------|-------------------|-------------------|----------------|
| `github` | `gh`, `git`, `jq` | `GH_TOKEN` (optional) | `api.github.com`, `github.com` |
| `tavily-search` | `curl`, `jq` | `TAVILY_API_KEY` | `api.tavily.com` |
| `tavily-research` | `curl`, `jq` | `TAVILY_API_KEY` | `api.tavily.com` |
| `k8s-incident-triage` | `kubectl` | `KUBECONFIG` (optional) | — |
| `k8s-pod-rightsizer` | `bash`, `kubectl`, `jq`, `curl` | `KUBECONFIG` (optional) | — |
| `k8s-cost-visibility` | `kubectl`, `jq`, `awk`, `bc` | `KUBECONFIG` (optional) | — |
| `code-review` | — | `OPENAI_API_KEY` or `ANTHROPIC_API_KEY` | Provider API domain |
| `codegen-react` | `node`, `npx`, `jq` | — | `registry.npmjs.org`, `cdn.tailwindcss.com` |
| `codegen-html` | `jq` | — | `cdn.tailwindcss.com`, `esm.sh` |
| `code-agent` | `bash`, `jq` | — | Package registries |

### Notes

- **Binary dependencies** are checked at runtime. Missing binaries produce clear error messages with installation instructions.
- **Environment variables** are resolved from the secret provider chain: agent-local encrypted file → global encrypted file → environment variables.
- **Egress domains** declared by skills are automatically merged into `forge.yaml` when added via `forge skills add`.
- **Ollama** requires models with tool-calling support (e.g., `llama3.1`, `mistral`). Older models without tool support will not work with script-backed skills.
