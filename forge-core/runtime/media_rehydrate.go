package runtime

import (
	"context"
	"fmt"
	"os"

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
			b, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("rehydrate media %q: %w", media.URI, err)
			}
			media.Bytes = b
		}
	}
	return nil
}
