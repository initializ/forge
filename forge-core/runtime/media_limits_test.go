package runtime

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
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
