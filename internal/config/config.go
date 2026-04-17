package config

import (
	"fmt"

	"github.com/caarlos0/env/v10"

	"github.com/eliminyro/memory-system/internal/service"
)

type Config struct {
	DatabaseURL string `env:"DATABASE_URL" envDefault:"postgres://memory:memory@localhost:5432/memory?sslmode=disable"`
	ServerAddr  string `env:"SERVER_ADDR" envDefault:":8080"`
	LogLevel    string `env:"LOG_LEVEL" envDefault:"info"`

	// Embedding provider: "ollama" or "gcp"
	EmbeddingProvider   string `env:"EMBEDDING_PROVIDER" envDefault:"ollama"`
	EmbeddingDimensions int    `env:"EMBEDDING_DIMENSIONS" envDefault:"1024"`

	// Ollama
	OllamaURL   string `env:"OLLAMA_URL" envDefault:"http://localhost:11434"`
	OllamaModel string `env:"OLLAMA_MODEL" envDefault:"nomic-embed-text"`

	// GCP Vertex AI
	GCPProject  string `env:"GCP_PROJECT"`
	GCPLocation string `env:"GCP_LOCATION" envDefault:"us-central1"`
	GCPModel    string `env:"GCP_EMBEDDING_MODEL" envDefault:"text-embedding-005"`

	// Admin
	AdminAllowedEmails string `env:"ADMIN_ALLOWED_EMAILS"`

	// Cleanup pipeline — nightly lint scan populates cleanup_queue with
	// near-duplicate candidates. When TELEGRAM_* are set, a summary is posted
	// after each scan. All cleanup knobs are optional: leaving them empty
	// disables the respective feature gracefully.
	CleanupIntervalHours int    `env:"CLEANUP_INTERVAL_HOURS" envDefault:"24"`
	CleanupEnabled       bool   `env:"CLEANUP_ENABLED" envDefault:"true"`
	TelegramBotToken     string `env:"TELEGRAM_BOT_TOKEN"`
	TelegramChatID       string `env:"TELEGRAM_CHAT_ID"`
}

func Load() (*Config, error) {
	cfg := &Config{}
	if err := env.Parse(cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return cfg, nil
}

// EmbeddingCfg converts config fields into a service.EmbeddingConfig.
func (c *Config) EmbeddingCfg() service.EmbeddingConfig {
	return service.EmbeddingConfig{
		Dimensions:  c.EmbeddingDimensions,
		OllamaURL:   c.OllamaURL,
		OllamaModel: c.OllamaModel,
		GCPProject:  c.GCPProject,
		GCPLocation: c.GCPLocation,
		GCPModel:    c.GCPModel,
	}
}
