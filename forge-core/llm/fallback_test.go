package llm

import (
	"context"
	"fmt"
	"testing"
)

// mockClient is a test double for llm.Client.
type mockClient struct {
	chatFunc       func(ctx context.Context, req *ChatRequest) (*ChatResponse, error)
	chatStreamFunc func(ctx context.Context, req *ChatRequest) (<-chan StreamDelta, error)
	modelID        string
}

func (m *mockClient) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	return m.chatFunc(ctx, req)
}

func (m *mockClient) ChatStream(ctx context.Context, req *ChatRequest) (<-chan StreamDelta, error) {
	if m.chatStreamFunc != nil {
		return m.chatStreamFunc(ctx, req)
	}
	return nil, fmt.Errorf("not implemented")
}

func (m *mockClient) ModelID() string {
	return m.modelID
}

func okClient(model string) *mockClient {
	return &mockClient{
		modelID: model,
		chatFunc: func(_ context.Context, _ *ChatRequest) (*ChatResponse, error) {
			return &ChatResponse{Message: ChatMessage{Content: "ok from " + model}}, nil
		},
		chatStreamFunc: func(_ context.Context, _ *ChatRequest) (<-chan StreamDelta, error) {
			ch := make(chan StreamDelta, 1)
			ch <- StreamDelta{Content: "ok from " + model, Done: true}
			close(ch)
			return ch, nil
		},
	}
}

func errorClient(model string, err error) *mockClient {
	return &mockClient{
		modelID: model,
		chatFunc: func(_ context.Context, _ *ChatRequest) (*ChatResponse, error) {
			return nil, err
		},
		chatStreamFunc: func(_ context.Context, _ *ChatRequest) (<-chan StreamDelta, error) {
			return nil, err
		},
	}
}

