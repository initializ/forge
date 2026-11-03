package runtime

import "testing"

func TestModelSupportsVision(t *testing.T) {
	vision := []string{
		"gpt-4o", "gpt-4o-mini", "gpt-4.1", "gpt-4-turbo", "gpt-5",
		"o1", "o3-mini", "o4-mini",
		"claude-3-5-sonnet", "claude-opus-4-8", "claude-sonnet-5", "claude-haiku-4-5", "claude-fable-5",
		"gemini-1.5-pro", "gemini-2.5-flash", "gemini-3.8-flash", "gemini-4-argon",
		"GPT-4O", // case-insensitive
	}
	for _, m := range vision {
		if !ModelSupportsVision(m) {
			t.Errorf("ModelSupportsVision(%q) = false, want true", m)
		}
	}
	textOnly := []string{
		"", "gpt-3.5-turbo", "gpt-4", "gpt-4-0613", "text-embedding-3-large",
		"claude-2.1", "claude-instant-1.2", "llama3.1", "mistral-large", "some-random-model",
	}
	for _, m := range textOnly {
		if ModelSupportsVision(m) {
			t.Errorf("ModelSupportsVision(%q) = true, want false", m)
		}
	}
}

func TestModelSupportsImageGen(t *testing.T) {
	yes := []struct{ provider, model string }{
		// openai-responses: any model qualifies (built-in image_generation tool).
		{"openai-responses", "gpt-5"},
		{"openai-responses", ""},
		{"OpenAI-Responses", "gpt-4o"}, // case-insensitive
		// gemini: only image models (name contains "image").
		{"gemini", "gemini-3-pro-image"},
		{"gemini", "gemini-3.1-flash-lite-image"},
		{"gemini", "gemini-2.5-flash-image"},
		{"Gemini", "GEMINI-3-PRO-IMAGE"}, // case-insensitive
	}
	for _, c := range yes {
		if !ModelSupportsImageGen(c.provider, c.model) {
			t.Errorf("ModelSupportsImageGen(%q, %q) = false, want true", c.provider, c.model)
		}
	}
	no := []struct{ provider, model string }{
		{"gemini", "gemini-3.8-flash"}, // non-image Gemini model
		{"gemini", "gemini-4-argon"},
		{"gemini", ""},
		{"openai", "gpt-5"}, // chat completions, no image_generation tool
		{"anthropic", "claude-opus-4-8"},
		{"bedrock", "claude-sonnet-5"},
		{"ollama", "llama3.1"},
		{"", "gemini-3-pro-image"}, // empty provider
	}
	for _, c := range no {
		if ModelSupportsImageGen(c.provider, c.model) {
			t.Errorf("ModelSupportsImageGen(%q, %q) = true, want false", c.provider, c.model)
		}
	}
}

func TestModelSupportsPDF(t *testing.T) {
	pdf := []string{
		"claude-3-5-sonnet", "claude-3-7-sonnet", "claude-opus-4-8", "claude-sonnet-5", "claude-sonnet-4-5", "claude-haiku-4-5", "claude-fable-5",
		"gemini-2.5-flash", "gemini-1.5-pro", "gemini-3.8-flash", "gemini-3-pro-image", // native Gemini client handles PDF inlineData
	}
	for _, m := range pdf {
		if !ModelSupportsPDF(m) {
			t.Errorf("ModelSupportsPDF(%q) = false, want true", m)
		}
	}
	// Excluded: bare Claude 3.0, the 3.5 Haiku release (unconfirmed), non-Anthropic/Gemini.
	noPDF := []string{"", "claude-3-opus", "claude-3-sonnet", "claude-3-5-haiku", "gpt-4o", "gpt-5", "o3-mini"}
	for _, m := range noPDF {
		if ModelSupportsPDF(m) {
			t.Errorf("ModelSupportsPDF(%q) = true, want false", m)
		}
	}
}

