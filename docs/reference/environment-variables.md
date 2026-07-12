---
title: "Environment Variables"
description: "All environment variables supported by Forge."
order: 3
---

## Environment Variables

| Variable | Description |
|----------|-------------|
| `FORGE_MODEL_PROVIDER` | Override LLM provider |
| `FORGE_MODEL_FALLBACKS` | Fallback chain (e.g., `"anthropic:claude-sonnet-4,gemini"`) |
| `FORGE_MEMORY_PERSISTENCE` | Set `false` to disable session persistence |
| `FORGE_MEMORY_LONG_TERM` | Set `true` to enable long-term memory |
| `FORGE_COMPRESSION` | Set `true`/`false` to override `compression.enabled` (reversible context compression); the `--compression` flag overrides both |
| `FORGE_EMBEDDING_PROVIDER` | Override embedding provider |
| `OPENAI_API_KEY` | OpenAI API key |
| `OPENAI_ORG_ID` | OpenAI Organization ID (enterprise); overrides `organization_id` in YAML |
| `ANTHROPIC_API_KEY` | Anthropic API key |
| `GEMINI_API_KEY` | Google Gemini API key |
| `TAVILY_API_KEY` | Tavily web search API key |
| `PERPLEXITY_API_KEY` | Perplexity web search API key |
| `WEB_SEARCH_PROVIDER` | Force web search provider (`tavily` or `perplexity`) |
| `OPENAI_BASE_URL` | Override OpenAI base URL |
| `ANTHROPIC_BASE_URL` | Override Anthropic base URL |
| `OLLAMA_BASE_URL` | Override Ollama base URL (default: `http://localhost:11434`) |
| `FORGE_CORS_ORIGINS` | Comma-separated CORS allowed origins for A2A server |
| `FORGE_AUTH_URL` | External auth provider URL for token validation |
| `FORGE_AUTH_ORG_ID` | Organization ID sent to external auth provider |
| `FORGE_AGENT_ID` | Agent identifier for audit entity identity (falls back to `agent_id` in YAML) |
| `FORGE_ORG_ID` | Organization identifier for tenancy stamping / audit |
| `FORGE_PASSPHRASE` | Passphrase for encrypted secrets file |

## Audit

Environment knobs for the audit pipeline. See
[Audit logging](../security/audit-logging.md) and
[Audit signing](../security/audit-signing.md) for the full model.

### Payload capture (FWS-8)

By default audit events are **metadata only** (sizes, counts, durations —
no prompts, completions, or raw tool I/O). These vars opt into raw
payloads field by field. `forge.yaml`'s `audit.capture` block overrides
these; the built-in default is all-off with redaction on. See
[Payload capture](../security/audit-logging.md#payload-capture-fws-8).

| Variable | Type | Default | Description |
|---|---|---|---|
| `FORGE_AUDIT_CAPTURE_TOOL_ARGS` | bool | `false` | Capture raw tool input on `tool_exec` (phase=start) |
| `FORGE_AUDIT_CAPTURE_TOOL_RESULT` | bool | `false` | Capture raw tool output on `tool_exec` (phase=end) |
| `FORGE_AUDIT_CAPTURE_LLM_MESSAGES` | bool | `false` | Capture the chat-messages array on `llm_call` |
| `FORGE_AUDIT_CAPTURE_LLM_RESPONSE` | bool | `false` | Capture the model completion text on `llm_call` |
| `FORGE_AUDIT_CAPTURE_REDACT` | bool | `true` | Run the vendor-secret regex scrub over captured fields before truncation. Turn off only when a downstream sink scrubs |
| `FORGE_AUDIT_CAPTURE_MAX_BYTES` | int | `16384` | Single-knob per-field byte cap applied to every captured field |

> Captured payloads can carry prompts, tool I/O, and PII even with
> redaction on — route the audit stream to a store appropriate to that
> sensitivity, and prefer enabling capture per-session for debugging
> rather than always-on.

### Signing (R6)

Opt-in Ed25519 signing of each audit event. When the key is unset the
stream is emitted unsigned. See
[Audit signing](../security/audit-signing.md).

| Variable | Default | Description |
|---|---|---|
| `FORGE_AUDIT_SIGNING_KEY_B64` | (unset) | Ed25519 private key as base64-encoded PKCS#8 DER **or** an inline PEM string. Setting it turns signing on |
| `FORGE_AUDIT_SIGNING_KID` | `forge-audit-v1` | Key id stamped as `kid` on signed events and advertised at `/.well-known/forge-audit-keys` |

### Export sinks (FWS-7)

Add a second sink alongside the always-on stderr safety net. If both a
socket and an HTTP endpoint are set, the socket wins.

| Variable | Default | Description |
|---|---|---|
| `FORGE_AUDIT_SOCKET` | (unset) | Unix socket path the sidecar listens on; enables the socket export sink |
| `FORGE_AUDIT_HTTP_ENDPOINT` | (unset) | HTTP endpoint for the export sink (used when `FORGE_AUDIT_SOCKET` is unset) |
| `FORGE_AUDIT_WRITE_TIMEOUT` | (impl default) | Go duration (e.g. `2s`) bounding each sink write; applies to both socket and HTTP sinks |

## OpenTelemetry tracing (OTel v1, #108)

Forge honors the standard OpenTelemetry SDK environment variables for
the `observability.tracing` subsystem. They sit between
`forge.yaml` and `--otel-*` CLI flags in the precedence stack — env
overrides yaml, flags override env. A set-but-empty env var does
**not** wipe a non-empty yaml value (absence-of-value is "no override").

See [Observability — Tracing](../core-concepts/observability-tracing.md)
for the full reference.

| Variable | Maps to |
|---|---|
| `OTEL_SDK_DISABLED` | inverted → `observability.tracing.enabled` |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | `endpoint` (preferred — signal-specific) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `endpoint` (generic fallback) |
| `OTEL_EXPORTER_OTLP_PROTOCOL` | `protocol` (`http/protobuf` or `grpc`) |
| `OTEL_EXPORTER_OTLP_HEADERS` | `headers` (merged with yaml; env wins on key collision) |
| `OTEL_EXPORTER_OTLP_TIMEOUT` | `timeout` (milliseconds) |
| `OTEL_SERVICE_NAME` | `service_name` |
| `OTEL_RESOURCE_ATTRIBUTES` | `resource_attrs` (merged with yaml) |
| `OTEL_TRACES_SAMPLER` | `sampler` (standard names) |
| `OTEL_TRACES_SAMPLER_ARG` | `sampler_ratio` |
