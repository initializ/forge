package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/llm"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

func runnerWithModel(model string) *Runner {
	return &Runner{modelConfig: &coreruntime.ModelConfig{Client: llm.ClientConfig{Model: model}}}
}

func imagePartMsg(mime string) a2a.Message {
	return a2a.Message{
		Role: a2a.MessageRoleUser,
		Parts: []a2a.Part{
			a2a.NewTextPart("look"),
			a2a.NewFilePart(a2a.FileContent{MimeType: mime, Bytes: []byte{1, 2, 3}}),
		},
	}
}

// TestCheckInboundMedia_Phase2 covers the flipped gate: images pass on a
// vision-capable model, and everything else is still rejected loudly (#255).
func TestCheckInboundMedia_Phase2(t *testing.T) {
	ctx := context.Background()

	t.Run("image accepted on vision model", func(t *testing.T) {
		r := runnerWithModel("gpt-4o")
		if got := r.checkInboundMedia(ctx, imagePartMsg("image/png"), nil); got != "" {
			t.Errorf("image on a vision model must be accepted, got reject: %q", got)
		}
	})

	t.Run("image rejected on non-vision model", func(t *testing.T) {
		r := runnerWithModel("gpt-3.5-turbo")
		got := r.checkInboundMedia(ctx, imagePartMsg("image/png"), nil)
		if got == "" {
			t.Fatal("image on a non-vision model must be rejected")
		}
		if !strings.Contains(got, "image/png") || !strings.Contains(got, "does not support image") {
			t.Errorf("reject message should name the mime and the non-vision reason; got %q", got)
		}
	})

	t.Run("document rejected even on vision model", func(t *testing.T) {
		r := runnerWithModel("gpt-4o")
		got := r.checkInboundMedia(ctx, imagePartMsg("application/pdf"), nil)
		if got == "" {
			t.Fatal("a document part must still be rejected in Phase 2")
		}
		if !strings.Contains(got, "application/pdf") {
			t.Errorf("reject message should name the pdf mime; got %q", got)
		}
	})

	t.Run("text-only accepted", func(t *testing.T) {
		r := runnerWithModel("gpt-3.5-turbo")
		msg := a2a.Message{Role: a2a.MessageRoleUser, Parts: []a2a.Part{a2a.NewTextPart("hi")}}
		if got := r.checkInboundMedia(ctx, msg, nil); got != "" {
			t.Errorf("text-only must never be gated, got %q", got)
		}
	})

	t.Run("nil modelConfig treats as non-vision", func(t *testing.T) {
		r := &Runner{}
		if got := r.checkInboundMedia(ctx, imagePartMsg("image/png"), nil); got == "" {
			t.Error("with no resolved model, image input must be rejected (fail closed)")
		}
	})
}
