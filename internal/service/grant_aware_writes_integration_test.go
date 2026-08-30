//go:build integration

package service_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/authzseed"
	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/service"
	"github.com/eliminyro/memory-system/internal/staleness"
)

// TestGrantAwareMemberCanWriteCommonPool: a non-admin holding tenant#member on
// the common pool creates and edits there by targeting it; an ungranted caller
// is refused at the resolver (ErrInvalidInput), never silently found/edited.
func TestGrantAwareMemberCanWriteCommonPool(t *testing.T) {
	f := newAuthzFixture(t)
	require.NoError(t, f.store.Write(context.Background(), authzseed.TenantMember(models.BootstrapTenantID, f.subjA)))
	memberCtx := ctxFor(f.tenantA, f.subjA)

	slug := "gaw-" + uuid.NewString()
	res, err := f.svc.StoreDocument(memberCtx, "learnings", nil, slug, "# T\n\n## H\nbody", false, "", &models.BootstrapTenantID, nil)
	require.NoError(t, err, "member stores in common pool")
	require.Equal(t, models.BootstrapTenantID, res.Document.TenantID, "doc created in common pool")

	body := "edited by member"
	_, err = f.svc.UpdateSection(memberCtx, res.Document.Sections[0].ID, &body, nil, &models.BootstrapTenantID)
	require.NoError(t, err, "member updates common-pool section")
	_, err = f.svc.UpdateDocumentTitle(memberCtx, res.Document.ID, "new title", &models.BootstrapTenantID)
	require.NoError(t, err, "member updates common-pool title")
	require.NoError(t, f.svc.MarkVerified(memberCtx, res.Document.Sections[0].ID, &models.BootstrapTenantID), "member marks verified")

	// Ungranted non-admin (member of B only) is refused targeting the pool.
	bCtx := ctxFor(f.tenantB, f.subjB)
	_, err = f.svc.StoreDocument(bCtx, "learnings", nil, "nope-"+uuid.NewString(), "# T\n\nbody", false, "", &models.BootstrapTenantID, nil)
	require.ErrorIs(t, err, apperr.ErrInvalidInput, "ungranted store refused")
	_, err = f.svc.UpdateSection(bCtx, f.secC, &body, nil, &models.BootstrapTenantID)
	require.ErrorIs(t, err, apperr.ErrInvalidInput, "ungranted update refused")
	_, err = f.svc.UpdateDocumentTitle(bCtx, f.docC, "hax", &models.BootstrapTenantID)
	require.ErrorIs(t, err, apperr.ErrInvalidInput, "ungranted title update refused")
}

// TestGrantAwareDeleteRequiresManager: a member CANNOT delete a common-pool
// doc/section (override OR no-override), a manager CAN, own-tenant + admin
// deletes still work. The no-override cases guard the readTenants-common hole.
func TestGrantAwareDeleteRequiresManager(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := context.Background()
	require.NoError(t, f.store.Write(ctx, authzseed.TenantMember(models.BootstrapTenantID, f.subjA)))
	mgr := "mgr-" + uuid.NewString()
	require.NoError(t, f.store.Write(ctx, authzseed.TenantManager(models.BootstrapTenantID, mgr)))
	memberCtx := ctxFor(f.tenantA, f.subjA)
	mgrCtx := ctxFor(f.tenantA, mgr)
	adminCtx := ctxFor(f.tenantA, f.admin)

	// A member is refused every way: override delete AND no-override delete
	// (the fetch spans the common pool, so the manager guard must hold).
	require.ErrorIs(t, f.svc.DeleteSection(memberCtx, f.secC, &models.BootstrapTenantID), apperr.ErrInvalidInput, "member override delete section")
	require.ErrorIs(t, f.svc.DeleteDocumentByID(memberCtx, f.docC, &models.BootstrapTenantID), apperr.ErrInvalidInput, "member override delete doc")
	require.ErrorIs(t, f.svc.DeleteDocumentByID(memberCtx, f.docC, nil), apperr.ErrInvalidInput, "member no-override common delete (hole closed)")
	require.ErrorIs(t, f.svc.DeleteSection(memberCtx, f.secC, nil), apperr.ErrInvalidInput, "member no-override common section delete (hole closed)")
	require.Equal(t, int64(1), countRows(t, f, &models.Section{}, f.secC), "common section survived member")
	require.Equal(t, int64(1), countRows(t, f, &models.Document{}, f.docC), "common doc survived member")

	// A manager deletes a common-pool section + document.
	require.NoError(t, f.svc.DeleteSection(mgrCtx, f.secC, &models.BootstrapTenantID), "manager deletes common section")
	require.NoError(t, f.svc.DeleteDocumentByID(mgrCtx, f.docC2, &models.BootstrapTenantID), "manager deletes common doc")

	// Admin still deletes via override; own-tenant delete still works.
	docX, _ := f.storeDoc(t, adminCtx, &models.BootstrapTenantID)
	require.NoError(t, f.svc.DeleteDocumentByID(adminCtx, docX, &models.BootstrapTenantID), "admin deletes common doc")
	require.NoError(t, f.svc.DeleteDocumentByID(memberCtx, f.docA, nil), "own-tenant delete works")
}

