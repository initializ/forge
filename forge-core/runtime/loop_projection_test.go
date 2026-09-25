package runtime

import (
	"testing"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/llm"
)

// TestA2AMessageToLLM_ProjectsImageParts verifies image file parts become
// multimodal ContentParts (text-of-record + image), while Content stays the
// flattened PromptText for the scanners (#255 Phase 2).
func TestA2AMessageToLLM_ProjectsImageParts(t *testing.T) {
	imgBytes := []byte{1, 2, 3, 4}
	msg := a2a.Message{
		Role: a2a.MessageRoleUser,
		Parts: []a2a.Part{
			a2a.NewTextPart("describe this"),
			a2a.NewFilePart(a2a.FileContent{Name: "p.jpg", MimeType: "image/jpg", Bytes: imgBytes}),
		},
	}
	got := a2aMessageToLLM(msg)

	if got.Role != llm.RoleUser {
		t.Errorf("role = %q", got.Role)
	}
	if got.Content != "describe this" {
		t.Errorf("Content (text-of-record) = %q, want %q", got.Content, "describe this")
	}
	if len(got.Parts) != 2 {
		t.Fatalf("Parts = %+v, want [text, image]", got.Parts)
	}
	if got.Parts[0].Type != llm.ContentPartText || got.Parts[0].Text != "describe this" {
		t.Errorf("first part should be the projected text; got %+v", got.Parts[0])
	}
	img := got.Parts[1]
	if img.Type != llm.ContentPartImage || img.Media == nil {
		t.Fatalf("second part should be an image media part; got %+v", img)
	}
	if img.Media.MimeType != "image/jpeg" { // image/jpg normalized
		t.Errorf("image mime = %q, want image/jpeg (normalized)", img.Media.MimeType)
	}
	if string(img.Media.Bytes) != string(imgBytes) {
		t.Errorf("image bytes not carried through")
	}
}

// TestA2AMessageToLLM_TextOnlyLeavesPartsNil pins that a text-only message
// keeps Parts nil (providers use the plain Content string — back-compat).
func TestA2AMessageToLLM_TextOnlyLeavesPartsNil(t *testing.T) {
	got := a2aMessageToLLM(a2a.Message{Role: a2a.MessageRoleUser, Parts: []a2a.Part{a2a.NewTextPart("hi")}})
	if got.Parts != nil {
		t.Errorf("text-only message must leave Parts nil, got %+v", got.Parts)
	}
	if got.Content != "hi" {
		t.Errorf("Content = %q, want hi", got.Content)
	}
}

// TestA2AMessageToLLM_NonImageFileNotProjected: a non-image file part (the gate
// would reject it) must not be projected into Parts if it somehow reaches here.
func TestA2AMessageToLLM_NonImageFileNotProjected(t *testing.T) {
	msg := a2a.Message{
		Role: a2a.MessageRoleUser,
		Parts: []a2a.Part{
			a2a.NewTextPart("read this"),
			a2a.NewFilePart(a2a.FileContent{Name: "d.pdf", MimeType: "application/pdf", Bytes: []byte{1}}),
		},
	}
	got := a2aMessageToLLM(msg)
	if got.Parts != nil {
		t.Errorf("non-image file part must not be projected; got %+v", got.Parts)
	}
}
