---
title: "Computer Use (experimental)"
description: "Opt-in vision-driven computer use for forge agents — read-only screenshots in Phase 1."
---

# Computer Use (experimental)

Give an agent the ability to **see the screen** (and, in later phases, operate it)
via a vision model. **Phase 1 is read-only**: a `computer_screenshot` tool that
captures the screen and returns it to the model using the image-bearing
tool-result capability. Control actions (click/type/key/scroll) are a later phase.

> **Off by default.** Nothing is enabled unless you set `computer_use.enabled: true`.
> See the [design doc](../design/computer-use.md) and the tracking issue for the
> full plan (profiles, topologies, containment, phasing).

## Enabling it

```yaml
# forge.yaml
computer_use:
  enabled: true
  profile: desktop          # "desktop" (host screen) — "browser" is a follow-up
  allow_native_host: true   # REQUIRED for the desktop profile (see below)
```

| Field | Meaning |
|-------|---------|
| `enabled` | Master switch. Off by default. |
| `profile` | `desktop` captures the host screen (macOS, Phase 1). `browser` (the headless-viewport profile) is not implemented yet. Default `desktop`. |
| `allow_native_host` | **Explicit acknowledgment** that the agent will capture the operator's own screen. The desktop profile **refuses to register without it** — a full-screen capture is sensitive. |
| `max_steps` | Reserved for Phase 2 (control actions). |

When enabled and gated, the agent gains one read-only tool: **`computer_screenshot`**.

## macOS permission (Screen Recording)

Desktop capture uses [robotgo](https://github.com/go-vgo/robotgo) (the single
native desktop backend; it also drives mouse/keyboard for the Phase-2 control
actions). robotgo is **cgo** and builds only on macOS with the Xcode command-line
tools — forge builds it behind a `darwin && cgo` tag, so Linux/Windows and
non-cgo builds compile a stub instead. macOS gates screen capture behind the
**Screen Recording** TCC permission:

1. Run the agent once; macOS prompts for Screen Recording for the terminal/host
   process (or grant it in **System Settings → Privacy & Security → Screen
   Recording**).
2. Restart the process after granting — macOS only applies the permission to new
   processes.

Without the permission, the capture returns a blank/denied image and the tool
surfaces an error (it does not silently send an empty screen).

## Security posture

- **A screenshot sends your entire screen to the model** — treat it like any other
  high-trust capability. Enable it only on agents and screens you intend the model
  to see, and prefer a dedicated/throwaway machine or account for anything beyond
  dogfooding (see the [design doc](../design/computer-use.md) → Deployment
  topologies and containment).
- **Opt-in + acknowledged:** `enabled` plus `allow_native_host` for the desktop
  profile; the tool is never registered otherwise.
- **Audited:** each capture runs as a normal tool call and is recorded in the
  [audit stream](audit-logging.md) (`tool_exec`). Richer per-action audit arrives
  with the Phase-2 control actions.
- **Read-only (Phase 1):** the tool only observes — it cannot click or type.
- Media bytes ride inline for the turn only and are **not persisted** into session
  history (ephemeral per-step frames).

## Scope and roadmap

- **Phase 1 (this):** read-only `computer_screenshot`, desktop profile, macOS.
- **Phase 2:** control actions (click/type/key/scroll) behind a session-start
  approval + step cap, and a native provider computer tool.
- **Later:** the browser profile (CDP viewport), a dedicated-runner topology, and
  Windows. See the [design doc](../design/computer-use.md).
