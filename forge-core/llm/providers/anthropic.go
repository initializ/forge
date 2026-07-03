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

// AnthropicClient implements llm.Client for the Anthropic Messages API.
type AnthropicClient struct {
	apiKey        string
	baseURL       string
	model         string
	authScheme    string
	promptCaching bool
	client        *http.Client
}

// NewAnthropicClient creates a new Anthropic client.
//
// When cfg.AuthScheme == "aws_sigv4", the client's http.Transport is
// wrapped with the SigV4 signer (issue #202 Phase 2) and the
// per-request x-api-key header is skipped. This routes outbound
// requests at any AWS SigV4-fronted gateway that speaks the
// Anthropic Messages wire format (custom proxies, Bedrock-compat
// proxies). Operators provide AWS credentials via the standard
// environment variables (AWS_ACCESS_KEY_ID / _SECRET_ / _SESSION_TOKEN)
// and the region via cfg.AWSRegion. APIKey is ignored on this path.
//
// All other AuthScheme values (including the empty default) preserve
// the pre-#202 contract: x-api-key + anthropic-version headers, no
// transport wrapping.
func NewAnthropicClient(cfg llm.ClientConfig) *AnthropicClient {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	timeout := time.Duration(cfg.TimeoutSecs) * time.Second
	if timeout == 0 {
		timeout = 120 * time.Second
	}
	httpClient := &http.Client{Timeout: timeout}
	if cfg.AuthScheme == "aws_sigv4" {
		httpClient.Transport = newBedrockSigningTransport(cfg.AWSRegion, http.DefaultTransport)
	}
	return &AnthropicClient{
		apiKey:        cfg.APIKey,
		baseURL:       strings.TrimRight(baseURL, "/"),
		model:         cfg.Model,
		authScheme:    cfg.AuthScheme,
		promptCaching: cfg.PromptCaching,
		client:        httpClient,
	}
}

// newBedrockSigningTransport wraps an underlying transport with a
// SigV4 signer pinned to the `bedrock` service in the configured
// region, reading credentials from env each call so a credential
// rotation propagates without restarting the agent. Issue #202 Phase 2.
func newBedrockSigningTransport(region string, underlying http.RoundTripper) http.RoundTripper {
	return &SigV4Transport{
		Underlying: underlying,
		Region:     region,
		Service:    "bedrock",
		Credentials: func() (SigV4Credentials, error) {
			c, ok := SigV4CredentialsFromEnv()
			if !ok {
				return c, fmt.Errorf("aws_sigv4: AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY env vars required")
			}
			return c, nil
		},
	}
}

func (c *AnthropicClient) ModelID() string { return c.model }

// Chat sends a non-streaming messages request.
func (c *AnthropicClient) Chat(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	body := c.toAnthropicRequest(req, false)
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshalling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/messages", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	c.setHeaders(httpReq)

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("anthropic error (status %d): %s", resp.StatusCode, string(respBody))
	}

	return c.parseAnthropicResponse(resp.Body)
}

// ChatStream sends a streaming messages request.
func (c *AnthropicClient) ChatStream(ctx context.Context, req *llm.ChatRequest) (<-chan llm.StreamDelta, error) {
	body := c.toAnthropicRequest(req, true)
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshalling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/messages", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	c.setHeaders(httpReq)

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic stream request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("anthropic stream error (status %d): %s", resp.StatusCode, string(respBody))
	}

	ch := make(chan llm.StreamDelta, 32)
	go func() {
		defer func() { _ = resp.Body.Close() }()
		defer close(ch)
		c.readAnthropicStream(resp.Body, ch)
	}()

	return ch, nil
}

func (c *AnthropicClient) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	// When the SigV4 transport is wrapped on the client, the
	// Authorization header is what authenticates the request — the
	// x-api-key header MUST NOT be sent, both because it isn't
	// validated upstream and because it would be included in the
	// SigV4 signed-headers set and complicate proxy debugging.
	// Issue #202 Phase 2.
	if c.authScheme != "aws_sigv4" {
		req.Header.Set("x-api-key", c.apiKey)
	}
}

