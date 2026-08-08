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

// ErrEmbeddingUnavailable is the tenant-safe error for a non-2xx upstream embedding
// response. Full status/body is logged server-side (audit #18), never propagated
// to callers (which would leak provider internals into MCP responses).
var ErrEmbeddingUnavailable = errors.New("embedding provider unavailable")
