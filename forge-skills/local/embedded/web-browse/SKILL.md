---
name: web-browse
icon: 🌐
category: web
tags:
  - browser
  - web
  - scraping
  - automation
  - extraction
description: Drive a headless browser to navigate pages, read content, click, and fill forms — for sites that need JavaScript rendering or interaction beyond a plain HTTP fetch.
metadata:
  forge:
    requires:
      capabilities:
        - browser
    egress_domains:
      - "$BROWSE_ALLOWED_DOMAIN"
    trust_hints:
      network: true
      filesystem: none
      shell: false
    guardrails:
      deny_output:
        - pattern: '-----BEGIN (CERTIFICATE|RSA PRIVATE KEY|EC PRIVATE KEY|PRIVATE KEY)-----'
          action: block
        - pattern: '(?i)\b(api[_-]?key|secret|password|bearer)\b\s*[:=]\s*\S{8,}'
          action: redact
    timeout_hint: 120
---

# Web Browse

Drive a real headless browser (Chromium) to accomplish web tasks that a plain
`http_request` cannot: pages that render content with JavaScript, multi-step
flows, forms, and anything gated behind a click.

The browser is registered only because this skill declares
`requires.capabilities: [browser]`. All navigation is forced through the
agent's egress proxy, so it obeys the same allowlist, SSRF, and
DNS-rebinding protections as every other web tool. Add the domains you need to
`egress_domains`; a page that redirects off-allowlist is blocked.

## Tools

You drive the browser through six tools. You never see raw HTML — every
observation is a compact **digest**: the page title, URL, a numbered list of
interactive elements (`[3] input(email) "Work email"`), and the start of the
page text.

- **browser_navigate** `{url, wait_ms?}` — load a page, get its digest.
- **browser_state** `{max_elements?, scroll_pages?, scroll_to_index?}` —
  re-read the current page (after it changed on its own, to scroll, or to see
  more elements). Returns a fresh digest with new indices and generation.
- **browser_click** `{index, generation}` — click the element at `index` from
  the latest digest. Returns the resulting page digest.
- **browser_fill** `{index, text, generation, submit?}` — type into an input
  (or pick a select option by label). `submit: true` presses Enter. Password
  and payment fields are refused unless this skill opts in.
- **browser_extract** `{mode?, selector?, max_chars?, offset?}` — pull page
  content: `text` (readable markdown, default), `links`, or `html` (scoped to
  a CSS selector). Long content is paginated — pass the `offset` from the
  previous call's header to continue.
- **browser_screenshot** `{full_page?, filename?}` — capture a PNG for the
  user (attached to the reply; you will not see the image). Use only when the
  user wants a visual — read pages with digests and browser_extract.

## Workflow

1. `browser_navigate` to the target URL. Read the digest's `[N]` elements and
   note the **Generation** number.
2. To act, pass that `generation` to `browser_click` / `browser_fill` with the
   element's index. Every action returns a fresh digest — read it, don't
   assume the old indices still hold.
3. If you act on a stale generation (the page navigated or changed), the tool
   returns an error **with a fresh digest** — retry using the new indices and
   generation.
4. For long articles or tables, `browser_extract` with `mode: text` and
   paginate via `offset`. Use `mode: links` to enumerate links.

## Notes

- Indices reset whenever the page changes. Always drive from the most recent
  digest.
- The browser keeps no cookies or profile between runs.
- Set `$BROWSE_ALLOWED_DOMAIN` (or replace it with a literal curated list) to
  the sites this agent may reach; production builds reject an open egress
  policy.
