package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"

	"github.com/initializ/forge/forge-core/llm"
)

// Media store subdirectories under the files dir (#255). Received uploads and
// model-generated output share the same content-addressed store but land in
// distinct subdirs so a future retention/GC pass can tell them apart.
const (
	mediaSubdirInbound   = "inbound"   // client-uploaded media
	mediaSubdirGenerated = "generated" // model-generated media (e.g. image_generation)
)

// persistMedia writes each inline media part in msg to the context's files dir
// (WithFilesDir → .forge/files/<subdir>) and records the on-disk path as the
// part's MediaRef.URI, while LEAVING Bytes in place for the current turn's
// request (#255). This is what makes media survive across turns: session
// history persists llm.ChatMessage with MediaRef.Bytes tagged json:"-", so only
// the URI is stored, and RehydrateMedia reloads the bytes on replay. The
// persisted files also give agent tools a real path to open a file.
//
// subdir separates received uploads (mediaSubdirInbound) from model-generated
// output (mediaSubdirGenerated). Files are content-addressed (sha256 + a
// MIME-derived extension), so identical media dedups and re-writing is
// idempotent. Best-effort: callers ignore the error — on failure the media
// simply stays inline for this turn and won't replay on the next one. Parts
// with no bytes, or that already carry a URI, are skipped.
func persistMedia(ctx context.Context, msg *llm.ChatMessage, subdir string) error {
	dir := FilesDirFromContext(ctx)
	if dir == "" {
		return nil // no files dir configured → media stays inline (this turn only)
	}
	mediaDir := filepath.Join(dir, subdir)
	made := false
	for i := range msg.Parts {
		m := msg.Parts[i].Media
		if m == nil || len(m.Bytes) == 0 || m.URI != "" {
			continue
		}
		if !made {
			if err := os.MkdirAll(mediaDir, 0o700); err != nil {
				return err
			}
			made = true
		}
		sum := sha256.Sum256(m.Bytes)
		path := filepath.Join(mediaDir, hex.EncodeToString(sum[:])+extForMIME(m.MimeType))
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
