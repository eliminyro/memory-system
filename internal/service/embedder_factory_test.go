package service

import (
	"fmt"
	"strings"
	"testing"
)

// TestNewEmbeddingProvider_TypeMapping asserts each provider name maps to the
// right concrete type and that an unknown provider is rejected. Providers whose
// construction needs ambient cloud credentials (gcp, aws) are covered by their
// own tests / the error path below rather than constructed here.
func TestNewEmbeddingProvider_TypeMapping(t *testing.T) {
	cfg := EmbeddingConfig{
		Dimensions:    8,
		OllamaURL:     "http://localhost:11434",
		OllamaModel:   "nomic-embed-text",
		OpenAIBaseURL: "https://api.openai.com/v1",
		OpenAIModel:   "text-embedding-3-small",
	}

	cases := []struct {
		provider string
		wantType string
	}{
		{"ollama", "*service.OllamaEmbedder"},
		{"openai", "*service.OpenAIEmbedder"},
		{"fake", "*service.FakeEmbedder"},
	}
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			p, err := NewEmbeddingProvider(tc.provider, cfg)
			if err != nil {
				t.Fatalf("construct %s: %v", tc.provider, err)
			}
			if got := fmt.Sprintf("%T", p); got != tc.wantType {
				t.Fatalf("%s -> %s, want %s", tc.provider, got, tc.wantType)
			}
		})
	}
}

func TestNewEmbeddingProvider_UnknownRejected(t *testing.T) {
	_, err := NewEmbeddingProvider("banana", EmbeddingConfig{})
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
	if !strings.Contains(err.Error(), "banana") {
		t.Fatalf("error should name the unknown provider, got %v", err)
	}
}

func TestNewEmbeddingProvider_AWSRequiresRegionAndModel(t *testing.T) {
	// The aws provider fails fast at construction when region/model are missing,
	// so a misconfigured deploy never reaches the first embed.
	if _, err := NewEmbeddingProvider("aws", EmbeddingConfig{}); err == nil {
		t.Fatal("expected error constructing aws provider without region/model")
	}
}
