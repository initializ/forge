package providers

import (
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/llm"
)

// A user message carrying a PDF document part must serialize to a Responses
// content array with an input_file block (OpenAI's native document path, #559),
// alongside the text as input_text.
func TestResponses_BuildRequest_PDFInputFile(t *testing.T) {
	pdf := []byte("%PDF-1.7\nfake pdf bytes")
	req := &llm.ChatRequest{
		Model: "gpt-5",
		Messages: []llm.ChatMessage{{
			Role:    llm.RoleUser,
			Content: "summarize this",
			Parts: []llm.ContentPart{
				llm.NewTextContentPart("summarize this"),
				llm.NewMediaContentPart(llm.ContentPartDocument, llm.MediaRef{
					MimeType: "application/pdf",
					URI:      "/agent/.forge/files/inbound/abc123.pdf",
					Bytes:    pdf,
				}),
			},
		}},
	}

	got := NewResponsesClient(llm.ClientConfig{Model: "gpt-5"}).buildRequest(req, false)
	if len(got.Input) != 1 {
		t.Fatalf("Input len = %d, want 1", len(got.Input))
	}
	blocks, ok := got.Input[0].Content.([]responsesContentBlock)
	if !ok {
		t.Fatalf("Content type = %T, want []responsesContentBlock for a media message", got.Input[0].Content)
	}
	var text, file *responsesContentBlock
	for i := range blocks {
		switch blocks[i].Type {
		case "input_text":
			text = &blocks[i]
		case "input_file":
			file = &blocks[i]
		default:
			t.Errorf("unexpected block type %q", blocks[i].Type)
		}
	}
	if text == nil || text.Text != "summarize this" {
		t.Errorf("missing/incorrect input_text block: %+v", text)
	}
	if file == nil {
		t.Fatal("missing input_file block for the PDF")
	}
	if file.Filename != "abc123.pdf" {
		t.Errorf("input_file filename = %q, want abc123.pdf (basename of URI)", file.Filename)
	}
	if !strings.HasPrefix(file.FileData, "data:application/pdf;base64,") {
		t.Errorf("input_file file_data = %q, want a data:application/pdf;base64, URL", file.FileData)
	}
}

// An image part serializes to input_image with a data: URL (completes the
// Responses multimodal path; previously Parts were ignored and images on a
// Responses model were silently not sent).
func TestResponses_BuildRequest_ImageInput(t *testing.T) {
	req := &llm.ChatRequest{
		Model: "gpt-5",
		Messages: []llm.ChatMessage{{
			Role:    llm.RoleUser,
			Content: "what is this",
			Parts: []llm.ContentPart{
				llm.NewTextContentPart("what is this"),
				llm.NewMediaContentPart(llm.ContentPartImage, llm.MediaRef{MimeType: "image/png", Bytes: []byte{1, 2, 3}}),
			},
		}},
	}
	got := NewResponsesClient(llm.ClientConfig{Model: "gpt-5"}).buildRequest(req, false)
	blocks, ok := got.Input[0].Content.([]responsesContentBlock)
	if !ok {
		t.Fatalf("Content type = %T, want []responsesContentBlock", got.Input[0].Content)
	}
	var sawImage bool
	for _, b := range blocks {
		if b.Type == "input_image" {
			sawImage = true
			if !strings.HasPrefix(b.ImageURL, "data:image/png;base64,") {
				t.Errorf("input_image image_url = %q, want data:image/png;base64,", b.ImageURL)
			}
		}
	}
	if !sawImage {
		t.Error("expected an input_image block for the image part")
	}
}

// A text-only user message must keep the plain-string content form — the wire
// shape is byte-identical to before #559 (no regression for the common case).
func TestResponses_BuildRequest_TextOnlyStaysString(t *testing.T) {
	req := &llm.ChatRequest{
		Model:    "gpt-5",
		Messages: []llm.ChatMessage{{Role: llm.RoleUser, Content: "just text"}},
	}
	got := NewResponsesClient(llm.ClientConfig{Model: "gpt-5"}).buildRequest(req, false)
	s, ok := got.Input[0].Content.(string)
	if !ok {
		t.Fatalf("Content type = %T, want string for a text-only message", got.Input[0].Content)
	}
	if s != "just text" {
		t.Errorf("Content = %q, want %q", s, "just text")
	}
}

// Media parts with no inline bytes (not yet rehydrated) are skipped, not emitted
// as empty blocks.
func TestResponses_BuildRequest_SkipsBytelessMedia(t *testing.T) {
	req := &llm.ChatRequest{
		Model: "gpt-5",
		Messages: []llm.ChatMessage{{
			Role: llm.RoleUser,
			Parts: []llm.ContentPart{
				llm.NewTextContentPart("hi"),
				llm.NewMediaContentPart(llm.ContentPartDocument, llm.MediaRef{MimeType: "application/pdf", URI: "/x/y.pdf"}),
			},
		}},
	}
	got := NewResponsesClient(llm.ClientConfig{Model: "gpt-5"}).buildRequest(req, false)
	blocks, ok := got.Input[0].Content.([]responsesContentBlock)
	if !ok {
		t.Fatalf("Content type = %T, want []responsesContentBlock", got.Input[0].Content)
	}
	for _, b := range blocks {
		if b.Type == "input_file" {
			t.Errorf("byteless document part must be skipped, got input_file block: %+v", b)
		}
	}
}
