package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/agent/config"
)

func TestLoad_Defaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte(""), 0600)

	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, "api", cfg.Auth)
	assert.Equal(t, "claude-haiku-4-5-20251001", cfg.Model)
	assert.Equal(t, "claude-haiku-4-5-20251001", cfg.ExtractorModel())
	assert.Equal(t, "claude-haiku-4-5-20251001", cfg.ReviewerModel())
}

func TestLoad_FullConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := `
auth: sdk
model: claude-haiku-4-5-20251001
extractor_model: claude-sonnet-4-5-20250929
reviewer_model: claude-haiku-4-5-20251001
memory_mcp_url: https://memory.example.com/mcp
memory_mcp_api_key: literal://test-key-123
`
	os.WriteFile(path, []byte(yaml), 0600)

	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, "sdk", cfg.Auth)
	assert.Equal(t, "claude-sonnet-4-5-20250929", cfg.ExtractorModel())
	assert.Equal(t, "claude-haiku-4-5-20251001", cfg.ReviewerModel())
	assert.Equal(t, "https://memory.example.com/mcp", cfg.MemoryMCPURL)
	assert.Equal(t, "literal://test-key-123", cfg.MemoryMCPAPIKey)
}
