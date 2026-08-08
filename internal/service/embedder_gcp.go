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
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

type GCPEmbedder struct {
	project    string
	location   string
	model      string
	dimensions int
	client     *http.Client
}

func NewGCPEmbedder(project, location, model string, dimensions int) (*GCPEmbedder, error) {
	ctx := context.Background()
	creds, err := google.FindDefaultCredentials(ctx, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return nil, fmt.Errorf("find default credentials: %w", err)
	}

	baseClient := oauth2.NewClient(ctx, creds.TokenSource)
	baseClient.Timeout = 30 * time.Second

	return &GCPEmbedder{
		project:    project,
		location:   location,
		model:      model,
		dimensions: dimensions,
		client:     baseClient,
	}, nil
}

func (e *GCPEmbedder) Dimensions() int {
	return e.dimensions
}

type gcpEmbedRequest struct {
	Instances  []gcpInstance `json:"instances"`
	Parameters gcpParameters `json:"parameters"`
}

type gcpInstance struct {
	Content string `json:"content"`
}

type gcpParameters struct {
	OutputDimensionality int `json:"outputDimensionality"`
}

type gcpEmbedResponse struct {
	Predictions []gcpPrediction `json:"predictions"`
}

type gcpPrediction struct {
	Embeddings gcpEmbeddings `json:"embeddings"`
}

type gcpEmbeddings struct {
	Values []float32 `json:"values"`
}

func (e *GCPEmbedder) Embed(ctx context.Context, text string) (pgvector.Vector, error) {
	reqBody := gcpEmbedRequest{
		Instances:  []gcpInstance{{Content: text}},
		Parameters: gcpParameters{OutputDimensionality: e.dimensions},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return pgvector.Vector{}, fmt.Errorf("marshal request: %w", err)
	}

	url := fmt.Sprintf(
		"https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/google/models/%s:predict",
		e.location, e.project, e.location, e.model,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return pgvector.Vector{}, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return pgvector.Vector{}, fmt.Errorf("vertex ai request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		// Log full detail server-side; never surface to caller (audit #18).
		slog.Error("vertex ai embedding request failed", "status", resp.StatusCode, "body", string(respBody))
		return pgvector.Vector{}, ErrEmbeddingUnavailable
	}

	var result gcpEmbedResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxEmbedResponseBytes)).Decode(&result); err != nil {
		return pgvector.Vector{}, fmt.Errorf("decode response: %w", err)
	}

	if len(result.Predictions) == 0 {
		return pgvector.Vector{}, fmt.Errorf("no predictions returned")
	}

	return pgvector.NewVector(result.Predictions[0].Embeddings.Values), nil
}
