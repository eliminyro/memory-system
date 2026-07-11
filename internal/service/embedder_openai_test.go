package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// canned OpenAI-compatible success body.
func openAIResponse(embedding []float32) string {
	b, _ := json.Marshal(openAIEmbedResponse{
		Data: []struct {
			Embedding []float32 `json:"embedding"`
		}{{Embedding: embedding}},
	})
	return string(b)
}

func TestOpenAIEmbedder_Success(t *testing.T) {
	want := []float32{0.1, 0.2, 0.3}
	var gotAuth, gotPath string
	var gotReq openAIEmbedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotReq)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, openAIResponse(want))
	}))
	defer srv.Close()

	e := NewOpenAIEmbedder(srv.URL, "sk-test", "text-embedding-3-small", 3)
	vec, err := e.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	got := vec.Slice()
	if len(got) != len(want) {
		t.Fatalf("dim = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("vec[%d] = %v, want %v", i, got[i], want[i])
		}
	}
	if e.Dimensions() != 3 {
		t.Fatalf("Dimensions() = %d, want 3", e.Dimensions())
	}
	// baseURL trailing-slash tolerance + correct endpoint.
	if gotPath != "/embeddings" {
		t.Fatalf("path = %q, want /embeddings", gotPath)
	}
	// API key present -> Bearer header.
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("Authorization = %q, want Bearer sk-test", gotAuth)
	}
	// dimensions override is sent when configured.
	if gotReq.Dimensions != 3 {
		t.Fatalf("request dimensions = %d, want 3", gotReq.Dimensions)
	}
	if gotReq.Model != "text-embedding-3-small" || gotReq.Input != "hello" {
		t.Fatalf("request model/input = %q/%q", gotReq.Model, gotReq.Input)
	}
}

func TestOpenAIEmbedder_NoAuthHeaderWhenKeyEmpty(t *testing.T) {
	var gotAuth string
	var gotReq openAIEmbedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotReq)
		_, _ = io.WriteString(w, openAIResponse([]float32{1}))
	}))
	defer srv.Close()

	// Trailing slash on base URL is tolerated. No key, no dimension override.
	e := NewOpenAIEmbedder(srv.URL+"/", "", "nomic", 0)
	if _, err := e.Embed(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "" {
		t.Fatalf("Authorization must be absent when key empty, got %q", gotAuth)
	}
	// dimensions omitted (omitempty) when 0.
	if gotReq.Dimensions != 0 {
		t.Fatalf("dimensions should be omitted when 0, got %d", gotReq.Dimensions)
	}
}

func TestOpenAIEmbedder_Non2xxReturnsSentinel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"boom internal detail"}`)
	}))
	defer srv.Close()

	e := NewOpenAIEmbedder(srv.URL, "", "m", 0)
	_, err := e.Embed(context.Background(), "x")
	// Audit #18: upstream body is NOT surfaced to the caller; a generic
	// sentinel is returned (and the detail is logged server-side).
	if !errors.Is(err, ErrEmbeddingUnavailable) {
		t.Fatalf("want ErrEmbeddingUnavailable, got %v", err)
	}
}

func TestOpenAIEmbedder_EmptyDataErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	defer srv.Close()

	e := NewOpenAIEmbedder(srv.URL, "", "m", 0)
	if _, err := e.Embed(context.Background(), "x"); err == nil {
		t.Fatal("expected error on empty data")
	}
}
