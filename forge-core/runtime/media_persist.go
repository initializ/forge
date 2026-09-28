package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"

	"github.com/initializ/forge/forge-core/llm"
)

// persistInboundMedia writes each inline media part in msg to the context's
// files dir (WithFilesDir → .forge/files/inbound) and records the on-disk path
// as the part's MediaRef.URI, while LEAVING Bytes in place for the current
// turn's request (#255 Phase 4). This is what makes media survive across turns:
// session history persists llm.ChatMessage with MediaRef.Bytes tagged json:"-",
// so only the URI is stored, and RehydrateMedia reloads the bytes on replay.
// The persisted files also give agent tools a real path to open an upload.
//
// Files are content-addressed (sha256 + a MIME-derived extension), so identical
// uploads dedup across turns/messages and re-writing is idempotent. Best-effort:
// callers ignore the error — on failure the media simply stays inline for this
// turn and won't replay on the next one. Parts with no bytes, or that already
// carry a URI, are skipped.
func persistInboundMedia(ctx context.Context, msg *llm.ChatMessage) error {
	dir := FilesDirFromContext(ctx)
	if dir == "" {
		return nil // no files dir configured → media stays inline (this turn only)
	}
	inboundDir := filepath.Join(dir, "inbound")
	made := false
	for i := range msg.Parts {
		m := msg.Parts[i].Media
		if m == nil || len(m.Bytes) == 0 || m.URI != "" {
			continue
		}
		if !made {
			if err := os.MkdirAll(inboundDir, 0o700); err != nil {
				return err
			}
			made = true
		}
		sum := sha256.Sum256(m.Bytes)
		path := filepath.Join(inboundDir, hex.EncodeToString(sum[:])+extForMIME(m.MimeType))
		if err := os.WriteFile(path, m.Bytes, 0o600); err != nil {
			return err
		}
		m.URI = path
	}
	return nil
}

// extForMIME returns a file extension for a media MIME type (for readable
// on-disk names). Falls back to .bin for anything unexpected.
func extForMIME(mime string) string {
	switch NormalizeImageMIME(mime) {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "application/pdf":
		return ".pdf"
	default:
		return ".bin"
	}
}
