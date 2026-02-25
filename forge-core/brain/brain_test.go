package brain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/initializ/forge/forge-core/llm"
)

// --- Model Registry Tests ---

func TestListModels(t *testing.T) {
	models := ListModels()
	if len(models) == 0 {
		t.Fatal("expected at least one model in registry")
	}
}

func TestDefaultModel(t *testing.T) {
	m := DefaultModel()
	if m.ID == "" {
		t.Fatal("default model has empty ID")
	}
	if !m.Default {
		t.Error("DefaultModel() returned non-default model")
	}
}

func TestLookupModel(t *testing.T) {
	m, ok := LookupModel("qwen3-0.6b-q4km")
	if !ok {
		t.Fatal("expected to find qwen3-0.6b-q4km")
	}
	if m.ID != "qwen3-0.6b-q4km" {
		t.Errorf("got ID %q, want %q", m.ID, "qwen3-0.6b-q4km")
	}

	_, ok = LookupModel("nonexistent")
	if ok {
		t.Error("expected LookupModel to return false for nonexistent model")
	}
}

// --- Download Tests ---

func TestDownloadModel(t *testing.T) {
	// Create test server that serves a small file
	content := []byte("fake gguf model content for testing")
	hash := sha256.Sum256(content)
	hashStr := hex.EncodeToString(hash[:])

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
		_, _ = w.Write(content)
	}))
	defer server.Close()

	// Use temp dir
	tmpDir := t.TempDir()
	origModelsDir := ModelsDir
	ModelsDir = func() string { return tmpDir }
	defer func() { ModelsDir = origModelsDir }()

	model := ModelInfo{
		ID:       "test-model",
		Name:     "Test Model",
		Filename: "test-model.gguf",
		URL:      server.URL + "/test-model.gguf",
		SHA256:   hashStr,
		Size:     int64(len(content)),
	}

	var lastProgress DownloadProgress
	err := DownloadModel(model, func(p DownloadProgress) {
		lastProgress = p
	})
	if err != nil {
		t.Fatalf("DownloadModel: %v", err)
	}

	// Check file exists
	destPath := filepath.Join(tmpDir, "test-model.gguf")
	data, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if string(data) != string(content) {
		t.Error("downloaded content doesn't match")
	}

	// Check progress was reported
	if lastProgress.DownloadedBytes != int64(len(content)) {
		t.Errorf("expected %d downloaded bytes, got %d", len(content), lastProgress.DownloadedBytes)
	}
}

func TestDownloadModelResume(t *testing.T) {
	content := []byte("0123456789abcdef")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rangeHeader := r.Header.Get("Range")
		if rangeHeader != "" {
			// Parse range and serve partial content
			var start int64
			_, _ = fmt.Sscanf(rangeHeader, "bytes=%d-", &start)
			w.Header().Set("Content-Length", fmt.Sprintf("%d", int64(len(content))-start))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(content[start:])
			return
		}
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
		_, _ = w.Write(content)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	origModelsDir := ModelsDir
	ModelsDir = func() string { return tmpDir }
	defer func() { ModelsDir = origModelsDir }()

	// Create partial file
	partPath := filepath.Join(tmpDir, "test-resume.gguf.part")
	_ = os.WriteFile(partPath, content[:8], 0o644)

	model := ModelInfo{
		ID:       "test-resume",
		Filename: "test-resume.gguf",
		URL:      server.URL + "/test-resume.gguf",
		Size:     int64(len(content)),
	}

	var resumed bool
	err := DownloadModel(model, func(p DownloadProgress) {
		if p.Resuming {
			resumed = true
		}
	})
	if err != nil {
		t.Fatalf("DownloadModel resume: %v", err)
	}
	if !resumed {
		t.Error("expected resume to be reported")
	}

	destPath := filepath.Join(tmpDir, "test-resume.gguf")
	data, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != string(content) {
		t.Errorf("content mismatch: got %q, want %q", string(data), string(content))
	}
}

