package database

import (
	"fmt"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/eliminyro/memory-system/internal/models"
)

func Connect(databaseURL string) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(databaseURL), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}

	// Connection pool settings
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get sql.DB: %w", err)
	}
	sqlDB.SetMaxOpenConns(10)
	sqlDB.SetMaxIdleConns(5)
	sqlDB.SetConnMaxLifetime(30 * time.Minute)

	// Enable pgvector extension
	if err := db.Exec("CREATE EXTENSION IF NOT EXISTS vector").Error; err != nil {
		return nil, fmt.Errorf("enable pgvector: %w", err)
	}

	return db, nil
}

func Migrate(db *gorm.DB, dimensions int) error {
	// Check existing vector dimensions BEFORE AutoMigrate (which may reset atttypmod).
	// If sections table exists with data, verify dimensions match.
	var existingDim int
	dimErr := db.Raw(`
		SELECT vector_dims(embedding) FROM sections WHERE embedding IS NOT NULL LIMIT 1
	`).Scan(&existingDim).Error
	if dimErr == nil && existingDim > 0 && existingDim != dimensions {
		return fmt.Errorf("embedding dimension mismatch: existing vectors are %d-dim, config wants %d — re-embed all data to fix", existingDim, dimensions)
	}

	// Migrate all models — Tenant first (referenced by Document and APIKey)
	if err := db.AutoMigrate(&models.Tenant{}, &models.APIKey{}, &models.Document{}, &models.Section{}); err != nil {
		return fmt.Errorf("auto-migrate: %w", err)
	}

	// Bootstrap tenant: insert default tenant for existing data
	if err := db.Exec(`
		INSERT INTO tenants (id, name, email, created_at, updated_at)
		VALUES ('00000000-0000-0000-0000-000000000001', 'default', '', NOW(), NOW())
		ON CONFLICT (id) DO NOTHING
	`).Error; err != nil {
		return fmt.Errorf("bootstrap tenant: %w", err)
	}

	// Ensure embedding column has the correct dimension
	alterSQL := fmt.Sprintf(
		`ALTER TABLE sections ALTER COLUMN embedding TYPE vector(%d)`, dimensions,
	)
	if err := db.Exec(alterSQL).Error; err != nil {
		return fmt.Errorf("set embedding dimension: %w", err)
	}

	// Create tsvector generated column, indexes, and constraints
	migrations := []string{
		`DO $$ BEGIN
			ALTER TABLE sections ADD COLUMN IF NOT EXISTS tsv tsvector
				GENERATED ALWAYS AS (to_tsvector('english', content)) STORED;
		EXCEPTION WHEN duplicate_column THEN NULL;
		END $$`,
		`CREATE INDEX IF NOT EXISTS idx_sections_tsv ON sections USING gin(tsv)`,
		`CREATE INDEX IF NOT EXISTS idx_sections_embedding ON sections USING hnsw (embedding vector_cosine_ops) WITH (m = 16, ef_construction = 64)`,
		// Drop old path index (not tenant-scoped) and create tenant-scoped one
		`DROP INDEX IF EXISTS idx_doc_path_with_null`,
		`DROP INDEX IF EXISTS idx_doc_path`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_doc_tenant_path ON documents (tenant_id, category, COALESCE(subcategory, ''), slug)`,
	}

	for _, m := range migrations {
		if err := db.Exec(m).Error; err != nil {
			return fmt.Errorf("migration: %w", err)
		}
	}

	return nil
}
