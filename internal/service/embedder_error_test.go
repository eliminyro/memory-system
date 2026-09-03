package service_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/service"
)

// TestOllamaEmbedder_UpstreamErrorNotLeaked guards audit #18: a non-2xx upstream
// body must NOT leak into the returned error — caller sees only the sentinel.
func TestOllamaEmbedder_UpstreamErrorNotLeaked(t *testing.T) {
	const secret = "SENSITIVE-UPSTREAM-DETAIL-do-not-leak"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"` + secret + `"}`))
	}))
	defer srv.Close()

	e := service.NewOllamaEmbedder(srv.URL, "nomic-embed-text", 768)
	_, err := e.Embed(context.Background(), "hello")
	require.Error(t, err)
	require.ErrorIs(t, err, service.ErrEmbeddingUnavailable)
	require.NotContains(t, err.Error(), secret, "upstream body must not leak into the returned error")
}
