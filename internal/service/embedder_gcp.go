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

const (
	// gcpMaxBatch caps instances per :predict call. Vertex accepts many instances
	// per request; batching cuts one HTTP round trip per section down to one per chunk.
	gcpMaxBatch = 250
	// Retry budget for transient upstream failures (429 / 5xx / transport errors).
	gcpMaxAttempts   = 5
	gcpRetryBaseWait = 200 * time.Millisecond
	gcpRetryMaxWait  = 5 * time.Second
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
	preds, err := e.predict(ctx, []gcpInstance{{Content: text}})
	if err != nil {
		return pgvector.Vector{}, err
	}
	if len(preds) == 0 {
		return pgvector.Vector{}, fmt.Errorf("no predictions returned")
	}
	return pgvector.NewVector(preds[0].Embeddings.Values), nil
}

// EmbedBatch embeds texts in native Vertex batches of gcpMaxBatch. Predictions
// map 1:1 to instances, so results stay in input order; length equals len(texts).
func (e *GCPEmbedder) EmbedBatch(ctx context.Context, texts []string) ([]pgvector.Vector, error) {
	if len(texts) == 0 {
		return []pgvector.Vector{}, nil
	}
	out := make([]pgvector.Vector, 0, len(texts))
	for start := 0; start < len(texts); start += gcpMaxBatch {
		end := start + gcpMaxBatch
		if end > len(texts) {
			end = len(texts)
		}
		instances := make([]gcpInstance, end-start)
		for i, t := range texts[start:end] {
			instances[i] = gcpInstance{Content: t}
		}
		preds, err := e.predict(ctx, instances)
		if err != nil {
			return nil, err
		}
		if len(preds) != len(instances) {
			return nil, fmt.Errorf("vertex ai returned %d predictions for %d instances", len(preds), len(instances))
		}
		for _, p := range preds {
			out = append(out, pgvector.NewVector(p.Embeddings.Values))
		}
	}
	return out, nil
}

// predict sends one :predict request for the given instances, shared by Embed
// (1 instance) and EmbedBatch (N) so the wire format cannot drift. It retries
// transient failures (429 / 500 / 503 / transport) with capped exponential
// backoff, honoring ctx; a non-retryable non-2xx returns ErrEmbeddingUnavailable.
func (e *GCPEmbedder) predict(ctx context.Context, instances []gcpInstance) ([]gcpPrediction, error) {
	body, err := json.Marshal(gcpEmbedRequest{
		Instances:  instances,
		Parameters: gcpParameters{OutputDimensionality: e.dimensions},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := fmt.Sprintf(
		"https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/google/models/%s:predict",
		e.location, e.project, e.location, e.model,
	)

	var lastErr error
	for attempt := 0; attempt < gcpMaxAttempts; attempt++ {
		if attempt > 0 {
			if err := gcpBackoff(ctx, attempt); err != nil {
				return nil, err
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := e.client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("vertex ai request: %w", err)
			if ctx.Err() != nil {
				return nil, lastErr
			}
			continue // transient transport error: retry
		}

		if resp.StatusCode == http.StatusOK {
			var result gcpEmbedResponse
			decErr := json.NewDecoder(io.LimitReader(resp.Body, maxEmbedResponseBytes)).Decode(&result)
			_ = resp.Body.Close()
			if decErr != nil {
				return nil, fmt.Errorf("decode response: %w", decErr)
			}
			return result.Predictions, nil
		}

		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		_ = resp.Body.Close()
		// Log full detail server-side; never surface to caller (audit #18).
		slog.Error("vertex ai embedding request failed", "status", resp.StatusCode, "body", string(respBody))
		lastErr = ErrEmbeddingUnavailable
		if !gcpRetryableStatus(resp.StatusCode) {
			return nil, ErrEmbeddingUnavailable
		}
	}
	return nil, lastErr
}

func gcpRetryableStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable:
		return true
	default:
		return false
	}
}

// gcpBackoff sleeps base*2^(attempt-1) capped at gcpRetryMaxWait, or returns
// ctx.Err() if the context is cancelled while waiting.
func gcpBackoff(ctx context.Context, attempt int) error {
	wait := gcpRetryBaseWait << (attempt - 1)
	if wait > gcpRetryMaxWait || wait <= 0 {
		wait = gcpRetryMaxWait
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
