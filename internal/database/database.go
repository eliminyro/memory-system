package database

import (
	"fmt"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/eliminyro/memory-system/internal/authletstore"
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

// TenantColumnDefaults are the operator-chosen DB-level defaults for the three
// per-tenant feature toggles. Migrate applies them via ALTER COLUMN SET DEFAULT
// after AutoMigrate, so any future raw INSERT into tenants picks up the
// operator's deploy-time choice.
type TenantColumnDefaults struct {
	StalenessMode      string
	DuplicateGuard     bool
	CleanupScanEnabled bool
}

func Migrate(db *gorm.DB, dimensions int, td TenantColumnDefaults) error {
	// Check existing vector dimensions BEFORE AutoMigrate (which may reset atttypmod).
	// If sections table exists with data, verify dimensions match.
	var existingDim int
	dimErr := db.Raw(`
		SELECT vector_dims(embedding) FROM sections WHERE embedding IS NOT NULL LIMIT 1
	`).Scan(&existingDim).Error
	if dimErr == nil && existingDim > 0 && existingDim != dimensions {
		return fmt.Errorf("embedding dimension mismatch: existing vectors are %d-dim, config wants %d — re-embed all data to fix", existingDim, dimensions)
	}

	// All schema changes, backfills, and seed data in a single transaction so a
	// crash mid-migration rolls back cleanly. Postgres supports DDL in
	// transactions, so this covers CREATE / ALTER / UPDATE / INSERT uniformly.
	return db.Transaction(func(tx *gorm.DB) error {
		return migrateInTx(tx, dimensions, td)
	})
}

func migrateInTx(tx *gorm.DB, dimensions int, td TenantColumnDefaults) error {
	// Migrate all models — Tenant first (referenced by Document, APIKey, TenantUser)
	if err := tx.AutoMigrate(
		&models.Tenant{},
		&models.TenantUser{},
		&models.APIKey{},
		&models.Document{},
		&models.Section{},
		&models.StalenessThreshold{},
		&models.OverrideLog{},
		&models.CleanupQueue{},
		&models.DeletionEvent{},
		// authlet tables — Phase A of authlet integration
		&authletstore.OAuthClient{},
		&authletstore.OAuthCode{},
		&authletstore.OAuthRefreshToken{},
		&authletstore.FamilyRevocation{},
		&authletstore.AuthletSigningKey{},
	); err != nil {
		return fmt.Errorf("auto-migrate: %w", err)
	}

	// Bootstrap tenant: insert default tenant for existing data
	if err := tx.Exec(`
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
	if err := tx.Exec(alterSQL).Error; err != nil {
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
		// Backfill verified_at for existing sections — treat first-time as verified at creation.
		// Only sets NULL values, so re-runs are no-ops.
		`UPDATE sections SET verified_at = created_at WHERE verified_at IS NULL`,
		// Backfill doc_type based on category/subcategory patterns for legacy docs.
		// Order matters: most specific first. Uses WHERE doc_type = 'reference' so re-runs
		// preserve any explicit classifications set after migration.
		`UPDATE documents SET doc_type = 'project_state' WHERE doc_type = 'reference' AND category = 'projects' AND slug = 'state'`,
		`UPDATE documents SET doc_type = 'audit' WHERE doc_type = 'reference' AND category = 'projects' AND (slug LIKE '%audit%' OR slug LIKE '%plan%' OR slug LIKE '%design%' OR slug LIKE '%backlog%')`,
		`UPDATE documents SET doc_type = 'learning' WHERE doc_type = 'reference' AND category = 'learnings'`,
		`UPDATE documents SET doc_type = 'preference' WHERE doc_type = 'reference' AND category = 'preferences'`,
		`UPDATE documents SET doc_type = 'tool' WHERE doc_type = 'reference' AND category = 'tools'`,
	}

	for _, m := range migrations {
		if err := tx.Exec(m).Error; err != nil {
			return fmt.Errorf("migration: %w", err)
		}
	}

	// Apply operator-chosen DB-level defaults for the three tenant toggles.
	// Values are pre-validated by config.ParseTenantDefaults to a fixed enum,
	// so direct interpolation is safe (Postgres DDL doesn't accept bind params).
	tenantDefaultMigrations := []string{
		fmt.Sprintf(`ALTER TABLE tenants ALTER COLUMN staleness_mode SET DEFAULT '%s'`, td.StalenessMode),
		fmt.Sprintf(`ALTER TABLE tenants ALTER COLUMN duplicate_guard SET DEFAULT %t`, td.DuplicateGuard),
		fmt.Sprintf(`ALTER TABLE tenants ALTER COLUMN cleanup_scan_enabled SET DEFAULT %t`, td.CleanupScanEnabled),
	}
	for _, m := range tenantDefaultMigrations {
		if err := tx.Exec(m).Error; err != nil {
			return fmt.Errorf("apply tenant column default: %w", err)
		}
	}

	// Seed staleness_thresholds — idempotent via ON CONFLICT DO NOTHING so operator
	// can tweak rows afterwards without the seed overwriting them.
	for _, t := range models.DefaultStalenessThresholds {
		if err := tx.Exec(
			`INSERT INTO staleness_thresholds (doc_type, days) VALUES (?, ?) ON CONFLICT (doc_type) DO NOTHING`,
			t.DocType, t.Days,
		).Error; err != nil {
			return fmt.Errorf("seed staleness threshold %s: %w", t.DocType, err)
		}
	}

	return nil
}
