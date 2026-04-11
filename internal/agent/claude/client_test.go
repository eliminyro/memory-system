package claude_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/eliminyro/memory-system/internal/agent/claude"
)

func TestNewClient_APIKey(t *testing.T) {
	c, err := claude.NewClient("api", "sk-test-key-123")
	assert.NoError(t, err)
	assert.NotNil(t, c)
}

func TestNewClient_SDK(t *testing.T) {
	c, err := claude.NewClient("sdk", "")
	assert.NoError(t, err)
	assert.NotNil(t, c)
}

func TestNewClient_InvalidAuth(t *testing.T) {
	_, err := claude.NewClient("invalid", "")
	assert.Error(t, err)
}
