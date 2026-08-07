package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/pgvector/pgvector-go"
)

// bedrockInvoker is the test seam over the Bedrock Runtime client; the real
// *bedrockruntime.Client satisfies it, so tests can stub without network.
type bedrockInvoker interface {
	InvokeModel(ctx context.Context, in *bedrockruntime.InvokeModelInput, optFns ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelOutput, error)
}

// loadAWSConfig is a test seam for injecting a config with deterministic
// credential-resolution failure; production uses the standard default config.
var loadAWSConfig = func(ctx context.Context, region string) (aws.Config, error) {
	// 30s overall request timeout matches the other embedders (the AWS default
	// HTTP client bounds only dial/TLS, not the whole request), so a stalled
	// Bedrock endpoint self-recovers as ErrEmbeddingUnavailable instead of
	// hanging the single import worker forever.
	return config.LoadDefaultConfig(ctx, config.WithRegion(region),
		config.WithHTTPClient(awshttp.NewBuildableClient().WithTimeout(30*time.Second)))
}

// AWSEmbedder generates embeddings via Amazon Bedrock Runtime InvokeModel.
type AWSEmbedder struct {
	client     bedrockInvoker
	model      string
	dimensions int
}

// NewAWSEmbedder resolves config + credentials from the standard AWS chain (env,
// shared config, or assumed role — never from config) and builds the embedder.
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

// newAWSEmbedder wraps a bedrockInvoker directly so success-path tests can build
// around a stub, bypassing the credential-resolving public constructor.
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

// Embed generates an embedding, dispatching on the model family prefix. Upstream
// errors/empty results are logged server-side and returned as the tenant-safe
// ErrEmbeddingUnavailable sentinel (audit #18) — provider internals never leak.
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
		// Log full detail server-side; never surface to caller (audit #18).
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
