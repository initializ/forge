package brain

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/initializ/forge/forge-core/llm"
)

// thinkTagRe matches <think>...</think> blocks (including empty ones) in model output.
var thinkTagRe = regexp.MustCompile(`(?s)<think>.*?</think>\s*`)

// ErrBrainNotCompiled is returned when the brain build tag is not enabled.
var ErrBrainNotCompiled = errors.New("brain: not compiled (build with -tags brain)")

// Config holds brain engine configuration.
type Config struct {
	ModelPath   string  // path to GGUF model file
	ContextSize int     // context window size (default 8192)
	GPULayers   int     // number of GPU layers (0 = CPU only)
	Threads     int     // number of CPU threads (0 = auto)
	Temperature float32 // sampling temperature (default 0.7)
	MaxTokens   int     // max tokens to generate (default 2048)
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		ContextSize: 8192, // balanced for CPU inference; Qwen3 supports up to 32K
		GPULayers:   0,
		Threads:     0,
		Temperature: 0.7,
		MaxTokens:   2048,
	}
}

// chatOpts holds per-request options for engine calls.
type chatOpts struct {
	MaxTokens   int
	Temperature float32
	Stop        []string
}

// chatResult holds the engine response.
type chatResult struct {
	Content    string
	ToolCalls  []llm.ToolCall
	Reasoning  string
	StopReason string
}

// streamChunk is a chunk from the engine's streaming response.
type streamChunk struct {
	Content string
	Done    bool
	Err     error
}

// engine is the internal interface for local model inference.
type engine interface {
	Chat(ctx context.Context, msgs []llm.ChatMessage, tools []llm.ToolDefinition, opts chatOpts) (*chatResult, error)
	ChatStream(ctx context.Context, msgs []llm.ChatMessage, tools []llm.ToolDefinition, opts chatOpts) (<-chan streamChunk, error)
	Close() error
}

// BrainClient wraps the engine and implements llm.Client for direct brain usage.
type BrainClient struct {
	eng     engine
	modelID string
}

// NewClient creates a new BrainClient from the given config.
// Returns ErrBrainNotCompiled if built without the brain tag.
func NewClient(cfg Config) (*BrainClient, error) {
	eng, err := newEngine(cfg)
	if err != nil {
		return nil, err
	}

	modelID := "brain"
	if cfg.ModelPath != "" {
		// Use model filename as ID
		m, ok := lookupModelByPath(cfg.ModelPath)
		if ok {
			modelID = m.ID
		}
	}

	return &BrainClient{eng: eng, modelID: modelID}, nil
}

// Chat sends a chat request to the local brain engine.
func (b *BrainClient) Chat(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	opts := chatOpts{
		MaxTokens:   req.MaxTokens,
		Temperature: 0.7,
	}
	if req.Temperature != nil {
		opts.Temperature = float32(*req.Temperature)
	}
	if opts.MaxTokens == 0 {
		opts.MaxTokens = 2048
	}

	result, err := b.eng.Chat(ctx, req.Messages, req.Tools, opts)
	if err != nil {
		return nil, err
	}

	// Strip <think>...</think> tags from response (Qwen3 may still emit them)
	content := stripThinkTags(result.Content)

	msg := llm.ChatMessage{
		Role:      llm.RoleAssistant,
		Content:   content,
		ToolCalls: result.ToolCalls,
	}

	return &llm.ChatResponse{
		ID:           fmt.Sprintf("brain-%s", b.modelID),
		Message:      msg,
		FinishReason: result.StopReason,
	}, nil
}

// ChatStream sends a streaming chat request to the local brain engine.
func (b *BrainClient) ChatStream(ctx context.Context, req *llm.ChatRequest) (<-chan llm.StreamDelta, error) {
	opts := chatOpts{
		MaxTokens:   req.MaxTokens,
		Temperature: 0.7,
	}
	if req.Temperature != nil {
		opts.Temperature = float32(*req.Temperature)
	}
	if opts.MaxTokens == 0 {
		opts.MaxTokens = 2048
	}

	chunks, err := b.eng.ChatStream(ctx, req.Messages, req.Tools, opts)
	if err != nil {
		return nil, err
	}

	out := make(chan llm.StreamDelta, 16)
	go func() {
		defer close(out)
		var buf strings.Builder
		thinkDone := false
		for chunk := range chunks {
			if chunk.Err != nil {
				return
			}
			if chunk.Done {
				// Flush any remaining buffered content
				if buf.Len() > 0 {
					cleaned := stripThinkTags(buf.String())
					if cleaned != "" {
						select {
						case out <- llm.StreamDelta{Content: cleaned}:
						case <-ctx.Done():
							return
						}
					}
				}
				select {
				case out <- llm.StreamDelta{Done: true, FinishReason: "stop"}:
				case <-ctx.Done():
				}
				return
			}
			// Buffer content until <think> block is fully closed
			if !thinkDone {
				buf.WriteString(chunk.Content)
				s := buf.String()
				if strings.Contains(s, "</think>") {
					thinkDone = true
					cleaned := stripThinkTags(s)
					buf.Reset()
					if cleaned != "" {
						select {
						case out <- llm.StreamDelta{Content: cleaned}:
						case <-ctx.Done():
							return
						}
					}
				}
				continue
			}
			select {
			case out <- llm.StreamDelta{Content: chunk.Content}:
			case <-ctx.Done():
				return
			}
		}
	}()

	return out, nil
}

// ModelID returns the brain model identifier.
func (b *BrainClient) ModelID() string {
	return b.modelID
}

// Close shuts down the brain engine and frees resources.
func (b *BrainClient) Close() error {
	if b.eng != nil {
		return b.eng.Close()
	}
	return nil
}

// stripThinkTags removes <think>...</think> blocks from model output.
// Qwen3 may still emit these even with /no_think.
func stripThinkTags(s string) string {
	result := thinkTagRe.ReplaceAllString(s, "")
	return strings.TrimSpace(result)
}

// lookupModelByPath finds a model in the registry by its file path.
func lookupModelByPath(path string) (ModelInfo, bool) {
	for _, m := range modelRegistry {
		if ModelPath(m.Filename) == path {
			return m, true
		}
	}
	return ModelInfo{}, false
}
