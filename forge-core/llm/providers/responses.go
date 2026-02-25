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

// ResponsesClient implements llm.Client using the OpenAI Responses API.
// This is used with ChatGPT OAuth tokens which are scoped to the Responses API
// endpoint (chatgpt.com/backend-api) rather than the Chat Completions API.
type ResponsesClient struct {
	apiKey       string
	baseURL      string
	model        string
	client       *http.Client
	disableStore bool // set store=false in requests (required for ChatGPT Codex backend)
}

// NewResponsesClient creates a new Responses API client.
func NewResponsesClient(cfg llm.ClientConfig) *ResponsesClient {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	timeout := time.Duration(cfg.TimeoutSecs) * time.Second
	if timeout == 0 {
		timeout = 120 * time.Second
	}
	return &ResponsesClient{
		apiKey:  cfg.APIKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   cfg.Model,
		client:  &http.Client{Timeout: timeout},
	}
}

func (c *ResponsesClient) ModelID() string { return c.model }

// Chat sends a Responses API request. The ChatGPT Codex backend requires
// streaming, so this method always uses stream=true internally and collects
// the full response from the streamed deltas.
func (c *ResponsesClient) Chat(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	ch, err := c.ChatStream(ctx, req)
	if err != nil {
		return nil, err
	}

	// Collect streamed deltas into a single response
	result := &llm.ChatResponse{
		Message: llm.ChatMessage{Role: llm.RoleAssistant},
	}
	// Track tool calls being assembled (keyed by ID)
	toolCallMap := make(map[string]*llm.ToolCall)
	var toolCallOrder []string

	for delta := range ch {
		if delta.Content != "" {
			result.Message.Content += delta.Content
		}
		for _, tc := range delta.ToolCalls {
			existing, ok := toolCallMap[tc.ID]
			if !ok {
				newTC := llm.ToolCall{
					ID:   tc.ID,
					Type: tc.Type,
					Function: llm.FunctionCall{
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					},
				}
				toolCallMap[tc.ID] = &newTC
				toolCallOrder = append(toolCallOrder, tc.ID)
			} else {
				// Append streamed argument deltas
				existing.Function.Arguments += tc.Function.Arguments
			}
		}
		if delta.FinishReason != "" {
			result.FinishReason = delta.FinishReason
		}
		if delta.Usage != nil {
			result.Usage = *delta.Usage
		}
	}

	// Build ordered tool calls slice
	for _, id := range toolCallOrder {
		result.Message.ToolCalls = append(result.Message.ToolCalls, *toolCallMap[id])
	}

	return result, nil
}

// ChatStream sends a streaming Responses API request.
func (c *ResponsesClient) ChatStream(ctx context.Context, req *llm.ChatRequest) (<-chan llm.StreamDelta, error) {
	body := c.buildRequest(req, true)
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshalling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/responses", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	c.setHeaders(httpReq)

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("responses api stream request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("responses api stream error (status %d): %s", resp.StatusCode, string(respBody))
	}

	ch := make(chan llm.StreamDelta, 32)
	go func() {
		defer func() { _ = resp.Body.Close() }()
		defer close(ch)
		c.readStream(resp.Body, ch)
	}()

	return ch, nil
}

func (c *ResponsesClient) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
}

// --- Request types ---

type responsesRequest struct {
	Model        string           `json:"model"`
	Instructions string           `json:"instructions,omitempty"`
	Input        []responsesInput `json:"input"`
	Tools        []responsesTool  `json:"tools,omitempty"`
	Temperature  *float64         `json:"temperature,omitempty"`
	MaxTokens    int              `json:"max_output_tokens,omitempty"`
	Stream       bool             `json:"stream,omitempty"`
	Store        *bool            `json:"store,omitempty"`
}

// responsesInput is a union type for Responses API input items.
// It can be a message (role+content) or a function_call_output.
type responsesInput struct {
	// For messages
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`

	// For function_call items from assistant
	Type      string `json:"type,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`

	// For function_call_output
	Output string `json:"output,omitempty"`
}

// responsesTool is the Responses API tool format (flat, not nested under "function").
type responsesTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

