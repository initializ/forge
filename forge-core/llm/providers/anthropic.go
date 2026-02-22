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
	apiKey  string
	baseURL string
	model   string
	client  *http.Client
}

// NewAnthropicClient creates a new Anthropic client.
func NewAnthropicClient(cfg llm.ClientConfig) *AnthropicClient {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	timeout := time.Duration(cfg.TimeoutSecs) * time.Second
	if timeout == 0 {
		timeout = 120 * time.Second
	}
	return &AnthropicClient{
		apiKey:  cfg.APIKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   cfg.Model,
		client:  &http.Client{Timeout: timeout},
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
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
}

// Anthropic-specific request types.
type anthropicRequest struct {
	Model     string             `json:"model"`
	Messages  []anthropicMessage `json:"messages"`
	System    string             `json:"system,omitempty"`
	MaxTokens int                `json:"max_tokens"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
	Stream    bool               `json:"stream,omitempty"`
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
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
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
	for _, m := range req.Messages {
		if m.Role == llm.RoleSystem {
			r.System = m.Content
			continue
		}
		r.Messages = append(r.Messages, c.convertMessage(m))
	}

	// Convert tools
	for _, t := range req.Tools {
		r.Tools = append(r.Tools, anthropicTool{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			InputSchema: t.Function.Parameters,
		})
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
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.InputTokens + resp.Usage.OutputTokens,
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
					CompletionTokens: ev.Usage.OutputTokens,
				},
			}

		case "message_stop":
			ch <- llm.StreamDelta{Done: true}
			return
		}
	}
}
