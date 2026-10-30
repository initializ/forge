package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/initializ/forge/forge-core/llm"
)

// GeminiClient implements llm.Client for Google's NATIVE Gemini generateContent
// API — not the OpenAI chat-completions compat shim. The native API carries
// text, image/PDF input (inlineData), function calling, and model-generated
// image OUTPUT (inlineData image parts), none of which the compat surface can
// represent faithfully (#255).
type GeminiClient struct {
	apiKey         string
	baseURL        string
	model          string
	authScheme     string
	authHeaderName string
	enableImageGen bool
	client         *http.Client
}

// NewGeminiClient creates a native Gemini client.
//
// Default base URL is the native generativelanguage endpoint
// (…/v1beta); Chat targets `/models/<model>:generateContent` and ChatStream
// `/models/<model>:streamGenerateContent?alt=sse`. Auth is the native
// `x-goog-api-key` header by default; the gateway auth_scheme values
// (bearer / apikey_header[_only]) compose the same way as the other clients so
// a Kong/gateway-fronted Gemini works. There is no SigV4 path for Gemini.
func NewGeminiClient(cfg llm.ClientConfig) *GeminiClient {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://generativelanguage.googleapis.com/v1beta"
	}
	timeout := time.Duration(cfg.TimeoutSecs) * time.Second
	if timeout == 0 {
		timeout = 120 * time.Second
	}
	return &GeminiClient{
		apiKey:         cfg.APIKey,
		baseURL:        strings.TrimRight(baseURL, "/"),
		model:          cfg.Model,
		authScheme:     cfg.AuthScheme,
		authHeaderName: cfg.AuthHeaderName,
		enableImageGen: cfg.EnableImageGeneration,
		client:         &http.Client{Timeout: timeout},
	}
}

func (c *GeminiClient) ModelID() string { return c.model }

func (c *GeminiClient) modelFor(req *llm.ChatRequest) string {
	if req.Model != "" {
		return req.Model
	}
	return c.model
}

// Chat sends a non-streaming generateContent request. Model-generated images
// (when image generation is enabled) arrive here as inlineData parts and are
// surfaced on resp.Message.Parts — the only path the agent loop uses.
func (c *GeminiClient) Chat(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	data, err := json.Marshal(c.toGeminiRequest(req))
	if err != nil {
		return nil, fmt.Errorf("marshalling request: %w", err)
	}
	endpoint := fmt.Sprintf("%s/models/%s:generateContent", c.baseURL, c.modelFor(req))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	c.setHeaders(httpReq)

	safeEndpoint := sanitizeEndpoint(endpoint)
	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("gemini request to %s: %w", safeEndpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("gemini error (status %d) calling %s: %s", resp.StatusCode, safeEndpoint, string(respBody))
	}

	result, err := parseGeminiResponse(resp.Body)
	if result != nil {
		result.Endpoint = safeEndpoint
	}
	return result, err
}

// ChatStream sends a streaming generateContent request (SSE).
func (c *GeminiClient) ChatStream(ctx context.Context, req *llm.ChatRequest) (<-chan llm.StreamDelta, error) {
	data, err := json.Marshal(c.toGeminiRequest(req))
	if err != nil {
		return nil, fmt.Errorf("marshalling request: %w", err)
	}
	endpoint := fmt.Sprintf("%s/models/%s:streamGenerateContent?alt=sse", c.baseURL, c.modelFor(req))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	c.setHeaders(httpReq)

	safeEndpoint := sanitizeEndpoint(endpoint)
	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("gemini stream request to %s: %w", safeEndpoint, err)
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("gemini stream error (status %d) calling %s: %s", resp.StatusCode, safeEndpoint, string(respBody))
	}

	ch := make(chan llm.StreamDelta, 32)
	go func() {
		defer func() { _ = resp.Body.Close() }()
		defer close(ch)
		readGeminiStream(resp.Body, ch)
	}()
	return ch, nil
}

func (c *GeminiClient) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	switch c.authScheme {
	case llm.AuthSchemeBearer:
		if c.apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.apiKey)
		}
	case llm.AuthSchemeAPIKeyHeaderOnly:
		// Gateway injects the real upstream credential; suppress the native key.
	default:
		if c.apiKey != "" {
			req.Header.Set("x-goog-api-key", c.apiKey)
		}
	}
	setGatewayAPIKeyHeader(req, c.authScheme, c.authHeaderName, c.apiKey)
}

// --- Request types ---

type geminiRequest struct {
	Contents          []geminiContent  `json:"contents"`
	SystemInstruction *geminiContent   `json:"systemInstruction,omitempty"`
	Tools             []geminiTool     `json:"tools,omitempty"`
	GenerationConfig  *geminiGenConfig `json:"generationConfig,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

// geminiPart is used for BOTH request and response parts.
type geminiPart struct {
	Text             string                  `json:"text,omitempty"`
	InlineData       *geminiInlineData       `json:"inlineData,omitempty"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
}

type geminiInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"` // base64
}

type geminiFunctionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

type geminiFunctionResponse struct {
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response"`
}

type geminiTool struct {
	FunctionDeclarations []geminiFuncDecl `json:"functionDeclarations"`
}

type geminiFuncDecl struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type geminiGenConfig struct {
	Temperature        *float64 `json:"temperature,omitempty"`
	MaxOutputTokens    int      `json:"maxOutputTokens,omitempty"`
	ResponseModalities []string `json:"responseModalities,omitempty"`
}

func (c *GeminiClient) toGeminiRequest(req *llm.ChatRequest) geminiRequest {
	var out geminiRequest
	for _, m := range req.Messages {
		if m.Role == llm.RoleSystem {
			out.SystemInstruction = &geminiContent{Parts: []geminiPart{{Text: m.Content}}}
			continue
		}
		out.Contents = append(out.Contents, geminiConvertMessage(m))
	}

	if len(req.Tools) > 0 {
		decls := make([]geminiFuncDecl, 0, len(req.Tools))
		for _, t := range req.Tools {
			decls = append(decls, geminiFuncDecl{
				Name:        t.Function.Name,
				Description: t.Function.Description,
				Parameters:  t.Function.Parameters,
			})
		}
		out.Tools = []geminiTool{{FunctionDeclarations: decls}}
	}

	// generationConfig is set only when something is configured, so a plain
	// text request stays minimal. Image generation is Gemini's responseModalities
	// mechanism (not a tool) — opt-in, and only honored by image-capable models.
	if req.Temperature != nil || req.MaxTokens > 0 || c.enableImageGen {
		gc := &geminiGenConfig{Temperature: req.Temperature, MaxOutputTokens: req.MaxTokens}
		if c.enableImageGen {
			gc.ResponseModalities = []string{"TEXT", "IMAGE"}
		}
		out.GenerationConfig = gc
	}
	return out
}

// geminiConvertMessage maps one provider-agnostic message to a Gemini content.
// Roles: user→user, assistant→model, tool→a user content carrying a
// functionResponse. Gemini pairs tool calls to results BY FUNCTION NAME (there
// is no opaque call id), so the assistant's ToolCall.ID is synthesized as the
// function name and recovered here from the tool message's Name/ToolCallID.
// Limitation: two parallel calls to the same function in one turn can't be
// disambiguated by name.
func geminiConvertMessage(m llm.ChatMessage) geminiContent {
	if m.Role == llm.RoleTool {
		name := m.Name
		if name == "" {
			name = m.ToolCallID
		}
		wrapped, _ := json.Marshal(map[string]string{"result": m.Content})
		return geminiContent{Role: "user", Parts: []geminiPart{{
			FunctionResponse: &geminiFunctionResponse{Name: name, Response: wrapped},
		}}}
	}

	role := "user"
	if m.Role == llm.RoleAssistant {
		role = "model"
	}

	// Assistant message with tool calls → text (if any) + functionCall parts.
	if m.Role == llm.RoleAssistant && len(m.ToolCalls) > 0 {
		var parts []geminiPart
		if m.Content != "" {
			parts = append(parts, geminiPart{Text: m.Content})
		}
		for _, tc := range m.ToolCalls {
			var args json.RawMessage
			if tc.Function.Arguments != "" {
				args = json.RawMessage(tc.Function.Arguments)
			}
			parts = append(parts, geminiPart{FunctionCall: &geminiFunctionCall{Name: tc.Function.Name, Args: args}})
		}
		return geminiContent{Role: role, Parts: parts}
	}

	// Multimodal message → text + inlineData parts.
	if len(m.Parts) > 0 {
		var parts []geminiPart
		for _, p := range m.Parts {
			switch p.Type {
			case llm.ContentPartImage, llm.ContentPartDocument:
				if p.Media == nil || len(p.Media.Bytes) == 0 {
					continue
				}
				parts = append(parts, geminiPart{InlineData: &geminiInlineData{
					MimeType: p.Media.MimeType,
					Data:     base64.StdEncoding.EncodeToString(p.Media.Bytes),
				}})
			default:
				if p.Text != "" {
					parts = append(parts, geminiPart{Text: p.Text})
				}
			}
		}
		return geminiContent{Role: role, Parts: parts}
	}

	return geminiContent{Role: role, Parts: []geminiPart{{Text: m.Content}}}
}

// --- Response types ---

type geminiResponse struct {
	Candidates    []geminiCandidate `json:"candidates"`
	UsageMetadata *geminiUsage      `json:"usageMetadata,omitempty"`
}

