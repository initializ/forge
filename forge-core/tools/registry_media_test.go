package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/initializ/forge/forge-core/llm"
)

// fakeMediaTool implements MediaResultTool — it returns an image alongside text.
type fakeMediaTool struct{ name string }

func (f *fakeMediaTool) Name() string                 { return f.name }
func (f *fakeMediaTool) Description() string          { return "media" }
func (f *fakeMediaTool) Category() Category           { return CategoryBuiltin }
func (f *fakeMediaTool) InputSchema() json.RawMessage { return json.RawMessage(`{}`) }
func (f *fakeMediaTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "text only", nil
}
func (f *fakeMediaTool) ExecuteWithMedia(context.Context, json.RawMessage) (string, []llm.MediaRef, error) {
	return "with image", []llm.MediaRef{{MimeType: "image/png", Bytes: []byte{1, 2, 3}}}, nil
}

func TestRegistry_ExecuteWithMedia(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(&fakeMediaTool{name: "shot"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(&fakeTool{name: "plain"}); err != nil {
		t.Fatal(err)
	}

	// Media tool → media returned.
	text, media, err := r.ExecuteWithMedia(context.Background(), "shot", nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if text != "with image" || len(media) != 1 || media[0].MimeType != "image/png" {
		t.Errorf("media tool: text=%q media=%+v", text, media)
	}

	// Plain tool → falls back to Execute, nil media (single call site works for both).
	text, media, err = r.ExecuteWithMedia(context.Background(), "plain", nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if media != nil {
		t.Errorf("plain tool must yield nil media, got %+v", media)
	}
	_ = text

	// Unknown tool → error.
	if _, _, err := r.ExecuteWithMedia(context.Background(), "nope", nil); err == nil {
		t.Error("unknown tool should error")
	}
}
