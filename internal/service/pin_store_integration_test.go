//go:build integration

package service_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/models"
)

func pinFlag(b bool) *bool { return &b }

// lastAccessedOf loads a document's persisted last_accessed_at.
func lastAccessedOf(t *testing.T, f *authzFixture, id uuid.UUID) *time.Time {
	t.Helper()
	var doc models.Document
	require.NoError(t, f.db.Where("id = ?", id).First(&doc).Error)
	return doc.LastAccessedAt
}

// TestStoreDocument_UpsertRefreshesLastAccessed is the C1 regression: re-storing
// (updating) an old, never-served doc must refresh last_accessed_at to now — a
// write is a usage signal — so access-retention never evicts a doc that was just
// updated. Before the fix, Save nulled last_accessed_at and COALESCE fell back to
// the preserved (old) created_at, making an updated doc look access-cold.
func TestStoreDocument_UpsertRefreshesLastAccessed(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	slug := "acc-" + uuid.NewString()
	body := "# T\n\n## H\nbody text"

	res, err := f.svc.StoreDocument(ctx, "learnings", nil, slug, body, true, "seed", nil, nil)
	require.NoError(t, err)
	id := res.Document.ID

	// Simulate an old, never-served doc: backdate created_at, null last_accessed_at.
	old := time.Now().Add(-400 * 24 * time.Hour)
	require.NoError(t, f.db.Model(&models.Document{}).Where("id = ?", id).
		Updates(map[string]any{"created_at": old, "last_accessed_at": nil}).Error)

	// Re-store the same slug (upsert) — a write.
	_, err = f.svc.StoreDocument(ctx, "learnings", nil, slug, body, true, "seed", nil, nil)
	require.NoError(t, err)

	la := lastAccessedOf(t, f, id)
	require.NotNil(t, la, "re-store must set last_accessed_at, not leave it NULL")
	require.WithinDuration(t, time.Now(), *la, time.Minute,
		"re-store must refresh last_accessed_at to now, so an updated doc is not access-cold")
}

// pinnedOf loads a document's persisted pinned flag.
func pinnedOf(t *testing.T, f *authzFixture, id uuid.UUID) bool {
	t.Helper()
	var doc models.Document
	require.NoError(t, f.db.Where("id = ?", id).First(&doc).Error)
	return doc.Pinned
}

// TestStoreDocument_Pin (5.1) proves store_memory's pin threads to
// documents.pinned: create defaults false, pin=true sets it, and on re-store an
// omitted pin preserves the current value while an explicit false clears it.
func TestStoreDocument_Pin(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	slug := "pin-" + uuid.NewString()
	body := "# T\n\n## H\npin body text"

	// Create without pin -> default false.
	res, err := f.svc.StoreDocument(ctx, "learnings", nil, slug, body, true, "seed", nil, nil)
	require.NoError(t, err)
	require.False(t, pinnedOf(t, f, res.Document.ID), "create without pin must default to unpinned")

	// Re-store with pin=true -> pinned.
	res, err = f.svc.StoreDocument(ctx, "learnings", nil, slug, body, true, "seed", nil, pinFlag(true))
	require.NoError(t, err)
	require.True(t, pinnedOf(t, f, res.Document.ID), "pin=true must set documents.pinned")

	// Re-store with pin omitted -> pin preserved (still true).
	res, err = f.svc.StoreDocument(ctx, "learnings", nil, slug, body, true, "seed", nil, nil)
	require.NoError(t, err)
	require.True(t, pinnedOf(t, f, res.Document.ID), "omitted pin on re-store must preserve the current pin")

	// Re-store with pin=false -> cleared.
	res, err = f.svc.StoreDocument(ctx, "learnings", nil, slug, body, true, "seed", nil, pinFlag(false))
	require.NoError(t, err)
	require.False(t, pinnedOf(t, f, res.Document.ID), "pin=false must clear documents.pinned")

	// Create with pin=true -> pinned from the start.
	res, err = f.svc.StoreDocument(ctx, "learnings", nil, "pin2-"+uuid.NewString(), body, true, "seed", nil, pinFlag(true))
	require.NoError(t, err)
	require.True(t, pinnedOf(t, f, res.Document.ID), "create with pin=true must set documents.pinned")
}
