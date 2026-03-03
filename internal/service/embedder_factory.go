package service

import "fmt"

// NewEmbeddingProvider creates an EmbeddingProvider based on the provider name.
func NewEmbeddingProvider(provider string, cfg EmbeddingConfig) (EmbeddingProvider, error) {
	switch provider {
	case "ollama":
		return NewOllamaEmbedder(cfg.OllamaURL, cfg.OllamaModel, cfg.Dimensions), nil
	case "gcp":
		return NewGCPEmbedder(cfg.GCPProject, cfg.GCPLocation, cfg.GCPModel, cfg.Dimensions)
	default:
		return nil, fmt.Errorf("unknown embedding provider: %s", provider)
	}
}

// EmbeddingConfig holds all provider-agnostic and provider-specific config.
type EmbeddingConfig struct {
	Dimensions int

	// Ollama
	OllamaURL   string
	OllamaModel string

	// GCP
	GCPProject  string
	GCPLocation string
	GCPModel    string
}
