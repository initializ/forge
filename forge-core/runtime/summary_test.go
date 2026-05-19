package runtime

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/llm"
)

func TestGenerateSummary_HappyPath(t *testing.T) {
	var capturedReq *llm.ChatRequest
	client := &mockLLMClient{
		chatFunc: func(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
			capturedReq = req
			return &llm.ChatResponse{
				Message: llm.ChatMessage{Role: llm.RoleAssistant, Content: "  short summary here  "},
			}, nil
		},
	}

	got := generateSummary(context.Background(), client, "long response text")
	if got != "short summary here" {
		t.Errorf("expected trimmed summary, got %q", got)
	}
	if capturedReq == nil || len(capturedReq.Messages) != 2 {
		t.Fatalf("expected 2 messages in summariser request, got %+v", capturedReq)
	}
	if capturedReq.Messages[0].Role != llm.RoleSystem {
		t.Errorf("expected system message first, got role %q", capturedReq.Messages[0].Role)
	}
	if capturedReq.Messages[1].Content != "long response text" {
		t.Errorf("expected response as user message, got %q", capturedReq.Messages[1].Content)
	}
}

func TestGenerateSummary_NilClient(t *testing.T) {
	if got := generateSummary(context.Background(), nil, "anything"); got != "" {
		t.Errorf("expected empty summary with nil client, got %q", got)
	}
}

func TestGenerateSummary_EmptyResponse(t *testing.T) {
	client := &mockLLMClient{
		chatFunc: func(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
			t.Fatal("client should not be called for empty response")
			return nil, nil
		},
	}
	if got := generateSummary(context.Background(), client, "   \n\t  "); got != "" {
		t.Errorf("expected empty summary for whitespace input, got %q", got)
	}
}

func TestGenerateSummary_ClientError(t *testing.T) {
	client := &mockLLMClient{
		chatFunc: func(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
			return nil, fmt.Errorf("simulated network failure")
		},
	}
	if got := generateSummary(context.Background(), client, "long response"); got != "" {
		t.Errorf("expected empty summary on client error (best-effort), got %q", got)
	}
}

func TestFinalizeResponse_SetsSummaryWhenLong(t *testing.T) {
	summariserCalls := 0
	client := &mockLLMClient{
		chatFunc: func(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
			summariserCalls++
			return &llm.ChatResponse{
				Message: llm.ChatMessage{Role: llm.RoleAssistant, Content: "generated summary"},
			}, nil
		},
	}
	exec := &LLMExecutor{client: client}

	long := strings.Repeat("a", summaryInlineThreshold+1)
	got := exec.finalizeResponse(context.Background(), llm.ChatMessage{Role: llm.RoleAssistant, Content: long})

	if got.Summary != "generated summary" {
		t.Errorf("expected Summary populated, got %q", got.Summary)
	}
	if summariserCalls != 1 {
		t.Errorf("expected exactly 1 summariser call, got %d", summariserCalls)
	}
}

func TestFinalizeResponse_SkipsSummaryWhenShort(t *testing.T) {
	client := &mockLLMClient{
		chatFunc: func(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
			t.Fatal("summariser should not be called for short responses")
			return nil, nil
		},
	}
	exec := &LLMExecutor{client: client}

	got := exec.finalizeResponse(context.Background(), llm.ChatMessage{Role: llm.RoleAssistant, Content: "short"})
	if got.Summary != "" {
		t.Errorf("expected empty Summary for short response, got %q", got.Summary)
	}
}

// TestFinalizeResponse_NoSummaryForShortTextWithFile verifies that when the
// LLM body is short but a tool attached a large file, no summariser pass runs.
// The LLM text already acts as the summary of the file, so a second pass would
// just summarise an already-short string.
func TestFinalizeResponse_NoSummaryForShortTextWithFile(t *testing.T) {
	client := &mockLLMClient{
		chatFunc: func(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
			t.Fatal("summariser should not be called when LLM body is short")
			return nil, nil
		},
	}
	exec := &LLMExecutor{client: client}

	fileBacked := a2a.Part{Kind: a2a.PartKindFile, File: &a2a.FileContent{Name: "report.md", Bytes: []byte("huge file content")}}
	got := exec.finalizeResponse(context.Background(), llm.ChatMessage{Role: llm.RoleAssistant, Content: "Here is the report."}, fileBacked)

	if got.Summary != "" {
		t.Errorf("expected empty Summary for short body, got %q", got.Summary)
	}
	if len(got.Parts) != 2 {
		t.Errorf("expected text + file parts, got %d parts", len(got.Parts))
	}
}