// Anthropic-specific request types.
//
// System is `any` because the Messages API accepts either a plain string or
// an array of content blocks. Without prompt caching it stays a string —
// byte-identical to the pre-caching wire format; with prompt caching it
// becomes []anthropicSystemBlock so a cache_control breakpoint can be
// attached.
type anthropicRequest struct {
	Model     string             `json:"model"`
	Messages  []anthropicMessage `json:"messages"`
	System    any                `json:"system,omitempty"`
	MaxTokens int                `json:"max_tokens"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
	Stream    bool               `json:"stream,omitempty"`
}

// anthropicCacheControl marks a prompt-cache breakpoint. Everything up to and
// including the marked block (in Anthropic's tools → system → messages cache
// order) is cached for ~5 minutes and re-billed at ~10% on hit.
type anthropicCacheControl struct {
	Type string `json:"type"` // always "ephemeral"
}

// anthropicSystemBlock is the block form of the system prompt, used only when
// prompt caching is enabled.
type anthropicSystemBlock struct {
	Type         string                 `json:"type"` // "text"
	Text         string                 `json:"text"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

type anthropicMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type anthropicContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
}

type anthropicTool struct {
	Name         string                 `json:"name"`
	Description  string                 `json:"description"`
	InputSchema  json.RawMessage        `json:"input_schema"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

func (c *AnthropicClient) toAnthropicRequest(req *llm.ChatRequest, stream bool) anthropicRequest {
	model := req.Model
	if model == "" {
		model = c.model
	}

	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 4096
	}

	r := anthropicRequest{
		Model:     model,
		MaxTokens: maxTokens,
		Stream:    stream,
	}

	// Extract system message and convert remaining messages
	var system string
	for _, m := range req.Messages {
		if m.Role == llm.RoleSystem {
			system = m.Content
			continue
		}
		r.Messages = append(r.Messages, c.convertMessage(m))
	}
	if system != "" {
		r.System = system
	}

	// Convert tools
	for _, t := range req.Tools {
		r.Tools = append(r.Tools, anthropicTool{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			InputSchema: t.Function.Parameters,
		})
	}

	// Prompt-cache breakpoints (opt-in via ClientConfig.PromptCaching).
	// Anthropic's cache prefix serializes tools → system → messages, so a
	// breakpoint on the system block caches tools+system, and one on the
	// last tool keeps the tools segment cached even when the system prompt
	// churns. Forge's provider-agnostic types cannot express cache_control,
	// so no caller placement can conflict with this injection. Tool order is
	// already deterministic (Registry.ToolDefinitions sorts by name), which
	// is what makes the cached prefix byte-stable across turns.
	if c.promptCaching {
		ephemeral := &anthropicCacheControl{Type: "ephemeral"}
		if n := len(r.Tools); n > 0 {
			r.Tools[n-1].CacheControl = ephemeral
		}
		if system != "" {
			r.System = []anthropicSystemBlock{{Type: "text", Text: system, CacheControl: ephemeral}}
		}
	}

	return r
}

func (c *AnthropicClient) convertMessage(m llm.ChatMessage) anthropicMessage {
	role := m.Role
	if role == llm.RoleAssistant {
		role = "assistant"
	}

	// Tool result message
	if m.Role == llm.RoleTool {
		blocks := []anthropicContentBlock{
			{
				Type:      "tool_result",
				ToolUseID: m.ToolCallID,
				Content:   m.Content,
			},
		}
		data, _ := json.Marshal(blocks)
		return anthropicMessage{Role: "user", Content: data}
	}

	// Assistant message with tool calls
	if m.Role == llm.RoleAssistant && len(m.ToolCalls) > 0 {
		var blocks []anthropicContentBlock
		if m.Content != "" {
			blocks = append(blocks, anthropicContentBlock{Type: "text", Text: m.Content})
		}
		for _, tc := range m.ToolCalls {
			blocks = append(blocks, anthropicContentBlock{
				Type:  "tool_use",
				ID:    tc.ID,
				Name:  tc.Function.Name,
				Input: json.RawMessage(tc.Function.Arguments),
			})
		}
		data, _ := json.Marshal(blocks)
		return anthropicMessage{Role: "assistant", Content: data}
	}

	// Simple text message
	data, _ := json.Marshal(m.Content)
	return anthropicMessage{Role: role, Content: data}
}

// Anthropic-specific response types.
type anthropicResponse struct {
	ID         string                  `json:"id"`
	Content    []anthropicContentBlock `json:"content"`
	StopReason string                  `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func (c *AnthropicClient) parseAnthropicResponse(body io.Reader) (*llm.ChatResponse, error) {
	var resp anthropicResponse
	if err := json.NewDecoder(body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("decoding anthropic response: %w", err)
	}

	msg := llm.ChatMessage{Role: llm.RoleAssistant}
	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			msg.Content += block.Text
		case "tool_use":
			msg.ToolCalls = append(msg.ToolCalls, llm.ToolCall{
				ID:   block.ID,
				Type: "function",
				Function: llm.FunctionCall{
					Name:      block.Name,
					Arguments: string(block.Input),
				},
			})
		}
	}

	finishReason := "stop"
	if resp.StopReason == "tool_use" {
		finishReason = "tool_calls"
	} else if resp.StopReason == "end_turn" {
		finishReason = "stop"
	} else if resp.StopReason != "" {
		finishReason = resp.StopReason
	}

	return &llm.ChatResponse{
		ID:      resp.ID,
		Message: msg,
		Usage: llm.UsageInfo{
			InputTokens:  resp.Usage.InputTokens,
			OutputTokens: resp.Usage.OutputTokens,
			TotalTokens:  resp.Usage.InputTokens + resp.Usage.OutputTokens,
		},
		FinishReason: finishReason,
	}, nil
}

