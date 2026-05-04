package secret_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/agent/secret"
)

func TestResolve_Literal(t *testing.T) {
	val, err := secret.Resolve(context.Background(), "literal://my-secret-value")
	require.NoError(t, err)
	assert.Equal(t, "my-secret-value", val)
}

func TestResolve_Env(t *testing.T) {
	t.Setenv("TEST_SECRET_KEY", "env-secret-value")

	val, err := secret.Resolve(context.Background(), "env://TEST_SECRET_KEY")
	require.NoError(t, err)
	assert.Equal(t, "env-secret-value", val)
}

func TestResolve_Env_NotSet(t *testing.T) {
	_, err := secret.Resolve(context.Background(), "env://NONEXISTENT_VAR_12345")
	assert.Error(t, err)
}

func TestResolve_File(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret")
	require.NoError(t, os.WriteFile(path, []byte("file-secret-value\n"), 0600))

	val, err := secret.Resolve(context.Background(), "file://"+path)
	require.NoError(t, err)
	assert.Equal(t, "file-secret-value", val)
}

func TestResolve_UnknownScheme(t *testing.T) {
	// No scheme — treated as literal for backwards compat
	val, err := secret.Resolve(context.Background(), "plain-api-key")
	require.NoError(t, err)
	assert.Equal(t, "plain-api-key", val)
}
