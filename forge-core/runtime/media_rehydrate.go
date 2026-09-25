package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/initializ/forge/forge-core/llm"
)

// RehydrateMedia fills the inline Bytes of every image/document ContentPart in
// msgs from its persisted URI reference, reading each file confined to the
// context's files dir (WithFilesDir). It is the read side of the #255
// history-reference design: history persists only URIs (llm.MediaRef.Bytes is
// json:"-" so it never bloats the session file or re-sends on replay), so the
// executor calls RehydrateMedia to reload the bytes before building a provider
// ChatRequest.
//
// Parts whose Bytes are already populated are left untouched (freshly ingested
// media not yet round-tripped through history). A URI that escapes the files
// dir, or that cannot be read, yields an error naming the offending part —
// callers decide whether that is fatal or degrades to a text reference.
func RehydrateMedia(ctx context.Context, msgs []llm.ChatMessage) error {
	for mi := range msgs {
		for pi := range msgs[mi].Parts {
			media := msgs[mi].Parts[pi].Media
			if media == nil || len(media.Bytes) > 0 || media.URI == "" {
				continue
			}
			path, ok := confinedFilesPath(ctx, media.URI)
			if !ok {
				return fmt.Errorf("rehydrate media: uri %q escapes the files dir", media.URI)
			}
			// confinedFilesPath is lexical (Abs+Rel) — it stops ../ and
			// absolute traversal but does not resolve symlinks, so a symlink
			// INSIDE the files dir pointing outward would otherwise be followed
			// by the read below. Resolve symlinks on both the target and the
			// files dir (the dir itself may sit under a symlink, e.g. macOS
			// /tmp -> /private/tmp) and re-confine before reading. This matters
			// once inbound (untrusted) files are persisted here (#255 Phase 4).
			realPath, err := resolveWithinFilesDir(ctx, path)
			if err != nil {
				return fmt.Errorf("rehydrate media %q: %w", media.URI, err)
			}
			b, err := os.ReadFile(realPath)
			if err != nil {
				return fmt.Errorf("rehydrate media %q: %w", media.URI, err)
			}
			media.Bytes = b
		}
	}
	return nil
}

// resolveWithinFilesDir resolves symlinks in path and confirms the real target
// still lives inside the (symlink-resolved) files dir. It complements the
// lexical confinedFilesPath: a symlink placed inside the files dir that points
// outward passes the lexical check but is caught here. Returns the resolved
// path to read, or an error if the target escapes the dir (or cannot be
// resolved — e.g. it does not exist).
func resolveWithinFilesDir(ctx context.Context, path string) (string, error) {
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	realDir, err := filepath.EvalSymlinks(FilesDirFromContext(ctx))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(realDir, realPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("resolved path escapes the files dir")
	}
	return realPath, nil
}
