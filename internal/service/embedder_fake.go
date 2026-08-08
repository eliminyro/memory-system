package service

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"math"

	"github.com/pgvector/pgvector-go"
)

// FakeEmbedder produces deterministic sha256-derived vectors: reproducible but
// semantically meaningless (tests needing similarity must seed by id, not ranking).
// Wired via EMBEDDING_PROVIDER=fake or injected by integration tests; never in production.
type FakeEmbedder struct {
	dim int
}

func NewFakeEmbedder(dim int) *FakeEmbedder {
	return &FakeEmbedder{dim: dim}
}

func (e *FakeEmbedder) Dimensions() int { return e.dim }

func (e *FakeEmbedder) Embed(_ context.Context, text string) (pgvector.Vector, error) {
	sum := sha256.Sum256([]byte(text))
	v := make([]float32, e.dim)
	// Walk the 32-byte digest as a circular byte stream of 4-byte uint32 windows.
	// Each window becomes one float in [-0.5, 0.5).
	buf := make([]byte, 4)
	for i := 0; i < e.dim; i++ {
		off := (i * 4) % len(sum)
		for j := 0; j < 4; j++ {
			buf[j] = sum[(off+j)%len(sum)]
		}
		u := binary.LittleEndian.Uint32(buf)
		v[i] = (float32(u) / float32(math.MaxUint32)) - 0.5
	}
	return pgvector.NewVector(v), nil
}
