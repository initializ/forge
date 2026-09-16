package providers

import (
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

// BedrockClient implements llm.Client for the native AWS Bedrock
// Converse API (issue #205). Unlike the aws_sigv4 auth_scheme — which
// signs an OpenAI or Anthropic wire-format request and reaches Bedrock
// only via its compat endpoints or a proxy — this client emits
// Bedrock's own model-agnostic Converse request/response shape at
//
//	POST <baseURL>/model/<modelId>/converse          (non-streaming)
//	POST <baseURL>/model/<modelId>/converse-stream   (streaming)
//
// so any Bedrock model (Claude, Nova, Llama, Mistral, Titan, …) works
// through one translation. SigV4 signing is intrinsic: the http.Client
// transport is always wrapped with the `bedrock`-service signer (reused
// from the aws_sigv4 path), so no auth_scheme is required. Credentials
// resolve from the standard AWS env vars (AWS_ACCESS_KEY_ID /
// _SECRET_ACCESS_KEY / _SESSION_TOKEN) and the region from cfg.AWSRegion.
type BedrockClient struct {
	baseURL       string
	modelID       string
	region        string
	promptCaching bool
	client        *http.Client
}

// NewBedrockClient creates a Bedrock Converse client. When cfg.BaseURL
// is empty it defaults to https://bedrock-runtime.<region>.amazonaws.com
// so the operator only needs to set model.aws_region. The transport is
// always wrapped with the SigV4 signer; APIKey is ignored on this path.
func NewBedrockClient(cfg llm.ClientConfig) *BedrockClient {
	baseURL := cfg.BaseURL
	if baseURL == "" && cfg.AWSRegion != "" {
		baseURL = fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com", cfg.AWSRegion)
	}
	timeout := time.Duration(cfg.TimeoutSecs) * time.Second
	if timeout == 0 {
		timeout = 120 * time.Second
	}
	httpClient := &http.Client{Timeout: timeout}
	httpClient.Transport = newBedrockSigningTransport(cfg.AWSRegion, http.DefaultTransport)
	return &BedrockClient{
		baseURL:       strings.TrimRight(baseURL, "/"),
		modelID:       cfg.Model,
		region:        cfg.AWSRegion,
		promptCaching: cfg.PromptCaching,
		client:        httpClient,
	}
}

func (c *BedrockClient) ModelID() string { return c.modelID }

// converseURL builds the Converse endpoint for the given model id. The
// model id (e.g. "anthropic.claude-sonnet-4-20250514-v1:0") is
// percent-encoded with the same uriEscape the SigV4 signer applies to
// each path segment — so the colon becomes %3A both on the wire (via the
// URL's RawPath) and in the canonical request the signature covers.
// Using url.PathEscape instead would leave the colon literal on the wire
// while the signer canonicalizes it to %3A, yielding SignatureDoesNotMatch.
//
// Plain model IDs and cross-region inference-profile IDs (dotted, e.g.
// "us.anthropic.claude-...") round-trip exactly. A raw inference-profile
// ARN (which embeds "/") would be canonicalized segment-by-segment by the
// signer and is not covered here — use the model/profile ID form, which
// is what Converse expects anyway.
func (c *BedrockClient) converseURL(model string, stream bool) string {
	action := "converse"
	if stream {
		action = "converse-stream"
	}
	return fmt.Sprintf("%s/model/%s/%s", c.baseURL, uriEscape(model, true), action)
}

// Chat sends a non-streaming Converse request.
func (c *BedrockClient) Chat(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	model := c.modelForRequest(req)
	body, err := json.Marshal(c.toConverseRequest(req))
	if err != nil {
		return nil, fmt.Errorf("marshalling request: %w", err)
	}

	endpoint := c.converseURL(model, false)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	safeEndpoint := sanitizeEndpoint(endpoint)
	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("bedrock request to %s: %w", safeEndpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("bedrock error (status %d) calling %s: %s", resp.StatusCode, safeEndpoint, string(respBody))
	}

	result, err := c.parseConverseResponse(resp.Body)
	if result != nil {
		result.Endpoint = safeEndpoint
	}
	return result, err
}

// ChatStream sends a streaming Converse request. The response is
// Bedrock's application/vnd.amazon.eventstream binary framing, decoded
// into provider-agnostic StreamDelta values.
func (c *BedrockClient) ChatStream(ctx context.Context, req *llm.ChatRequest) (<-chan llm.StreamDelta, error) {
	model := c.modelForRequest(req)
	body, err := json.Marshal(c.toConverseRequest(req))
	if err != nil {
		return nil, fmt.Errorf("marshalling request: %w", err)
	}

	endpoint := c.converseURL(model, true)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/vnd.amazon.eventstream")

	safeEndpoint := sanitizeEndpoint(endpoint)
	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("bedrock stream request to %s: %w", safeEndpoint, err)
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("bedrock stream error (status %d) calling %s: %s", resp.StatusCode, safeEndpoint, string(respBody))
	}

	ch := make(chan llm.StreamDelta, 32)
	go func() {
		defer func() { _ = resp.Body.Close() }()
		defer close(ch)
		c.readConverseStream(resp.Body, ch)
	}()
	return ch, nil
}

