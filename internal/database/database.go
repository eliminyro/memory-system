package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"

	"github.com/eliminyro/memory-system/internal/authletstore"
	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/authzseed"
	"github.com/eliminyro/memory-system/internal/models"
)

func Connect(databaseURL string) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(databaseURL), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}

	// PostgreSQL is mandatory (audit #8.2): near-duplicate self-joins, pgvector,
	// and tsvector are Postgres-only; the sqlite gorm driver (unit tests only)
	// silently degrades them. Fail fast rather than serve a half-broken corpus.
	if name := db.Name(); name != "postgres" {
		return nil, fmt.Errorf("unsupported database dialect %q: memory-system requires PostgreSQL", name)
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
// per-tenant toggles. Migrate applies them via ALTER COLUMN SET DEFAULT after
// AutoMigrate, so raw INSERTs into tenants pick up the deploy-time choice.
type TenantColumnDefaults struct {
	StalenessMode      string
	DuplicateGuard     bool
	CleanupScanEnabled bool
}

func Migrate(db *gorm.DB, provider, model string, dimensions int, td TenantColumnDefaults) error {
	// Check existing vector dimensions BEFORE AutoMigrate (which may reset atttypmod).
	var existingDim int
	dimErr := db.Raw(`
		SELECT vector_dims(embedding) FROM sections WHERE embedding IS NOT NULL LIMIT 1
	`).Scan(&existingDim).Error
	corpusPopulated := dimErr == nil && existingDim > 0
	if corpusPopulated && existingDim != dimensions {
		return fmt.Errorf("embedding dimension mismatch: existing vectors are %d-dim, config wants %d — re-embed all data to fix", existingDim, dimensions)
	}

	// All DDL, backfills, and seeds in one transaction so a crash rolls back
	// cleanly. Postgres allows DDL in transactions: CREATE/ALTER/UPDATE/INSERT.
	return db.Transaction(func(tx *gorm.DB) error {
		return migrateInTx(tx, provider, model, dimensions, corpusPopulated, td)
	})
}

func migrateInTx(tx *gorm.DB, provider, model string, dimensions int, corpusPopulated bool, td TenantColumnDefaults) error {
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
		&models.EmbeddingMetadata{},
		&models.ImportJob{},
		// authlet tables — Phase A of authlet integration
		&authletstore.OAuthClient{},
		&authletstore.OAuthCode{},
		&authletstore.OAuthRefreshToken{},
		&authletstore.FamilyRevocation{},
		&authletstore.AuthletSigningKey{},
		// authorization engine relation tuples
		&authz.RelationTuple{},
	); err != nil {
		return fmt.Errorf("auto-migrate: %w", err)
	}

	// Embedding-identity guard (audit #13/#16): freeze the (provider, model, dim)
	// that built the corpus and refuse a swap that would corrupt
	// similarity/dedup/retention. After AutoMigrate so the table exists.
	if err := guardEmbeddingIdentity(tx, provider, model, dimensions, corpusPopulated); err != nil {
		return err
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
		// Backfill doc_type from category/subcategory for legacy docs. Most specific
		// first; WHERE doc_type = 'reference' so re-runs preserve explicit classifications.
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

	// Apply operator-chosen DB defaults for the three tenant toggles. Values are
	// pre-validated by config.ParseTenantDefaults, so interpolation is safe (DDL
	// rejects bind params).
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

	// Backfill authz relation tuples from existing domain rows. Same transaction,
	// idempotent, safe on every migrate. Pass 1: tuples populated but not yet
	// driving any authorization decision.
	if err := authzseed.Backfill(context.Background(), authz.NewPostgresStore(tx), tx); err != nil {
		return fmt.Errorf("authz backfill: %w", err)
	}

	return nil
}

// guardEmbeddingIdentity enforces that a populated corpus's embedding (provider,
// model, dimension) never changes silently (audit #13/#16).
//   - Empty corpus or no metadata row: adopt the current identity and proceed.
//   - Populated + recorded: refuse a differing provider OR model even at the same
//     dimension — cosine similarity, dedup, and retention are only comparable
//     within one embedding space. Dimension is validated earlier in Migrate.
func guardEmbeddingIdentity(tx *gorm.DB, provider, model string, dimensions int, corpusPopulated bool) error {
	var meta models.EmbeddingMetadata
	err := tx.Where("id = ?", models.EmbeddingMetadataSingletonID).First(&meta).Error
	metaMissing := errors.Is(err, gorm.ErrRecordNotFound)
	if err != nil && !metaMissing {
		return fmt.Errorf("read embedding metadata: %w", err)
	}

	if !corpusPopulated || metaMissing {
		rec := models.EmbeddingMetadata{
			ID:         models.EmbeddingMetadataSingletonID,
			Provider:   provider,
			Model:      model,
			Dimensions: dimensions,
			UpdatedAt:  time.Now(),
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{"provider", "model", "dimensions", "updated_at"}),
		}).Create(&rec).Error; err != nil {
			return fmt.Errorf("record embedding metadata: %w", err)
		}
		return nil
	}

	if meta.Provider != provider || meta.Model != model {
		return fmt.Errorf(
			"embedding provider/model change refused: corpus was built with provider=%q model=%q (dim %d), config now requests provider=%q model=%q (dim %d) — similarity/dedup/retention would silently corrupt; restore the previous EMBEDDING_PROVIDER/model or re-embed all data",
			meta.Provider, meta.Model, meta.Dimensions, provider, model, dimensions,
		)
	}

	// Same provider+model: sync the recorded dimension (already validated upstream).
	if meta.Dimensions != dimensions {
		if err := tx.Model(&models.EmbeddingMetadata{}).
			Where("id = ?", models.EmbeddingMetadataSingletonID).
			Update("dimensions", dimensions).Error; err != nil {
			return fmt.Errorf("sync embedding metadata dimension: %w", err)
		}
	}
	return nil
}
