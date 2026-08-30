// Package authzseed populates the RBAC engine's relation tuples out-of-band:
// pure constructors (single source of truth) + idempotent Backfill/BootstrapAdmins
// writers. Write-only; wiring tuples into decisions is Pass 2.
package authzseed

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/models"
)

// --- Pure tuple constructors (single source of truth) ---

// TenantSystemEdge returns tenant:<T>#system@system:memory, seeded at tenant
// create; lets global admins reach admin on every tenant via the
// "admin from system" rewrite.
func TenantSystemEdge(tenantID uuid.UUID) authz.Tuple {
	return authz.Tuple{
		ObjectType:  authz.TypeTenant,
		ObjectID:    tenantID.String(),
		Relation:    authz.RelSystem,
		SubjectType: authz.TypeSystem,
		SubjectID:   authz.SystemObjectID,
	}
}

// tenantUserTuple builds tenant:<T>#<relation>@user:<subjectID>, the shape the
// five exported tenant-user constructors share (they differ only by relation).
func tenantUserTuple(tenantID uuid.UUID, relation, subjectID string) authz.Tuple {
	return authz.Tuple{
		ObjectType:  authz.TypeTenant,
		ObjectID:    tenantID.String(),
		Relation:    relation,
		SubjectType: authz.TypeUser,
		SubjectID:   subjectID,
	}
}

// TenantMember returns tenant:<T>#member@user:<subjectID>.
func TenantMember(tenantID uuid.UUID, subjectID string) authz.Tuple {
	return tenantUserTuple(tenantID, authz.RelMember, subjectID)
}

// TenantAdmin returns tenant:<T>#admin@user:<subjectID>.
func TenantAdmin(tenantID uuid.UUID, subjectID string) authz.Tuple {
	return tenantUserTuple(tenantID, authz.RelAdmin, subjectID)
}

// TenantOwner returns tenant:<T>#owner@user:<subjectID>, the personal-tenant
// owner grant. An owner folds up into manager (owner ⇒ manager) but not admin,
// so it confers full self-management without system-admin reach.
func TenantOwner(tenantID uuid.UUID, subjectID string) authz.Tuple {
	return tenantUserTuple(tenantID, authz.RelOwner, subjectID)
}

// TenantManager returns tenant:<T>#manager@user:<subjectID>.
func TenantManager(tenantID uuid.UUID, subjectID string) authz.Tuple {
	return tenantUserTuple(tenantID, authz.RelManager, subjectID)
}

// TenantViewer returns tenant:<T>#viewer@user:<subjectID>.
func TenantViewer(tenantID uuid.UUID, subjectID string) authz.Tuple {
	return tenantUserTuple(tenantID, authz.RelViewer, subjectID)
}

// DocumentTenantEdge returns document:<D>#tenant@tenant:<T>, set at document
// create; routes document viewer/editor through the owning tenant's membership.
func DocumentTenantEdge(docID, tenantID uuid.UUID) authz.Tuple {
	return authz.Tuple{
		ObjectType:  authz.TypeDocument,
		ObjectID:    docID.String(),
		Relation:    authz.RelTenant,
		SubjectType: authz.TypeTenant,
		SubjectID:   tenantID.String(),
	}
}

// DocumentViewer returns document:<D>#viewer@user:<subjectID>, a per-document
// guest read grant.
func DocumentViewer(docID uuid.UUID, subjectID string) authz.Tuple {
	return authz.Tuple{
		ObjectType:  authz.TypeDocument,
		ObjectID:    docID.String(),
		Relation:    authz.RelViewer,
		SubjectType: authz.TypeUser,
		SubjectID:   subjectID,
	}
}

// DocumentEditor returns document:<D>#editor@user:<subjectID>, a per-document
// guest write grant (also confers read via the editor⇒viewer rewrite).
func DocumentEditor(docID uuid.UUID, subjectID string) authz.Tuple {
	return authz.Tuple{
		ObjectType:  authz.TypeDocument,
		ObjectID:    docID.String(),
		Relation:    authz.RelEditor,
		SubjectType: authz.TypeUser,
		SubjectID:   subjectID,
	}
}

// SystemAdmin returns system:memory#admin@user:<subjectID>, the global-admin
// grant seeded from ADMIN_ALLOWED_EMAILS.
func SystemAdmin(subjectID string) authz.Tuple {
	return authz.Tuple{
		ObjectType:  authz.TypeSystem,
		ObjectID:    authz.SystemObjectID,
		Relation:    authz.RelAdmin,
		SubjectType: authz.TypeUser,
		SubjectID:   subjectID,
	}
}

// CommonPoolViewerWildcard returns tenant:<bootstrap>#viewer@user:*, the public
// read grant on the common/bootstrap pool.
func CommonPoolViewerWildcard() authz.Tuple {
	return authz.Tuple{
		ObjectType:  authz.TypeTenant,
		ObjectID:    models.BootstrapTenantID.String(),
		Relation:    authz.RelViewer,
		SubjectType: authz.TypeUser,
		SubjectID:   authz.Wildcard,
	}
}

// APIKeySubjectID resolves an API key's authz subject: explicit subject_id when
// set, else the tenant service principal. Mirrors the auth layer so backfill
// membership matches request-time subjects.
func APIKeySubjectID(k models.APIKey) string {
	if k.SubjectID != nil && *k.SubjectID != "" {
		return *k.SubjectID
	}
	return authz.ServicePrincipalID(k.TenantID.String())
}

// --- Idempotent writers ---

