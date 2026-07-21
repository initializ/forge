---
title: "Quick Start"
description: "Talk to a working agent in under 60 seconds with forge try."
order: 2
---

Talk to a working agent, and watch it use a tool, in under 60 seconds. One command:

```bash
brew install initializ/tap/forge && forge try
```

No build, no cluster, no config. `forge try` scaffolds a keyless demo agent into
a throwaway workspace, finds whatever model credential you already have (an env
key, an OpenAI sign-in, or a local Ollama), and drops you into a chat whose every
tool call and egress check renders inline.

## What it looks like

```
$ forge try

  forge try: talking to a live agent in your terminal.
  No build, no cluster. Ctrl-D or /exit to quit.

  Using OpenAI (signed in).
  Agent: quickstart · skills: weather · tools: http_request, datetime_now, math_calculate

  Try:  what's the weather in Tokyo?
        what's 17% of 4,200?
        what time is it in UTC?

you › what's the weather in Tokyo, should I pack an umbrella?

  ▸ tool   weather_current(location=Tokyo)
  ▸ egress wttr.in   ✓ allowed
  ◂ 18C, light rain this evening

agent › 18C in Tokyo with light rain expected tonight. Yes, take the umbrella.

  audit  {"tools":["weather_current"],"egress":["wttr.in:allow"]}

you › ^D

  You just ran an agent whose every tool call and egress you can see and audit.
  Want to keep it and make it yours?  ->  forge try --keep   (writes ./forge-quickstart)
  Then edit skills/, run it as a service with forge serve, or deploy with forge package.
```

The inline `▸ tool` / `▸ egress` lines are the agent's own audit stream, the same
signal the enterprise story rests on, surfaced as the first-run view. Add `--audit`
to see the full NDJSON, or `--quiet` to hide the loop.

## One-shot mode

For a non-interactive taste (CI, docs, a quick check):

```bash
forge try --once "what's 2 + 2?"
```

## No credential yet?

`forge try` resolves, in order: an explicit `--provider`/`--model`, then an env
key (`ANTHROPIC_API_KEY` / `OPENAI_API_KEY` / `GEMINI_API_KEY`), then a saved
OpenAI sign-in, then a local [Ollama](https://ollama.com) daemon. With none of
those, it offers a one-time picker (sign in with OpenAI, paste a key, or use
Ollama). Nothing is written to disk unless you pass `--keep`.

## The ladder

`forge try` is rung one. Each step adds one capability:

1. **60s** — `forge try`: talk to an agent, watch it use a tool.
2. **5 min** — `forge try --keep`, then edit `skills/<name>/SKILL.md` and add a skill. See [Your First Skill](your-first-skill.md).
3. **15 min** — `forge serve`, tail the audit log, add a guardrail and watch it block. See [Ship to Production](ship-to-production.md).
4. **Ship** — `forge package`: an egress-enforced container into your own cluster. See [Ship to Production](ship-to-production.md).

## Next steps

- [Your First Skill](your-first-skill.md) — teach the agent something new.
- [Ship to Production](ship-to-production.md) — the full init -> build -> package -> deploy pipeline.
- [Installation](installation.md) — every install method.
