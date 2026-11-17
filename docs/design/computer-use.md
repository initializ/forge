---
title: "Design: Computer Use for forge agents"
description: "Proposal for adding vision-driven computer-use capability (screenshot → action loop) to forge agents, one profile/OS at a time."
status: proposal
---

# Design: Computer Use for forge agents

**Status:** proposal (not yet implemented) · **Owner:** TBD · **Tracking issue:** TBD

Give a forge agent the ability to **operate a computer** — look at a screen, move the
mouse, click, type, and scroll — driven by a vision model in a screenshot→action
loop. Opt-in at agent creation, **macOS first**, containment-first.

> This is a planning document. Nothing here is built yet. The "Open decisions"
> section lists the forks to resolve before implementation.

## Goals / non-goals

**Goals**
- A `computer` tool family an agent can be granted at creation time.
- Use a provider's **native, trained** computer-use tool (Anthropic first) — don't invent an untrained action vocabulary.
- Reuse the #255 multimodal foundation (vision input, `MediaRef`, media persistence).
- Ship **one profile/OS at a time**, with containment designed in from Phase 1.

**Non-goals (initially)**
- A bespoke OS-automation DSL. The model already speaks the provider's computer vocabulary.
- Running computer use on the operator's primary desktop as the default (see Containment).
- Replacing the existing DOM-based `browser_*` tools — computer use is complementary (pixel/vision vs. DOM).

## The two deployment profiles

Computer-use use-cases split cleanly by **what is being driven**, and that split
decides the infra and trust model:

| | **Profile A — Browser CU (containerized)** | **Profile B — Desktop CU (native/VM host)** |
|---|---|---|
| Drives | A **browser viewport** (web apps) | A **full OS desktop** + native GUI apps |
| OS | **Linux**, as a disposable **container** | **macOS** and **Windows / Windows Server** |
| Why not the other OS | Desktop apps that need automating are Windows/macOS; Linux is the server/container target, not a desktop-app target | A browser alone doesn't need a full desktop |
| Execution backend | **CDP against the headless browser** (no desktop needed — see below) | **robotgo** (or OS built-ins) against the real/VM desktop |
| Containment | Ephemeral container; no host desktop; egress-bounded | Throwaway **VM** strongly recommended; TCC/UAC perms |
| Coordinate space | Browser viewport (WxH) | Screen (WxH) |

The key realization: **Profile A does not need a Linux desktop at all.** forge's
existing browser `Manager` already drives Chromium via CDP (chromedp). Coordinate
screenshots + synthetic input (`Input.dispatchMouseEvent` / `dispatchKeyEvent`,
exposed by chromedp as `MouseClickXY` / `KeyEvent`) run directly against the browser
viewport — so "browser computer use" is just a new **backend over the browser we
already launch**, no `Xvfb`/X11 desktop required. That makes the containerized
Linux profile both the simplest to build and the safest to run.

### One model contract, two backends — behind a provider-neutral seam

Both providers' computer tools emit the same action vocabulary against an abstract
coordinate space. So the model side is identical for both profiles; only the
executor differs. **Decision (4): the provider↔computer-tool translation sits
behind a provider-neutral seam from the start** — a small adapter that maps the
forge-internal action set to/from a provider's native computer tool. Implement
the **Anthropic** adapter first; **OpenAI Responses** then slots in as a second
adapter rather than a rewrite. The `computer.Backend` (executor) and the
`ProviderComputerAdapter` (translation) are independent axes:

```
   model (Anthropic computer_20250124 first; OpenAI Responses computer_use_preview later)
            │
            ▼
   ProviderComputerAdapter               // provider-neutral translation seam (axis 1)
   │  normalizes to forge actions: screenshot | mouse_move | left_click | type | key | scroll | cursor_position
            ▼
   computer.Backend interface            // executor (axis 2) — forge-cli/tools/computer
   ├─ BrowserBackend   (Profile A)       // CDP against the chromedp viewport (reuses browser.Manager)
   └─ DesktopBackend   (Profile B)       // robotgo (macOS now; Windows later); VM-hosted or native-host (flagged)
```

`Backend`:
```go
type Backend interface {
    Screenshot(ctx) (png []byte, w, h int, err error)
    MouseMove(ctx, x, y int) error
    Click(ctx, x, y int, button string) error
    Type(ctx, text string) error
    Key(ctx, combo string) error          // e.g. "cmd+c", "Return"
    Scroll(ctx, x, y, dx, dy int) error
    CursorPosition(ctx) (x, y int, err error)
}
```

