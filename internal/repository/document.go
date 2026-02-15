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

type DocumentRepository struct {
	db *gorm.DB
}

func NewDocumentRepository(db *gorm.DB) *DocumentRepository {
	return &DocumentRepository{db: db}
}

func (r *DocumentRepository) Create(ctx context.Context, doc *models.Document) error {
	return r.db.WithContext(ctx).Create(doc).Error
}

func (r *DocumentRepository) GetByPath(ctx context.Context, category string, subcategory *string, slug string) (*models.Document, error) {
	var doc models.Document
	q := r.db.WithContext(ctx).Where("category = ? AND slug = ?", category, slug)
	if subcategory != nil {
		q = q.Where("subcategory = ?", *subcategory)
	} else {
		q = q.Where("subcategory IS NULL")
	}
	if err := q.Preload("Sections", func(db *gorm.DB) *gorm.DB {
		return db.Order("ordinal ASC")
	}).First(&doc).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: document %s/%s", apperr.ErrNotFound, category, slug)
		}
		return nil, err
	}
	return &doc, nil
}

func (r *DocumentRepository) GetByID(ctx context.Context, id uuid.UUID) (*models.Document, error) {
	var doc models.Document
	if err := r.db.WithContext(ctx).
		Preload("Sections", func(db *gorm.DB) *gorm.DB {
			return db.Order("ordinal ASC")
		}).
		First(&doc, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: document %s", apperr.ErrNotFound, id)
		}
		return nil, err
	}
	return &doc, nil
}

func (r *DocumentRepository) List(ctx context.Context, category *string, subcategory *string) ([]models.Document, error) {
	var docs []models.Document
	q := r.db.WithContext(ctx)
	if category != nil {
		q = q.Where("category = ?", *category)
	}
	if subcategory != nil {
		q = q.Where("subcategory = ?", *subcategory)
	}
	if err := q.Order("category, subcategory, slug").Find(&docs).Error; err != nil {
		return nil, err
	}
	return docs, nil
}

func (r *DocumentRepository) Save(ctx context.Context, doc *models.Document) error {
	return r.db.WithContext(ctx).Save(doc).Error
}

func (r *DocumentRepository) Delete(ctx context.Context, id uuid.UUID) error {
	result := r.db.WithContext(ctx).Delete(&models.Document{}, id)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("%w: document %s", apperr.ErrNotFound, id)
	}
	return nil
}

func (r *DocumentRepository) DeleteByPath(ctx context.Context, category string, subcategory *string, slug string) error {
	q := r.db.WithContext(ctx).Where("category = ? AND slug = ?", category, slug)
	if subcategory != nil {
		q = q.Where("subcategory = ?", *subcategory)
	} else {
		q = q.Where("subcategory IS NULL")
	}
	result := q.Delete(&models.Document{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("%w: document %s/%s", apperr.ErrNotFound, category, slug)
	}
	return nil
}
