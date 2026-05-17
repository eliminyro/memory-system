//go:build integration

package pipeline_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/agent/pipeline"
	"github.com/eliminyro/memory-system/internal/agent/pipeline/e2etest"
)

// --- Group A: happy paths ---

func TestE2E_AcceptVerdict_CreatesDocument(t *testing.T) {
	t.Parallel()
	h := e2etest.New(t)
	ctx := context.Background()

	h.LLM.Queue(e2etest.FakeResponse{Out: e2etest.ExtractorJSON(
		e2etest.Candidate("learnings/go/test-fact", "Test Fact", "deterministic content", "new"),
	)})
	h.LLM.Queue(e2etest.FakeResponse{Out: e2etest.ReviewerJSON(
		e2etest.Accept("new, not in KB"),
	)})

	candidates, err := pipeline.Extract(ctx, h.LLM, h.Model, e2etest.SessionWithText("anything"))
	require.NoError(t, err)
	require.Len(t, candidates, 1)

	reviewed, err := pipeline.Review(ctx, h.LLM, h.Model, h.MCPClient, candidates)
	require.NoError(t, err)
	require.Len(t, reviewed, 1)

	acc, mer, rej, err := pipeline.Write(ctx, h.MCPClient, reviewed)
	require.NoError(t, err)
	assert.Equal(t, 1, acc)
	assert.Equal(t, 0, mer)
	assert.Equal(t, 0, rej)
	assert.Equal(t, int64(1), h.CountDocuments())
}

func TestE2E_MergeVerdict_UpdatesSection(t *testing.T) {
	t.Parallel()
	h := e2etest.New(t)
	ctx := context.Background()

	doc := h.SeedDocument("learnings", "go", "existing", "Existing Doc")
	sec := h.SeedSection(doc, "Heading", "original content")

	h.LLM.Queue(e2etest.FakeResponse{Out: e2etest.ExtractorJSON(
		e2etest.Candidate("learnings/go/existing", "Heading", "updated content", "update"),
	)})
	h.LLM.Queue(e2etest.FakeResponse{Out: e2etest.ReviewerJSON(
		e2etest.Merge("overlaps with existing section", sec.ID.String()),
	)})

	candidates, err := pipeline.Extract(ctx, h.LLM, h.Model, e2etest.SessionWithText("anything"))
	require.NoError(t, err)
	reviewed, err := pipeline.Review(ctx, h.LLM, h.Model, h.MCPClient, candidates)
	require.NoError(t, err)
	acc, mer, rej, err := pipeline.Write(ctx, h.MCPClient, reviewed)
	require.NoError(t, err)

	assert.Equal(t, 0, acc)
	assert.Equal(t, 1, mer)
	assert.Equal(t, 0, rej)
	assert.Equal(t, int64(1), h.CountDocuments())
	updated, err := h.GetSection(sec.ID)
	require.NoError(t, err)
	assert.Contains(t, updated.Content, "updated content")
}

func TestE2E_RejectVerdict_NoDBChange(t *testing.T) {
	t.Parallel()
	h := e2etest.New(t)
	ctx := context.Background()

	h.LLM.Queue(e2etest.FakeResponse{Out: e2etest.ExtractorJSON(
		e2etest.Candidate("learnings/go/ephemeral", "X", "session-specific noise", "new"),
	)})
	h.LLM.Queue(e2etest.FakeResponse{Out: e2etest.ReviewerJSON(
		e2etest.Reject("too session-specific"),
	)})

	candidates, _ := pipeline.Extract(ctx, h.LLM, h.Model, e2etest.SessionWithText("x"))
	reviewed, _ := pipeline.Review(ctx, h.LLM, h.Model, h.MCPClient, candidates)
	acc, mer, rej, err := pipeline.Write(ctx, h.MCPClient, reviewed)
	require.NoError(t, err)

	assert.Equal(t, 0, acc)
	assert.Equal(t, 0, mer)
	assert.Equal(t, 1, rej)
	assert.Equal(t, int64(0), h.CountDocuments())
}

