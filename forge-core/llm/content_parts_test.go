package llm

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// TestChatMessage_TextOnlyWireIsUnchanged pins the back-compat invariant: a
// text-only ChatMessage (no Parts) marshals byte-identically to the pre-#255
// shape — no "parts" key — so existing sessions, prompt-cache prefixes, and
// provider requests are untouched.
func TestChatMessage_TextOnlyWireIsUnchanged(t *testing.T) {
	b, err := json.Marshal(ChatMessage{Role: RoleUser, Content: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if want := `{"role":"user","content":"hello"}`; got != want {
		t.Errorf("text-only wire = %s, want %s", got, want)
	}
	if strings.Contains(got, "parts") {
		t.Errorf("empty Parts must be omitted from the wire; got %s", got)
	}
}

// TestMediaRef_BytesNeverSerialized is the #255 history-bloat guard: MediaRef.Bytes
// is json:"-", so marshaling a message with media never writes the inline bytes —
// only the URI reference survives. This is what keeps session history from
// re-persisting (and re-sending) base64 media every turn.
func TestMediaRef_BytesNeverSerialized(t *testing.T) {
	msg := ChatMessage{
		Role:    RoleUser,
		Content: "look at this",
		Parts: []ContentPart{
			NewTextContentPart("look at this"),
			NewMediaContentPart(ContentPartImage, MediaRef{
				MimeType: "image/png",
				URI:      ".forge/files/inbound/photo.png",
				Bytes:    []byte("SUPER-SECRET-RAW-IMAGE-BYTES"),
			}),
		},
	}
	b, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if strings.Contains(got, "SUPER-SECRET-RAW-IMAGE-BYTES") {
		t.Errorf("inline Bytes leaked into the serialized message:\n%s", got)
	}
	if b64 := base64.StdEncoding.EncodeToString([]byte("SUPER-SECRET-RAW-IMAGE-BYTES")); strings.Contains(got, b64) {
		t.Errorf("inline Bytes leaked as base64 into the serialized message:\n%s", got)
	}
	if !strings.Contains(got, ".forge/files/inbound/photo.png") {
		t.Errorf("URI reference should survive serialization; got %s", got)
	}

	// Round-trip: unmarshal keeps the URI, drops the bytes (rehydration is a
	// separate step).
	var back ChatMessage
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !back.HasMedia() {
		t.Fatal("round-tripped message should still report HasMedia")
	}
	if got := back.Parts[1].Media.URI; got != ".forge/files/inbound/photo.png" {
		t.Errorf("round-tripped URI = %q", got)
	}
	if len(back.Parts[1].Media.Bytes) != 0 {
		t.Errorf("round-tripped Bytes must be empty (json:\"-\"); got %d bytes", len(back.Parts[1].Media.Bytes))
	}
}

// TestChatMessage_HasMedia covers the media predicate.
func TestChatMessage_HasMedia(t *testing.T) {
	if (ChatMessage{Content: "x"}).HasMedia() {
		t.Error("text-only message must not report media")
	}
	textParts := ChatMessage{Parts: []ContentPart{NewTextContentPart("x")}}
	if textParts.HasMedia() {
		t.Error("text-only parts must not report media")
	}
	withImage := ChatMessage{Parts: []ContentPart{NewMediaContentPart(ContentPartImage, MediaRef{MimeType: "image/png"})}}
	if !withImage.HasMedia() {
		t.Error("message with an image part must report media")
	}
}