// TestModelSupportsPDFForProvider covers the provider-aware PDF gate (#559):
// OpenAI accepts PDFs only on the Responses transport (input_file), never on
// plain chat-completions; Anthropic/Gemini are unchanged (name-based).
func TestModelSupportsPDFForProvider(t *testing.T) {
	yes := []struct{ provider, model string }{
		// OpenAI Responses: modern multimodal models carry PDF via input_file.
		{"openai-responses", "gpt-4o"},
		{"openai-responses", "gpt-4.1"},
		{"openai-responses", "gpt-5"},
		{"openai-responses", "o3"},
		{"OpenAI-Responses", "gpt-5"}, // case-insensitive provider
		// Non-OpenAI families keep working, independent of provider spelling.
		{"anthropic", "claude-sonnet-5"},
		{"gemini", "gemini-3-pro-image"},
	}
	for _, c := range yes {
		if !ModelSupportsPDFForProvider(c.provider, c.model) {
			t.Errorf("ModelSupportsPDFForProvider(%q, %q) = false, want true", c.provider, c.model)
		}
	}
	no := []struct{ provider, model string }{
		// Plain chat-completions has no document block — never PDF-capable,
		// even for a model that carries PDF on the Responses path.
		{"openai", "gpt-5"},
		{"openai", "gpt-4o"},
		// Responses on an unknown/old model (fail-closed).
		{"openai-responses", "gpt-3.5-turbo"},
		{"openai-responses", "gpt-4-vision"},
		{"openai-responses", ""},
		// Non-OpenAI with a non-PDF model.
		{"gemini", "gemini-nano"},
		{"anthropic", "claude-2.1"},
	}
	for _, c := range no {
		if ModelSupportsPDFForProvider(c.provider, c.model) {
			t.Errorf("ModelSupportsPDFForProvider(%q, %q) = true, want false", c.provider, c.model)
		}
	}
	// The name-only ModelSupportsPDF must NOT have gained OpenAI models — the
	// OpenAI path is Responses-gated via the provider-aware function only.
	for _, m := range []string{"gpt-4o", "gpt-5", "o3"} {
		if ModelSupportsPDF(m) {
			t.Errorf("ModelSupportsPDF(%q) = true; OpenAI PDF must stay provider-gated, not name-based", m)
		}
	}
}

// TestPDFCapableImpliesVisionCapable pins the invariant that every PDF-capable
// model is also vision-capable — Claude document support is built on vision
// infra, so a model declared document-capable must accept images too (#534
// review). This catches the class of bug where a family is added to
// pdfCapablePrefixes but forgotten in visionCapablePrefixes.
func TestPDFCapableImpliesVisionCapable(t *testing.T) {
	// Both PDF allowlists must be subsets of the vision allowlist — document
	// input rides the same multimodal models as image input.
	for _, prefix := range append(append([]string{}, pdfCapablePrefixes...), openaiResponsesPDFCapablePrefixes...) {
		sample := prefix + "-x" // a concrete model name in that family
		if !ModelSupportsVision(sample) {
			t.Errorf("model %q is PDF-capable but not vision-capable — mirror %q into visionCapablePrefixes", sample, prefix)
		}
	}
}

func TestIsDocumentMIME(t *testing.T) {
	if !IsDocumentMIME("application/pdf") || !IsDocumentMIME("APPLICATION/PDF ") {
		t.Error("application/pdf must be a document MIME")
	}
	for _, mt := range []string{"", "image/png", "text/plain", "application/zip", "video/mp4"} {
		if IsDocumentMIME(mt) {
			t.Errorf("IsDocumentMIME(%q) = true, want false", mt)
		}
	}
}

func TestIsImageMIME(t *testing.T) {
	for _, mt := range []string{"image/png", "image/jpeg", "image/jpg", "IMAGE/PNG", "image/gif", "image/webp"} {
		if !IsImageMIME(mt) {
			t.Errorf("IsImageMIME(%q) = false, want true", mt)
		}
	}
	for _, mt := range []string{"", "application/pdf", "video/mp4", "text/plain", "image/tiff"} {
		if IsImageMIME(mt) {
			t.Errorf("IsImageMIME(%q) = true, want false", mt)
		}
	}
}

func TestNormalizeImageMIME(t *testing.T) {
	if got := NormalizeImageMIME("image/jpg"); got != "image/jpeg" {
		t.Errorf("NormalizeImageMIME(image/jpg) = %q, want image/jpeg", got)
	}
	if got := NormalizeImageMIME("IMAGE/PNG "); got != "image/png" {
		t.Errorf("NormalizeImageMIME normalized = %q, want image/png", got)
	}
}
