package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Auth            string `yaml:"auth"`
	APIKey          string `yaml:"api_key"`
	Model           string `yaml:"model"`
	ExtractorModelV string `yaml:"extractor_model"`
	ReviewerModelV  string `yaml:"reviewer_model"`
	MemoryMCPURL    string `yaml:"memory_mcp_url"`
	MemoryMCPAPIKey string `yaml:"memory_mcp_api_key"`
}

func (c *Config) ExtractorModel() string {
	if c.ExtractorModelV != "" {
		return c.ExtractorModelV
	}
	return c.Model
}

func (c *Config) ReviewerModel() string {
	if c.ReviewerModelV != "" {
		return c.ReviewerModelV
	}
	return c.Model
}

func Load(path string) (*Config, error) {
	cfg := &Config{
		Auth:         "api",
		Model:        "claude-haiku-4-5-20251001",
		MemoryMCPURL: "https://memory-mcp.a11s.dev/mcp",
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if cfg.Model == "" {
		cfg.Model = "claude-haiku-4-5-20251001"
	}
	if cfg.Auth == "" {
		cfg.Auth = "api"
	}

	return cfg, nil
}
