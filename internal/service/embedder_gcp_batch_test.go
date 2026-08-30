package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// gcpStubRT records instance counts per :predict call and returns predictions
// that echo the instance index (encoded in "v<N>") so order can be verified.
type gcpStubRT struct {
	sizes    []int
	statuses []int // scripted status per call; 0/absent means 200
	calls    int
}

func (rt *gcpStubRT) RoundTrip(req *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(req.Body)
	var parsed gcpEmbedRequest
	_ = json.Unmarshal(body, &parsed)

	idx := rt.calls
	rt.calls++
	rt.sizes = append(rt.sizes, len(parsed.Instances))

	status := http.StatusOK
	if idx < len(rt.statuses) && rt.statuses[idx] != 0 {
		status = rt.statuses[idx]
	}
	if status != http.StatusOK {
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader(`{"error":"boom"}`)),
			Header:     make(http.Header),
		}, nil
	}

	preds := make([]gcpPrediction, len(parsed.Instances))
	for i, inst := range parsed.Instances {
		n, _ := strconv.Atoi(strings.TrimPrefix(inst.Content, "v"))
		preds[i] = gcpPrediction{Embeddings: gcpEmbeddings{Values: []float32{float32(n)}}}
	}
	respBody, _ := json.Marshal(gcpEmbedResponse{Predictions: preds})
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(respBody)),
		Header:     make(http.Header),
	}, nil
}

func newStubGCP(rt http.RoundTripper) *GCPEmbedder {
	return &GCPEmbedder{
		project:    "p",
		location:   "us-central1",
		model:      "m",
		dimensions: 1,
		client:     &http.Client{Transport: rt},
	}
}

func TestGCPEmbedBatchChunkingAndOrder(t *testing.T) {
	const n = 600
	texts := make([]string, n)
	for i := range texts {
		texts[i] = fmt.Sprintf("v%d", i)
	}
	rt := &gcpStubRT{}
	e := newStubGCP(rt)

	vecs, err := e.EmbedBatch(context.Background(), texts)
	require.NoError(t, err)
	require.Len(t, vecs, n)
	require.Equal(t, []int{gcpMaxBatch, gcpMaxBatch, n - 2*gcpMaxBatch}, rt.sizes)
	for i, v := range vecs {
		require.Equal(t, float32(i), v.Slice()[0])
	}
}

func TestGCPEmbedBatchEmpty(t *testing.T) {
	rt := &gcpStubRT{}
	e := newStubGCP(rt)

	vecs, err := e.EmbedBatch(context.Background(), nil)
	require.NoError(t, err)
	require.Empty(t, vecs)
	require.Equal(t, 0, rt.calls)
}

func TestGCPPredictRetriesOn429(t *testing.T) {
	rt := &gcpStubRT{statuses: []int{http.StatusTooManyRequests, http.StatusOK}}
	e := newStubGCP(rt)

	v, err := e.Embed(context.Background(), "v7")
	require.NoError(t, err)
	require.Equal(t, float32(7), v.Slice()[0])
	require.Equal(t, 2, rt.calls)
}

func TestGCPPredictNoRetryOn400(t *testing.T) {
	rt := &gcpStubRT{statuses: []int{http.StatusBadRequest}}
	e := newStubGCP(rt)

	_, err := e.Embed(context.Background(), "v1")
	require.ErrorIs(t, err, ErrEmbeddingUnavailable)
	require.Equal(t, 1, rt.calls)
}