// TestGrantAwareMergeStaysAdminOnly: an out-of-scope op (merge) still refuses a
// tenant override for a non-admin, even one holding member on the target.
func TestGrantAwareMergeStaysAdminOnly(t *testing.T) {
	f := newAuthzFixture(t)
	require.NoError(t, f.store.Write(context.Background(), authzseed.TenantMember(models.BootstrapTenantID, f.subjA)))
	memberCtx := ctxFor(f.tenantA, f.subjA)

	_, err := f.svc.MergeDocuments(memberCtx, f.docC, f.docC2, nil, &models.BootstrapTenantID)
	require.ErrorIs(t, err, apperr.ErrInvalidInput, "member merge override still refused (out of scope)")
}

// TestGrantAwareFailClosedWithoutAuthz: with no authz engine every Check denies,
// so a cross-tenant write is refused while an own-tenant write is unaffected.
func TestGrantAwareFailClosedWithoutAuthz(t *testing.T) {
	f := newAuthzFixture(t)
	nilSvc := service.NewMemoryService(
		f.db,
		repository.NewDocumentRepository(f.db),
		repository.NewSectionRepository(f.db),
		service.NewFakeEmbedder(fakeDim),
		repository.NewTenantRepository(f.db),
		repository.NewAPIKeyRepository(f.db),
		repository.NewLintRepository(f.db),
		staleness.NewThresholdStore(f.db),
		repository.NewOverrideLogRepository(f.db),
		repository.NewCleanupQueueRepository(f.db),
		nil, nil,
		nil, // no authz store -> nil engine -> fail closed
	)
	ctx := ctxFor(f.tenantA, f.subjA)

	_, err := nilSvc.StoreDocument(ctx, "learnings", nil, "fc-"+uuid.NewString(), "# T\n\nbody", false, "", &models.BootstrapTenantID, nil)
	require.ErrorIs(t, err, apperr.ErrInvalidInput, "cross-tenant write denied when authz unavailable")

	res, err := nilSvc.StoreDocument(ctx, "learnings", nil, "fc-own-"+uuid.NewString(), "# T\n\nbody", false, "", nil, nil)
	require.NoError(t, err, "own-tenant write unaffected without authz")
	require.Equal(t, f.tenantA, res.Document.TenantID, "own-tenant write lands in home tenant")
}

// TestGrantAwareGuestEditorUpdateStillWorks: the pre-existing per-document guest
// editor (no tenant grant) still updates a common-pool section via no-override.
func TestGrantAwareGuestEditorUpdateStillWorks(t *testing.T) {
	f := newAuthzFixture(t)
	require.NoError(t, f.store.Write(context.Background(), authzseed.DocumentEditor(f.docC, f.subjB)))
	bCtx := ctxFor(f.tenantB, f.subjB)

	body := "guest edit"
	_, err := f.svc.UpdateSection(bCtx, f.secC, &body, nil, nil)
	require.NoError(t, err, "guest editor updates common-pool section via no-override")
}
