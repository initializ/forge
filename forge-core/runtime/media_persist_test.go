package runtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/llm"
)

func imageMsg(mime string, data []byte) llm.ChatMessage {
	return llm.ChatMessage{
		Role:    llm.RoleUser,
		Content: "hi",
		Parts: []llm.ContentPart{
			llm.NewTextContentPart("hi"),
			llm.NewMediaContentPart(llm.ContentPartImage, llm.MediaRef{MimeType: mime, Bytes: data}),
		},
	}
}

// TestPersistAndRehydrate_CrossTurnRoundTrip is the core Phase 4 guarantee:
// inbound media is persisted to .forge/files with a URI; the session marshal
// drops the inline Bytes (json:"-") keeping only the URI, and RehydrateMedia
// reloads the Bytes on the next turn — so multi-turn conversations replay the
// media without ever storing base64 in history (#255).
func TestPersistAndRehydrate_CrossTurnRoundTrip(t *testing.T) {
	dir := t.TempDir()
	ctx := WithFilesDir(context.Background(), dir)
	orig := []byte("the-real-image-bytes-xyz")

	msg := imageMsg("image/png", orig)
	if err := persistMedia(ctx, &msg, mediaSubdirInbound); err != nil {
		t.Fatalf("persist: %v", err)
	}

	// URI points under .forge/files/inbound and the file holds the bytes.
	uri := msg.Parts[1].Media.URI
	if uri == "" {
		t.Fatal("URI not set after persist")
	}
	if !strings.HasPrefix(uri, filepath.Join(dir, "inbound")) {
		t.Errorf("URI %q not under the inbound dir", uri)
	}
	if got, _ := os.ReadFile(uri); string(got) != string(orig) {
		t.Errorf("persisted file content mismatch")
	}
	if !strings.HasSuffix(uri, ".png") {
		t.Errorf("URI should carry a mime-derived extension; got %q", uri)
	}
	// Bytes stay inline for THIS turn's request.
	if string(msg.Parts[1].Media.Bytes) != string(orig) {
		t.Error("Bytes must stay inline for the current turn")
	}

	// Simulate session persistence: marshal drops Bytes, keeps URI.
	data, err := json.Marshal([]llm.ChatMessage{msg})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), base64.StdEncoding.EncodeToString(orig)) {
		t.Errorf("inline bytes leaked into persisted history:\n%s", data)
	}
	var replayed []llm.ChatMessage
	if err := json.Unmarshal(data, &replayed); err != nil {
		t.Fatal(err)
	}
	if len(replayed[0].Parts[1].Media.Bytes) != 0 {
		t.Fatal("Bytes must be dropped from persisted history")
	}
	if replayed[0].Parts[1].Media.URI != uri {
		t.Fatalf("URI must survive persistence: got %q", replayed[0].Parts[1].Media.URI)
	}

	// Next turn: rehydrate reloads the bytes from the URI.
	if err := RehydrateMedia(ctx, replayed); err != nil {
		t.Fatalf("rehydrate: %v", err)
	}
	if string(replayed[0].Parts[1].Media.Bytes) != string(orig) {
		t.Errorf("rehydrated bytes = %q, want %q", replayed[0].Parts[1].Media.Bytes, orig)
	}
}

// TestPersistInboundMedia_ContentAddressedAndIdempotent: identical bytes across
// two messages map to the same path (dedup), and re-persisting is a no-op once
// a URI is set.
func TestPersistInboundMedia_ContentAddressedAndIdempotent(t *testing.T) {
	dir := t.TempDir()
	ctx := WithFilesDir(context.Background(), dir)
	data := []byte("same-bytes")

	a := imageMsg("image/png", data)
	b := imageMsg("image/png", data)
	if err := persistMedia(ctx, &a, mediaSubdirInbound); err != nil {
		t.Fatal(err)
	}
	if err := persistMedia(ctx, &b, mediaSubdirInbound); err != nil {
		t.Fatal(err)
	}
	if a.Parts[1].Media.URI != b.Parts[1].Media.URI {
		t.Errorf("identical bytes should map to the same content-addressed path: %q vs %q", a.Parts[1].Media.URI, b.Parts[1].Media.URI)
	}

	// A part that already has a URI is left untouched (idempotent).
	preset := imageMsg("image/png", data)
	preset.Parts[1].Media.URI = "/already/set.png"
	if err := persistMedia(ctx, &preset, mediaSubdirInbound); err != nil {
		t.Fatal(err)
	}
	if preset.Parts[1].Media.URI != "/already/set.png" {
		t.Error("a part with an existing URI must not be re-persisted")
	}
}

// TestPersistMedia_GeneratedSubdir: model-generated media lands under the
// generated/ subdir (distinct from inbound/) so a retention pass can tell
// received uploads from generated output (#536 review).
func TestPersistMedia_GeneratedSubdir(t *testing.T) {
	dir := t.TempDir()
	ctx := WithFilesDir(context.Background(), dir)
	msg := imageMsg("image/png", []byte("generated"))
	if err := persistMedia(ctx, &msg, mediaSubdirGenerated); err != nil {
		t.Fatal(err)
	}
	uri := msg.Parts[1].Media.URI
	if !strings.HasPrefix(uri, filepath.Join(dir, "generated")) {
		t.Errorf("generated media should live under generated/, got %q", uri)
	}
}

// TestPersistInboundMedia_NoFilesDirIsNoop: without a files dir, persistence is
// skipped (media stays inline for the turn) and no error is raised.
func TestPersistInboundMedia_NoFilesDirIsNoop(t *testing.T) {
	msg := imageMsg("image/png", []byte("x"))
	if err := persistMedia(context.Background(), &msg, mediaSubdirInbound); err != nil {
		t.Fatalf("no files dir should be a no-op, got %v", err)
	}
	if msg.Parts[1].Media.URI != "" {
		t.Error("no files dir → URI must stay empty")
	}
}
