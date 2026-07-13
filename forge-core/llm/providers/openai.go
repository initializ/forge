// Package providers implements LLM client providers for various APIs.
package providers

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	apiKey         string
	baseURL        string
	model          string
	orgID          string
	authScheme     string
	authHeaderName string
	promptCaching  bool
	client         *http.Client
}

// NewOpenAIClient creates a new OpenAI client.
//
// When cfg.AuthScheme == "aws_sigv4" the client's http.Transport is
// wrapped with the SigV4 signer (issue #202 Phase 2) and the per-
// request Authorization: Bearer header is skipped. Routes outbound
// at AWS Bedrock's OpenAI compatibility endpoint or any other
// SigV4-fronted OpenAI-shaped gateway. AWS credentials resolve via
// AWS_ACCESS_KEY_ID / _SECRET_ / _SESSION_TOKEN env; region via
// cfg.AWSRegion. APIKey is ignored on this path.
//
// Empty AuthScheme preserves the pre-#202 contract byte-for-byte.
func NewOpenAIClient(cfg llm.ClientConfig) *OpenAIClient {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	timeout := time.Duration(cfg.TimeoutSecs) * time.Second
	if timeout == 0 {
		timeout = 120 * time.Second
	}
	httpClient := &http.Client{Timeout: timeout}
	if cfg.AuthScheme == llm.AuthSchemeAWSSigV4 {
		httpClient.Transport = newBedrockSigningTransport(cfg.AWSRegion, http.DefaultTransport)
	}
	return &OpenAIClient{
		apiKey:         cfg.APIKey,
		baseURL:        strings.TrimRight(baseURL, "/"),
		model:          cfg.Model,
		orgID:          cfg.OrgID,
		authScheme:     cfg.AuthScheme,
		authHeaderName: cfg.AuthHeaderName,
		promptCaching:  cfg.PromptCaching,
		client:         httpClient,
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
	// When AuthScheme=aws_sigv4 the SigV4 transport will stamp the
	// Authorization header itself; don't pre-populate a Bearer token
	// that would be replaced and confuse trace logs. Issue #202 Phase 2.
	if c.apiKey != "" && c.authScheme != llm.AuthSchemeAWSSigV4 {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	if c.orgID != "" {
		req.Header.Set("OpenAI-Organization", c.orgID)
	}
	setGatewayAPIKeyHeader(req, c.authScheme, c.authHeaderName, c.apiKey)
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
	// PromptCacheKey pins OpenAI's prompt-cache routing so requests from
	// this agent land on the same cache shard. Prefix caching itself is
	// automatic (≥1024 tokens); the key improves hit locality. Only set
	// when ClientConfig.PromptCaching is on.
	PromptCacheKey string `json:"prompt_cache_key,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type openaiMessage struct {
	Role       string         `json:"role"`
	Content    *string        `json:"content,omitempty"`
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
		msg := openaiMessage{
			Role:       m.Role,
			ToolCalls:  m.ToolCalls,
			ToolCallID: m.ToolCallID,
			Name:       m.Name,
		}
		// Assistant messages with tool_calls may omit content (valid per OpenAI spec).
		// All other roles must always include content as a string.
		if m.Role == "assistant" && len(m.ToolCalls) > 0 && m.Content == "" {
			// Leave Content nil — omitempty will omit the field entirely.
		} else {
			content := m.Content
			msg.Content = &content
		}
		msgs[i] = msg
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

	if c.promptCaching {
		r.PromptCacheKey = derivePromptCacheKey(model, req)
	}

	return r
}

// derivePromptCacheKey builds a stable cache-routing key from the parts of
// the request that define the cacheable prefix: model, system prompt, and
// tool names. Identical (model, system, tools) across turns → identical key
// → OpenAI routes the session to the same cache shard.
func derivePromptCacheKey(model string, req *llm.ChatRequest) string {
	h := sha256.New()
	h.Write([]byte(model))
	h.Write([]byte{0})
	for _, m := range req.Messages {
		if m.Role == llm.RoleSystem {
			h.Write([]byte(m.Content))
			break
		}
	}
	h.Write([]byte{0})
	for _, t := range req.Tools {
		h.Write([]byte(t.Function.Name))
		h.Write([]byte{0})
	}
	return "forge-" + hex.EncodeToString(h.Sum(nil))[:16]
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
			InputTokens:  resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens,
			TotalTokens:  resp.Usage.TotalTokens,
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
				InputTokens:  chunk.Usage.PromptTokens,
				OutputTokens: chunk.Usage.CompletionTokens,
				TotalTokens:  chunk.Usage.TotalTokens,
			}
		}
		ch <- delta
	}
}
