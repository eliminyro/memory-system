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

// ErrEmbeddingUnavailable is the generic, tenant-safe error returned when an
// upstream embedding provider responds with a non-2xx status. The full upstream
// status and body are logged server-side (audit #18) and never propagated to
// callers, which would otherwise leak provider internals into MCP responses.
var ErrEmbeddingUnavailable = errors.New("embedding provider unavailable")
