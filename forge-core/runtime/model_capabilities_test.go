package runtime

import "testing"

func TestModelSupportsVision(t *testing.T) {
	vision := []string{
		"gpt-4o", "gpt-4o-mini", "gpt-4.1", "gpt-4-turbo", "gpt-5",
		"o1", "o3-mini", "o4-mini",
		"claude-3-5-sonnet", "claude-opus-4-8", "claude-sonnet-5", "claude-haiku-4-5",
		"gemini-1.5-pro", "gemini-2.5-flash",
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