func TestDownloadModelSHA256Mismatch(t *testing.T) {
	content := []byte("test content")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(content)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	origModelsDir := ModelsDir
	ModelsDir = func() string { return tmpDir }
	defer func() { ModelsDir = origModelsDir }()

	model := ModelInfo{
		ID:       "test-bad-hash",
		Filename: "test-bad-hash.gguf",
		URL:      server.URL + "/test.gguf",
		SHA256:   "0000000000000000000000000000000000000000000000000000000000000000",
		Size:     int64(len(content)),
	}

	err := DownloadModel(model, nil)
	if err == nil {
		t.Fatal("expected SHA256 mismatch error")
	}
	if !contains(err.Error(), "sha256 mismatch") {
		t.Errorf("expected sha256 mismatch error, got: %v", err)
	}
}

// --- Confidence Scoring Tests ---

func TestScoreConfidenceHighQuality(t *testing.T) {
	result := &chatResult{
		Content: "The weather in San Francisco is currently 65 degrees Fahrenheit with clear skies.",
	}
	score := ScoreConfidence(result, false)
	if score < 0.7 {
		t.Errorf("expected high confidence for quality response, got %.2f", score)
	}
}

func TestScoreConfidenceLowQualityHedging(t *testing.T) {
	result := &chatResult{
		Content: "I'm not sure, but I think maybe it could be possibly around 65 degrees, perhaps.",
	}
	goodResult := &chatResult{
		Content: "The weather in San Francisco is currently 65 degrees Fahrenheit with clear skies.",
	}
	hedgingScore := ScoreConfidence(result, false)
	goodScore := ScoreConfidence(goodResult, false)
	if hedgingScore >= goodScore {
		t.Errorf("hedging response (%.2f) should score lower than good response (%.2f)", hedgingScore, goodScore)
	}
}

func TestScoreConfidenceEmptyResponse(t *testing.T) {
	result := &chatResult{Content: ""}
	score := ScoreConfidence(result, false)
	if score > 0.5 {
		t.Errorf("expected low confidence for empty response, got %.2f", score)
	}
}

func TestScoreConfidenceRepetition(t *testing.T) {
	result := &chatResult{
		Content: "The answer is 42. The answer is 42. The answer is 42. The answer is 42.",
	}
	goodResult := &chatResult{
		Content: "The answer to life, the universe, and everything is 42 according to The Hitchhiker's Guide.",
	}
	repScore := ScoreConfidence(result, false)
	goodScore := ScoreConfidence(goodResult, false)
	if repScore >= goodScore {
		t.Errorf("repetitive response (%.2f) should score lower than good response (%.2f)", repScore, goodScore)
	}
}

func TestScoreConfidenceValidToolCall(t *testing.T) {
	result := &chatResult{
		Content: "",
		ToolCalls: []llm.ToolCall{
			{
				ID:   "tc-1",
				Type: "function",
				Function: llm.FunctionCall{
					Name:      "web_search",
					Arguments: `{"query": "weather SF"}`,
				},
			},
		},
	}
	score := ScoreConfidence(result, true)
	if score < 0.5 {
		t.Errorf("expected reasonable confidence for valid tool call, got %.2f", score)
	}
}

func TestScoreConfidenceInvalidToolCall(t *testing.T) {
	result := &chatResult{
		Content: "",
		ToolCalls: []llm.ToolCall{
			{
				ID:   "tc-1",
				Type: "function",
				Function: llm.FunctionCall{
					Name:      "",
					Arguments: "not json",
				},
			},
		},
	}
	score := ScoreConfidence(result, true)
	if score > 0.5 {
		t.Errorf("expected low confidence for invalid tool call, got %.2f", score)
	}
}

// --- Tool Parsing Tests ---

