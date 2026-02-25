package brain

import (
	"context"
	"fmt"

	"github.com/initializ/forge/forge-core/llm"
)

// DefaultThreshold is the default confidence threshold for routing.
const DefaultThreshold = 0.7

// Router implements llm.Client with confidence-gated brain → remote fallback.
type Router struct {
	brain     *BrainClient
	remote    llm.Client // nil if brain-only mode
	threshold float64
}

// RouterConfig holds configuration for creating a Router.
type RouterConfig struct {
	BrainConfig Config
	Remote      llm.Client // optional remote fallback
	Threshold   float64    // confidence threshold (default 0.7)
}

// NewRouter creates a new confidence-gated router.
func NewRouter(cfg RouterConfig) (*Router, error) {
	brainClient, err := NewClient(cfg.BrainConfig)
	if err != nil {
		return nil, fmt.Errorf("brain router: %w", err)
	}

	threshold := cfg.Threshold
	if threshold <= 0 {
		threshold = DefaultThreshold
	}

	return &Router{
		brain:     brainClient,
		remote:    cfg.Remote,
		threshold: threshold,
	}, nil
}

// NewRouterFromClient creates a Router from an existing BrainClient.
func NewRouterFromClient(brain *BrainClient, remote llm.Client, threshold float64) *Router {
	if threshold <= 0 {
		threshold = DefaultThreshold
	}
	return &Router{
		brain:     brain,
		remote:    remote,
		threshold: threshold,
	}
}

// Chat sends a chat request through the confidence router.
// 1. Call brain → get response + confidence score
// 2. If confidence >= threshold → return brain response
// 3. If confidence < threshold && remote != nil → call remote
// 4. If confidence < threshold && remote == nil → return brain response (best effort)
func (r *Router) Chat(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	// Get brain response
	brainResp, err := r.brain.Chat(ctx, req)
	if err != nil {
		// If brain fails and we have a remote, try remote
		if r.remote != nil {
			return r.remote.Chat(ctx, req)
		}
		return nil, fmt.Errorf("brain router: %w", err)
	}

	// Score confidence
	result := &chatResult{
		Content:   brainResp.Message.Content,
		ToolCalls: brainResp.Message.ToolCalls,
	}
	hasTools := len(req.Tools) > 0
	confidence := ScoreConfidence(result, hasTools)

	// Accept if confidence meets threshold
	if confidence >= r.threshold {
		return brainResp, nil
	}

	// Escalate to remote if available
	if r.remote != nil {
		return r.remote.Chat(ctx, req)
	}

	// Best effort: return brain response anyway
	return brainResp, nil
}

// ChatStream sends a streaming request through the confidence router.
// Uses non-streaming brain call for confidence check.
// If accepted: emits as single-chunk stream.
// If rejected + remote: delegates to remote.ChatStream().
func (r *Router) ChatStream(ctx context.Context, req *llm.ChatRequest) (<-chan llm.StreamDelta, error) {
	// Get brain response (non-streaming for confidence check)
	brainResp, err := r.brain.Chat(ctx, req)
	if err != nil {
		if r.remote != nil {
			return r.remote.ChatStream(ctx, req)
		}
		return nil, fmt.Errorf("brain router: %w", err)
	}

	// Score confidence
	result := &chatResult{
		Content:   brainResp.Message.Content,
		ToolCalls: brainResp.Message.ToolCalls,
	}
	hasTools := len(req.Tools) > 0
	confidence := ScoreConfidence(result, hasTools)

	// If below threshold and remote available, delegate streaming to remote
	if confidence < r.threshold && r.remote != nil {
		return r.remote.ChatStream(ctx, req)
	}

	// Emit brain response as a single-chunk stream
	out := make(chan llm.StreamDelta, 2)
	go func() {
		defer close(out)

		if brainResp.Message.Content != "" {
			out <- llm.StreamDelta{
				Content: brainResp.Message.Content,
			}
		}

		if len(brainResp.Message.ToolCalls) > 0 {
			out <- llm.StreamDelta{
				ToolCalls: brainResp.Message.ToolCalls,
			}
		}

		out <- llm.StreamDelta{
			Done:         true,
			FinishReason: brainResp.FinishReason,
		}
	}()

	return out, nil
}

// ModelID returns the brain model identifier (or remote if fallback).
func (r *Router) ModelID() string {
	return "brain:" + r.brain.ModelID()
}

// Close shuts down the brain engine.
func (r *Router) Close() error {
	return r.brain.Close()
}
