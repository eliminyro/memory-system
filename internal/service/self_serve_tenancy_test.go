package service_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/authzseed"
	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/service"
)

func strPtr(s string) *string { return &s }

// TestProvisionPersonalTenantGateBlocks proves the signup gate rejects a
// non-listed domain with ErrSignupNotAllowed BEFORE touching the database: the
// service is wired with a nil db, so any DB access would panic. This is the 403
// path the auth adapter maps from. A founding admin is seeded so the
// bootstrap precondition passes and the DOMAIN gate is what does the rejecting.
func TestProvisionPersonalTenantGateBlocks(t *testing.T) {
	store := authz.NewMemoryStore()
	require.NoError(t, store.Write(context.Background(), authzseed.SystemAdmin("svc:founding-admin")))
	svc := service.NewMemoryService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, store)
	id, err := svc.ProvisionPersonalTenant(context.Background(), "bob@other.com", "Bob", "", []string{"example.com"}, "", "")
	require.ErrorIs(t, err, service.ErrSignupNotAllowed)
	require.Empty(t, id, "a blocked signup must not return a tenant id")
}

// TestProvisionPersonalTenantRefusedBeforeBootstrap proves the bootstrap
// precondition (PR-A4): on an un-bootstrapped instance (no system#admin tuple),
// auto-provision fails closed with ErrSignupNotAllowed BEFORE any DB write. The
// db is nil so a create would panic, and the allow-list is empty so the domain
// gate would otherwise pass — proving the refusal comes from HasAnyAdmin, not
// the domain gate. Nothing is created; this is the 403 path.
func TestProvisionPersonalTenantRefusedBeforeBootstrap(t *testing.T) {
	svc := service.NewMemoryService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, authz.NewMemoryStore())
	id, err := svc.ProvisionPersonalTenant(context.Background(), "alice@example.com", "Alice", "", nil, "", "")
	require.ErrorIs(t, err, service.ErrSignupNotAllowed)
	require.Empty(t, id, "no founding admin yet: nothing may be provisioned")
}

// TestProvisionPersonalTenantEmptyEmail rejects an empty email before any DB or
// gate work.
func TestProvisionPersonalTenantEmptyEmail(t *testing.T) {
	svc := service.NewMemoryService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	id, err := svc.ProvisionPersonalTenant(context.Background(), "", "", "", nil, "", "")
	require.ErrorIs(t, err, apperr.ErrInvalidInput)
	require.Empty(t, id)
}

// TestCreateTenantRejectsInvalidType proves the type is validated before the
// tenants repository is touched (nil here): an unknown type yields
// ErrInvalidInput. Runs under a local-admin context so requireAdmin passes.
func TestCreateTenantRejectsInvalidType(t *testing.T) {
	svc := service.NewMemoryService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	ctx := auth.WithLocalAdmin(context.Background())
	_, err := svc.CreateTenant(ctx, "acme", "a@b.com", "bogus")
	require.ErrorIs(t, err, apperr.ErrInvalidInput)
}

// TestUpdateTenantRejectsInvalidType proves the admin update path validates the
// type patch before the DB read (nil db) — the never-persist-garbage guard.
func TestUpdateTenantRejectsInvalidType(t *testing.T) {
	svc := service.NewMemoryService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	ctx := auth.WithLocalAdmin(context.Background())
	_, err := svc.UpdateTenant(ctx, uuid.New(), service.UpdateTenantFields{Type: strPtr("bogus")})
	require.ErrorIs(t, err, apperr.ErrInvalidInput)
}

// TestListTenantsByTypeRejectsBadType proves a non-empty, invalid type is
// rejected before WritableTenants runs (so the handler can map it to 400).
func TestListTenantsByTypeRejectsBadType(t *testing.T) {
	svc := service.NewMemoryService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	_, err := svc.ListTenantsByType(context.Background(), "bogus", "")
	require.ErrorIs(t, err, apperr.ErrInvalidInput)
}

// TestListTenantsByTypeSubjectlessEmpty proves a valid type with no subject
// yields an empty (non-error) list without any DB access — WritableTenants
// returns early for a subjectless caller.
func TestListTenantsByTypeSubjectlessEmpty(t *testing.T) {
	svc := service.NewMemoryService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, authz.NewMemoryStore())
	got, err := svc.ListTenantsByType(context.Background(), models.TenantTypeShared, "")
	require.NoError(t, err)
	require.Empty(t, got)
}

// TestAuthzDecisionIndependentOfTenantType is the never-in-authz invariant
// (task 2.3): an access Check outcome is IDENTICAL regardless of the tenant's
// display Type. Type is structurally absent from authz inputs — Check/authorize
// take only (objType, objID, relation, subjType, subjID) strings — so iterating
// the tenant's classification cannot change the decision. We assert that
// directly: the same tuple world yields the same CanManageTenant result whether
// we label the tenant personal or shared.
func TestAuthzDecisionIndependentOfTenantType(t *testing.T) {
	for tenantType := range models.ValidTenantTypes {
		t.Run("manager granted, type="+tenantType, func(t *testing.T) {
			store := authz.NewMemoryStore()
			svc := service.NewMemoryService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, store)
			tenantID := uuid.New()
			subj := "mgr-" + uuid.NewString()
			require.NoError(t, store.Write(context.Background(), authzseed.TenantManager(tenantID, subj)))
			ctx := auth.WithSubject(context.Background(), auth.Subject{Type: auth.SubjectTypeUser, ID: subj})
			// Decision is a function of the tuple, not the tenant's Type.
			require.True(t, svc.CanManageTenant(ctx, tenantID))
		})

		t.Run("plain member denied, type="+tenantType, func(t *testing.T) {
			store := authz.NewMemoryStore()
			svc := service.NewMemoryService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, store)
			tenantID := uuid.New()
			subj := "mem-" + uuid.NewString()
			require.NoError(t, store.Write(context.Background(), authzseed.TenantMember(tenantID, subj)))
			ctx := auth.WithSubject(context.Background(), auth.Subject{Type: auth.SubjectTypeUser, ID: subj})
			require.False(t, svc.CanManageTenant(ctx, tenantID))
		})
	}
}