func TestParseToolCallsValid(t *testing.T) {
	text := `I'll search for that.
{"name": "web_search", "arguments": {"query": "weather SF"}}`

	calls, err := parseToolCalls(text)
	if err != nil {
		t.Fatalf("parseToolCalls: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}
	if calls[0].Function.Name != "web_search" {
		t.Errorf("expected name %q, got %q", "web_search", calls[0].Function.Name)
	}
	if calls[0].Function.Arguments != `{"query": "weather SF"}` {
		t.Errorf("unexpected arguments: %s", calls[0].Function.Arguments)
	}
}

func TestParseToolCallsInCodeBlock(t *testing.T) {
	text := "```json\n{\"name\": \"calculator\", \"arguments\": {\"expression\": \"2+2\"}}\n```"

	calls, err := parseToolCalls(text)
	if err != nil {
		t.Fatalf("parseToolCalls: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}
	if calls[0].Function.Name != "calculator" {
		t.Errorf("expected name %q, got %q", "calculator", calls[0].Function.Name)
	}
}

func TestParseToolCallsNoMatch(t *testing.T) {
	text := "This is just a regular response with no tool calls."

	_, err := parseToolCalls(text)
	if err == nil {
		t.Error("expected error for text without tool calls")
	}
}

func TestParseToolCallsMultiple(t *testing.T) {
	text := `{"name": "search", "arguments": {"q": "a"}}
{"name": "calculate", "arguments": {"expr": "1+1"}}`

	calls, err := parseToolCalls(text)
	if err != nil {
		t.Fatalf("parseToolCalls: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(calls))
	}
}

func TestParseToolCallsPartialJSON(t *testing.T) {
	text := `{"name": "search", "arguments": {"q": "test"}} some extra text {"invalid json`

	calls, err := parseToolCalls(text)
	if err != nil {
		t.Fatalf("parseToolCalls: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 valid tool call, got %d", len(calls))
	}
}

// --- Router Tests ---

type mockEngine struct {
	chatFn   func(ctx context.Context, msgs []llm.ChatMessage, tools []llm.ToolDefinition, opts chatOpts) (*chatResult, error)
	streamFn func(ctx context.Context, msgs []llm.ChatMessage, tools []llm.ToolDefinition, opts chatOpts) (<-chan streamChunk, error)
}

func (m *mockEngine) Chat(ctx context.Context, msgs []llm.ChatMessage, tools []llm.ToolDefinition, opts chatOpts) (*chatResult, error) {
	return m.chatFn(ctx, msgs, tools, opts)
}

func (m *mockEngine) ChatStream(ctx context.Context, msgs []llm.ChatMessage, tools []llm.ToolDefinition, opts chatOpts) (<-chan streamChunk, error) {
	if m.streamFn != nil {
		return m.streamFn(ctx, msgs, tools, opts)
	}
	return nil, fmt.Errorf("stream not implemented")
}

func (m *mockEngine) Close() error { return nil }

type mockRemote struct {
	chatResp   *llm.ChatResponse
	chatErr    error
	streamCh   <-chan llm.StreamDelta
	streamErr  error
	chatCalled bool
}

func (m *mockRemote) Chat(_ context.Context, _ *llm.ChatRequest) (*llm.ChatResponse, error) {
	m.chatCalled = true
	return m.chatResp, m.chatErr
}

func (m *mockRemote) ChatStream(_ context.Context, _ *llm.ChatRequest) (<-chan llm.StreamDelta, error) {
	return m.streamCh, m.streamErr
}

func (m *mockRemote) ModelID() string { return "remote-model" }

func TestRouterAcceptsBrainResponse(t *testing.T) {
	brain := &BrainClient{
		eng: &mockEngine{
			chatFn: func(_ context.Context, _ []llm.ChatMessage, _ []llm.ToolDefinition, _ chatOpts) (*chatResult, error) {
				return &chatResult{
					Content:    "The capital of France is Paris, a major European city known for the Eiffel Tower.",
					StopReason: "stop",
				}, nil
			},
		},
		modelID: "test",
	}

	remote := &mockRemote{
		chatResp: &llm.ChatResponse{
			Message: llm.ChatMessage{Content: "remote response"},
		},
	}

	router := NewRouterFromClient(brain, remote, 0.7)

	resp, err := router.Chat(context.Background(), &llm.ChatRequest{
		Messages: []llm.ChatMessage{{Role: "user", Content: "What is the capital of France?"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if remote.chatCalled {
		t.Error("remote should not have been called for high-confidence response")
	}
	if resp.Message.Content != "The capital of France is Paris, a major European city known for the Eiffel Tower." {
		t.Errorf("expected brain response, got: %s", resp.Message.Content)
	}
}

func TestRouterEscalatesToRemote(t *testing.T) {
	brain := &BrainClient{
		eng: &mockEngine{
			chatFn: func(_ context.Context, _ []llm.ChatMessage, _ []llm.ToolDefinition, _ chatOpts) (*chatResult, error) {
				return &chatResult{
					Content:    "",
					StopReason: "stop",
				}, nil
			},
		},
		modelID: "test",
	}

	remote := &mockRemote{
		chatResp: &llm.ChatResponse{
			Message: llm.ChatMessage{Content: "Precise remote answer with full details."},
		},
	}

	router := NewRouterFromClient(brain, remote, 0.7)

	resp, err := router.Chat(context.Background(), &llm.ChatRequest{
		Messages: []llm.ChatMessage{{Role: "user", Content: "Explain quantum computing"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if !remote.chatCalled {
		result := &chatResult{Content: ""}
		score := ScoreConfidence(result, false)
		t.Errorf("remote should have been called for low-confidence response (score=%.2f)", score)
	}
	if resp.Message.Content != "Precise remote answer with full details." {
		t.Errorf("expected remote response, got: %s", resp.Message.Content)
	}
}

func TestRouterBestEffortWithoutRemote(t *testing.T) {
	brain := &BrainClient{
		eng: &mockEngine{
			chatFn: func(_ context.Context, _ []llm.ChatMessage, _ []llm.ToolDefinition, _ chatOpts) (*chatResult, error) {
				return &chatResult{
					Content:    "I'm not sure, maybe",
					StopReason: "stop",
				}, nil
			},
		},
		modelID: "test",
	}

	router := NewRouterFromClient(brain, nil, 0.7)

	resp, err := router.Chat(context.Background(), &llm.ChatRequest{
		Messages: []llm.ChatMessage{{Role: "user", Content: "test"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	// Should return brain response as best effort
	if resp.Message.Content != "I'm not sure, maybe" {
		t.Errorf("expected brain best-effort response, got: %s", resp.Message.Content)
	}
}

func TestRouterStreamAcceptsBrain(t *testing.T) {
	brain := &BrainClient{
		eng: &mockEngine{
			chatFn: func(_ context.Context, _ []llm.ChatMessage, _ []llm.ToolDefinition, _ chatOpts) (*chatResult, error) {
				return &chatResult{
					Content:    "The answer is clear and definitive without any hedging language present.",
					StopReason: "stop",
				}, nil
			},
		},
		modelID: "test",
	}

	router := NewRouterFromClient(brain, nil, 0.7)

	ch, err := router.ChatStream(context.Background(), &llm.ChatRequest{
		Messages: []llm.ChatMessage{{Role: "user", Content: "test"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	var chunks []llm.StreamDelta
	for delta := range ch {
		chunks = append(chunks, delta)
	}

	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk")
	}

	// Last chunk should be done
	last := chunks[len(chunks)-1]
	if !last.Done {
		t.Error("expected last chunk to be done")
	}
}

func TestRouterModelID(t *testing.T) {
	brain := &BrainClient{modelID: "qwen3-0.6b-q4km"}
	router := NewRouterFromClient(brain, nil, 0.7)
	if got := router.ModelID(); got != "brain:qwen3-0.6b-q4km" {
		t.Errorf("ModelID() = %q, want %q", got, "brain:qwen3-0.6b-q4km")
	}
}

// --- Stub Tests ---

func TestStubReturnsError(t *testing.T) {
	_, err := newEngine(Config{ModelPath: "/tmp/test.gguf"})
	if err == nil {
		t.Fatal("expected error from stub engine")
	}
	if err != ErrBrainNotCompiled {
		t.Errorf("expected ErrBrainNotCompiled, got: %v", err)
	}
}

// --- Helpers ---

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// --- Prompt Formatting Tests ---

func TestFormatToolPrompt(t *testing.T) {
	tools := []llm.ToolDefinition{
		{
			Type: "function",
			Function: llm.FunctionSchema{
				Name:        "web_search",
				Description: "Search the web",
				Parameters:  []byte(`{"type":"object","properties":{"query":{"type":"string"}}}`),
			},
		},
	}

	prompt := FormatToolPrompt(tools)
	if prompt == "" {
		t.Fatal("expected non-empty prompt")
	}
	if !contains(prompt, "web_search") {
		t.Error("prompt should contain tool name")
	}
	if !contains(prompt, "Search the web") {
		t.Error("prompt should contain tool description")
	}
}

func TestFormatToolPromptEmpty(t *testing.T) {
	prompt := FormatToolPrompt(nil)
	if prompt != "" {
		t.Error("expected empty prompt for no tools")
	}
}
