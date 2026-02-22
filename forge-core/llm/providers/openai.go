// Package providers implements LLM client providers for various APIs.
package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/initializ/forge/forge-core/llm"
)

// OpenAIClient implements llm.Client for the OpenAI Chat Completions API.
// Also works with Azure OpenAI and any OpenAI-compatible endpoint.
type OpenAIClient struct {
	apiKey  string
	baseURL string
	model   string
	orgID   string
	client  *http.Client
}

// NewOpenAIClient creates a new OpenAI client.
func NewOpenAIClient(cfg llm.ClientConfig) *OpenAIClient {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	timeout := time.Duration(cfg.TimeoutSecs) * time.Second
	if timeout == 0 {
		timeout = 120 * time.Second
	}
	return &OpenAIClient{
		apiKey:  cfg.APIKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   cfg.Model,
		orgID:   cfg.OrgID,
		client:  &http.Client{Timeout: timeout},
	}
}

func (c *OpenAIClient) ModelID() string { return c.model }

// Chat sends a non-streaming chat completion request.
func (c *OpenAIClient) Chat(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	body := c.toOpenAIRequest(req, false)
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshalling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	c.setHeaders(httpReq)

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("openai error (status %d): %s", resp.StatusCode, string(respBody))
	}

	return c.parseOpenAIResponse(resp.Body)
}

// ChatStream sends a streaming chat completion request.
func (c *OpenAIClient) ChatStream(ctx context.Context, req *llm.ChatRequest) (<-chan llm.StreamDelta, error) {
	body := c.toOpenAIRequest(req, true)
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshalling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	c.setHeaders(httpReq)

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai stream request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("openai stream error (status %d): %s", resp.StatusCode, string(respBody))
	}

	ch := make(chan llm.StreamDelta, 32)
	go func() {
		defer func() { _ = resp.Body.Close() }()
		defer close(ch)
		c.readSSEStream(resp.Body, ch)
	}()

	return ch, nil
}

func (c *OpenAIClient) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	if c.orgID != "" {
		req.Header.Set("OpenAI-Organization", c.orgID)
	}
}

// openaiRequest is the OpenAI-specific request format.
type openaiRequest struct {
	Model         string               `json:"model"`
	Messages      []openaiMessage      `json:"messages"`
	Tools         []llm.ToolDefinition `json:"tools,omitempty"`
	Temperature   *float64             `json:"temperature,omitempty"`
	MaxTokens     int                  `json:"max_tokens,omitempty"`
	Stream        bool                 `json:"stream,omitempty"`
	StreamOptions *streamOptions       `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type openaiMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	ToolCalls  []llm.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	Name       string         `json:"name,omitempty"`
}

func (c *OpenAIClient) toOpenAIRequest(req *llm.ChatRequest, stream bool) openaiRequest {
	model := req.Model
	if model == "" {
		model = c.model
	}

	msgs := make([]openaiMessage, len(req.Messages))
	for i, m := range req.Messages {
		msgs[i] = openaiMessage{
			Role:       m.Role,
			Content:    m.Content,
			ToolCalls:  m.ToolCalls,
			ToolCallID: m.ToolCallID,
			Name:       m.Name,
		}
	}

	r := openaiRequest{
		Model:       model,
		Messages:    msgs,
		Tools:       req.Tools,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Stream:      stream,
	}

	if stream {
		r.StreamOptions = &streamOptions{IncludeUsage: true}
	}

	return r
}

// openaiResponse is the OpenAI-specific response format.
type openaiResponse struct {
	ID      string `json:"id"`
	Choices []struct {
		Message struct {
			Role      string         `json:"role"`
			Content   string         `json:"content"`
			ToolCalls []llm.ToolCall `json:"tool_calls,omitempty"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

func (c *OpenAIClient) parseOpenAIResponse(body io.Reader) (*llm.ChatResponse, error) {
	var resp openaiResponse
	if err := json.NewDecoder(body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("decoding openai response: %w", err)
	}

	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("openai returned no choices")
	}

	choice := resp.Choices[0]
	return &llm.ChatResponse{
		ID: resp.ID,
		Message: llm.ChatMessage{
			Role:      choice.Message.Role,
			Content:   choice.Message.Content,
			ToolCalls: choice.Message.ToolCalls,
		},
		Usage: llm.UsageInfo{
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalTokens:      resp.Usage.TotalTokens,
		},
		FinishReason: choice.FinishReason,
	}, nil
}

// openaiStreamChunk is a streaming response chunk.
type openaiStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content   string         `json:"content"`
			ToolCalls []llm.ToolCall `json:"tool_calls,omitempty"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage,omitempty"`
}

func (c *OpenAIClient) readSSEStream(r io.Reader, ch chan<- llm.StreamDelta) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "data: [DONE]" {
			ch <- llm.StreamDelta{Done: true}
			return
		}
		after, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}

		var chunk openaiStreamChunk
		if err := json.Unmarshal([]byte(after), &chunk); err != nil {
			continue
		}

		delta := llm.StreamDelta{}
		if len(chunk.Choices) > 0 {
			c0 := chunk.Choices[0]
			delta.Content = c0.Delta.Content
			delta.ToolCalls = c0.Delta.ToolCalls
			if c0.FinishReason != nil {
				delta.FinishReason = *c0.FinishReason
			}
		}
		if chunk.Usage != nil {
			delta.Usage = &llm.UsageInfo{
				PromptTokens:     chunk.Usage.PromptTokens,
				CompletionTokens: chunk.Usage.CompletionTokens,
				TotalTokens:      chunk.Usage.TotalTokens,
			}
		}
		ch <- delta
	}
}
