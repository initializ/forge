package runtime

import "strings"

// visionCapablePrefixes lists model-name prefixes whose models accept image
// input via their provider's native multimodal API (#255). It mirrors the
// prefix-map style of ModelContextWindows and is the single runtime source of
// truth for image capability — the catalog is display-only and can't describe
// Claude (its Models list is empty).
//
// Conservative by design: a model NOT matched here is treated as text-only, so
// an image sent to it is rejected loudly at ingest rather than silently dropped
// or sent as a malformed request. Base "gpt-4"/"gpt-3.5" are intentionally
// absent (only the vision variants are listed).
var visionCapablePrefixes = []string{
	// OpenAI (chat + reasoning models with vision).
	"gpt-4o", "gpt-4.1", "gpt-4-turbo", "gpt-4-vision", "gpt-5",
	"o1", "o3", "o4",
	// Anthropic: Claude 3 and later are all vision-capable. The 4.x/5 models
	// carry the family name (claude-opus-4…, claude-sonnet-5…, claude-fable-5)
	// so the family prefixes cover them. Every pdfCapablePrefixes family is
	// listed here too — Claude document support is built on vision infra, so a
	// PDF-capable model is necessarily vision-capable.
	"claude-3", "claude-opus", "claude-sonnet", "claude-haiku", "claude-fable",
	// Google Gemini (native generateContent client). The flagship + flash/pro
	// families are multimodal; broad prefixes cover 1.5/2.x and the 3.x/4
	// generations.
	"gemini-1.5", "gemini-2", "gemini-3", "gemini-4",
}

// pdfCapablePrefixes lists model-name prefixes whose models accept a PDF
// document natively — via Anthropic's document block (#255 Phase 3) or Gemini's
// inlineData (the native Gemini client, #255 follow-up). Covers the confirmed
// families: Anthropic Sonnet (3.5/3.7 + 4.x/5), Opus (4.x), Haiku 4.5+, Fable 5;
// and Gemini 1.5 / 2.x (native generateContent handles PDF inlineData).
//
// Prefix design (#534 review):
//   - "claude-haiku" matches the 4.x/5 family naming (claude-haiku-4-5…), which
//     Anthropic confirms supports native PDF. It does NOT match the older
//     "claude-3-5-haiku" naming, so Haiku 3.5 (unconfirmed) stays excluded.
//   - The 3.5/3.7 prefixes name "sonnet" explicitly so they don't catch
//     claude-3-5-haiku.
//   - Bare "claude-3" (Claude 3.0) is excluded — it predates PDF support. Its
//     models are claude-3-{opus,sonnet,haiku}, which don't match the
//     "claude-opus"/"claude-sonnet"/"claude-haiku" family prefixes, so they
//     fall through to a loud reject.
//
// Unknown-model default is fail-closed (loud reject, not a provider error).
// OpenAI Responses input_file document support remains a deferred follow-up.
var pdfCapablePrefixes = []string{
	"claude-3-5-sonnet", "claude-3-7-sonnet", "claude-opus", "claude-sonnet", "claude-haiku", "claude-fable",
	"gemini-1.5", "gemini-2", "gemini-3", "gemini-4",
}

// ModelSupportsVision reports whether the named model accepts image input.
// Case-insensitive prefix match; unknown/empty models return false.
func ModelSupportsVision(model string) bool {
	return matchesPrefix(model, visionCapablePrefixes)
}

// ModelSupportsPDF reports whether the named model accepts a PDF document
// natively. Case-insensitive prefix match; unknown/empty models return false.
func ModelSupportsPDF(model string) bool {
	return matchesPrefix(model, pdfCapablePrefixes)
}

// ModelSupportsImageGen reports whether a provider+model can GENERATE images
// (the `image_generation`/EnableImageGeneration opt-in is meaningful), so UIs
// can offer the toggle only where it does something (#255):
//   - openai-responses: the image_generation built-in tool works on the general
//     models, so any model qualifies.
//   - gemini: only the image models — identified by "image" in the name
//     (gemini-3-pro-image, gemini-2.5-flash-image, …); responseModalities errors
//     on non-image Gemini models.
//
// Every other provider (openai chat, anthropic, bedrock, ollama) returns false.
func ModelSupportsImageGen(provider, model string) bool {
	p := strings.ToLower(strings.TrimSpace(provider))
	m := strings.ToLower(strings.TrimSpace(model))
	switch p {
	case "openai-responses":
		return true
	case "gemini":
		return strings.Contains(m, "image")
	default:
		return false
	}
}

func matchesPrefix(model string, prefixes []string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return false
	}
	for _, p := range prefixes {
		if strings.HasPrefix(m, p) {
			return true
		}
	}
	return false
}

// IsImageMIME reports whether a MIME type denotes an image forge can forward as
// inline vision input. Restricted to the formats the vision providers accept
// (Anthropic + OpenAI both support png/jpeg/gif/webp).
func IsImageMIME(mime string) bool {
	switch NormalizeImageMIME(mime) {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	}
	return false
}

// IsDocumentMIME reports whether a MIME type denotes a document forge can
// forward as a native document block. Restricted to PDF today (the only format
// with native provider support without extraction).
func IsDocumentMIME(mime string) bool {
	return strings.ToLower(strings.TrimSpace(mime)) == "application/pdf"
}

// NormalizeImageMIME lowercases and canonicalizes an image MIME type — notably
// mapping the common non-standard "image/jpg" to "image/jpeg", which is the
// form Anthropic's image source block requires.
func NormalizeImageMIME(mime string) string {
	m := strings.ToLower(strings.TrimSpace(mime))
	if m == "image/jpg" {
		return "image/jpeg"
	}
	return m
}
