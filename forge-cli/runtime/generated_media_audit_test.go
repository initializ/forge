package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/initializ/forge/forge-core/llm"
)

func TestGeneratedMediaAuditFields(t *testing.T) {
	// Text-only response → no media event (the common case).
	if got := generatedMediaAuditFields([]llm.ContentPart{llm.NewTextContentPart("hi")}); got != nil {
		t.Errorf("text-only parts should yield no media fields, got %v", got)
	}
	// Part with empty bytes (e.g. a rehydratable URI reference) → skipped.
	if got := generatedMediaAuditFields([]llm.ContentPart{
		{Type: llm.ContentPartImage, Media: &llm.MediaRef{MimeType: "image/png"}},
	}); got != nil {
		t.Errorf("media part with no bytes should be skipped, got %v", got)
	}

	img := []byte("\x89PNG\r\n\x1a\n fake image bytes")
	parts := []llm.ContentPart{
		llm.NewTextContentPart("here is your image"),
		llm.NewMediaContentPart(llm.ContentPartImage, llm.MediaRef{MimeType: "image/jpeg", Bytes: img}),
	}
	got := generatedMediaAuditFields(parts)
	if len(got) != 1 {
		t.Fatalf("expected 1 media entry, got %d (%v)", len(got), got)
	}
	e := got[0]
	if e["mime"] != "image/jpeg" {
		t.Errorf("mime = %v, want image/jpeg", e["mime"])
	}
	if e["bytes"] != len(img) {
		t.Errorf("bytes = %v, want %d", e["bytes"], len(img))
	}
	sum := sha256.Sum256(img)
	want := hex.EncodeToString(sum[:])
	if e["sha256"] != want {
		t.Errorf("sha256 = %v, want %s (must match persistMedia's content-addressed filename)", e["sha256"], want)
	}
}
