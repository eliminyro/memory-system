//go:build integration

package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/authzseed"
	"github.com/eliminyro/memory-system/internal/models"
)

// failDeleteStore wraps a real store but forces every tuple Delete to fail.
// Writes/reads still delegate so grant setup and lifecycle reads work.
type failDeleteStore struct {
	authz.Store
}

func (failDeleteStore) Delete(context.Context, authz.Tuple) error {
	return errors.New("forced authz delete failure")
}

// TestServicePrincipalAdmin_GrantDowngradeReconcile guards H1: a runtime admin
// grant/downgrade must keep the tenant's svc-principal system#admin tuple in
// sync with whether any admin tenant_user remains. The svc principal is what a
// subject-less operator API key resolves to; before this fix its admin grant
// was seeded add-only at boot, so a downgraded last admin left the operator key
// with lingering global admin.
func TestServicePrincipalAdmin_GrantDowngradeReconcile(t *testing.T) {
	db := openServicePG(t)
	store := authz.NewPostgresStore(db)
	svc := newAdminTestSvc(db, store)
	ctx := auth.WithLocalAdmin(context.Background())

	// Shared tenant (default type) so the personal single-owner rule doesn't apply.
	tenant, err := svc.CreateTenant(ctx, "h1-"+uuid.NewString(), "")
	require.NoError(t, err)
	svcAdmin := authzseed.SystemAdmin(authz.ServicePrincipalID(tenant.ID.String()))

	// Granting an admin seeds the svc-admin tuple immediately, not only at boot.
	email := "h1-" + uuid.NewString() + "@example.com"
	_, err = svc.GrantTenantUser(ctx, email, tenant.ID, models.TenantUserRoleAdmin)
	require.NoError(t, err)
	requireTuple(t, store, svcAdmin)

	// Downgrading the only admin to member removes the svc-admin tuple.
	_, err = svc.UpdateTenantUserRole(ctx, email, models.TenantUserRoleMember)
	require.NoError(t, err)
	requireNoTuple(t, store, svcAdmin)

	// A fresh member does not resurrect it...
	email2 := "h1b-" + uuid.NewString() + "@example.com"
	_, err = svc.GrantTenantUser(ctx, email2, tenant.ID, models.TenantUserRoleMember)
	require.NoError(t, err)
	requireNoTuple(t, store, svcAdmin)

	// ...but upgrading that member back to admin brings the svc-admin tuple back.
	_, err = svc.UpdateTenantUserRole(ctx, email2, models.TenantUserRoleAdmin)
	require.NoError(t, err)
	requireTuple(t, store, svcAdmin)
}

// TestUpdateTenantUserRole_AuthzDeleteFailureRollsBack guards H2 atomicity: on
// an admin->member downgrade the admin-tuple Delete must hard-fail and roll the
// role UPDATE back, not commit the row and swallow the tuple error (the old
// best-effort path retained privilege — row said member while the admin tuple
// lived on). A rollback is fail-CLOSED and safe.
func TestUpdateTenantUserRole_AuthzDeleteFailureRollsBack(t *testing.T) {
	db := openServicePG(t)
	real := authz.NewPostgresStore(db)
	svc := newAdminTestSvc(db, failDeleteStore{real})
	ctx := auth.WithLocalAdmin(context.Background())

	tenant, err := svc.CreateTenant(ctx, "h2-"+uuid.NewString(), "")
	require.NoError(t, err)

	// Setup uses only tuple Writes, which the faulting store still delegates.
	email := "h2-" + uuid.NewString() + "@example.com"
	tu, err := svc.GrantTenantUser(ctx, email, tenant.ID, models.TenantUserRoleAdmin)
	require.NoError(t, err)

	// Downgrade admin->member: the admin-tuple Delete fails, the tx rolls back.
	_, err = svc.UpdateTenantUserRole(ctx, email, models.TenantUserRoleMember)
	require.Error(t, err, "a failed tuple delete must surface, not be swallowed")

	// The row's role is still admin (transaction rolled back)...
	var got models.TenantUser
	require.NoError(t, db.Where("id = ?", tu.ID).First(&got).Error)
	require.Equal(t, models.TenantUserRoleAdmin, got.Role, "role must remain admin after rollback")

	// ...and the admin tuple is still present (the delete never committed).
	requireTuple(t, real, authzseed.TenantAdmin(tenant.ID, tu.ID.String()))
}

// TestDeleteDocumentByID_PrunesTuples guards L2: deleting a document must prune
// its relation_tuples — both the document->tenant parent edge seeded at create
// and any per-document guest grant — since relation_tuples has no FK cascade to
// documents.
func TestDeleteDocumentByID_PrunesTuples(t *testing.T) {
	db := openServicePG(t)
	store := authz.NewPostgresStore(db)
	svc := newAdminTestSvc(db, store)
	ctx := auth.WithLocalAdmin(context.Background())

	tenant, err := svc.CreateTenant(ctx, "l2-"+uuid.NewString(), "")
	require.NoError(t, err)

	docCtx := auth.WithTenantID(ctx, tenant.ID)
	res, err := svc.StoreDocument(docCtx, "learnings", nil, "l2-doc-"+uuid.NewString(), "# T\n\nbody", true, "seed", nil)
	require.NoError(t, err)
	docID := res.Document.ID

	// The document->tenant parent edge is seeded at create.
	requireTuple(t, store, authzseed.DocumentTenantEdge(docID, tenant.ID))

	// Grant a per-document guest editor.
	targetEmail := "l2-" + uuid.NewString() + "@example.com"
	targetTU, err := svc.GrantTenantUser(ctx, targetEmail, tenant.ID, models.TenantUserRoleMember)
	require.NoError(t, err)
	require.NoError(t, svc.GrantDocumentAccess(docCtx, docID, targetEmail, authz.RelEditor))
	requireTuple(t, store, authzseed.DocumentEditor(docID, targetTU.ID.String()))

	// Deleting the document prunes both its #tenant edge and #editor grant.
	require.NoError(t, svc.DeleteDocumentByID(docCtx, docID, nil))

	edges, err := store.ReadByObjectRelation(context.Background(), authz.TypeDocument, docID.String(), authz.RelTenant)
	require.NoError(t, err)
	require.Empty(t, edges, "document->tenant edge must be pruned on delete")

	editors, err := store.ReadByObjectRelation(context.Background(), authz.TypeDocument, docID.String(), authz.RelEditor)
	require.NoError(t, err)
	require.Empty(t, editors, "guest editor grant must be pruned on delete")
}
