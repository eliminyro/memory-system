package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/pgvector/pgvector-go"
)

// maxEmbedResponseBytes caps the success body the HTTP embedders decode. An
// embedding-vector JSON is tiny; the ceiling stops a misbehaving/hostile
// operator-configured endpoint from streaming a huge 200 body into an OOM. A
// truncated body then fails the decode → the existing embedding-error path.
const maxEmbedResponseBytes = 8 << 20 // 8 MiB

type OllamaEmbedder struct {
	url        string
	model      string
	dimensions int
	client     *http.Client
}

func NewOllamaEmbedder(ollamaURL, model string, dimensions int) *OllamaEmbedder {
	return &OllamaEmbedder{
		url:        ollamaURL,
		model:      model,
		dimensions: dimensions,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (e *OllamaEmbedder) Dimensions() int {
	return e.dimensions
}

type embedRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

type embedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

// Embed generates an embedding vector for the given text.
func (e *OllamaEmbedder) Embed(ctx context.Context, text string) (pgvector.Vector, error) {
	body, err := json.Marshal(embedRequest{Model: e.model, Input: text})
	if err != nil {
		return pgvector.Vector{}, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return pgvector.Vector{}, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return pgvector.Vector{}, fmt.Errorf("ollama request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		// Log full detail server-side; never surface to caller (audit #18).
		slog.Error("ollama embedding request failed", "status", resp.StatusCode, "body", string(body))
		return pgvector.Vector{}, ErrEmbeddingUnavailable
	}

	var result embedResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxEmbedResponseBytes)).Decode(&result); err != nil {
		return pgvector.Vector{}, fmt.Errorf("decode response: %w", err)
	}

	if len(result.Embeddings) == 0 {
		return pgvector.Vector{}, fmt.Errorf("no embeddings returned")
	}

	return pgvector.NewVector(result.Embeddings[0]), nil
}