func (c *BedrockClient) modelForRequest(req *llm.ChatRequest) string {
	if req.Model != "" {
		return req.Model
	}
	return c.modelID
}

// ── Converse request types ──────────────────────────────────────────

type converseRequest struct {
	Messages        []converseMessage     `json:"messages"`
	System          []converseSystemBlock `json:"system,omitempty"`
	InferenceConfig *converseInferenceCfg `json:"inferenceConfig,omitempty"`
	ToolConfig      *converseToolConfig   `json:"toolConfig,omitempty"`
}

type converseInferenceCfg struct {
	MaxTokens   int      `json:"maxTokens,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
}

type converseSystemBlock struct {
	Text       string           `json:"text,omitempty"`
	CachePoint *converseCachePt `json:"cachePoint,omitempty"`
}

// converseCachePt is a Converse prompt-cache breakpoint. Everything up
// to and including the block it follows is cached (~5 min). Injected
// only when PromptCaching is enabled, mirroring the anthropic client.
type converseCachePt struct {
	Type string `json:"type"` // always "default"
}

type converseMessage struct {
	Role    string                 `json:"role"` // "user" | "assistant"
	Content []converseContentBlock `json:"content"`
}

// converseContentBlock is a Converse content block. Exactly one field is
// set per block: text, toolUse (assistant tool call), or toolResult
// (tool output, carried in a user-role message).
type converseContentBlock struct {
	Text       string              `json:"text,omitempty"`
	ToolUse    *converseToolUse    `json:"toolUse,omitempty"`
	ToolResult *converseToolResult `json:"toolResult,omitempty"`
	CachePoint *converseCachePt    `json:"cachePoint,omitempty"`
}

type converseToolUse struct {
	ToolUseID string          `json:"toolUseId"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
}

type converseToolResult struct {
	ToolUseID string                    `json:"toolUseId"`
	Content   []converseToolResultBlock `json:"content"`
}

type converseToolResultBlock struct {
	Text string `json:"text,omitempty"`
}

type converseToolConfig struct {
	Tools []converseTool `json:"tools"`
}

type converseTool struct {
	ToolSpec   *converseToolSpec `json:"toolSpec,omitempty"`
	CachePoint *converseCachePt  `json:"cachePoint,omitempty"`
}

type converseToolSpec struct {
	Name        string              `json:"name"`
	Description string              `json:"description,omitempty"`
	InputSchema converseInputSchema `json:"inputSchema"`
}

type converseInputSchema struct {
	JSON json.RawMessage `json:"json"`
}

// toConverseRequest translates the provider-agnostic ChatRequest into a
// Converse body. System messages are collected into the top-level
// `system` array; tool-result messages (RoleTool) become toolResult
// content blocks wrapped in a user-role message; assistant tool calls
// become toolUse blocks. This mirrors the Anthropic Messages mapping
// (Bedrock's Converse shape is closely modeled on it) but uses
// Converse's field names.
func (c *BedrockClient) toConverseRequest(req *llm.ChatRequest) converseRequest {
	out := converseRequest{}

	var system []converseSystemBlock
	for _, m := range req.Messages {
		if m.Role == llm.RoleSystem {
			if m.Content != "" {
				system = append(system, converseSystemBlock{Text: m.Content})
			}
			continue
		}
		out.Messages = append(out.Messages, c.convertMessage(m))
	}
	if len(system) > 0 {
		out.System = system
	}

	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 4096
	}
	out.InferenceConfig = &converseInferenceCfg{
		MaxTokens:   maxTokens,
		Temperature: req.Temperature,
	}

	if len(req.Tools) > 0 {
		tc := &converseToolConfig{}
		for _, t := range req.Tools {
			schema := t.Function.Parameters
			if len(schema) == 0 {
				// Converse requires a JSON schema object; supply an empty one.
				schema = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			tc.Tools = append(tc.Tools, converseTool{
				ToolSpec: &converseToolSpec{
					Name:        t.Function.Name,
					Description: t.Function.Description,
					InputSchema: converseInputSchema{JSON: schema},
				},
			})
		}
		out.ToolConfig = tc
	}

	// Prompt-cache breakpoints (opt-in via ClientConfig.PromptCaching).
	// Converse serializes the cached prefix as tools → system → messages,
	// so a cachePoint after the last tool keeps the tools segment cached,
	// and one after the last system block caches tools+system. Matches the
	// anthropic client's placement (anthropic.go). Forge's agnostic types
	// cannot express cachePoint, so no caller placement can conflict.
	if c.promptCaching {
		cp := &converseCachePt{Type: "default"}
		if out.ToolConfig != nil && len(out.ToolConfig.Tools) > 0 {
			out.ToolConfig.Tools = append(out.ToolConfig.Tools, converseTool{CachePoint: cp})
		}
		if len(out.System) > 0 {
			out.System = append(out.System, converseSystemBlock{CachePoint: cp})
		}
	}

	return out
}

