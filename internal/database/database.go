package database

import (
	"fmt"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/eliminyro/memory-mcp/internal/models"
)

func Connect(databaseURL string) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(databaseURL), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}

	// Enable pgvector extension
	if err := db.Exec("CREATE EXTENSION IF NOT EXISTS vector").Error; err != nil {
		return nil, fmt.Errorf("enable pgvector: %w", err)
	}

	return db, nil
}

func Migrate(db *gorm.DB) error {
	if err := db.AutoMigrate(&models.Document{}, &models.Section{}); err != nil {
		return fmt.Errorf("auto-migrate: %w", err)
	}

	// Create tsvector generated column and GIN index (AutoMigrate can't do these)
	migrations := []string{
		`DO $$ BEGIN
			ALTER TABLE sections ADD COLUMN IF NOT EXISTS tsv tsvector
				GENERATED ALWAYS AS (to_tsvector('english', content)) STORED;
		EXCEPTION WHEN duplicate_column THEN NULL;
		END $$`,
		`CREATE INDEX IF NOT EXISTS idx_sections_tsv ON sections USING gin(tsv)`,
		`CREATE INDEX IF NOT EXISTS idx_sections_embedding ON sections USING hnsw (embedding vector_cosine_ops)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_doc_path_with_null ON documents (category, COALESCE(subcategory, ''), slug)`,
	}

	for _, m := range migrations {
		if err := db.Exec(m).Error; err != nil {
			return fmt.Errorf("migration: %w", err)
		}
	}

	return nil
}
