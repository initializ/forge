package computer

import (
	"bytes"
	"fmt"
	"image"
	"image/png"

	xdraw "golang.org/x/image/draw"
)

// maxScreenshotEdge bounds the long edge (px) of a screenshot sent to the model.
// A Retina/6K full-screen capture (e.g. 2880×1800 up to 6016×3384) base64-encodes
// to well past Anthropic's ~5 MB per-image ceiling and would 400 the turn — and
// even under the ceiling it burns tokens/latency every loop step. 1568 is
// Anthropic's recommended computer-use resolution; downscaling to it keeps frames
// small while staying legible for the model (#585 review).
const maxScreenshotEdge = 1568

// normalizeScreenshot downscales a PNG so its long edge is at most
// maxScreenshotEdge, preserving aspect ratio. Captures already within the bound
// are returned unchanged (no decode/re-encode). w/h are the source dimensions
// the backend reported; the returned dimensions reflect any resize.
func normalizeScreenshot(pngBytes []byte, w, h int) ([]byte, int, int, error) {
	longEdge := w
	if h > longEdge {
		longEdge = h
	}
	if longEdge <= maxScreenshotEdge || longEdge == 0 {
		return pngBytes, w, h, nil // already small enough (common case) or dims unknown
	}

	src, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		return nil, 0, 0, fmt.Errorf("decoding screenshot for downscale: %w", err)
	}
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	le := sw
	if sh > le {
		le = sh
	}
	if le <= maxScreenshotEdge || le == 0 {
		return pngBytes, sw, sh, nil
	}
	// Pin the long edge to exactly maxScreenshotEdge and scale the short edge by
	// the ratio (rounded), so the long edge isn't left at 1567 by truncation.
	var dw, dh int
	if sw >= sh {
		dw = maxScreenshotEdge
		dh = int(float64(sh)*float64(maxScreenshotEdge)/float64(sw) + 0.5)
	} else {
		dh = maxScreenshotEdge
		dw = int(float64(sw)*float64(maxScreenshotEdge)/float64(sh) + 0.5)
	}
	if dw < 1 {
		dw = 1
	}
	if dh < 1 {
		dh = 1
	}

	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	// CatmullRom: high-quality downscale; screenshots have text/edges where a
	// box filter would look muddy.
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, b, xdraw.Over, nil)

	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return nil, 0, 0, fmt.Errorf("re-encoding downscaled screenshot: %w", err)
	}
	return buf.Bytes(), dw, dh, nil
}
