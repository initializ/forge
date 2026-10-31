package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/llm"
)

// A model-generated image with no caption (empty Content, one image Part, no
// tool calls) is a valid response — the loop must NOT overwrite it with the
// "wasn't able to produce a response" placeholder (#255). Regression for the
// image-gen case where the user saw the image AND the placeholder text.
func TestExecute_MediaOnlyResponseNotTreatedAsEmpty(t *testing.T) {
	imgBytes := []byte{0x89, 'P', 'N', 'G', 1, 2, 3, 4}

	client := &mockLLMClient{
		chatFunc: func(_ context.Context, _ *llm.ChatRequest) (*llm.ChatResponse, error) {
			return &llm.ChatResponse{
				Message: llm.ChatMessage{
					Role: llm.RoleAssistant,
					// No text, no tool calls — only a generated image part.
					Parts: []llm.ContentPart{
						llm.NewMediaContentPart(llm.ContentPartImage, llm.MediaRef{MimeType: "image/png", Bytes: imgBytes}),
					},
				},
				FinishReason: "stop",
			}, nil
		},
	}

	// No tools configured → exercises the `e.tools == nil` return path that
	// substituted the placeholder on empty Content.
	executor := NewLLMExecutor(LLMExecutorConfig{Client: client})

	task := &a2a.Task{ID: "img-only-1"}
	msg := &a2a.Message{Role: a2a.MessageRoleUser, Parts: []a2a.Part{a2a.NewTextPart("draw a cat")}}

	resp, err := executor.Execute(context.Background(), task, msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected a response")
	}

	var hasFile bool
	for _, p := range resp.Parts {
		if p.Kind == a2a.PartKindText && strings.Contains(p.Text, "wasn't able to produce a response") {
			t.Errorf("media-only response must not carry the empty-response placeholder; parts=%+v", resp.Parts)
		}
		if p.Kind == a2a.PartKindFile && p.File != nil && strings.HasPrefix(p.File.MimeType, "image/") {
			hasFile = true
		}
	}
	if !hasFile {
		t.Errorf("generated image must survive as a file part; parts=%+v", resp.Parts)
	}
}
