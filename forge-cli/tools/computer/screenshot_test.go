package computer

import (
	"bytes"
	"image"
	"image/png"
	"testing"
)

func makePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestNormalizeScreenshot_DownscalesOversized(t *testing.T) {
	// A Retina-ish 3000x2000 capture must be downscaled so the long edge is
	// maxScreenshotEdge, aspect preserved.
	in := makePNG(t, 3000, 2000)
	out, w, h, err := normalizeScreenshot(in, 3000, 2000)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if w != maxScreenshotEdge {
		t.Errorf("long edge = %d, want %d", w, maxScreenshotEdge)
	}
	// Aspect preserved (3000:2000 = 1.5): height ≈ width/1.5, within rounding.
	wantH := 2000 * maxScreenshotEdge / 3000
	if h < wantH-2 || h > wantH+2 {
		t.Errorf("height = %d, want aspect-preserved ~%d", h, wantH)
	}
	if bytes.Equal(out, in) {
		t.Error("oversized capture should have been re-encoded (different bytes)")
	}
	// Decode the result to confirm it's a valid, smaller PNG.
	cfg, _, derr := image.DecodeConfig(bytes.NewReader(out))
	if derr != nil || cfg.Width != maxScreenshotEdge {
		t.Errorf("decoded result: %dx%d err=%v", cfg.Width, cfg.Height, derr)
	}
}

func TestNormalizeScreenshot_PassesThroughSmall(t *testing.T) {
	in := makePNG(t, 800, 600)
	out, w, h, err := normalizeScreenshot(in, 800, 600)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if w != 800 || h != 600 {
		t.Errorf("dims = %dx%d, want 800x600", w, h)
	}
	if !bytes.Equal(out, in) {
		t.Error("a capture within the bound must pass through unchanged (no re-encode)")
	}
}

func TestNormalizeScreenshot_ExactBoundUnchanged(t *testing.T) {
	in := makePNG(t, maxScreenshotEdge, 1000)
	out, _, _, err := normalizeScreenshot(in, maxScreenshotEdge, 1000)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !bytes.Equal(out, in) {
		t.Error("a capture exactly at the bound must pass through unchanged")
	}
}
