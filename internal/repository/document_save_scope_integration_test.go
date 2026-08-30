//go:build integration

package repository_test

import (
	"context"
	"math/rand"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
)

// TestDocumentSave_TenantScoped guards B13: DocumentRepository.Save is a PK-keyed
// UPDATE with no tenant predicate, so a caller presenting another tenant's doc id
// could overwrite it. Save now verifies the row exists under the write tenant and
// returns ErrNotFound (without mutating the row) for a cross-tenant id.
func TestDocumentSave_TenantScoped(t *testing.T) {
	db := openLintPG(t)
	rng := rand.New(rand.NewSource(13))
	owner := seedTenant(t, db)
	attacker := seedTenant(t, db)
	t.Cleanup(func() {
		cleanupTenant(db, owner)
		cleanupTenant(db, attacker)
	})

	docID := seedDoc(t, db, owner, "b13-"+uuid.NewString(), randUnit(rng))
	docs := repository.NewDocumentRepository(db)
	ctx := context.Background()

	// Attacker presents the owner's doc id as its own and tries to overwrite it.
	evil := &models.Document{
		ID: docID, TenantID: attacker, Category: "learnings", Slug: "hijack", Title: "HACKED", DocType: "learning",
	}
	err := docs.Save(ctx, attacker, evil)
	require.Error(t, err)
	require.ErrorIs(t, err, apperr.ErrNotFound)

	// The owner's row is untouched by the rejected cross-tenant write.
	got, err := docs.GetByID(ctx, repository.ReadTenants(owner), docID)
	require.NoError(t, err)
	require.NotEqual(t, "HACKED", got.Title)
	require.Equal(t, "learnings", got.Category)

	// A legitimate owner write still succeeds.
	got.Title = "updated-legit"
	require.NoError(t, docs.Save(ctx, owner, got))
	reload, err := docs.GetByID(ctx, repository.ReadTenants(owner), docID)
	require.NoError(t, err)
	require.Equal(t, "updated-legit", reload.Title)
}
