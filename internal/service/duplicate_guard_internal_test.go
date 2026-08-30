package service

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHashContent(t *testing.T) {
	got := hashContent("hello world")
	sum := sha256.Sum256([]byte("hello world"))
	require.Equal(t, hex.EncodeToString(sum[:]), got)
	require.Len(t, got, 64)
	assert.Equal(t, got, hashContent("hello world"), "deterministic")
	assert.NotEqual(t, got, hashContent("hello worlds"), "different input differs")
}

func TestCentroid(t *testing.T) {
	c := centroid([]pgvector.Vector{
		pgvector.NewVector([]float32{1, 2, 3}),
		pgvector.NewVector([]float32{3, 4, 5}),
	})
	assert.Equal(t, []float32{2, 3, 4}, c.Slice(), "component-wise mean")

	single := centroid([]pgvector.Vector{pgvector.NewVector([]float32{7, -1})})
	assert.Equal(t, []float32{7, -1}, single.Slice())

	assert.Empty(t, centroid(nil).Slice(), "empty input yields zero-length vector")
}