type geminiCandidate struct {
	Content      geminiContent `json:"content"`
	FinishReason string        `json:"finishReason,omitempty"`
}

type geminiUsage struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
}

func parseGeminiResponse(r io.Reader) (*llm.ChatResponse, error) {
	var gr geminiResponse
	if err := json.NewDecoder(r).Decode(&gr); err != nil {
		return nil, fmt.Errorf("decoding gemini response: %w", err)
	}
	result := &llm.ChatResponse{Message: llm.ChatMessage{Role: llm.RoleAssistant}}

	// Zero candidates (e.g. a safety block) is not an error — return an empty
	// assistant turn with the finish reason rather than panicking.
	if len(gr.Candidates) > 0 {
		cand := gr.Candidates[0]
		for _, p := range cand.Content.Parts {
			switch {
			case p.FunctionCall != nil:
				args := "{}"
				if len(p.FunctionCall.Args) > 0 {
					args = string(p.FunctionCall.Args)
				}
				result.Message.ToolCalls = append(result.Message.ToolCalls, llm.ToolCall{
					ID:       p.FunctionCall.Name, // Gemini pairs by name; see geminiConvertMessage
					Type:     "function",
					Function: llm.FunctionCall{Name: p.FunctionCall.Name, Arguments: args},
				})
			case p.InlineData != nil && p.InlineData.Data != "":
				if raw, derr := base64.StdEncoding.DecodeString(p.InlineData.Data); derr == nil && len(raw) > 0 {
					result.Message.Parts = append(result.Message.Parts, llm.NewMediaContentPart(
						llm.ContentPartImage, llm.MediaRef{MimeType: p.InlineData.MimeType, Bytes: raw}))
				}
			case p.Text != "":
				result.Message.Content += p.Text
			}
		}
		result.FinishReason = mapGeminiFinish(cand.FinishReason, len(result.Message.ToolCalls) > 0)
	}

	if gr.UsageMetadata != nil {
		result.Usage = llm.UsageInfo{
			InputTokens:  gr.UsageMetadata.PromptTokenCount,
			OutputTokens: gr.UsageMetadata.CandidatesTokenCount,
			TotalTokens:  gr.UsageMetadata.TotalTokenCount,
		}
	}
	return result, nil
}

func mapGeminiFinish(reason string, hasToolCalls bool) string {
	if hasToolCalls {
		return "tool_calls"
	}
	switch reason {
	case "MAX_TOKENS":
		return "length"
	case "STOP", "":
		return "stop"
	default:
		return strings.ToLower(reason)
	}
}

// readGeminiStream parses an alt=sse streamGenerateContent response. Each
// `data:` frame is a partial GenerateContentResponse. The agent loop is
// non-streaming (it uses Chat), so this exists for interface parity.
func readGeminiStream(r io.Reader, ch chan<- llm.StreamDelta) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var lastUsage *llm.UsageInfo
	finish := ""
	hadToolCall := false

	for scanner.Scan() {
		line := scanner.Text()
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "" {
			continue
		}
		var gr geminiResponse
		if json.Unmarshal([]byte(data), &gr) != nil {
			continue
		}
		if len(gr.Candidates) > 0 {
			cand := gr.Candidates[0]
			for _, p := range cand.Content.Parts {
				switch {
				case p.FunctionCall != nil:
					hadToolCall = true
					args := "{}"
					if len(p.FunctionCall.Args) > 0 {
						args = string(p.FunctionCall.Args)
					}
					ch <- llm.StreamDelta{ToolCalls: []llm.ToolCall{{
						ID: p.FunctionCall.Name, Type: "function",
						Function: llm.FunctionCall{Name: p.FunctionCall.Name, Arguments: args},
					}}}
				case p.InlineData != nil && p.InlineData.Data != "":
					if raw, derr := base64.StdEncoding.DecodeString(p.InlineData.Data); derr == nil && len(raw) > 0 {
						ch <- llm.StreamDelta{Parts: []llm.ContentPart{llm.NewMediaContentPart(
							llm.ContentPartImage, llm.MediaRef{MimeType: p.InlineData.MimeType, Bytes: raw})}}
					}
				case p.Text != "":
					ch <- llm.StreamDelta{Content: p.Text}
				}
			}
			if cand.FinishReason != "" {
				finish = cand.FinishReason
			}
		}
		if gr.UsageMetadata != nil {
			lastUsage = &llm.UsageInfo{
				InputTokens:  gr.UsageMetadata.PromptTokenCount,
				OutputTokens: gr.UsageMetadata.CandidatesTokenCount,
				TotalTokens:  gr.UsageMetadata.TotalTokenCount,
			}
		}
	}

	ch <- llm.StreamDelta{Done: true, FinishReason: mapGeminiFinish(finish, hadToolCall), Usage: lastUsage}
}
