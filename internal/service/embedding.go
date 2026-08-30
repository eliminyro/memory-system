package service

import (
	"context"
	"errors"

	"github.com/pgvector/pgvector-go"
)

// EmbeddingProvider generates vector embeddings from text.
type EmbeddingProvider interface {
	Embed(ctx context.Context, text string) (pgvector.Vector, error)
	Dimensions() int
}

// BatchEmbedder is an optional capability: providers that can embed many texts
// in one upstream call implement it; callers fall back to looping Embed otherwise.
type BatchEmbedder interface {
	EmbedBatch(ctx context.Context, texts []string) ([]pgvector.Vector, error)
}

// ErrEmbeddingUnavailable is the tenant-safe error for a non-2xx upstream embedding
// response. Full status/body is logged server-side (audit #18), never propagated
// to callers (which would leak provider internals into MCP responses).
var ErrEmbeddingUnavailable = errors.New("embedding provider unavailable")