func TestE2E_MixedVerdicts_CountsCorrect(t *testing.T) {
	t.Parallel()
	h := e2etest.New(t)
	ctx := context.Background()

	doc := h.SeedDocument("learnings", "go", "existing", "Existing")
	sec := h.SeedSection(doc, "H", "original")

	h.LLM.Queue(e2etest.FakeResponse{Out: e2etest.ExtractorJSON(
		e2etest.Candidate("learnings/go/new-fact", "A", "A content", "new"),
		e2etest.Candidate("learnings/go/existing", "H", "merged content", "update"),
		e2etest.Candidate("learnings/go/junk", "C", "C content", "new"),
	)})
	h.LLM.Queue(e2etest.FakeResponse{Out: e2etest.ReviewerJSON(
		e2etest.Accept(""),
		e2etest.Merge("", sec.ID.String()),
		e2etest.Reject(""),
	)})

	candidates, _ := pipeline.Extract(ctx, h.LLM, h.Model, e2etest.SessionWithText("x"))
	reviewed, _ := pipeline.Review(ctx, h.LLM, h.Model, h.MCPClient, candidates)
	acc, mer, rej, err := pipeline.Write(ctx, h.MCPClient, reviewed)
	require.NoError(t, err)

	assert.Equal(t, 1, acc)
	assert.Equal(t, 1, mer)
	assert.Equal(t, 1, rej)
	// 1 pre-existing doc + 1 newly accepted = 2.
	assert.Equal(t, int64(2), h.CountDocuments())
}

// --- Group B: empty / no-op paths ---

func TestE2E_EmptySession_NoLLMCalls(t *testing.T) {
	t.Parallel()
	h := e2etest.New(t)
	ctx := context.Background()

	candidates, err := pipeline.Extract(ctx, h.LLM, h.Model, e2etest.EmptySession())
	require.NoError(t, err)
	assert.Empty(t, candidates)
	assert.Empty(t, h.LLM.Calls(), "extractor must short-circuit on empty session")
	assert.Equal(t, int64(0), h.CountDocuments())
}

func TestE2E_ZeroCandidates_SkipsReviewer(t *testing.T) {
	t.Parallel()
	h := e2etest.New(t)
	ctx := context.Background()

	h.LLM.Queue(e2etest.FakeResponse{Out: e2etest.ExtractorJSON()}) // empty []

	candidates, err := pipeline.Extract(ctx, h.LLM, h.Model, e2etest.SessionWithText("body"))
	require.NoError(t, err)
	assert.Empty(t, candidates)

	reviewed, err := pipeline.Review(ctx, h.LLM, h.Model, h.MCPClient, candidates)
	require.NoError(t, err)
	assert.Empty(t, reviewed)

	assert.Len(t, h.LLM.Calls(), 1, "reviewer must not be called when candidates is empty")
}

// --- Group C: writer guards ---

func TestE2E_MalformedPath_2Parts_Rejected(t *testing.T) {
	t.Parallel()
	h := e2etest.New(t)
	ctx := context.Background()

	h.LLM.Queue(e2etest.FakeResponse{Out: e2etest.ExtractorJSON(
		e2etest.Candidate("learnings/go", "X", "content", "new"),
	)})
	h.LLM.Queue(e2etest.FakeResponse{Out: e2etest.ReviewerJSON(
		e2etest.Accept(""),
	)})

	candidates, _ := pipeline.Extract(ctx, h.LLM, h.Model, e2etest.SessionWithText("x"))
	reviewed, _ := pipeline.Review(ctx, h.LLM, h.Model, h.MCPClient, candidates)
	acc, mer, rej, err := pipeline.Write(ctx, h.MCPClient, reviewed)
	require.NoError(t, err)

	assert.Equal(t, 0, acc)
	assert.Equal(t, 0, mer)
	assert.Equal(t, 1, rej, "2-part path must be rejected by writer guard")
	assert.Equal(t, int64(0), h.CountDocuments())
}

func TestE2E_MalformedPath_4Parts_Rejected(t *testing.T) {
	t.Parallel()
	h := e2etest.New(t)
	ctx := context.Background()

	h.LLM.Queue(e2etest.FakeResponse{Out: e2etest.ExtractorJSON(
		e2etest.Candidate("a/b/c/d", "X", "content", "new"),
	)})
	h.LLM.Queue(e2etest.FakeResponse{Out: e2etest.ReviewerJSON(
		e2etest.Accept(""),
	)})

	candidates, _ := pipeline.Extract(ctx, h.LLM, h.Model, e2etest.SessionWithText("x"))
	reviewed, _ := pipeline.Review(ctx, h.LLM, h.Model, h.MCPClient, candidates)
	acc, mer, rej, err := pipeline.Write(ctx, h.MCPClient, reviewed)
	require.NoError(t, err)

	assert.Equal(t, 0, acc)
	assert.Equal(t, 0, mer)
	assert.Equal(t, 1, rej)
	assert.Equal(t, int64(0), h.CountDocuments())
}

