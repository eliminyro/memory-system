package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/authz"
	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/models"
)

type TenantRepository struct {
	db *gorm.DB
}

func NewTenantRepository(db *gorm.DB) *TenantRepository {
	return &TenantRepository{db: db}
}

func (r *TenantRepository) Create(ctx context.Context, tenant *models.Tenant) error {
	return r.db.WithContext(ctx).Create(tenant).Error
}

func (r *TenantRepository) GetByID(ctx context.Context, id uuid.UUID) (*models.Tenant, error) {
	var tenant models.Tenant
	if err := r.db.WithContext(ctx).First(&tenant, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: tenant %s", apperr.ErrNotFound, id)
		}
		return nil, err
	}
	return &tenant, nil
}

func (r *TenantRepository) List(ctx context.Context) ([]models.Tenant, error) {
	var tenants []models.Tenant
	if err := r.db.WithContext(ctx).Order("name").Find(&tenants).Error; err != nil {
		return nil, err
	}
	return tenants, nil
}

// GetByIDs fetches the tenants for the given ids in one query. Used by the read
// path to label cross-tenant results with their owning tenant's name/type
// without an N+1. Missing ids are simply absent from the result.
func (r *TenantRepository) GetByIDs(ctx context.Context, ids []uuid.UUID) ([]models.Tenant, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var tenants []models.Tenant
	if err := r.db.WithContext(ctx).Where("id IN ?", ids).Find(&tenants).Error; err != nil {
		return nil, err
	}
	return tenants, nil
}

func (r *TenantRepository) Update(ctx context.Context, tenant *models.Tenant) error {
	return r.db.WithContext(ctx).Save(tenant).Error
}

// Delete removes a tenant and everything scoped to it in one transaction: the
// authz relation tuples (document, tenant, and service-principal), its documents
// (with their sections), API keys, and the per-tenant bookkeeping tables
// (import_jobs, cleanup_queue, override_log, deletion_events). tenant_users cascade
// via their FK. Leaving any of these behind orphans rows and — for the tuples —
// leaves live authorization grants pointing at a tenant that no longer exists.
func (r *TenantRepository) Delete(ctx context.Context, id uuid.UUID) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Prune document-scoped authz tuples FIRST — the object_id set is derived from
		// the documents that must still exist. Covers document#viewer/editor guest
		// grants and the document->tenant parent edge (subject_type=tenant).
		if err := tx.Exec(`
			DELETE FROM relation_tuples
			WHERE object_type = ? AND object_id IN (
				SELECT id::text FROM documents WHERE tenant_id = ?
			)
		`, authz.TypeDocument, id).Error; err != nil {
			return fmt.Errorf("delete tenant document tuples: %w", err)
		}

		// Delete sections of the tenant's documents (raw subquery).
		if err := tx.Exec(`
			DELETE FROM sections WHERE document_id IN (
				SELECT id FROM documents WHERE tenant_id = ?
			)
		`, id).Error; err != nil {
			return fmt.Errorf("delete tenant sections: %w", err)
		}

		if err := tx.Where("tenant_id = ?", id).Delete(&models.Document{}).Error; err != nil {
			return fmt.Errorf("delete tenant documents: %w", err)
		}

		if err := tx.Where("tenant_id = ?", id).Delete(&models.APIKey{}).Error; err != nil {
			return fmt.Errorf("delete tenant api keys: %w", err)
		}

		// Per-tenant bookkeeping tables, all scoped by tenant_id.
		if err := tx.Where("tenant_id = ?", id).Delete(&models.ImportJob{}).Error; err != nil {
			return fmt.Errorf("delete tenant import jobs: %w", err)
		}
		if err := tx.Where("tenant_id = ?", id).Delete(&models.CleanupQueue{}).Error; err != nil {
			return fmt.Errorf("delete tenant cleanup queue: %w", err)
		}
		if err := tx.Where("tenant_id = ?", id).Delete(&models.OverrideLog{}).Error; err != nil {
			return fmt.Errorf("delete tenant override log: %w", err)
		}
		if err := tx.Where("tenant_id = ?", id).Delete(&models.DeletionEvent{}).Error; err != nil {
			return fmt.Errorf("delete tenant deletion events: %w", err)
		}

		// Tenant-scoped authz tuples. The tenant id occupies two positions:
		//   - object: tenant:<id>#{system,admin,owner,manager,member,viewer}@... grants.
		//   - subject: document:<d>#tenant@tenant:<id> edges (already pruned above with
		//     the documents; covered here defensively).
		if err := tx.Exec(`
			DELETE FROM relation_tuples
			WHERE (object_type = ? AND object_id = ?)
			   OR (subject_type = ? AND subject_id = ?)
		`, authz.TypeTenant, id.String(), authz.TypeTenant, id.String()).Error; err != nil {
			return fmt.Errorf("delete tenant tuples: %w", err)
		}

		// Service-principal tuples: the tenant's svc principal is a user subject
		// (subject_type=user, subject_id="svc:<tenant>") and can hold grants OUTSIDE
		// the tenant object — notably system:memory#admin — so it needs its own prune.
		if err := tx.Exec(`
			DELETE FROM relation_tuples
			WHERE subject_type = ? AND subject_id = ?
		`, authz.TypeUser, authz.ServicePrincipalID(id.String())).Error; err != nil {
			return fmt.Errorf("delete tenant service-principal tuples: %w", err)
		}

		result := tx.Delete(&models.Tenant{}, id)
		if result.Error != nil {
			return fmt.Errorf("delete tenant: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			return fmt.Errorf("%w: tenant %s", apperr.ErrNotFound, id)
		}
		return nil
	})
}
