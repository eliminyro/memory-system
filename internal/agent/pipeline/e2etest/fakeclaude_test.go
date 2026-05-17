package e2etest_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/agent/pipeline/e2etest"
)

func TestFakeLLM_FIFOQueue(t *testing.T) {
	f := e2etest.NewFakeLLM()
	f.Queue(e2etest.FakeResponse{Out: "first"})
	f.Queue(e2etest.FakeResponse{Out: "second"})

	out, err := f.Complete(context.Background(), "m", "sys", "u")
	require.NoError(t, err)
	assert.Equal(t, "first", out)

	out, err = f.Complete(context.Background(), "m", "sys", "u")
	require.NoError(t, err)
	assert.Equal(t, "second", out)
}

func TestFakeLLM_RecordsCalls(t *testing.T) {
	f := e2etest.NewFakeLLM()
	f.Queue(e2etest.FakeResponse{Out: "ok"})
	_, _ = f.Complete(context.Background(), "haiku", "system-prompt", "user-prompt")

	calls := f.Calls()
	require.Len(t, calls, 1)
	assert.Equal(t, "haiku", calls[0].Model)
	assert.Equal(t, "system-prompt", calls[0].System)
	assert.Equal(t, "user-prompt", calls[0].User)
}

func TestFakeLLM_ReturnsError(t *testing.T) {
	f := e2etest.NewFakeLLM()
	f.Queue(e2etest.FakeResponse{Err: errors.New("rate limited")})
	_, err := f.Complete(context.Background(), "m", "s", "u")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rate limited")
}

func TestFakeLLM_MatcherSkipsNonMatching(t *testing.T) {
	f := e2etest.NewFakeLLM()
	f.Queue(e2etest.FakeResponse{
		Match: func(_, u string) bool { return strings.Contains(u, "chunk-2") },
		Out:   "chunk-2-response",
	})
	f.Queue(e2etest.FakeResponse{Out: "fallback"})

	out, err := f.Complete(context.Background(), "m", "s", "chunk-1 body")
	require.NoError(t, err)
	assert.Equal(t, "fallback", out, "non-matching matcher should be skipped")

	out, err = f.Complete(context.Background(), "m", "s", "chunk-2 body")
	require.NoError(t, err)
	assert.Equal(t, "chunk-2-response", out)
}

func TestFakeLLM_ExhaustedReturnsError(t *testing.T) {
	f := e2etest.NewFakeLLM()
	_, err := f.Complete(context.Background(), "m", "s", "u")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no FakeLLM response")
}