func TestFallbackChain_SingleCandidate_Success(t *testing.T) {
	c := okClient("gpt-4o")
	fc := NewFallbackChain([]FallbackCandidate{
		{Provider: "openai", Model: "gpt-4o", Client: c},
	})

	resp, err := fc.Chat(context.Background(), &ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Message.Content != "ok from gpt-4o" {
		t.Errorf("unexpected response: %s", resp.Message.Content)
	}
}

func TestFallbackChain_SingleCandidate_PassthroughError(t *testing.T) {
	rawErr := fmt.Errorf("openai error (status 429): rate limited")
	c := errorClient("gpt-4o", rawErr)
	fc := NewFallbackChain([]FallbackCandidate{
		{Provider: "openai", Model: "gpt-4o", Client: c},
	})

	_, err := fc.Chat(context.Background(), &ChatRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
	// Single-candidate passes through raw error, not classified
	if err != rawErr {
		t.Errorf("expected raw error passthrough, got: %v", err)
	}
}

func TestFallbackChain_PrimarySuccess(t *testing.T) {
	fc := NewFallbackChain([]FallbackCandidate{
		{Provider: "openai", Model: "gpt-4o", Client: okClient("gpt-4o")},
		{Provider: "anthropic", Model: "claude", Client: okClient("claude")},
	})

	resp, err := fc.Chat(context.Background(), &ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Message.Content != "ok from gpt-4o" {
		t.Errorf("expected primary response, got: %s", resp.Message.Content)
	}
}

func TestFallbackChain_FallbackOn429(t *testing.T) {
	fc := NewFallbackChain([]FallbackCandidate{
		{Provider: "openai", Model: "gpt-4o", Client: errorClient("gpt-4o",
			fmt.Errorf("openai error (status 429): rate limited"))},
		{Provider: "anthropic", Model: "claude", Client: okClient("claude")},
	})

	resp, err := fc.Chat(context.Background(), &ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Message.Content != "ok from claude" {
		t.Errorf("expected fallback response, got: %s", resp.Message.Content)
	}
}

func TestFallbackChain_FallbackOn503(t *testing.T) {
	fc := NewFallbackChain([]FallbackCandidate{
		{Provider: "openai", Model: "gpt-4o", Client: errorClient("gpt-4o",
			fmt.Errorf("openai error (status 503): service unavailable"))},
		{Provider: "anthropic", Model: "claude", Client: okClient("claude")},
	})

	resp, err := fc.Chat(context.Background(), &ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Message.Content != "ok from claude" {
		t.Errorf("expected fallback response, got: %s", resp.Message.Content)
	}
}

func TestFallbackChain_AbortOnAuthError(t *testing.T) {
	fc := NewFallbackChain([]FallbackCandidate{
		{Provider: "openai", Model: "gpt-4o", Client: errorClient("gpt-4o",
			fmt.Errorf("openai error (status 401): invalid api key"))},
		{Provider: "anthropic", Model: "claude", Client: okClient("claude")},
	})

	_, err := fc.Chat(context.Background(), &ChatRequest{})
	if err == nil {
		t.Fatal("expected error for auth failure")
	}
	fe, ok := err.(*FailoverError)
	if !ok {
		t.Fatalf("expected FailoverError, got %T", err)
	}
	if fe.Reason != FailoverAuth {
		t.Errorf("reason = %q, want %q", fe.Reason, FailoverAuth)
	}
}

func TestFallbackChain_AbortOnFormatError(t *testing.T) {
	fc := NewFallbackChain([]FallbackCandidate{
		{Provider: "openai", Model: "gpt-4o", Client: errorClient("gpt-4o",
			fmt.Errorf("openai error (status 400): bad request"))},
		{Provider: "anthropic", Model: "claude", Client: okClient("claude")},
	})

	_, err := fc.Chat(context.Background(), &ChatRequest{})
	if err == nil {
		t.Fatal("expected error for format failure")
	}
	fe, ok := err.(*FailoverError)
	if !ok {
		t.Fatalf("expected FailoverError, got %T", err)
	}
	if fe.Reason != FailoverFormat {
		t.Errorf("reason = %q, want %q", fe.Reason, FailoverFormat)
	}
}

func TestFallbackChain_AllFail(t *testing.T) {
	fc := NewFallbackChain([]FallbackCandidate{
		{Provider: "openai", Model: "gpt-4o", Client: errorClient("gpt-4o",
			fmt.Errorf("openai error (status 429): rate limited"))},
		{Provider: "anthropic", Model: "claude", Client: errorClient("claude",
			fmt.Errorf("anthropic error (status 503): overloaded"))},
	})

	_, err := fc.Chat(context.Background(), &ChatRequest{})
	if err == nil {
		t.Fatal("expected error when all candidates fail")
	}
	exhausted, ok := err.(*FallbackExhaustedError)
	if !ok {
		t.Fatalf("expected FallbackExhaustedError, got %T: %v", err, err)
	}
	if len(exhausted.Errors) != 2 {
		t.Errorf("expected 2 errors, got %d", len(exhausted.Errors))
	}
}

func TestFallbackChain_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	fc := NewFallbackChain([]FallbackCandidate{
		{Provider: "openai", Model: "gpt-4o", Client: okClient("gpt-4o")},
		{Provider: "anthropic", Model: "claude", Client: okClient("claude")},
	})

	_, err := fc.Chat(ctx, &ChatRequest{})
	if err == nil {
		t.Fatal("expected context error")
	}
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
}

func TestFallbackChain_CooldownSkip(t *testing.T) {
	callCount := 0
	slowClient := &mockClient{
		modelID: "gpt-4o",
		chatFunc: func(_ context.Context, _ *ChatRequest) (*ChatResponse, error) {
			callCount++
			return nil, fmt.Errorf("openai error (status 429): rate limited")
		},
	}

	fc := NewFallbackChain([]FallbackCandidate{
		{Provider: "openai", Model: "gpt-4o", Client: slowClient},
		{Provider: "anthropic", Model: "claude", Client: okClient("claude")},
	})

	// First call: primary fails, fallback succeeds
	resp, err := fc.Chat(context.Background(), &ChatRequest{})
	if err != nil {
		t.Fatalf("first call: unexpected error: %v", err)
	}
	if resp.Message.Content != "ok from claude" {
		t.Errorf("first call: expected fallback, got: %s", resp.Message.Content)
	}
	if callCount != 1 {
		t.Errorf("first call: expected 1 primary call, got %d", callCount)
	}

	// Second call: primary should be in cooldown, skipped entirely
	resp, err = fc.Chat(context.Background(), &ChatRequest{})
	if err != nil {
		t.Fatalf("second call: unexpected error: %v", err)
	}
	if resp.Message.Content != "ok from claude" {
		t.Errorf("second call: expected fallback, got: %s", resp.Message.Content)
	}
	if callCount != 1 {
		t.Errorf("second call: primary should have been skipped, call count = %d", callCount)
	}
}

func TestFallbackChain_ModelID(t *testing.T) {
	fc := NewFallbackChain([]FallbackCandidate{
		{Provider: "openai", Model: "gpt-4o", Client: okClient("gpt-4o")},
		{Provider: "anthropic", Model: "claude", Client: okClient("claude")},
	})

	if fc.ModelID() != "gpt-4o" {
		t.Errorf("ModelID() = %q, want %q", fc.ModelID(), "gpt-4o")
	}
}

func TestFallbackChain_ModelID_Empty(t *testing.T) {
	fc := NewFallbackChain([]FallbackCandidate{})
	if fc.ModelID() != "" {
		t.Errorf("ModelID() = %q, want empty", fc.ModelID())
	}
}

func TestFallbackChain_ChatStream_FallbackOn429(t *testing.T) {
	fc := NewFallbackChain([]FallbackCandidate{
		{Provider: "openai", Model: "gpt-4o", Client: errorClient("gpt-4o",
			fmt.Errorf("openai stream error (status 429): rate limited"))},
		{Provider: "anthropic", Model: "claude", Client: okClient("claude")},
	})

	ch, err := fc.ChatStream(context.Background(), &ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	delta := <-ch
	if delta.Content != "ok from claude" {
		t.Errorf("expected fallback stream, got: %s", delta.Content)
	}
}

func TestFallbackChain_ChatStream_SingleCandidate(t *testing.T) {
	rawErr := fmt.Errorf("openai stream error (status 429): rate limited")
	fc := NewFallbackChain([]FallbackCandidate{
		{Provider: "openai", Model: "gpt-4o", Client: errorClient("gpt-4o", rawErr)},
	})

	_, err := fc.ChatStream(context.Background(), &ChatRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
	// Single candidate: raw error passthrough
	if err != rawErr {
		t.Errorf("expected raw error passthrough, got: %v", err)
	}
}

func TestFallbackChain_ChatStream_AbortOnAuth(t *testing.T) {
	fc := NewFallbackChain([]FallbackCandidate{
		{Provider: "openai", Model: "gpt-4o", Client: errorClient("gpt-4o",
			fmt.Errorf("openai stream error (status 401): unauthorized"))},
		{Provider: "anthropic", Model: "claude", Client: okClient("claude")},
	})

	_, err := fc.ChatStream(context.Background(), &ChatRequest{})
	if err == nil {
		t.Fatal("expected error for auth failure")
	}
	fe, ok := err.(*FailoverError)
	if !ok {
		t.Fatalf("expected FailoverError, got %T", err)
	}
	if fe.Reason != FailoverAuth {
		t.Errorf("reason = %q, want %q", fe.Reason, FailoverAuth)
	}
}

func TestFallbackChain_ChatStream_AllFail(t *testing.T) {
	fc := NewFallbackChain([]FallbackCandidate{
		{Provider: "openai", Model: "gpt-4o", Client: errorClient("gpt-4o",
			fmt.Errorf("openai stream error (status 429): rate limited"))},
		{Provider: "anthropic", Model: "claude", Client: errorClient("claude",
			fmt.Errorf("anthropic stream error (status 503): overloaded"))},
	})

	_, err := fc.ChatStream(context.Background(), &ChatRequest{})
	if err == nil {
		t.Fatal("expected error when all candidates fail")
	}
	_, ok := err.(*FallbackExhaustedError)
	if !ok {
		t.Fatalf("expected FallbackExhaustedError, got %T", err)
	}
}

func TestFallbackChain_AllInCooldown(t *testing.T) {
	fc := NewFallbackChain([]FallbackCandidate{
		{Provider: "openai", Model: "gpt-4o", Client: errorClient("gpt-4o",
			fmt.Errorf("openai error (status 429): rate limited"))},
		{Provider: "anthropic", Model: "claude", Client: errorClient("claude",
			fmt.Errorf("anthropic error (status 503): overloaded"))},
	})

	// First call exhausts all candidates and puts them in cooldown
	_, _ = fc.Chat(context.Background(), &ChatRequest{})

	// Second call: all in cooldown, no candidates tried
	_, err := fc.Chat(context.Background(), &ChatRequest{})
	if err == nil {
		t.Fatal("expected error when all in cooldown")
	}
}

func TestFallbackChain_SuccessResetsCooldown(t *testing.T) {
	callCount := 0
	flaky := &mockClient{
		modelID: "gpt-4o",
		chatFunc: func(_ context.Context, _ *ChatRequest) (*ChatResponse, error) {
			callCount++
			if callCount == 1 {
				return nil, fmt.Errorf("openai error (status 503): temporary")
			}
			return &ChatResponse{Message: ChatMessage{Content: "recovered"}}, nil
		},
	}

	fc := NewFallbackChain([]FallbackCandidate{
		{Provider: "openai", Model: "gpt-4o", Client: flaky},
		{Provider: "anthropic", Model: "claude", Client: okClient("claude")},
	})

	// First: primary fails, fallback succeeds
	resp, err := fc.Chat(context.Background(), &ChatRequest{})
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if resp.Message.Content != "ok from claude" {
		t.Errorf("first: expected fallback, got %s", resp.Message.Content)
	}

	// Manually reset cooldown for primary (simulates time passing)
	fc.cooldown.MarkSuccess("openai")

	// Second: primary succeeds now
	resp, err = fc.Chat(context.Background(), &ChatRequest{})
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if resp.Message.Content != "recovered" {
		t.Errorf("second: expected recovered, got %s", resp.Message.Content)
	}
}
