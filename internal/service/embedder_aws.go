package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/pgvector/pgvector-go"
)

// bedrockInvoker is the minimal seam over the Bedrock Runtime client used by
// AWSEmbedder. The real *bedrockruntime.Client satisfies it, so tests can swap
// in a stub without any network access.
type bedrockInvoker interface {
	InvokeModel(ctx context.Context, in *bedrockruntime.InvokeModelInput, optFns ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelOutput, error)
}

// loadAWSConfig is a package-level seam so tests can inject a config whose
// credential resolution deterministically fails, without depending on the host
// AWS credential chain. Production callers get the standard default config.
var loadAWSConfig = func(ctx context.Context, region string) (aws.Config, error) {
	return config.LoadDefaultConfig(ctx, config.WithRegion(region))
}

// AWSEmbedder generates embeddings via Amazon Bedrock Runtime InvokeModel.
type AWSEmbedder struct {
	client     bedrockInvoker
	model      string
	dimensions int
}

// NewAWSEmbedder resolves AWS config + credentials from the standard chain and
// builds a Bedrock-backed embedder. Credentials are never taken from config —
// they come from the environment, shared config, or an assumed role.
func NewAWSEmbedder(region, model string, dimensions int) (*AWSEmbedder, error) {
	if region == "" {
		return nil, fmt.Errorf("aws: region is required")
	}
	if model == "" {
		return nil, fmt.Errorf("aws: model is required")
	}

	ctx := context.Background()
	cfg, err := loadAWSConfig(ctx, region)
	if err != nil {
		return nil, fmt.Errorf("aws: load config: %w", err)
	}

	// Fail fast on missing credentials so a misconfigured deploy surfaces at
	// startup rather than on the first embedding request.
	if _, err := cfg.Credentials.Retrieve(ctx); err != nil {
		return nil, fmt.Errorf("aws: could not resolve credentials: %w", err)
	}

	return newAWSEmbedder(bedrockruntime.NewFromConfig(cfg), model, dimensions), nil
}

// newAWSEmbedder wraps a bedrockInvoker directly. It exists so the success-path
// tests can construct an embedder around a stub without going through the
// credential-resolving public constructor.
func newAWSEmbedder(inv bedrockInvoker, model string, dim int) *AWSEmbedder {
	return &AWSEmbedder{
		client:     inv,
		model:      model,
		dimensions: dim,
	}
}

func (e *AWSEmbedder) Dimensions() int {
	return e.dimensions
}

type titanRequest struct {
	InputText  string `json:"inputText"`
	Dimensions int    `json:"dimensions,omitempty"`
	Normalize  bool   `json:"normalize"`
}

type titanResponse struct {
	Embedding []float32 `json:"embedding"`
}

type cohereRequest struct {
	Texts     []string `json:"texts"`
	InputType string   `json:"input_type"`
}

type cohereResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

// Embed generates an embedding vector for the given text, dispatching on the
// model family prefix. On any upstream error or empty result it logs the full
// detail server-side and returns the tenant-safe ErrEmbeddingUnavailable
// sentinel (audit #18) — provider internals never reach the caller.
func (e *AWSEmbedder) Embed(ctx context.Context, text string) (pgvector.Vector, error) {
	var body []byte
	var err error

	switch {
	case strings.HasPrefix(e.model, "amazon.titan"):
		req := titanRequest{InputText: text, Normalize: true}
		if e.dimensions > 0 {
			req.Dimensions = e.dimensions
		}
		body, err = json.Marshal(req)
	case strings.HasPrefix(e.model, "cohere."):
		body, err = json.Marshal(cohereRequest{Texts: []string{text}, InputType: "search_document"})
	default:
		return pgvector.Vector{}, fmt.Errorf("aws: unsupported embedding model family: %s", e.model)
	}
	if err != nil {
		return pgvector.Vector{}, fmt.Errorf("marshal request: %w", err)
	}

	out, err := e.client.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
		ModelId:     aws.String(e.model),
		ContentType: aws.String("application/json"),
		Accept:      aws.String("application/json"),
		Body:        body,
	})
	if err != nil {
		// Log the full upstream detail server-side; never surface it to the
		// caller (audit #18) — it can carry provider internals.
		slog.Error("bedrock embedding request failed", "model", e.model, "err", err)
		return pgvector.Vector{}, ErrEmbeddingUnavailable
	}

	var vec []float32
	switch {
	case strings.HasPrefix(e.model, "amazon.titan"):
		var result titanResponse
		if err := json.Unmarshal(out.Body, &result); err != nil {
			return pgvector.Vector{}, fmt.Errorf("decode response: %w", err)
		}
		vec = result.Embedding
	case strings.HasPrefix(e.model, "cohere."):
		var result cohereResponse
		if err := json.Unmarshal(out.Body, &result); err != nil {
			return pgvector.Vector{}, fmt.Errorf("decode response: %w", err)
		}
		if len(result.Embeddings) > 0 {
			vec = result.Embeddings[0]
		}
	}

	if len(vec) == 0 {
		slog.Error("bedrock embedding response contained no vector", "model", e.model)
		return pgvector.Vector{}, ErrEmbeddingUnavailable
	}

	return pgvector.NewVector(vec), nil
}
