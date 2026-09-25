package runtime

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"strings"
	"testing"
)

// pngWithDims builds a minimal but valid PNG (signature + IHDR chunk) reporting
// the given dimensions. image.DecodeConfig reads only the header, so this is
// enough to exercise the dimension bound WITHOUT allocating a real pixel
// buffer — exactly the decompression-bomb shape (tiny bytes, huge reported
// dimensions).
func pngWithDims(t *testing.T, w, h uint32) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.Write([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}) // signature
	ihdr := make([]byte, 0, 13)
	ihdr = binary.BigEndian.AppendUint32(ihdr, w)
	ihdr = binary.BigEndian.AppendUint32(ihdr, h)
	ihdr = append(ihdr, 8, 2, 0, 0, 0) // bit depth 8, color type 2 (RGB), no compression/filter/interlace
	_ = binary.Write(&buf, binary.BigEndian, uint32(len(ihdr)))
	buf.WriteString("IHDR")
	buf.Write(ihdr)
	crc := crc32.ChecksumIEEE(append([]byte("IHDR"), ihdr...))
	_ = binary.Write(&buf, binary.BigEndian, crc)
	return buf.Bytes()
}

func TestCheckImageLimits(t *testing.T) {
	small := pngWithDims(t, 100, 100)

	t.Run("valid small png passes", func(t *testing.T) {
		if got := CheckImageLimits("image/png", small); got != "" {
			t.Errorf("valid png rejected: %q", got)
		}
	})

	t.Run("empty bytes rejected", func(t *testing.T) {
		if CheckImageLimits("image/png", nil) == "" {
			t.Error("empty image bytes must be rejected")
		}
	})

	t.Run("oversized bytes rejected before decode", func(t *testing.T) {
		big := make([]byte, MaxImagePartBytes+1)
		if CheckImageLimits("image/png", big) == "" {
			t.Error("image over the byte cap must be rejected")
		}
	})

	t.Run("decompression-bomb dimensions rejected", func(t *testing.T) {
		bomb := pngWithDims(t, 100_000, 100_000) // 1e10 px, but tiny bytes
		if len(bomb) > 1024 {
			t.Fatalf("forged png unexpectedly large (%d bytes)", len(bomb))
		}
		got := CheckImageLimits("image/png", bomb)
		if got == "" {
			t.Fatal("a tiny file reporting gigapixel dimensions must be rejected")
		}
	})

	t.Run("single oversized dimension rejected (product alone would pass)", func(t *testing.T) {
		// 100_001 x 1 = 100_001 px — UNDER MaxImagePixels, so the raw
		// width*height check would ACCEPT it. The per-dimension bound is what
		// rejects it, and it's also the guard that keeps the product from
		// overflowing int64 for a forged 32-bit PNG dimension (#532 review).
		strip := pngWithDims(t, 100_001, 1)
		got := CheckImageLimits("image/png", strip)
		if got == "" {
			t.Fatal("an image with a single dimension over the per-side limit must be rejected")
		}
		if !strings.Contains(got, "per-side") {
			t.Errorf("reject reason should cite the per-side limit; got %q", got)
		}
	})

	t.Run("large square dimensions rejected before the product multiply", func(t *testing.T) {
		// DecodeConfig returns these (verified), so the per-dimension bound
		// fires before int64(w)*int64(h) is ever computed.
		if CheckImageLimits("image/png", pngWithDims(t, 200_000, 200_000)) == "" {
			t.Error("200000x200000 must be rejected")
		}
	})

	t.Run("forged overflow dimensions rejected (decoder guards, we don't rely on it)", func(t *testing.T) {
		// width=height=2^32-1: Go's png DecodeConfig itself errors on this
		// (non-positive after int32 wrap), so it's rejected as malformed — the
		// security property holds regardless of the decoder's internal cap.
		if CheckImageLimits("image/png", pngWithDims(t, 0xFFFFFFFF, 0xFFFFFFFF)) == "" {
			t.Error("a forged gigantic-dimension png must be rejected")
		}
	})

	t.Run("malformed png in a supported format rejected", func(t *testing.T) {
		if CheckImageLimits("image/png", []byte("not really a png")) == "" {
			t.Error("malformed png must be rejected")
		}
	})

	t.Run("webp bounded by byte cap only (no stdlib decoder)", func(t *testing.T) {
		// Small webp-ish bytes: no registered decoder → passes (byte cap holds).
		if got := CheckImageLimits("image/webp", []byte("RIFF....WEBP....")); got != "" {
			t.Errorf("small webp should pass (byte-cap-only): %q", got)
		}
		// Oversized webp still rejected on size.
		if CheckImageLimits("image/webp", make([]byte, MaxImagePartBytes+1)) == "" {
			t.Error("oversized webp must be rejected on the byte cap")
		}
	})
}