// Backfill derives the full tuple set from existing rows and writes it via
// store. Idempotent: re-running produces the same set. Reads use db, writes use
// store, so it can run inside the migration tx (pass authz.PostgresStore(tx), tx).
//
//   - each tenant           -> system parent edge + svc:<tenant> membership
//   - each tenant_user      -> membership (+ admin when role == admin, + owner when role == owner)
//   - admin tenant_user     -> system#admin for its tenant's svc principal
//   - each document         -> document#tenant parent edge
//   - each api_key          -> its subject's membership
//   - common/bootstrap pool -> viewer@user:* wildcard (public read)
func Backfill(ctx context.Context, store authz.Store, db *gorm.DB) error {
	// Common/bootstrap pool public read.
	if err := store.Write(ctx, CommonPoolViewerWildcard()); err != nil {
		return err
	}

	var tenants []models.Tenant
	if err := db.WithContext(ctx).Find(&tenants).Error; err != nil {
		return err
	}
	for _, t := range tenants {
		if err := store.Write(ctx, TenantSystemEdge(t.ID)); err != nil {
			return err
		}
		if err := store.Write(ctx, TenantMember(t.ID, authz.ServicePrincipalID(t.ID.String()))); err != nil {
			return err
		}
	}

	var tenantUsers []models.TenantUser
	if err := db.WithContext(ctx).Find(&tenantUsers).Error; err != nil {
		return err
	}
	for _, tu := range tenantUsers {
		if err := store.Write(ctx, TenantMember(tu.TenantID, tu.ID.String())); err != nil {
			return err
		}
		switch tu.Role {
		case models.TenantUserRoleAdmin:
			if err := store.Write(ctx, TenantAdmin(tu.TenantID, tu.ID.String())); err != nil {
				return err
			}
		case models.TenantUserRoleOwner:
			if err := store.Write(ctx, TenantOwner(tu.TenantID, tu.ID.String())); err != nil {
				return err
			}
		}
	}

	// Admin tenant_users also make their tenant's svc principal a global admin so
	// operator API keys (svc:<tenant>) keep the admin tool surface — see
	// seedAdminServicePrincipals. BootstrapAdmins runs this same pass, so on the
	// server-start path (cmd/server) it executes twice per boot — idempotent, so
	// harmless. The call is kept here DELIBERATELY: Migrate-only tools that never
	// invoke BootstrapAdmins rely on this Backfill pass as
	// their ONLY source of admin svc-principal seeding, so dropping it to dedupe the
	// server-path double would silently regress admin API-key access for them.
	if err := seedAdminServicePrincipals(ctx, store, db); err != nil {
		return err
	}

	var keys []models.APIKey
	if err := db.WithContext(ctx).Find(&keys).Error; err != nil {
		return err
	}
	for _, k := range keys {
		if err := store.Write(ctx, TenantMember(k.TenantID, APIKeySubjectID(k))); err != nil {
			return err
		}
	}

	// Documents can be the largest table; stream in batches to bound memory.
	var batch []models.Document
	return db.WithContext(ctx).FindInBatches(&batch, 1000, func(_ *gorm.DB, _ int) error {
		for _, d := range batch {
			if err := store.Write(ctx, DocumentTenantEdge(d.ID, d.TenantID)); err != nil {
				return err
			}
		}
		return nil
	}).Error
}

// BootstrapAdmins seeds system:memory#admin for each email with a tenant_users
// row (verified email -> tenant_users.id); unknown emails are skipped, never
// invented. Also grants system#admin to every admin tenant's svc principal
// (operator API-key admin gap; see seedAdminServicePrincipals), independent of
// the allowlist. Idempotent. Pass the parsed ADMIN_ALLOWED_EMAILS list here.
func BootstrapAdmins(ctx context.Context, store authz.Store, db *gorm.DB, emails []string, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	for _, email := range emails {
		if email == "" {
			continue
		}
		var tu models.TenantUser
		err := db.WithContext(ctx).Where("email = ?", email).Limit(1).Find(&tu).Error
		if err != nil {
			return err
		}
		if tu.ID == uuid.Nil {
			logger.Warn("authzseed: bootstrap admin skipped, no tenant_user row", "email", email)
			continue
		}
		if err := store.Write(ctx, SystemAdmin(tu.ID.String())); err != nil {
			return err
		}
		logger.Info("authzseed: bootstrap admin seeded", "email", email, "subject_id", tu.ID.String())
	}
	// Grant system#admin to every admin tenant's svc principal (operator API-key
	// gap), independent of the allowlist and idempotent.
	return seedAdminServicePrincipals(ctx, store, db)
}

// seedAdminServicePrincipals grants system:memory#admin to the svc principal
// (svc:<tenant_id>) of every tenant with at least one admin tenant_user.
//
// Closes the operator API-key admin gap: a key with no subject_id resolves to
// svc:<tenant_id> (see APIKeySubjectID), which backfill only makes a tenant
// member — so an API-key-only operator would otherwise lose the admin surface
// that bootstrap grants the human JWT identity.
//
// Idempotent: store.Write is ON CONFLICT DO NOTHING; DISTINCT collapses
// multiple admin tenant_users per tenant to one svc grant.
func seedAdminServicePrincipals(ctx context.Context, store authz.Store, db *gorm.DB) error {
	var adminTenantIDs []uuid.UUID
	if err := db.WithContext(ctx).
		Model(&models.TenantUser{}).
		Where("role = ?", models.TenantUserRoleAdmin).
		Distinct().
		Pluck("tenant_id", &adminTenantIDs).Error; err != nil {
		return err
	}
	for _, tid := range adminTenantIDs {
		if err := store.Write(ctx, SystemAdmin(authz.ServicePrincipalID(tid.String()))); err != nil {
			return err
		}
	}
	return nil
}
