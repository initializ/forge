# Skill Builder LLM (workspace-level)

The `forge ui` skill builder generates SKILL.md files via an LLM. That
LLM is configured at the **workspace level** — independent of any
specific agent's runtime LLM — so the same configuration works across
every agent in the workspace and is usable before any agent has been
scaffolded.

## Why workspace-level

Pre-#92 the skill builder borrowed credentials from whichever agent
the operator clicked into: it read the agent's `forge.yaml` and `.env`,
applied a hardcoded model upgrade (`gpt-4.1` for openai, `claude-opus-4-6`
for anthropic), and called the LLM from inside the `forge ui` process.
That model conflated two distinct concerns ("agent runtime LLM" vs.
"build-time codegen LLM"), broke against any agent pointed at a custom
OpenAI-compatible endpoint (the upgrade requested a model the endpoint
didn't host), caused cross-agent env-var stomping when switching agents,
and had no answer for empty workspaces.

The workspace-level model fixes all four:

- One LLM for the operator's skill-building work, used across every
  agent in the workspace.
- Operator picks the model — no hardcoded upgrade.
- Credentials threaded as request-scoped data; the UI process's
  environment is never mutated by handler calls.
- Works in an empty workspace before any agent is scaffolded.

## Configuration file

`<workspace>/.forge/ui.yaml`:

```yaml
skill_builder:
  provider: openai            # openai | anthropic | gemini | ollama
  model: gpt-4.1              # operator-chosen; no hardcoded upgrade
  base_url: https://...       # optional, for OpenAI-compatible endpoints
  api_key_env: OPENAI_API_KEY # which env var holds the key (default per provider)
```

- `provider` (required) — one of `openai`, `anthropic`, `gemini`, `ollama`.
- `model` (required) — operator picks. The skill builder uses this
  verbatim; there is no `SkillBuilderCodegenModel` hardcoded upgrade.
- `base_url` (optional, openai only) — set this for OpenAI-compatible
  endpoints (OpenRouter, vLLM, litellm, etc.).
- `api_key_env` (optional) — name of the environment variable the
  `forge ui` process reads for the API key. Defaults per provider
  (`OPENAI_API_KEY` / `ANTHROPIC_API_KEY` / `GEMINI_API_KEY`). Set this
  if you keep the skill-builder credentials under a different name
  (e.g. `WORKSPACE_LLM_KEY`) to avoid collisions with per-agent
  runtime credentials.

The API key itself is **never** stored in `ui.yaml` — only the env var
name is. Set the env var in your shell before launching `forge ui`.

## Resolution precedence

The loader resolves the skill-builder LLM through three tiers, in order:

1. `<workspace>/.forge/ui.yaml` — primary, per-workspace.
2. `~/.forge/ui.yaml` — fallback, operator's machine-wide default.
3. The picked agent's `forge.yaml` + `.env` — **deprecated** fallback.
   When this tier resolves, the UI banner shows a deprecation warning
   prompting the operator to configure workspace settings. This
   compatibility shim will be removed in a future release.

If none of the tiers resolves and the skill builder is invoked, the
chat handler returns a 400 with a message pointing to Settings.

## Setting it up

### Via the UI (recommended)

1. Open `forge ui --dir <your-workspace>` and click any agent's
   Skill Builder.
2. If no workspace-level config exists, the banner reads
   **"Workspace skill-builder LLM is not configured"** — click
   **Configure**.
3. Fill the form (provider, model, optional base URL, optional API key
   env override) and **paste your API key** in the password field.
4. Save. The key is written to `<workspace>/.forge/.env` (mode 0600)
   under the env var name shown in the form. An auto-generated
   `.forge/.gitignore` protects the file from being committed.

The key value is never sent back by the GET endpoint and never
appears in `ui.yaml` — only `<workspace>/.forge/.env` ever holds it.
To rotate, open Settings and paste a new value; submitting an empty
key leaves the saved value untouched.

### Via the file

```
mkdir -p <workspace>/.forge
cat > <workspace>/.forge/ui.yaml <<'YAML'
skill_builder:
  provider: openai
  model: gpt-4.1
YAML
echo 'OPENAI_API_KEY=sk-...' > <workspace>/.forge/.env
chmod 600 <workspace>/.forge/.env
echo '.env' > <workspace>/.forge/.gitignore
```

Then launch `forge ui --dir <workspace>`. The `forge ui` process
consults `<workspace>/.forge/.env` for any env var named in `ui.yaml`,
with the OS environment as a fallback.

### Via the API

```sh
# Persist config + key in one PUT (api_key is optional; omit to leave
# the saved key unchanged).
curl -X PUT http://localhost:4200/api/settings/skill-builder \
  -H 'Content-Type: application/json' \
  -d '{"provider":"openai","model":"gpt-4.1","api_key":"sk-..."}'

curl http://localhost:4200/api/settings/skill-builder
```

### Via the API

```sh
curl -X PUT http://localhost:4200/api/settings/skill-builder \
  -H 'Content-Type: application/json' \
  -d '{"provider":"openai","model":"gpt-4.1"}'

curl http://localhost:4200/api/settings/skill-builder
```

## Status banner semantics

The skill builder header shows the resolved configuration plus a hint
about where it came from:

| Banner says | What it means |
|---|---|
| `openai/gpt-4.1` (clean) | Workspace config resolved successfully; API key found. |
| `openai/gpt-4.1` + **using agent fallback (deprecated)** | No workspace/user config exists; the picked agent's `forge.yaml` is being used. Configure workspace settings to migrate. |
| `openai/gpt-4.1` + **API key not configured (env: OPENAI_API_KEY)** | Config resolved but the named env var is empty in the `forge ui` process. Set it and reload. |
| **Workspace skill-builder LLM is not configured** | First-run state. Click Configure. |

## How the builder converses

The Skill Builder is a multi-turn chat: the full conversation is replayed
to the LLM on every turn with the Skill Designer system prompt
(`forge-ui/skill_builder_context.go`). The prompt enforces an
**interview-with-convergence** style so the session produces a skill fast
instead of looping:

- Reads the whole conversation each turn and **never re-asks** an answered
  question.
- Asks **at most one** clarifying question per turn, and only when a
  genuinely blocking unknown remains.
- Drafts the moment it knows the four essentials — the task + tool(s), the
  credentials/env, the command-line tools the scripts invoke, and an install
  recipe for any binary the base image lacks — preferring a sensible default
  (noted in the skill) over another question. It will not draft with an
  invented package name or download URL.

### Built-in tool awareness (issue #270)

The prompt lists Forge's always-registered built-in tools and tells the
builder to **prefer them** over a custom tool or tool-less prose:

- `datetime_now`, `web_search`, `http_request`, `json_parse`, `csv_parse`,
  `math_calculate`, `uuid_generate`, `file_create`, and the scheduler family
  (`schedule_set` / `schedule_list` / `schedule_delete` / `schedule_history`).
  A prompt test pins this list against `builtins.All()` so it can't drift as
  new default built-ins ship.
- A skill USES a built-in by instructing the agent to call it **by name** —
  no `## Tool:` section, no script, no `requires.bins`. Those are only for
  *custom* tools the skill provides. A built-in-only skill (e.g. "reply with
  the current Brisbane time in German" → call `datetime_now`) has no
  `## Tool:` section at all, and the builder no longer scaffolds a redundant
  `brisbane_time` tool.
- There is **no "conversational only" path** for anything needing live data
  (time, live web, an API, a calculation): the agent can't know it without a
  tool call, so a tool-less skill would only hallucinate. The builder must
  wire the built-in.
- **Role separation:** the builder AUTHORS a `SKILL.md` — it never performs
  the requested behavior in chat and never fabricates tool output (e.g.
  inventing a specific time). The skill goes in the `skill` field, not a
  role-played `message`.
- **Scheduling (gated):** for time/event-oriented skills (recurrence,
  reminders, monitoring, digests) the builder proactively asks once whether
  the task should run on a schedule and, if so, wires `schedule_set` with the
  parsed cadence — writing "runs daily" in prose schedules nothing. It does
  not ask for skills with no temporal dimension. An explicit scheduling
  request is always honored. **Kubernetes caveat:** dynamic `schedule_set`
  calls are gated by `scheduler.kubernetes.allow_dynamic` (off by default), so
  a scheduling skill won't self-register on a default K8s deploy unless the
  operator enables it or declares the schedule in `forge.yaml`.

### Custom binaries

A skill's `requires.bins` entry can be a bare name (already in the base
image) **or** a mapping that also tells the build how to install a binary
the base image lacks — the builder emits the right one:

- `- {name: ripgrep, apt: ripgrep}` (or `apk:` on the Alpine base)
- `- {name: mytool, url: "https://…/mytool", dest: /usr/local/bin/mytool, chmod: "0755"}`
- `- {name: foo, run: ["curl -L https://… | tar xz -C /usr/local/bin"]}`

The builder will ask for a package name or download URL rather than invent
one.

### Structured output (`{message, skill}`)

Each turn the builder returns a single JSON envelope, not markdown fences:

```json
{
  "message": "<the chat reply: a question, a chosen default, or a draft summary>",
  "skill": null
}
```

`skill` stays `null` while the interview is still converging. The moment the
skill is draftable it becomes the full artifact:

```json
{
  "message": "Here's your skill.",
  "skill": {
    "skill_md": "<the complete SKILL.md content>",
    "scripts": { "my-tool.sh": "<complete script>" }
  }
}
```

The chat handler parses this envelope (`parseSkillEnvelope`), streaming a
content-free `progress` keepalive during generation and delivering the
`message` and `skill_draft` at completion — the UI renders `message` in the
chat and loads `skill_md`/`scripts` into the editor. This replaces the old
quadruple-backtick fence format, which LLMs frequently mis-nested. A model
that still emits the legacy fences degrades gracefully: the handler falls
back to fence extraction so the draft is never silently lost.

In edit mode the full updated skill still comes back in `skill.skill_md`
(never a partial diff), with the `**Changed:**` summary in `message`.

### What it always gets right

Regardless of the conversation, generated skills keep the Forge runtime
contract: scripts read their JSON input from `$1` (`INPUT="${1:-}"`), emit
structured JSON (never raw text), each `## Tool:` section carries an Input
table + Output schema + request→input examples, and edit mode preserves
existing `## Tool:` names (renaming breaks wired agents — issue #193).

## Why split `ui.yaml` and `.env`?

Same trust-boundary reasoning as `forge init`'s `forge.yaml` / `.env`
split:

- `ui.yaml` is non-secret (provider name, model name, base URL, env
  var name). Operators may want to check this into their workspace
  repo so the team shares the same skill-builder configuration.
- `.env` is the secret material (API key value). It lives under
  `.forge/` with mode 0600 and an auto-generated `.gitignore` that
  protects it from being committed.

If you want to keep skill-builder credentials separate from per-agent
runtime credentials (recommended, especially when agents point at
OpenAI-compatible endpoints other than openai.com), set
`api_key_env: WORKSPACE_LLM_KEY` (or similar). The key value lands at
`<workspace>/.forge/.env` under that name; per-agent runtime
credentials in each `<agent>/.env` stay untouched.

## Decoupling rules the implementation enforces

These are pinned by regression tests:

- The skill builder LLM is independent of any agent's runtime LLM.
  Agent A can ship with `provider: anthropic` and Claude while you
  use GPT-4.1 to build skills.
- The skill builder **never** calls `os.Setenv` on the `forge ui`
  process. Credentials are passed via request-scoped values.
- The previous `SkillBuilderCodegenModel` mapping (which forced
  `gpt-4.1` / `claude-opus-4-6` regardless of the agent's configured
  model) is removed. The operator's chosen model is used verbatim.
