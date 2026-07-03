package compress

import (
	"context"

	"github.com/initializ/ctxzip"

	"github.com/initializ/forge/forge-core/runtime"
)

// AfterToolExecHook returns a hook that compresses tool output at production
// time, before the loop appends it to Memory. This is the primary compression
// seam: because the output is compressed exactly once, the bytes stored in
// history never change afterwards, keeping the conversation prefix stable for
// provider prompt caches.
//
// Register it AFTER redaction/guardrail hooks so it compresses what those
// hooks left, not what they were about to remove.
//
// The hook never fails the loop: on any problem it leaves ToolOutput as-is.
// Error results (hctx.Error != nil) are always left verbatim — dropping parts
// of an error the user is about to debug is the catastrophic failure mode.
func (r *Runtime) AfterToolExecHook() runtime.Hook {
	return func(_ context.Context, hctx *runtime.HookContext) error {
		if hctx.Error != nil || len(hctx.ToolOutput) < r.minSize {
			return nil
		}
		// Never compress the expansion tool's own output — the model just
		// asked for those bytes back; re-crushing them recreates the marker
		// it resolved and the loop chases its own tail (observed live).
		if hctx.ToolName == expandToolName {
			return nil
		}

		opts := ctxzip.DefaultOptions()
		opts.Store = r.store
		// A single fresh message: no prefix to freeze, nothing "recent" to
		// protect — those windows exist for whole conversations.
		opts.FreezePrefix = 0
		opts.ProtectRecent = 0
		// The tool-call arguments are the best available relevance signal for
		// what the model wanted from this output — and they are fixed at call
		// time, so recompression determinism is not a concern here.
		opts.Query = hctx.ToolInput
		opts.MustKeep = r.keep

		msgs := []ctxzip.Message{{
			Role:    ctxzip.RoleTool,
			Content: hctx.ToolOutput,
			Name:    hctx.ToolName,
		}}
		res, err := ctxzip.Compress(msgs, opts)
		if err != nil || res == nil || res.SavedTokens() == 0 {
			return nil
		}

		for _, tr := range res.Transforms {
			r.rememberMarkers(tr.Markers)
		}
		r.debugf("compressed tool output", map[string]any{
			"tool":          hctx.ToolName,
			"tokens_before": res.TokensBefore,
			"tokens_after":  res.TokensAfter,
			"saved_tokens":  res.SavedTokens(),
		})
		hctx.ToolOutput = res.Messages[0].Content
		return nil
	}
}
