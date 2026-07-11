// Package authzseed populates the relationship-based authorization engine's
// relation tuples out-of-band: lifecycle constructors used by the service on
// resource creation, a one-shot Backfill run from the migration path, and a
// startup BootstrapAdmins seeder for the global-admin allowlist.
//
// It is deliberately a thin, side-effect-free-by-default helper: the tuple
// constructors are pure functions (the single source of truth for which tuple
// shape each domain event produces), and the Backfill/BootstrapAdmins writers
// are idempotent — re-running them writes the same tuple set (the underlying
// authz.Store dedupes writes). Nothing here reads tuples to make a decision;
// it only writes them. Wiring the tuples into authorization DECISIONS happens
// separately (Pass 2).
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

// TenantSystemEdge returns the parent edge tenant:<T>#system@system:memory,
// seeded at tenant create. It lets global admins (system:memory#admin) reach
// admin on every tenant via the namespace's "admin from system" rewrite.
func TenantSystemEdge(tenantID uuid.UUID) authz.Tuple {
	return authz.Tuple{
		ObjectType:  authz.TypeTenant,
		ObjectID:    tenantID.String(),
		Relation:    authz.RelSystem,
		SubjectType: authz.TypeSystem,
		SubjectID:   authz.SystemObjectID,
	}
}

// TenantMember returns tenant:<T>#member@user:<subjectID>.
func TenantMember(tenantID uuid.UUID, subjectID string) authz.Tuple {
	return authz.Tuple{
		ObjectType:  authz.TypeTenant,
		ObjectID:    tenantID.String(),
		Relation:    authz.RelMember,
		SubjectType: authz.TypeUser,
		SubjectID:   subjectID,
	}
}

// TenantAdmin returns tenant:<T>#admin@user:<subjectID>.
func TenantAdmin(tenantID uuid.UUID, subjectID string) authz.Tuple {
	return authz.Tuple{
		ObjectType:  authz.TypeTenant,
		ObjectID:    tenantID.String(),
		Relation:    authz.RelAdmin,
		SubjectType: authz.TypeUser,
		SubjectID:   subjectID,
	}
}

// DocumentTenantEdge returns the parent edge document:<D>#tenant@tenant:<T>,
// set at document create. It routes document viewer/editor through the owning
// tenant's membership.
func DocumentTenantEdge(docID, tenantID uuid.UUID) authz.Tuple {
	return authz.Tuple{
		ObjectType:  authz.TypeDocument,
		ObjectID:    docID.String(),
		Relation:    authz.RelTenant,
		SubjectType: authz.TypeTenant,
		SubjectID:   tenantID.String(),
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

// APIKeySubjectID resolves an API key row's authorization subject id: its
// explicit subject_id when set, else the tenant service principal. It mirrors
// the auth layer's resolution so backfill membership matches request-time
// subjects.
func APIKeySubjectID(k models.APIKey) string {
	if k.SubjectID != nil && *k.SubjectID != "" {
		return *k.SubjectID
	}
	return authz.ServicePrincipalID(k.TenantID.String())
}

// --- Idempotent writers ---

// Backfill derives the full tuple set from existing domain rows and writes it
// via store. It is idempotent: re-running produces the same tuple set. Reads
// use db, writes use store, so callers can run it inside the migration
// transaction by passing an authz.PostgresStore(tx) and tx.
//
//   - each tenant           -> system parent edge + svc:<tenant> membership
//   - each tenant_user      -> membership (+ admin when role == admin)
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
		if tu.Role == models.TenantUserRoleAdmin {
			if err := store.Write(ctx, TenantAdmin(tu.TenantID, tu.ID.String())); err != nil {
				return err
			}
		}
	}

	// Admin tenant_users also make their tenant's service principal a global
	// admin, so operator API keys (which resolve to svc:<tenant>) keep the admin
	// tool surface — see seedAdminServicePrincipals.
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

// BootstrapAdmins seeds system:memory#admin for every email in emails that has
// a tenant_users row, resolving the verified email to its tenant_users.id
// subject. Emails without a row are skipped with a log line (never invented).
// It additionally grants system:memory#admin to the service principal
// (svc:<tenant_id>) of every tenant that has an admin tenant_user, closing the
// operator API-key admin gap (see seedAdminServicePrincipals) — this runs on
// every startup, independent of the allowlist. Idempotent: a second run writes
// no duplicates. Read ADMIN_ALLOWED_EMAILS at startup only and pass the parsed
// list here.
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
	// Also grant system#admin to the service principal of every tenant that has
	// an admin tenant_user, so operator API keys (svc:<tenant>) get the admin
	// tool surface without manual SQL. Independent of the email allowlist and
	// idempotent.
	return seedAdminServicePrincipals(ctx, store, db)
}

// seedAdminServicePrincipals grants system:memory#admin to the service
// principal (svc:<tenant_id>) of every tenant that has at least one admin
// tenant_user (role == admin).
//
// This closes the operator API-key admin gap: an API key with no explicit
// subject_id resolves to its tenant's service principal svc:<tenant_id> (see
// APIKeySubjectID / authz.ServicePrincipalID), which the tenant backfill only
// makes a tenant *member*. Bootstrap already grants system#admin to the human
// JWT identity (system:memory#admin@user:<tenant_users.id>), so without this an
// operator whose only session is an API key would lose the admin tool surface.
// Granting the service principal system#admin makes a fresh install give the
// operator tenant's API keys admin with no manual SQL.
//
// Idempotent: store.Write is ON CONFLICT DO NOTHING, and multiple admin
// tenant_users for one tenant collapse to a single svc grant via DISTINCT.
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