func (c *BedrockClient) convertMessage(m llm.ChatMessage) converseMessage {
	// Tool result message → user-role message with a toolResult block.
	if m.Role == llm.RoleTool {
		return converseMessage{
			Role: "user",
			Content: []converseContentBlock{{
				ToolResult: &converseToolResult{
					ToolUseID: m.ToolCallID,
					Content:   []converseToolResultBlock{{Text: m.Content}},
				},
			}},
		}
	}

	// Assistant message with tool calls → text (if any) + toolUse blocks.
	if m.Role == llm.RoleAssistant && len(m.ToolCalls) > 0 {
		var blocks []converseContentBlock
		if m.Content != "" {
			blocks = append(blocks, converseContentBlock{Text: m.Content})
		}
		for _, tc := range m.ToolCalls {
			input := json.RawMessage(tc.Function.Arguments)
			if len(input) == 0 {
				input = json.RawMessage(`{}`)
			}
			blocks = append(blocks, converseContentBlock{
				ToolUse: &converseToolUse{
					ToolUseID: tc.ID,
					Name:      tc.Function.Name,
					Input:     input,
				},
			})
		}
		return converseMessage{Role: "assistant", Content: blocks}
	}

	// Plain text message.
	role := m.Role
	if role != llm.RoleAssistant {
		role = "user"
	}
	return converseMessage{
		Role:    role,
		Content: []converseContentBlock{{Text: m.Content}},
	}
}

// ── Converse response types ─────────────────────────────────────────

type converseResponse struct {
	Output struct {
		Message converseMessage `json:"message"`
	} `json:"output"`
	StopReason string `json:"stopReason"`
	Usage      struct {
		InputTokens  int `json:"inputTokens"`
		OutputTokens int `json:"outputTokens"`
		TotalTokens  int `json:"totalTokens"`
	} `json:"usage"`
}

func (c *BedrockClient) parseConverseResponse(body io.Reader) (*llm.ChatResponse, error) {
	var resp converseResponse
	if err := json.NewDecoder(body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("decoding bedrock response: %w", err)
	}

	msg := llm.ChatMessage{Role: llm.RoleAssistant}
	for _, block := range resp.Output.Message.Content {
		switch {
		case block.Text != "":
			msg.Content += block.Text
		case block.ToolUse != nil:
			msg.ToolCalls = append(msg.ToolCalls, llm.ToolCall{
				ID:   block.ToolUse.ToolUseID,
				Type: "function",
				Function: llm.FunctionCall{
					Name:      block.ToolUse.Name,
					Arguments: string(block.ToolUse.Input),
				},
			})
		}
	}

	return &llm.ChatResponse{
		ID:      "",
		Message: msg,
		Usage: llm.UsageInfo{
			InputTokens:  resp.Usage.InputTokens,
			OutputTokens: resp.Usage.OutputTokens,
			TotalTokens:  resp.Usage.TotalTokens,
		},
		FinishReason: mapConverseStopReason(resp.StopReason),
	}, nil
}

// ── Converse streaming ──────────────────────────────────────────────

// Converse stream event payloads (one JSON object per eventstream frame,
// keyed by the frame's `:event-type` header).
type converseStreamStart struct {
	Start struct {
		ToolUse *struct {
			ToolUseID string `json:"toolUseId"`
			Name      string `json:"name"`
		} `json:"toolUse"`
	} `json:"start"`
	ContentBlockIndex int `json:"contentBlockIndex"`
}

