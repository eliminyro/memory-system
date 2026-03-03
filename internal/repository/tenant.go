package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"

	apperr "github.com/eliminyro/memory-mcp/internal/errors"
	"github.com/eliminyro/memory-mcp/internal/models"
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

// Delete removes a tenant and cascades to API keys and documents (with their sections).
func (r *TenantRepository) Delete(ctx context.Context, id uuid.UUID) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Delete sections belonging to tenant's documents
		if err := tx.Exec(`
			DELETE FROM sections WHERE document_id IN (
				SELECT id FROM documents WHERE tenant_id = ?
			)
		`, id).Error; err != nil {
			return fmt.Errorf("delete tenant sections: %w", err)
		}

		// Delete tenant's documents
		if err := tx.Where("tenant_id = ?", id).Delete(&models.Document{}).Error; err != nil {
			return fmt.Errorf("delete tenant documents: %w", err)
		}

		// Delete tenant's API keys
		if err := tx.Where("tenant_id = ?", id).Delete(&models.APIKey{}).Error; err != nil {
			return fmt.Errorf("delete tenant api keys: %w", err)
		}

		// Delete tenant
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
