package service

import (
	"testing"

	"github.com/google/uuid"

	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/authzseed"
	"github.com/eliminyro/memory-system/internal/models"
)

// TestRoleElevatedTuple covers the single source of truth for the role->relation
// mapping shared by GrantTenantUser, UpdateTenantUserRole, and RevokeTenantUser
// (R2): admin and owner carry their elevated tuple; a plain member carries none.
func TestRoleElevatedTuple(t *testing.T) {
	tenantID := uuid.New()
	subj := uuid.NewString()

	t.Run("admin -> tenant#admin", func(t *testing.T) {
		got, ok := roleElevatedTuple(tenantID, models.TenantUserRoleAdmin, subj)
		if !ok {
			t.Fatal("ok = false, want true for admin")
		}
		if got != authzseed.TenantAdmin(tenantID, subj) {
			t.Fatalf("tuple = %+v, want TenantAdmin", got)
		}
		if got.Relation != authz.RelAdmin {
			t.Fatalf("relation = %q, want %q", got.Relation, authz.RelAdmin)
		}
	})

	t.Run("owner -> tenant#owner", func(t *testing.T) {
		got, ok := roleElevatedTuple(tenantID, models.TenantUserRoleOwner, subj)
		if !ok {
			t.Fatal("ok = false, want true for owner")
		}
		if got != authzseed.TenantOwner(tenantID, subj) {
			t.Fatalf("tuple = %+v, want TenantOwner", got)
		}
		if got.Relation != authz.RelOwner {
			t.Fatalf("relation = %q, want %q", got.Relation, authz.RelOwner)
		}
	})

	t.Run("member -> no elevated tuple", func(t *testing.T) {
		got, ok := roleElevatedTuple(tenantID, models.TenantUserRoleMember, subj)
		if ok {
			t.Fatalf("ok = true, want false for member (got %+v)", got)
		}
	})
}
