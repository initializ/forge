---
title: "Multimodal I/O over A2A"
description: "Sending images and PDFs to a forge agent, and receiving media back, over the A2A protocol."
order: 9
---

# Multimodal I/O over A2A

Forge agents accept **images** and **PDF documents** as input and can return media as output — over the **existing A2A message schema**. No envelope change was required: A2A already modeled media as a `file` part; forge now honors those parts instead of dropping them (#255).

> **Backward compatible.** Text-only messages are unchanged and serialize byte-for-byte as before. Media support is purely additive.

## The A2A shape (unchanged)

A message is a list of typed parts. The media type is `file`:

```jsonc
// a2a.Part
{ "kind": "file", "file": { "name": "photo.png", "mimeType": "image/png", "bytes": "<base64>" } }
```

`FileContent` fields: `name` (optional), `mimeType` (required for media), `bytes` (the raw file **standard-base64-encoded**), or `uri` (by reference).

> **Inline `bytes` only, today.** Forge feeds media to the model from inline `bytes`. A `file` part with only a `uri` (no bytes) is **not fetched** — it's rejected. URI-fetch (with egress control) is a tracked follow-up. Send `bytes`.

## Input — send an image or PDF

### JSON-RPC (`POST /`)

```json
{
  "jsonrpc": "2.0", "id": 1, "method": "tasks/send",
  "params": {
    "id": "t-1",
    "message": {
      "role": "user",
      "parts": [
        { "kind": "text", "text": "What's in this image?" },
        { "kind": "file", "file": { "name": "photo.png", "mimeType": "image/png", "bytes": "iVBORw0KGgo..." } }
      ]
    }
  }
}
```

### REST (`POST /tasks/send`)

Same `message`, wrapped in the REST envelope:

```json
{ "task": { "id": "t-1", "message": { "role": "user", "parts": [ /* …same parts… */ ] } } }
```

A PDF is identical with `"mimeType": "application/pdf"`.

### What the model must support

Media the resolved model can't consume is **rejected loudly** (a 4xx / JSON-RPC error naming the part + reason) — never silently dropped and answered anyway.

| Media | MIME | Capable models |
|-------|------|----------------|
| Image | `image/png`, `image/jpeg`, `image/gif`, `image/webp` | vision models — OpenAI `gpt-4o`/`gpt-4.1`/`gpt-5`/`o1`/`o3`/`o4`, Anthropic Claude 3+, Gemini 1.5/2 |
| PDF | `application/pdf` | Anthropic Sonnet 3.5+, Opus 4+, Haiku 4.5+, Fable 5 |

### Limits (enforced at ingest)

| Bound | Limit | Reject reason |
|-------|-------|---------------|
| Per-image bytes | 5 MiB | `image_limit_exceeded` |
| Image dimensions | 100 000 px/side, 50 MP total | `image_limit_exceeded` |
| Images per message | 20 | `too_many_image_parts` |
| Per-PDF bytes | 32 MiB (+`%PDF-` sniff) | `document_limit_exceeded` |
| PDFs per message | 5 | `too_many_document_parts` |
| Request body | 32 MiB (both transports) | HTTP 413 |
| Concurrent media requests | 4 | 429 / unavailable (shed) |

A rejection emits an [`input_media_rejected`](../security/audit-logging.md) audit event. Note media **bytes aren't text-scannable**, so guardrail/intent scanning applies to the text/data parts only.

## Output — receive media back

Also the existing A2A shape: the response `message.parts` can carry `file` parts alongside text.

```json
{
  "role": "agent",
  "parts": [
    { "kind": "text", "text": "Here's the chart:" },
    { "kind": "file", "file": { "mimeType": "image/png", "bytes": "iVBORw0KGgo..." } }
  ]
}
```

File parts come from two sources:

- **Tools** that produce files (`file_create`, `browser_screenshot`) — always on.
- **Model-generated images** — opt-in via `models.default.image_generation: true` on an `openai-responses` model (sends the `image_generation` built-in tool; see [forge.yaml schema](forge-yaml-schema.md)).

## Persistence

Inbound uploads and model-generated output are written under the agent's files dir — `.forge/files/inbound/` and `.forge/files/generated/` respectively — content-addressed. Session history stores only the on-disk path (never base64), and media is reloaded per turn, so multi-turn conversations keep their media without bloating the session file. See [Runtime Engine → Image and document input](../core-concepts/runtime-engine.md#image-and-document-input-multimodal).

## In the web dashboard

The [chat UI](web-dashboard.md) has a **📎 attach** button: select one or more images/PDFs (validated against the accepted types and size caps above), send them with your message, and see images rendered inline and documents as download links — in both your message and the agent's reply.

## Follow-ups (not yet supported)

URI-fetch input (egress-gated) · OpenAI Responses `input_file` documents · Gemini/Bedrock media · text-extraction fallback for non-native document models · remote/distributed session media replay · streaming (partial-image) generated output.
