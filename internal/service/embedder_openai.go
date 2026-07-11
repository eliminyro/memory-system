package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/pgvector/pgvector-go"
)

// OpenAIEmbedder calls any OpenAI-compatible /v1/embeddings endpoint. The wire
// format ({"model","input"} -> {"data":[{"embedding":[...]}]}) is identical
// across OpenAI, Azure OpenAI, vLLM, LM Studio, LocalAI, and HuggingFace TEI,
// so one client parameterized by base URL + optional API key + model covers
// all of them.
type OpenAIEmbedder struct {
	baseURL    string
	apiKey     string
	model      string
	dimensions int
	client     *http.Client
}

// NewOpenAIEmbedder builds an OpenAI-compatible embedder. baseURL is the API
// root that exposes /embeddings (e.g. https://api.openai.com/v1); a trailing
// slash is tolerated. apiKey may be empty for local servers that don't require
// auth. dimensions, when > 0, is sent as the requested output size for models
// that support truncation (text-embedding-3-*) and is the dimension the corpus
// is pinned to by the startup guard.
func NewOpenAIEmbedder(baseURL, apiKey, model string, dimensions int) *OpenAIEmbedder {
	return &OpenAIEmbedder{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		model:      model,
		dimensions: dimensions,
		client:     &http.Client{Timeout: 30 * time.Second},
	}
}

func (e *OpenAIEmbedder) Dimensions() int { return e.dimensions }

type openAIEmbedRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
	// Dimensions is only emitted when > 0 (omitempty). Models that support
	// output-dimension truncation honor it; lenient servers ignore it.
	Dimensions int `json:"dimensions,omitempty"`
}

type openAIEmbedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// Embed generates an embedding vector for text via the OpenAI-compatible API.
func (e *OpenAIEmbedder) Embed(ctx context.Context, text string) (pgvector.Vector, error) {
	body, err := json.Marshal(openAIEmbedRequest{
		Model:      e.model,
		Input:      text,
		Dimensions: e.dimensions,
	})
	if err != nil {
		return pgvector.Vector{}, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return pgvector.Vector{}, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return pgvector.Vector{}, fmt.Errorf("openai request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		// Full upstream status/body is logged server-side only (audit #18);
		// callers get a generic sentinel so provider internals never leak into
		// MCP responses.
		slog.Error("openai embedding request failed", "status", resp.StatusCode, "body", string(b))
		return pgvector.Vector{}, ErrEmbeddingUnavailable
	}

	var result openAIEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return pgvector.Vector{}, fmt.Errorf("decode response: %w", err)
	}
	if len(result.Data) == 0 || len(result.Data[0].Embedding) == 0 {
		return pgvector.Vector{}, fmt.Errorf("no embeddings returned")
	}
	return pgvector.NewVector(result.Data[0].Embedding), nil
}