func TestE2E_MergeWithoutTarget_FallsBackToStore(t *testing.T) {
	t.Parallel()
	h := e2etest.New(t)
	ctx := context.Background()

	h.LLM.Queue(e2etest.FakeResponse{Out: e2etest.ExtractorJSON(
		e2etest.Candidate("learnings/go/orphan-merge", "H", "content", "update"),
	)})
	h.LLM.Queue(e2etest.FakeResponse{Out: e2etest.ReviewerJSON(
		// merge without merge_target — writer falls back to store.
		e2etest.Merge("", ""),
	)})

	candidates, _ := pipeline.Extract(ctx, h.LLM, h.Model, e2etest.SessionWithText("x"))
	reviewed, _ := pipeline.Review(ctx, h.LLM, h.Model, h.MCPClient, candidates)
	acc, mer, rej, err := pipeline.Write(ctx, h.MCPClient, reviewed)
	require.NoError(t, err)

	assert.Equal(t, 1, acc, "merge-without-target falls back to StoreMemory")
	assert.Equal(t, 0, mer)
	assert.Equal(t, 0, rej)
	assert.Equal(t, int64(1), h.CountDocuments())
}

// --- Group D: multi-chunk + error paths ---

func TestE2E_LongSession_ChunkedExtractDedupes(t *testing.T) {
	t.Parallel()
	h := e2etest.New(t)
	ctx := context.Background()

	// Same candidate emitted for every chunk; deduplicateCandidates should collapse to 1.
	dupJSON := e2etest.ExtractorJSON(
		e2etest.Candidate("learnings/go/dup", "H", "the same content", "new"),
	)
	h.LLM.Queue(e2etest.FakeResponse{Out: dupJSON}) // chunk 1
	h.LLM.Queue(e2etest.FakeResponse{Out: dupJSON}) // chunk 2
	h.LLM.Queue(e2etest.FakeResponse{Out: e2etest.ReviewerJSON(
		e2etest.Accept(""),
	)})

	candidates, err := pipeline.Extract(ctx, h.LLM, h.Model, e2etest.LongSession("chunk-2-marker"))
	require.NoError(t, err)
	require.Len(t, candidates, 1, "dedup must collapse identical candidates across chunks")

	reviewed, err := pipeline.Review(ctx, h.LLM, h.Model, h.MCPClient, candidates)
	require.NoError(t, err)

	acc, _, _, err := pipeline.Write(ctx, h.MCPClient, reviewed)
	require.NoError(t, err)
	assert.Equal(t, 1, acc)
	assert.Equal(t, int64(1), h.CountDocuments())
}

func TestE2E_ExtractorInvalidJSON_Errors(t *testing.T) {
	t.Parallel()
	h := e2etest.New(t)
	ctx := context.Background()

	h.LLM.Queue(e2etest.FakeResponse{Out: "this is not JSON, just garbage"})

	candidates, err := pipeline.Extract(ctx, h.LLM, h.Model, e2etest.SessionWithText("body"))
	require.Error(t, err, "extractor must surface JSON parse failure")
	assert.Nil(t, candidates)

	// Reviewer must never have been called.
	calls := h.LLM.Calls()
	assert.Len(t, calls, 1)
	assert.Equal(t, int64(0), h.CountDocuments())
}

func TestE2E_MCPStoreError_PropagatesInWriter(t *testing.T) {
	t.Parallel()
	h := e2etest.New(t)
	ctx := context.Background()

	h.LLM.Queue(e2etest.FakeResponse{Out: e2etest.ExtractorJSON(
		e2etest.Candidate("learnings/go/will-fail", "H", "content", "new"),
	)})
	h.LLM.Queue(e2etest.FakeResponse{Out: e2etest.ReviewerJSON(
		e2etest.Accept(""),
	)})

	candidates, _ := pipeline.Extract(ctx, h.LLM, h.Model, e2etest.SessionWithText("x"))
	reviewed, _ := pipeline.Review(ctx, h.LLM, h.Model, h.MCPClient, candidates)

	// Revoke the API key so the next StoreMemory call returns 401.
	h.RevokeAPIKey()

	acc, _, _, err := pipeline.Write(ctx, h.MCPClient, reviewed)
	require.Error(t, err, "MCP server-side error must propagate")
	assert.Equal(t, 0, acc)
	assert.Equal(t, int64(0), h.CountDocuments())
}
