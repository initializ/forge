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
	// carry the family name (claude-opus-4…, claude-sonnet-5…) so the family
	// prefixes cover them.
	"claude-3", "claude-opus", "claude-sonnet", "claude-haiku",
	// Google Gemini (served through the OpenAI-compat client).
	"gemini-1.5", "gemini-2",
}

// pdfCapablePrefixes lists model-name prefixes whose models accept a PDF
// document natively via Anthropic's document block (#255 Phase 3). Scoped
// conservatively to the SONNET and OPUS families, which are confirmed on
// Anthropic's PDF-support matrix: the 3.5/3.7 Sonnet releases plus the 4.x/5
// Sonnet & Opus families (claude-sonnet-4…, claude-sonnet-5, claude-opus-4…).
//
// Deliberately NARROW (#534 review):
//   - HAIKU is excluded — Anthropic's PDF support was Sonnet-first and Haiku's
//     native-PDF coverage is unconfirmed; rejecting a Haiku PDF cleanly at the
//     gate is preferable to sending it and getting an opaque provider error.
//     (Fail closed; widen here once confirmed.)
//   - Bare "claude-3" (Claude 3.0) is excluded — it predates PDF support. The
//     3.0 models are claude-3-{opus,sonnet,haiku}, which do NOT match the
//     "claude-sonnet"/"claude-opus" family prefixes, so they fall through to a
//     loud reject. The 3.5/3.7 prefixes below name "sonnet" explicitly so they
//     don't catch claude-3-5-haiku.
//
// OpenAI Responses input_file and Gemini document support are deferred follow-ups.
var pdfCapablePrefixes = []string{
	"claude-3-5-sonnet", "claude-3-7-sonnet", "claude-opus", "claude-sonnet",
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
