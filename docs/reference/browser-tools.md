---
title: "Browser Tools"
description: "Opt-in headless-browser tool family for navigating, reading, and interacting with web pages."
order: 11
---

Forge agents can drive a real headless browser (Chromium) to accomplish web
tasks a plain `http_request` cannot: pages that render content with
JavaScript, multi-step flows, forms, and anything gated behind a click. The
capability is **opt-in** — registered only when an active skill asks for it —
and every request is forced through the agent's egress proxy.

## Activation

The browser tool family (`browser_navigate`, `browser_state`,
`browser_click`, `browser_fill`, `browser_extract`, `browser_screenshot`)
registers only when **both** conditions hold:

1. An active skill declares the capability in its `SKILL.md` frontmatter:

   ```yaml
   metadata:
     forge:
       requires:
         capabilities:
           - browser
   ```

2. A Chromium-compatible binary is found at startup (`chromium`,
   `chromium-browser`, `google-chrome`, `headless-shell`, …, or the path in
   `FORGE_BROWSER_BIN`).

If a skill declares the capability but no browser binary is found, the agent
starts without the browser tools and logs an actionable error. Agents with no
browser-requiring skill never register the tools, never start the proxy for
the browser, and never launch Chromium.

This mirrors the `cli_execute` conditional-registration model: the skill layer
holds task and policy; the tool layer holds the capability.

## The digest model

The tools are token-optimized. The LLM never sees raw HTML. Every observation
is a compact **digest**:

```
Page: Pricing — Vendor
URL: https://vendor.example/pricing
Generation: 7 (pass as "generation" to browser_click/browser_fill; indices reset when the page changes)

Interactive elements:
[0] link "Products" -> /products
[2] input(email) "Work email"
[3] input(password) "Password" ⚠ fill-protected
[5] select "Plan" = "Starter" [Starter, Pro, Enterprise]
(showing 100 of 143 elements — browser_state with a larger max_elements or scrolling to see more)

--- page text (first 1200 of 9400 chars; browser_extract for more) ---
...
```

Interactions reference an element by its `[N]` index plus the digest's
`generation` number. Because the LLM acts by index instead of composing CSS
selectors from raw HTML, a complex page costs ~1–3 KB per observation instead
of 50–200 KB, and each action returns the new state — collapsing
observe→act→observe into one round trip.

If an action references a stale generation (the page navigated or mutated
since the digest), the tool returns an error **with a fresh digest** so the
model recovers in one turn.

## Tools

| Tool | Input | Returns |
|------|-------|---------|
| `browser_navigate` | `url`, `wait_ms?` | page digest |
| `browser_state` | `max_elements?`, `scroll_pages?`, `scroll_to_index?` | fresh digest |
| `browser_click` | `index`, `generation` | confirmation + digest |
| `browser_fill` | `index`, `text`, `generation`, `submit?` | confirmation + digest |
| `browser_extract` | `mode?` (`text`\|`links`\|`html`), `selector?`, `max_chars?`, `offset?` | paginated content |
| `browser_screenshot` | `full_page?`, `filename?` | file artifact (PNG) |

`browser_extract` defaults to readable markdown text; `html` mode is
selector-scoped (full-page HTML is never returned). `browser_screenshot`
writes a PNG to the agent's files directory and returns a path-only JSON
result — the image is uploaded to the channel as an attachment and never
enters the LLM conversation.

## Security

- **Egress.** All browser traffic routes through the same `EgressProxy` used
  by `http_request` and `cli_execute` — identical allowlist, SSRF IP
  validation, and DNS-rebinding protection. Chromium launches with
  `--proxy-server` pointing at the proxy and `--proxy-bypass-list=<-loopback>`
  so even localhost traffic is proxied. A navigation to a non-allowlisted
  domain is refused and surfaced as an egress-policy error. When a skill
  declares the browser capability, the proxy is started even in-container and
  in dev-open mode; the browser never runs unproxied.
- **Guardrails.** Skill `deny_output` patterns redact or block secrets in page
  digests and extracted content. `deny_commands` patterns can constrain
  navigation URLs and typed form content. The tools are deniable via
  `denied_tools` like any other.
- **Form safety.** `browser_fill` refuses password and payment fields
  (`type=password`, `autocomplete` `cc-*` / `…-password`) — marked
  `⚠ fill-protected` in the digest — unless the skill opts in:

  ```yaml
  metadata:
    forge:
      guardrails:
        browser:
          allow_sensitive_fill: true
  ```
- **Audit.** The security analyzer scores the browser capability high-risk
  (+15). Declaring `browser` while `trust_hints.network: false` is a Critical
  trust violation; declaring it with no `deny_output` guardrail is a warning.
- **No persistence.** Each run uses a throwaway browser profile — no cookies
  or session state carries across runs.

## Environment variables

| Variable | Effect |
|----------|--------|
| `FORGE_BROWSER_BIN` | Absolute path to the Chromium binary (overrides discovery). |
| `FORGE_BROWSER_HEADLESS` | `false` or `0` runs headful (local debugging only). Default headless. |

## Packaging

`forge build` installs Chromium into the image only when the agent's skills
declare the browser capability; other agents get no browser dependency.
Because browsing requires network, `forge package --prod` rejects a browser
skill configured with `dev-open` egress — ship a curated `egress_domains`
allowlist.

## Troubleshooting

- **Tools not registered.** Confirm a skill declares
  `requires.capabilities: [browser]` and that a browser binary is on `PATH`
  or `FORGE_BROWSER_BIN` is set. Check startup logs for the reason.
- **Navigation blocked.** The domain is not in the egress allowlist — add it
  to the skill's `egress_domains`.
- **Stale index errors.** Always drive from the most recent digest; indices
  and the generation number reset whenever the page changes.
