package service

import (
	"context"

	"github.com/pgvector/pgvector-go"
)

// EmbeddingProvider generates vector embeddings from text.
type EmbeddingProvider interface {
	Embed(ctx context.Context, text string) (pgvector.Vector, error)
	Dimensions() int
}
