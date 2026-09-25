package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/initializ/forge/forge-core/llm"
)

// TestRehydrateMedia_LoadsBytesFromURI is the read side of the #255
// history-reference design: a persisted message carries only a URI (Bytes
// dropped by json:"-"); RehydrateMedia reloads the bytes from the files dir
// before the message is handed to a provider.
func TestRehydrateMedia_LoadsBytesFromURI(t *testing.T) {
	dir := t.TempDir()
	want := []byte("the-real-image-bytes")
	uri := filepath.Join(dir, "photo.png")
	if err := os.WriteFile(uri, want, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := WithFilesDir(context.Background(), dir)

	msgs := []llm.ChatMessage{{
		Role:  llm.RoleUser,
		Parts: []llm.ContentPart{llm.NewMediaContentPart(llm.ContentPartImage, llm.MediaRef{MimeType: "image/png", URI: uri})},
	}}
	if err := RehydrateMedia(ctx, msgs); err != nil {
		t.Fatalf("RehydrateMedia: %v", err)
	}
	if got := msgs[0].Parts[0].Media.Bytes; string(got) != string(want) {
		t.Errorf("rehydrated bytes = %q, want %q", got, want)
	}
}

// TestRehydrateMedia_AlreadyPopulatedIsUntouched: freshly ingested media that
// hasn't round-tripped through history keeps its bytes and is not re-read.
func TestRehydrateMedia_AlreadyPopulatedIsUntouched(t *testing.T) {
	ctx := WithFilesDir(context.Background(), t.TempDir())
	msgs := []llm.ChatMessage{{
		Role:  llm.RoleUser,
		Parts: []llm.ContentPart{llm.NewMediaContentPart(llm.ContentPartImage, llm.MediaRef{MimeType: "image/png", URI: "/does/not/exist.png", Bytes: []byte("inline")})},
	}}
	if err := RehydrateMedia(ctx, msgs); err != nil {
		t.Fatalf("RehydrateMedia should skip already-populated bytes, got %v", err)
	}
	if got := string(msgs[0].Parts[0].Media.Bytes); got != "inline" {
		t.Errorf("bytes = %q, want inline (unchanged)", got)
	}
}

// TestRehydrateMedia_RejectsPathEscape: a URI outside the files dir must be
// refused, not read — the same confinement fileArtifactFromToolResult uses.
func TestRehydrateMedia_RejectsPathEscape(t *testing.T) {
	dir := t.TempDir()
	// A secret sitting outside the files dir.
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := WithFilesDir(context.Background(), dir)
	msgs := []llm.ChatMessage{{
		Role:  llm.RoleUser,
		Parts: []llm.ContentPart{llm.NewMediaContentPart(llm.ContentPartImage, llm.MediaRef{URI: secret})},
	}}
	if err := RehydrateMedia(ctx, msgs); err == nil {
		t.Fatal("a URI escaping the files dir must be rejected")
	}
	if len(msgs[0].Parts[0].Media.Bytes) != 0 {
		t.Error("bytes must not be populated from an out-of-confinement path")
	}
}

// TestMemory_MediaCountsTowardBudget: media parts carry no Content chars but
// must charge the budget, so an image-heavy history still trims (#255).
func TestMemory_MediaCountsTowardBudget(t *testing.T) {
	imagePart := llm.NewMediaContentPart(llm.ContentPartImage, llm.MediaRef{MimeType: "image/png", URI: "a.png"})

	withMedia := NewMemory("", 0, "")
	withMedia.Append(llm.ChatMessage{Role: llm.RoleUser, Content: "hi", Parts: []llm.ContentPart{imagePart}})
	textOnly := NewMemory("", 0, "")
	textOnly.Append(llm.ChatMessage{Role: llm.RoleUser, Content: "hi"})

	if withMedia.totalChars() <= textOnly.totalChars() {
		t.Errorf("a message with a media part must charge more budget than text-only: media=%d text=%d",
			withMedia.totalChars(), textOnly.totalChars())
	}
	if got := withMedia.totalChars() - textOnly.totalChars(); got != mediaCharWeight {
		t.Errorf("media budget delta = %d, want mediaCharWeight (%d)", got, mediaCharWeight)
	}
}