type converseStreamDelta struct {
	Delta struct {
		Text    string `json:"text"`
		ToolUse *struct {
			Input string `json:"input"`
		} `json:"toolUse"`
	} `json:"delta"`
	ContentBlockIndex int `json:"contentBlockIndex"`
}

type converseStreamStop struct {
	StopReason string `json:"stopReason"`
}

type converseStreamMetadata struct {
	Usage struct {
		InputTokens  int `json:"inputTokens"`
		OutputTokens int `json:"outputTokens"`
		TotalTokens  int `json:"totalTokens"`
	} `json:"usage"`
}

// readConverseStream decodes Bedrock's vnd.amazon.eventstream frames and
// forwards provider-agnostic StreamDeltas. Text deltas stream through as
// they arrive; a tool call is accumulated across its contentBlockStart
// (id + name) and the partial-JSON `input` fragments carried on each
// contentBlockDelta, then emitted whole on contentBlockStop. Mirrors the
// anthropic client's streaming contract.
func (c *BedrockClient) readConverseStream(r io.Reader, ch chan<- llm.StreamDelta) {
	dec := newEventStreamDecoder(r)
	// Tool calls under construction, keyed by contentBlockIndex.
	tools := map[int]*llm.ToolCall{}

	for {
		ev, err := dec.Next()
		if err != nil {
			// io.EOF is the clean end of stream; anything else is a
			// decode/transport failure surfaced to the consumer.
			if err != io.EOF {
				ch <- llm.StreamDelta{FinishReason: "error"}
			}
			ch <- llm.StreamDelta{Done: true}
			return
		}

		// Modeled exceptions arrive as frames with :message-type=exception.
		if ev.MessageType == "exception" || ev.ExceptionType != "" {
			ch <- llm.StreamDelta{FinishReason: "error"}
			ch <- llm.StreamDelta{Done: true}
			return
		}

		switch ev.EventType {
		case "contentBlockStart":
			var e converseStreamStart
			if json.Unmarshal(ev.Payload, &e) != nil {
				continue
			}
			if e.Start.ToolUse != nil {
				tools[e.ContentBlockIndex] = &llm.ToolCall{
					ID:   e.Start.ToolUse.ToolUseID,
					Type: "function",
					Function: llm.FunctionCall{
						Name: e.Start.ToolUse.Name,
					},
				}
			}
		case "contentBlockDelta":
			var e converseStreamDelta
			if json.Unmarshal(ev.Payload, &e) != nil {
				continue
			}
			if e.Delta.Text != "" {
				ch <- llm.StreamDelta{Content: e.Delta.Text}
			}
			if e.Delta.ToolUse != nil {
				if tc := tools[e.ContentBlockIndex]; tc != nil {
					tc.Function.Arguments += e.Delta.ToolUse.Input
				}
			}
		case "contentBlockStop":
			var e converseStreamDelta // reuse: only ContentBlockIndex is read
			if json.Unmarshal(ev.Payload, &e) != nil {
				continue
			}
			if tc := tools[e.ContentBlockIndex]; tc != nil {
				if tc.Function.Arguments == "" {
					tc.Function.Arguments = "{}"
				}
				ch <- llm.StreamDelta{ToolCalls: []llm.ToolCall{*tc}}
				delete(tools, e.ContentBlockIndex)
			}
		case "messageStop":
			var e converseStreamStop
			if json.Unmarshal(ev.Payload, &e) == nil && e.StopReason != "" {
				ch <- llm.StreamDelta{FinishReason: mapConverseStopReason(e.StopReason)}
			}
		case "metadata":
			var e converseStreamMetadata
			if json.Unmarshal(ev.Payload, &e) == nil {
				ch <- llm.StreamDelta{Usage: &llm.UsageInfo{
					InputTokens:  e.Usage.InputTokens,
					OutputTokens: e.Usage.OutputTokens,
					TotalTokens:  e.Usage.TotalTokens,
				}}
			}
		}
	}
}

// mapConverseStopReason maps Converse stopReason values onto Forge's
// finish-reason vocabulary (OpenAI-style, matching the other providers).
func mapConverseStopReason(reason string) string {
	switch reason {
	case "tool_use":
		return "tool_calls"
	case "end_turn", "stop_sequence":
		return "stop"
	case "max_tokens":
		return "length"
	case "":
		return "stop"
	default:
		return reason
	}
}