// Anthropic streaming event types.
type anthropicContentBlockStart struct {
	Index        int                   `json:"index"`
	ContentBlock anthropicContentBlock `json:"content_block"`
}

type anthropicContentBlockDelta struct {
	Index int `json:"index"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text,omitempty"`
		PartialJSON string `json:"partial_json,omitempty"`
	} `json:"delta"`
}

type anthropicMessageDelta struct {
	Delta struct {
		StopReason string `json:"stop_reason"`
	} `json:"delta"`
	Usage struct {
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func (c *AnthropicClient) readAnthropicStream(r io.Reader, ch chan<- llm.StreamDelta) {
	scanner := bufio.NewScanner(r)
	var currentToolCall *llm.ToolCall
	var eventType string

	for scanner.Scan() {
		line := scanner.Text()

		if after, ok := strings.CutPrefix(line, "event: "); ok {
			eventType = after
			continue
		}

		after, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}

		switch eventType {
		case "content_block_start":
			var ev anthropicContentBlockStart
			if json.Unmarshal([]byte(after), &ev) != nil {
				continue
			}
			if ev.ContentBlock.Type == "tool_use" {
				currentToolCall = &llm.ToolCall{
					ID:   ev.ContentBlock.ID,
					Type: "function",
					Function: llm.FunctionCall{
						Name: ev.ContentBlock.Name,
					},
				}
			}

		case "content_block_delta":
			var ev anthropicContentBlockDelta
			if json.Unmarshal([]byte(after), &ev) != nil {
				continue
			}
			switch ev.Delta.Type {
			case "text_delta":
				ch <- llm.StreamDelta{Content: ev.Delta.Text}
			case "input_json_delta":
				if currentToolCall != nil {
					currentToolCall.Function.Arguments += ev.Delta.PartialJSON
				}
			}

		case "content_block_stop":
			if currentToolCall != nil {
				ch <- llm.StreamDelta{
					ToolCalls: []llm.ToolCall{*currentToolCall},
				}
				currentToolCall = nil
			}

		case "message_delta":
			var ev anthropicMessageDelta
			if json.Unmarshal([]byte(after), &ev) != nil {
				continue
			}
			finishReason := "stop"
			if ev.Delta.StopReason == "tool_use" {
				finishReason = "tool_calls"
			}
			ch <- llm.StreamDelta{
				FinishReason: finishReason,
				Usage: &llm.UsageInfo{
					OutputTokens: ev.Usage.OutputTokens,
				},
			}

		case "message_stop":
			ch <- llm.StreamDelta{Done: true}
			return
		}
	}
}
