package llm

import (
	"context"
	"fmt"
)

// FallbackCandidate pairs a provider/model label with its LLM client.
type FallbackCandidate struct {
	Provider string
	Model    string
	Client   Client
}

// FallbackChain implements the Client interface by trying multiple LLM
// providers in order. When the primary provider fails with a retriable error
// (429, 503, timeouts), the chain moves to the next candidate. Non-retriable
// errors (400 bad request, 401 auth) abort immediately.
//
// When there is only one candidate, FallbackChain delegates directly without
// error classification to preserve exact current behavior.
type FallbackChain struct {
	candidates []FallbackCandidate
	cooldown   *CooldownTracker
}

// NewFallbackChain creates a new fallback chain from the given candidates.
// At least one candidate is required.
func NewFallbackChain(candidates []FallbackCandidate) *FallbackChain {
	return &FallbackChain{
		candidates: candidates,
		cooldown:   NewCooldownTracker(),
	}
}

// Chat tries each candidate in order until one succeeds or all are exhausted.
func (fc *FallbackChain) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	// Single-candidate optimization: delegate directly, no classification.
	if len(fc.candidates) == 1 {
		return fc.candidates[0].Client.Chat(ctx, req)
	}

	var errors []*FailoverError

	for _, c := range fc.candidates {
		// Check context cancellation
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		// Skip providers in cooldown
		if !fc.cooldown.IsAvailable(c.Provider) {
			continue
		}

		resp, err := c.Client.Chat(ctx, req)
		if err == nil {
			fc.cooldown.MarkSuccess(c.Provider)
			return resp, nil
		}

		fe := ClassifyError(err, c.Provider, c.Model)
		errors = append(errors, fe)

		// Non-retriable errors abort immediately
		if !fe.IsRetriable() {
			return nil, fe
		}

		// Retriable — mark failure and try next
		fc.cooldown.MarkFailure(c.Provider, fe.Reason)
	}

	if len(errors) == 0 {
		return nil, fmt.Errorf("all fallback candidates in cooldown")
	}
	return nil, &FallbackExhaustedError{Errors: errors}
}

// ChatStream tries each candidate in order for streaming requests.
func (fc *FallbackChain) ChatStream(ctx context.Context, req *ChatRequest) (<-chan StreamDelta, error) {
	// Single-candidate optimization: delegate directly, no classification.
	if len(fc.candidates) == 1 {
		return fc.candidates[0].Client.ChatStream(ctx, req)
	}

	var errors []*FailoverError

	for _, c := range fc.candidates {
		// Check context cancellation
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		// Skip providers in cooldown
		if !fc.cooldown.IsAvailable(c.Provider) {
			continue
		}

		ch, err := c.Client.ChatStream(ctx, req)
		if err == nil {
			fc.cooldown.MarkSuccess(c.Provider)
			return ch, nil
		}

		fe := ClassifyError(err, c.Provider, c.Model)
		errors = append(errors, fe)

		// Non-retriable errors abort immediately
		if !fe.IsRetriable() {
			return nil, fe
		}

		// Retriable — mark failure and try next
		fc.cooldown.MarkFailure(c.Provider, fe.Reason)
	}

	if len(errors) == 0 {
		return nil, fmt.Errorf("all fallback candidates in cooldown")
	}
	return nil, &FallbackExhaustedError{Errors: errors}
}

// ModelID returns the primary candidate's model identifier.
func (fc *FallbackChain) ModelID() string {
	if len(fc.candidates) > 0 {
		return fc.candidates[0].Client.ModelID()
	}
	return ""
}