func (c *ResponsesClient) buildRequest(req *llm.ChatRequest, stream bool) responsesRequest {
	model := req.Model
	if model == "" {
		model = c.model
	}

	var instructions string
	var inputs []responsesInput

	for _, msg := range req.Messages {
		switch msg.Role {
		case llm.RoleSystem:
			// System messages become the instructions field
			if instructions != "" {
				instructions += "\n"
			}
			instructions += msg.Content

		case llm.RoleUser:
			inputs = append(inputs, responsesInput{
				Role:    "user",
				Content: msg.Content,
			})

		case llm.RoleAssistant:
			if msg.Content != "" {
				inputs = append(inputs, responsesInput{
					Role:    "assistant",
					Content: msg.Content,
				})
			}
			// If assistant had tool calls, add them as function_call items
			for _, tc := range msg.ToolCalls {
				inputs = append(inputs, responsesInput{
					Type:      "function_call",
					CallID:    tc.ID,
					ID:        tc.ID,
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				})
			}

		case llm.RoleTool:
			// Tool result messages become function_call_output
			inputs = append(inputs, responsesInput{
				Type:   "function_call_output",
				CallID: msg.ToolCallID,
				Output: msg.Content,
			})
		}
	}

	// Convert tools from Chat Completions format to Responses API format
	var tools []responsesTool
	for _, t := range req.Tools {
		tools = append(tools, responsesTool{
			Type:        "function",
			Name:        t.Function.Name,
			Description: t.Function.Description,
			Parameters:  t.Function.Parameters,
		})
	}

	r := responsesRequest{
		Model:        model,
		Instructions: instructions,
		Input:        inputs,
		Tools:        tools,
		Temperature:  req.Temperature,
		MaxTokens:    req.MaxTokens,
		Stream:       stream,
	}

	if c.disableStore {
		f := false
		r.Store = &f
	}

	return r
}

// --- Response types ---

type responsesResponse struct {
	ID     string            `json:"id"`
	Status string            `json:"status"`
	Output []responsesOutput `json:"output"`
	Usage  *responsesUsage   `json:"usage,omitempty"`
}

type responsesOutput struct {
	Type    string                 `json:"type"` // "message" or "function_call"
	Role    string                 `json:"role,omitempty"`
	Content []responsesContentPart `json:"content,omitempty"`

	// For function_call outputs
	ID        string `json:"id,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type responsesContentPart struct {
	Type string `json:"type"` // "output_text"
	Text string `json:"text"`
}

type responsesUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// --- Streaming ---

type streamOutputItemAdded struct {
	OutputIndex int             `json:"output_index"`
	Item        responsesOutput `json:"item"`
}

type streamTextDelta struct {
	OutputIndex  int    `json:"output_index"`
	ContentIndex int    `json:"content_index"`
	Delta        string `json:"delta"`
}

type streamFnArgsDelta struct {
	OutputIndex int    `json:"output_index"`
	Delta       string `json:"delta"`
}

type streamCompleted struct {
	Response responsesResponse `json:"response"`
}

func (c *ResponsesClient) readStream(r io.Reader, ch chan<- llm.StreamDelta) {
	// Track function calls being built so we can emit them with correct IDs
	type pendingFC struct {
		id   string
		name string
	}
	pendingFCs := make(map[int]*pendingFC)

	scanner := bufio.NewScanner(r)
	var currentEvent string

	for scanner.Scan() {
		line := scanner.Text()

		if after, ok := strings.CutPrefix(line, "event: "); ok {
			currentEvent = after
			continue
		}

		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}

		switch currentEvent {
		case "response.output_text.delta":
			var ev streamTextDelta
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				continue
			}
			ch <- llm.StreamDelta{Content: ev.Delta}

		case "response.output_item.added":
			var ev streamOutputItemAdded
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				continue
			}
			if ev.Item.Type == "function_call" {
				pendingFCs[ev.OutputIndex] = &pendingFC{
					id:   ev.Item.CallID,
					name: ev.Item.Name,
				}
				// Emit initial tool call with name
				ch <- llm.StreamDelta{
					ToolCalls: []llm.ToolCall{{
						ID:   ev.Item.CallID,
						Type: "function",
						Function: llm.FunctionCall{
							Name:      ev.Item.Name,
							Arguments: "",
						},
					}},
				}
			}

		case "response.function_call_arguments.delta":
			var ev streamFnArgsDelta
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				continue
			}
			fc := pendingFCs[ev.OutputIndex]
			if fc == nil {
				continue
			}
			ch <- llm.StreamDelta{
				ToolCalls: []llm.ToolCall{{
					ID:   fc.id,
					Type: "function",
					Function: llm.FunctionCall{
						Name:      fc.name,
						Arguments: ev.Delta,
					},
				}},
			}

		case "response.completed":
			var ev streamCompleted
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				continue
			}
			delta := llm.StreamDelta{Done: true}
			if ev.Response.Usage != nil {
				delta.Usage = &llm.UsageInfo{
					PromptTokens:     ev.Response.Usage.InputTokens,
					CompletionTokens: ev.Response.Usage.OutputTokens,
					TotalTokens:      ev.Response.Usage.TotalTokens,
				}
			}
			// Determine finish reason from output
			for _, out := range ev.Response.Output {
				if out.Type == "function_call" {
					delta.FinishReason = "tool_calls"
					break
				}
			}
			if delta.FinishReason == "" {
				delta.FinishReason = "stop"
			}
			ch <- delta
			return
		}

		currentEvent = ""
	}
}
