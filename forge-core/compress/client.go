package compress

import (
	"context"

	"github.com/initializ/ctxzip"

	"github.com/initializ/forge/forge-core/llm"
)

// WrapClient decorates an llm.Client so every outbound request has its live
// zone compressed. It sits below the FallbackChain, so it also covers retry
// calls and the compactor's summarization call.
//
// Cache discipline ("passthrough is sacred"): only the live zone is touched —
// the system prompt (frozen prefix) and the most recent turns are forwarded
// byte-identical. Determinism matters just as much: the relevance query is
// pinned to the FIRST user message of the conversation, never the latest
// turn. Deriving it from the latest turn would recompress the same historic
// message to different bytes each turn and bust the provider prompt cache.
func (r *Runtime) WrapClient(inner llm.Client) llm.Client {
	return &compressingClient{inner: inner, rt: r}
}

type compressingClient struct {
	inner llm.Client
	rt    *Runtime
}

// Chat implements llm.Client.
func (c *compressingClient) Chat(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	return c.inner.Chat(ctx, c.compressRequest(ctx, req))
}

// ChatStream implements llm.Client.
func (c *compressingClient) ChatStream(ctx context.Context, req *llm.ChatRequest) (<-chan llm.StreamDelta, error) {
	return c.inner.ChatStream(ctx, c.compressRequest(ctx, req))
}

// ModelID implements llm.Client.
func (c *compressingClient) ModelID() string { return c.inner.ModelID() }

// compressRequest returns req with compressed live-zone messages, or req
// unchanged when there is nothing to gain. The caller's request is never
// mutated — the loop may reuse its message slice.
func (c *compressingClient) compressRequest(ctx context.Context, req *llm.ChatRequest) *llm.ChatRequest {
	if req == nil || len(req.Messages) == 0 {
		return req
	}

	zmsgs := make([]ctxzip.Message, len(req.Messages))
	for i, m := range req.Messages {
		zmsgs[i] = ctxzip.Message{
			Role:       m.Role,
			Content:    m.Content,
			Name:       m.Name,
			ToolCallID: m.ToolCallID,
		}
	}

	opts := ctxzip.DefaultOptions()
	opts.Store = c.rt.store
	opts.Query = firstUserContent(req.Messages)
	// Expansion results in history must stay verbatim — recompressing them
	// recreates the marker the model already resolved.
	opts.SkipNames = map[string]bool{expandToolName: true}
	opts.MustKeep = c.rt.keep

	// <= 0 for the same reason as the hook: never apply inflated output.
	res, err := ctxzip.Compress(zmsgs, opts)
	if err != nil || res == nil || res.SavedTokens() <= 0 {
		return req
	}

	out := *req
	out.Messages = make([]llm.ChatMessage, len(req.Messages))
	copy(out.Messages, req.Messages)
	for _, tr := range res.Transforms {
		out.Messages[tr.Index].Content = res.Messages[tr.Index].Content
		c.rt.rememberMarkers(tr.Markers)
	}

	c.rt.debugf("compressed request", map[string]any{
		"messages":      len(req.Messages),
		"transformed":   len(res.Transforms),
		"tokens_before": res.TokensBefore,
		"tokens_after":  res.TokensAfter,
		"saved_tokens":  res.SavedTokens(),
	})
	c.rt.recordCompression(ctx, "request", "", res.TokensBefore, res.TokensAfter)
	return &out
}

// firstUserContent returns the first user message — the pinned task
// statement. It is stable for the whole session, which keeps compression
// deterministic across turns (see WrapClient). Returning a single space when
// absent suppresses ctxzip's derive-from-recent-messages fallback, which
// would reintroduce turn-varying output.
func firstUserContent(msgs []llm.ChatMessage) string {
	for _, m := range msgs {
		if m.Role == llm.RoleUser {
			return m.Content
		}
	}
	return " "
}
