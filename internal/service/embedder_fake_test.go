package service_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/service"
)

func TestFakeEmbedder_Deterministic(t *testing.T) {
	e := service.NewFakeEmbedder(768)
	v1, err := e.Embed(context.Background(), "hello world")
	require.NoError(t, err)
	v2, err := e.Embed(context.Background(), "hello world")
	require.NoError(t, err)
	assert.Equal(t, v1.Slice(), v2.Slice(), "same input must produce same vector")
}

func TestFakeEmbedder_DifferentInputsDifferentVectors(t *testing.T) {
	e := service.NewFakeEmbedder(768)
	v1, err := e.Embed(context.Background(), "hello")
	require.NoError(t, err)
	v2, err := e.Embed(context.Background(), "world")
	require.NoError(t, err)
	assert.NotEqual(t, v1.Slice(), v2.Slice())
}

func TestFakeEmbedder_RespectsDimensions(t *testing.T) {
	e := service.NewFakeEmbedder(256)
	v, err := e.Embed(context.Background(), "x")
	require.NoError(t, err)
	assert.Equal(t, 256, len(v.Slice()))
	assert.Equal(t, 256, e.Dimensions())
}
