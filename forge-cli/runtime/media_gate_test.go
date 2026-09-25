package runtime

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/llm"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

func runnerWithModel(model string) *Runner {
	return &Runner{modelConfig: &coreruntime.ModelConfig{Client: llm.ClientConfig{Model: model}}}
}

// validPNG builds a minimal decodable PNG (signature + IHDR) with the given
// dimensions — enough for image.DecodeConfig, without a real pixel buffer.
func validPNG(w, h uint32) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a})
	ihdr := make([]byte, 0, 13)
	ihdr = binary.BigEndian.AppendUint32(ihdr, w)
	ihdr = binary.BigEndian.AppendUint32(ihdr, h)
	ihdr = append(ihdr, 8, 2, 0, 0, 0)
	_ = binary.Write(&buf, binary.BigEndian, uint32(len(ihdr)))
	buf.WriteString("IHDR")
	buf.Write(ihdr)
	_ = binary.Write(&buf, binary.BigEndian, crc32.ChecksumIEEE(append([]byte("IHDR"), ihdr...)))
	return buf.Bytes()
}

func fileMsg(mime string, data []byte) a2a.Message {
	return a2a.Message{
		Role: a2a.MessageRoleUser,
		Parts: []a2a.Part{
			a2a.NewTextPart("look"),
			a2a.NewFilePart(a2a.FileContent{MimeType: mime, Bytes: data}),
		},
	}
}

// TestCheckInboundMedia_Phase2 covers the flipped gate: images pass on a
// vision-capable model, and everything else is still rejected loudly (#255).
func TestCheckInboundMedia_Phase2(t *testing.T) {
	ctx := context.Background()

	t.Run("image accepted on vision model", func(t *testing.T) {
		r := runnerWithModel("gpt-4o")
		if got := r.checkInboundMedia(ctx, fileMsg("image/png", validPNG(64, 64)), nil); got != "" {
			t.Errorf("image on a vision model must be accepted, got reject: %q", got)
		}
	})

	t.Run("image rejected on non-vision model", func(t *testing.T) {
		r := runnerWithModel("gpt-3.5-turbo")
		got := r.checkInboundMedia(ctx, fileMsg("image/png", validPNG(64, 64)), nil)
		if got == "" {
			t.Fatal("image on a non-vision model must be rejected")
		}
		if !strings.Contains(got, "image/png") || !strings.Contains(got, "does not support image") {
			t.Errorf("reject message should name the mime and the non-vision reason; got %q", got)
		}
	})

	t.Run("document rejected even on vision model", func(t *testing.T) {
		r := runnerWithModel("gpt-4o")
		got := r.checkInboundMedia(ctx, fileMsg("application/pdf", []byte("%PDF-1.7")), nil)
		if got == "" || !strings.Contains(got, "application/pdf") {
			t.Errorf("a document part must still be rejected in Phase 2; got %q", got)
		}
	})

	t.Run("oversized image rejected", func(t *testing.T) {
		r := runnerWithModel("gpt-4o")
		got := r.checkInboundMedia(ctx, fileMsg("image/png", make([]byte, coreruntime.MaxImagePartBytes+1)), nil)
		if got == "" || !strings.Contains(got, "limit") {
			t.Errorf("oversized image must be rejected with a size reason; got %q", got)
		}
	})

	t.Run("decompression-bomb dimensions rejected", func(t *testing.T) {
		r := runnerWithModel("gpt-4o")
		got := r.checkInboundMedia(ctx, fileMsg("image/png", validPNG(100_000, 100_000)), nil)
		if got == "" {
			t.Error("a gigapixel image must be rejected")
		}
	})

	t.Run("too many image parts rejected", func(t *testing.T) {
		r := runnerWithModel("gpt-4o")
		parts := []a2a.Part{a2a.NewTextPart("many")}
		for range coreruntime.MaxImagePartsPerMessage + 1 {
			parts = append(parts, a2a.NewFilePart(a2a.FileContent{MimeType: "image/png", Bytes: validPNG(16, 16)}))
		}
		got := r.checkInboundMedia(ctx, a2a.Message{Role: a2a.MessageRoleUser, Parts: parts}, nil)
		if got == "" || !strings.Contains(got, "image") {
			t.Errorf("more than %d images must be rejected; got %q", coreruntime.MaxImagePartsPerMessage, got)
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
		if got := r.checkInboundMedia(ctx, fileMsg("image/png", validPNG(16, 16)), nil); got == "" {
			t.Error("with no resolved model, image input must be rejected (fail closed)")
		}
	})
}

// TestAcquireMediaSlot verifies the concurrency semaphore bounds media requests
// and never gates non-media requests (#255 DoS control).
func TestAcquireMediaSlot(t *testing.T) {
	r := &Runner{mediaSem: make(chan struct{}, 2)}

	// Non-media requests are never bounded.
	for range 5 {
		if _, ok := r.acquireMediaSlot(false); !ok {
			t.Fatal("non-media request must never be shed")
		}
	}

	// Media requests fill the 2 slots, then the 3rd is shed.
	rel1, ok1 := r.acquireMediaSlot(true)
	rel2, ok2 := r.acquireMediaSlot(true)
	if !ok1 || !ok2 {
		t.Fatal("first two media slots must be granted")
	}
	if _, ok3 := r.acquireMediaSlot(true); ok3 {
		t.Fatal("third media slot must be shed when at capacity")
	}
	// Releasing one frees a slot.
	rel1()
	rel3, ok := r.acquireMediaSlot(true)
	if !ok {
		t.Fatal("a slot must be available after release")
	}
	rel2()
	rel3()

	// A nil semaphore disables the bound (never sheds).
	rNil := &Runner{}
	if _, ok := rNil.acquireMediaSlot(true); !ok {
		t.Fatal("nil mediaSem must disable the bound")
	}
}
