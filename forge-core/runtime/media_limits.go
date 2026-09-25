package runtime

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif"  // register gif DecodeConfig
	_ "image/jpeg" // register jpeg DecodeConfig
	_ "image/png"  // register png DecodeConfig
)

// Media ingest limits (#255 DoS controls). These bound what a single inbound
// message can carry so that raising the request-body cap to admit real images
// cannot be turned into a memory/CPU-exhaustion vector. They are enforced at
// the ingest gate (before dispatch), alongside a concurrency semaphore that
// bounds how many media-bearing requests are processed at once.
const (
	// MaxImagePartBytes caps a single image part's raw (pre-base64) bytes.
	// 5 MiB matches Anthropic's per-image limit and fits several images plus
	// text under the request-body cap.
	MaxImagePartBytes = 5 << 20

	// MaxImagePartsPerMessage caps how many image parts one message may carry,
	// so a body full of tiny images can't fan out into a huge provider request.
	MaxImagePartsPerMessage = 20

	// MaxImagePixels bounds decoded image dimensions (width*height) to defuse
	// decompression bombs — a tiny file that decodes to a gigapixel canvas.
	// ~50 MP covers legitimate high-resolution photos. Enforced only for the
	// stdlib-decodable formats (png/jpeg/gif); webp is bounded by the byte cap.
	MaxImagePixels = 50_000_000

	// maxImageDim bounds a single dimension BEFORE the width*height product, so
	// that product can't overflow int64. PNG's IHDR carries 32-bit dimensions
	// (JPEG/GIF are 16-bit), so a forged PNG can report dims near 2^31; two such
	// dims multiplied would wrap int64 negative and slip past the pixel check.
	// 100_000 keeps the product ≤ 1e10 (safe) and no legitimate image is that
	// large on any single side.
	maxImageDim = 100_000
)

// CheckImageLimits validates a single image part's bytes against the per-image
// size and decode-dimension bounds. It returns a non-empty reason string when
// the part must be rejected, or "" when it passes.
//
// The dimension check uses image.DecodeConfig, which reads only the header and
// does NOT allocate the pixel buffer, so it is cheap. Formats without a
// registered decoder (webp) can't be dimension-checked here and are bounded by
// the byte cap alone; a malformed image in a format we DO support is rejected.
func CheckImageLimits(mime string, data []byte) string {
	if len(data) == 0 {
		return "image part has no bytes"
	}
	if len(data) > MaxImagePartBytes {
		return fmt.Sprintf("image exceeds the %d-byte per-image limit (%d bytes)", MaxImagePartBytes, len(data))
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		// No registered decoder for this format (e.g. webp) → rely on the byte
		// cap. A decode error for a format we DO support means it's malformed.
		if imageFormatIsStdlibDecodable(mime) {
			return "image could not be decoded (malformed or truncated)"
		}
		return ""
	}
	// Per-dimension bound first, so the product below can't overflow int64
	// (a forged PNG can report dims near 2^31; the raw product would wrap).
	if cfg.Width > maxImageDim || cfg.Height > maxImageDim {
		return fmt.Sprintf("image dimension %dx%d exceeds the %d-px per-side limit", cfg.Width, cfg.Height, maxImageDim)
	}
	if int64(cfg.Width)*int64(cfg.Height) > MaxImagePixels {
		return fmt.Sprintf("image dimensions %dx%d exceed the %d-pixel limit", cfg.Width, cfg.Height, MaxImagePixels)
	}
	return ""
}

func imageFormatIsStdlibDecodable(mime string) bool {
	switch NormalizeImageMIME(mime) {
	case "image/png", "image/jpeg", "image/gif":
		return true
	}
	return false
}