## The one genuinely new internal capability: image-bearing tool results

Everything else is mechanical; this is the piece forge does not have today.

A computer-use loop is: model asks for a `screenshot` → harness runs it → **the
screenshot image goes back into the model's context** → model decides the next
action. But forge's tool path is **text-only**:

- `tools.Tool.Execute` returns `(string, error)` — `forge-core/tools/tool.go:37`
- the executor stores the result as `ChatMessage{Role: RoleTool, Content: <string>}` — `forge-core/runtime/loop.go:983`
- the Anthropic client serializes that as a **text** `tool_result` block — `forge-core/llm/providers/anthropic.go:327`

Contrast with `browser_screenshot`, which deliberately **does not** show the model
the image ("you will not see the image") — it attaches the PNG as a user artifact.
Computer use needs the opposite.

**Required change:** let a tool result carry an image the model sees.
- `llm.ChatMessage` already has `Parts []ContentPart` (from #255) — a `RoleTool`
  message can hold an image Part. The gap is (a) a tool-execution path that lets a
  tool return media (not just a string), and (b) the Anthropic/OpenAI clients
  serializing a `RoleTool` message's image Part as an **image `tool_result`
  content block**. Reuses the #255 `MediaRef` + base64 machinery end-to-end.

This is the load-bearing change and should be the Phase-0 spike.

## Provider computer-use tools (reuse what forge already has)

Don't build an action vocabulary — use a native one. Both providers forge already
has clients for support one:

A `ProviderComputerAdapter` wraps each, so only the first is built now and the
rest are additive (decision 4):

| Provider | Tool | Notes |
|---|---|---|
| **Anthropic** | tool type `computer_20250124` + beta header `computer-use-2025-01-24` | **First adapter (decision 4).** Most mature; model trained on it. Reference: `anthropics/anthropic-quickstarts` → `computer-use-demo` (runs in a Linux/Docker VM by default). |
| **OpenAI** | Responses `computer_use_preview` tool + `computer-use-preview` model | Second adapter — forge already has the `openai-responses` client, so it's a new adapter, not a rewrite. |
| Gemini | "Computer Use" preview | Newest/least proven; later. |

Client work for the Anthropic adapter (localized to `anthropic.go`): emit the
`computer` tool definition (display WxH + scale), set the beta header, accept
image `tool_result` content. The `computer` builtin's job is only to execute the
emitted actions and return screenshots.

## OS support matrix (backend layer)

`robotgo` (`go-vgo/robotgo`) is the cross-platform native backend candidate:

| OS | robotgo | Prerequisites | Caveats |
|---|---|---|---|
| **macOS** | ✅ | Xcode CLT (cgo) | TCC perms: Accessibility + Screen Recording (prompted once; the agent cannot self-grant) |
| **Windows / Server** | ✅ | MinGW/gcc for cgo (Win32) | Cannot drive UAC-elevated windows / the secure desktop; DPI scaling care |
| **Linux (native)** | ⚠️ **X11 only** | `libx11-dev`, `libxtst-dev`, `libxkbcommon-dev`, xcb, cgo | **Wayland (Ubuntu default) blocks input injection + screen capture.** Native-Wayland would need a different backend (`ydotool`/`uinput` + PipeWire/portal). |

**Linux is not a native-desktop target** (per the two-profile model). Linux CU =
Profile A, the **containerized browser backend (CDP)** — which sidesteps
X11/Wayland entirely because there is no desktop, just the browser viewport. (If a
true Linux *desktop* CU is ever needed, the clean path is a headless **`Xvfb` +
`xdotool`** display inside the container — the Anthropic-demo approach — not the
operator's Wayland session.)

## Security & containment (the crux)

Computer use can do anything the user can; this is the highest-risk capability
forge would ship. Containment is a feature, not an afterthought.

- **Off by default; opt-in only at agent creation.** (Same gating style as the
  `image_generation` / `suggest_actions` toggles.)
- **Containment default (decision 3):** a disposable **VM** is the recommended and
  eventual production default (macOS: `cirruslabs/tart` or `trycua/lume` on Apple
  Silicon; Windows: a throwaway Windows VM). For **early dogfooding**, native-host
  desktop CU is **permitted behind an explicit acknowledgment flag** — e.g.
  `computer_use.allow_native_host: true` (and/or a `FORGE_COMPUTER_USE_I_UNDERSTAND=1`
  env gate) — which logs a loud startup warning and is audited. Without the flag,
  a `desktop` profile on a non-VM backend refuses to start.
- **Profile A** is inherently safer: ephemeral container, no host desktop, and the
  agent's existing **egress allowlist** still bounds where the browser can go.
- **Approval (decision 5):** a **single session-start DEFER approval** gates the
  whole computer-use session, paired with a hard **`max_steps` cap** (and idle
  timeout) — not per-action approval. forge already has the human-in-the-loop
  approval machinery (`security.defer.tools.<tool>`); the computer tool requests
  one approval before the first action and the step cap bounds the blast radius
  thereafter. (Per-action approval remains a future opt-in for high-risk fleets.)
- **Step cap + idle timeout + kill switch**, and **audit every action** on the
  existing NDJSON stream (one event per action with coordinates/keys, like
  `tool_exec`).
- **Permissions are out-of-band**: macOS TCC (Accessibility + Screen Recording),
  Windows UAC — documented and prompted; the agent cannot grant them itself.
- Guardrails/egress continue to bound the agent's other tools.

## Creation-time enablement

A **"Computer use (experimental)"** step in `forge init` and the web create form,
gated exactly like the recent toggles — shown only when the chosen provider+model
is CU-capable **and** the deployment matches a supported profile. Enabling it:

- scaffolds the `computer` tool, **conditionally registered** in the runner
  (mirroring `interactiveChannelConfigured` for `suggest_actions`),
- writes a `computer_use:` config block — `profile: browser | desktop`,
  `backend: cdp | robotgo | vm`, `display: {width, height}`,
  `approval: session_start` (decision 5), `max_steps`, and (desktop-only)
  `allow_native_host: false` (decision 3),
- prints the permissions + sandbox warning and defaults approval **on**.

## Phasing (macOS desktop + Linux browser first)

| Phase | Scope | Risk |
|---|---|---|
| **0 — spike** | Image-bearing tool-result plumbing + a `robotgo` screenshot+click PoC; round-trip one screenshot→click through the Anthropic loop. | De-risks the one new internal capability. |
| **1 — read-only** | `screenshot` + `cursor_position` only; Anthropic loop; opt-in; audit every call. Both profiles. | Safe to dogfood. |
| **2 — control** | `click/type/key/scroll` behind a **session-start DEFER approval + `max_steps` cap** + kill switch (decision 5). | Mutating — approval-gated. |
| **3 — containment + breadth** | VM backend for Profile B (Tart/Lume); OpenAI Responses as 2nd provider; Windows desktop backend; Profile A hardening. | Production posture. |

Profile A (containerized browser CU) and Profile B/macOS (desktop CU) can progress
in parallel after Phase 0, since they share the model contract and the
image-tool-result plumbing and differ only in the `Backend` impl.

## Resolved decisions

1. **Profile B macOS backend:** **`robotgo`**, behind the `Backend` interface.
2. **Profile A backend:** **CDP against the existing browser** (`browser.Manager`) — no Linux desktop / Xvfb.
3. **Containment default for Profile B:** VM is the recommended/production default, but **native-host is allowed behind an explicit acknowledgment flag** for early dogfooding (loud warning + audited; refuses without the flag).
4. **Provider scope:** build the **provider-neutral action seam up front** (`ProviderComputerAdapter`), implemented **against Anthropic first**; OpenAI Responses slots in as a second adapter.
5. **Approval granularity (Phase 2):** **one session-start DEFER approval + `max_steps` cap** (not per-action; per-action is a future opt-in).

## Affected / new components (for the eventual implementation)

- **New:** `forge-cli/tools/computer/` — `Backend` interface, `BrowserBackend` (CDP, reuses `browser.Manager`), `DesktopBackend` (robotgo/macOS), the `computer` tool family + conditional registration.
- `forge-core/tools/tool.go` + `forge-core/runtime/loop.go` — image-bearing tool-result path.
- `forge-core/llm/providers/anthropic.go` (+ later `responses.go`) — native computer tool definition, beta header, image `tool_result` content.
- `forge-core/types/config.go` + `forge-cli/cmd/init.go` + `forge-ui` — `computer_use` config + creation toggle.
- Docs: `core-concepts/tools-and-builtins.md`, a new `security/computer-use.md` (permissions + sandbox runbook), `reference/forge-yaml-schema.md`.
