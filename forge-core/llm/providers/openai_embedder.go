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

const (
	defaultEmbeddingModel = "text-embedding-3-small"
	defaultEmbeddingDims  = 1536
	embeddingTimeout      = 30 * time.Second
)

// OpenAIEmbedder implements llm.Embedder using the OpenAI Embeddings API.
type OpenAIEmbedder struct {
	apiKey  string
	baseURL string
	model   string
	dims    int
	client  *http.Client
}

// OpenAIEmbedderConfig configures the OpenAI embedder.
type OpenAIEmbedderConfig struct {
	APIKey  string
	BaseURL string
	Model   string
	Dims    int
}

// NewOpenAIEmbedder creates an OpenAI embedder.
func NewOpenAIEmbedder(cfg OpenAIEmbedderConfig) *OpenAIEmbedder {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	model := cfg.Model
	if model == "" {
		model = defaultEmbeddingModel
	}
	dims := cfg.Dims
	if dims <= 0 {
		dims = defaultEmbeddingDims
	}
	return &OpenAIEmbedder{
		apiKey:  cfg.APIKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		dims:    dims,
		client:  &http.Client{Timeout: embeddingTimeout},
	}
}

func (e *OpenAIEmbedder) Dimensions() int { return e.dims }

// Embed produces embeddings for the given texts using POST /v1/embeddings.
func (e *OpenAIEmbedder) Embed(ctx context.Context, req *llm.EmbeddingRequest) (*llm.EmbeddingResponse, error) {
	if len(req.Texts) == 0 {
		return &llm.EmbeddingResponse{Model: e.model}, nil
	}

	model := req.Model
	if model == "" {
		model = e.model
	}

	body := embeddingRequest{
		Model: model,
		Input: req.Texts,
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshalling embedding request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/embeddings", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+e.apiKey)
	}

	resp, err := e.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("embedding request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("embedding error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var embResp embeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&embResp); err != nil {
		return nil, fmt.Errorf("decoding embedding response: %w", err)
	}

	embeddings := make([][]float32, len(embResp.Data))
	for i, d := range embResp.Data {
		embeddings[i] = d.Embedding
	}

	return &llm.EmbeddingResponse{
		Embeddings: embeddings,
		Model:      embResp.Model,
		Usage: llm.UsageInfo{
			PromptTokens: embResp.Usage.PromptTokens,
			TotalTokens:  embResp.Usage.TotalTokens,
		},
	}, nil
}

// embeddingRequest is the OpenAI embeddings API request format.
type embeddingRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// embeddingResponse is the OpenAI embeddings API response format.
type embeddingResponse struct {
	Data  []embeddingData `json:"data"`
	Model string          `json:"model"`
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

type embeddingData struct {
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}
