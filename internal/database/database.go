package database

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
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
		// Match logger.Default, but ignore ErrRecordNotFound: it is control flow
		// here (callers check errors.Is(err, gorm.ErrRecordNotFound)), not an
		// error worth logging. Without this the import worker's empty-queue poll
		// — SELECT ... FOR UPDATE SKIP LOCKED via First() — spams a "record not
		// found" line on every tick.
		Logger: logger.New(
			log.New(os.Stdout, "\r\n", log.LstdFlags),
			logger.Config{
				SlowThreshold:             200 * time.Millisecond,
				LogLevel:                  logger.Warn,
				IgnoreRecordNotFoundError: true,
				Colorful:                  true,
			},
		),
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

// migrateAdvisoryLock serializes schema migration across replicas: gorm
// AutoMigrate is check-then-act and not concurrency-safe, so concurrent boots
// race the DDL and crashloop. A tx-scoped advisory lock (like retention /
// bootstrap / signing-key activation) makes losers block, then observe a
// fully-migrated schema so their AutoMigrate/DDL is a no-op. Use a distinct
// fixed key (not shared with retention/rotation locks).
const migrateAdvisoryLock int64 = 0x4D49475241544531 // "MIGRATE1"

// TenantColumnDefaults are the operator-chosen DB-level defaults for the three
// per-tenant toggles. Migrate applies them via ALTER COLUMN SET DEFAULT after
// AutoMigrate, so raw INSERTs into tenants pick up the deploy-time choice.
type TenantColumnDefaults struct {
	StalenessMode      string
	DuplicateGuard     bool
	CleanupScanEnabled bool
}

// GlobalConfigDefaults are the env-derived bootstrap values seeded into the
// instance_config singleton at migrate time; once the row exists a stored value
// wins (seed is ON CONFLICT DO NOTHING).
type GlobalConfigDefaults struct {
	MMRLambda             float64
	StalenessPenalty      float64
	CandidatePool         int
	SnippetChars          int
	HistoryRetentionDays  int
	StalenessDefault      string
	DuplicateGuardDefault bool
	CleanupScanDefault    bool
	DuplicateThreshold    float64
	SelfServicePolicy     string
	SignupDomains         string
	AdminEmails           string
	CleanupEnabled        bool
	CleanupIntervalHours  int
	RateLimitRPS          float64
	RateLimitBurst        int
	TrustedProxyDepth     int
	MaxRequestBytes       int64
	LogLevel              string
	WebhookURL            string
}

// BaselineGlobalConfigDefaults returns the built-in seed values (matching the
// InstanceConfig column defaults) for callers that don't source them from env.
func BaselineGlobalConfigDefaults() GlobalConfigDefaults {
	return GlobalConfigDefaults{
		MMRLambda: 0.5, StalenessPenalty: 0.2, CandidatePool: 20, SnippetChars: 400, HistoryRetentionDays: 90,
		StalenessDefault: models.StalenessModeHard, DuplicateGuardDefault: true, CleanupScanDefault: true,
		DuplicateThreshold: 0.85, SelfServicePolicy: models.SelfServicePolicyOpen,
		CleanupEnabled: true, CleanupIntervalHours: 24,
		RateLimitRPS: 20, RateLimitBurst: 40, TrustedProxyDepth: 0, MaxRequestBytes: 1048576,
		LogLevel: "info",
	}
}

func Migrate(db *gorm.DB, provider, model string, dimensions int, td TenantColumnDefaults, gc GlobalConfigDefaults) error {
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
		// lock_timeout bounds the DDL table-lock waits so a stuck migration
		// surfaces instead of hanging boot forever.
		if err := tx.Exec("SET LOCAL lock_timeout = '60s'").Error; err != nil {
			return fmt.Errorf("set migrate lock_timeout: %w", err)
		}
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", migrateAdvisoryLock).Error; err != nil {
			return fmt.Errorf("acquire migrate advisory lock: %w", err)
		}
		return migrateInTx(tx, provider, model, dimensions, corpusPopulated, td, gc)
	})
}

func migrateInTx(tx *gorm.DB, provider, model string, dimensions int, corpusPopulated bool, td TenantColumnDefaults, gc GlobalConfigDefaults) error {
	// Migrate all models — Tenant first (referenced by Document, APIKey, TenantUser)
	if err := tx.AutoMigrate(
		&models.Tenant{},
		&models.TenantUser{},
		&models.APIKey{},
		&models.Document{},
		&models.Section{},
		&models.Edge{},
		&models.StalenessThreshold{},
		&models.OverrideLog{},
		&models.CleanupQueue{},
		&models.DeletionEvent{},
		&models.MutationHistory{},
		&models.EmbeddingMetadata{},
		&models.InstanceConfig{},
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

	// Bootstrap tenant: insert the default tenant for existing data, carrying the
	// operator-chosen toggle defaults (td). This insert runs before the ALTER COLUMN
	// SET DEFAULT block below, so it must set the toggles explicitly or the default
	// pool would fall to the static off/false/false column defaults and diverge from
	// every tenant created via CreateTenant. td is pre-validated by
	// config.ParseTenantDefaults, so interpolation is safe (DDL rejects bind params).
	bootstrapSQL := fmt.Sprintf(`
		INSERT INTO tenants (id, name, staleness_mode, duplicate_guard, cleanup_scan_enabled, created_at, updated_at)
		VALUES ('00000000-0000-0000-0000-000000000001', 'default', '%s', %t, %t, NOW(), NOW())
		ON CONFLICT (id) DO NOTHING
	`, td.StalenessMode, td.DuplicateGuard, td.CleanupScanEnabled)
	if err := tx.Exec(bootstrapSQL).Error; err != nil {
		return fmt.Errorf("bootstrap tenant: %w", err)
	}

	// Ensure embedding column has the correct dimension
	alterSQL := fmt.Sprintf(
		`ALTER TABLE sections ALTER COLUMN embedding TYPE vector(%d)`, dimensions,
	)
	if err := tx.Exec(alterSQL).Error; err != nil {
		return fmt.Errorf("set embedding dimension: %w", err)
	}

	// Structural DDL AutoMigrate can't express: the tsv generated column plus the
	// GIN / HNSW / partial-unique indexes. Idempotent; runs after AutoMigrate.
	schemaDDL := []string{
		`DO $$ BEGIN
			ALTER TABLE sections ADD COLUMN IF NOT EXISTS tsv tsvector
				GENERATED ALWAYS AS (to_tsvector('english', content)) STORED;
		EXCEPTION WHEN duplicate_column THEN NULL;
		END $$`,
		`CREATE INDEX IF NOT EXISTS idx_sections_tsv ON sections USING gin(tsv)`,
		`CREATE INDEX IF NOT EXISTS idx_sections_embedding ON sections USING hnsw (embedding vector_cosine_ops) WITH (m = 16, ef_construction = 64)`,
		// One ACTIVE doc per (tenant, path): partial unique so an archived row frees
		// the slot for re-store. AutoMigrate can't express a partial index.
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_doc_tenant_path_active ON documents (tenant_id, category, COALESCE(subcategory, ''), slug) WHERE archived_at IS NULL`,
		// One unresolved cleanup_queue row per (tenant, doc_a, doc_b): partial unique
		// keeps the check-then-insert Upsert race-safe; resolved rows don't block re-enqueue.
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_cleanup_pending_pair ON cleanup_queue (tenant_id, doc_a_id, doc_b_id) WHERE resolved_at IS NULL`,
		// One edge per (source, target, type); backs Create's idempotent-on-conflict path.
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_document_edges_triple ON document_edges (source_document_id, target_document_id, edge_type)`,
	}

	for _, m := range schemaDDL {
		if err := tx.Exec(m).Error; err != nil {
			return fmt.Errorf("schema ddl: %w", err)
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

	// Seed the singleton from env bootstrap defaults exactly once: fresh rows are
	// inserted seeded; a pre-existing row is upgraded to env (guarded by
	// globals_seeded so admin edits + the history toggle survive).
	if err := tx.Exec(
		`INSERT INTO instance_config
			(id, history_enabled, mmr_lambda, staleness_penalty, candidate_pool, snippet_chars,
			 history_retention_days, staleness_default, duplicate_guard_default, cleanup_scan_default,
			 duplicate_threshold, self_service_policy, signup_domains, admin_emails, cleanup_enabled,
			 cleanup_interval_hours, rate_limit_rps, rate_limit_burst, trusted_proxy_depth,
			 max_request_bytes, log_level, webhook_url, globals_seeded, updated_at)
		 VALUES (?, false, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, true, now())
		 ON CONFLICT (id) DO UPDATE SET
			mmr_lambda = EXCLUDED.mmr_lambda, staleness_penalty = EXCLUDED.staleness_penalty,
			candidate_pool = EXCLUDED.candidate_pool,
			snippet_chars = EXCLUDED.snippet_chars, history_retention_days = EXCLUDED.history_retention_days,
			staleness_default = EXCLUDED.staleness_default, duplicate_guard_default = EXCLUDED.duplicate_guard_default,
			cleanup_scan_default = EXCLUDED.cleanup_scan_default, duplicate_threshold = EXCLUDED.duplicate_threshold,
			self_service_policy = EXCLUDED.self_service_policy, signup_domains = EXCLUDED.signup_domains,
			admin_emails = EXCLUDED.admin_emails,
			cleanup_enabled = EXCLUDED.cleanup_enabled, cleanup_interval_hours = EXCLUDED.cleanup_interval_hours,
			rate_limit_rps = EXCLUDED.rate_limit_rps, rate_limit_burst = EXCLUDED.rate_limit_burst,
			trusted_proxy_depth = EXCLUDED.trusted_proxy_depth, max_request_bytes = EXCLUDED.max_request_bytes,
			log_level = EXCLUDED.log_level, webhook_url = EXCLUDED.webhook_url,
			globals_seeded = true, updated_at = now()
		 WHERE instance_config.globals_seeded = false`,
		models.InstanceConfigSingletonID,
		gc.MMRLambda, gc.StalenessPenalty, gc.CandidatePool, gc.SnippetChars, gc.HistoryRetentionDays,
		gc.StalenessDefault, gc.DuplicateGuardDefault, gc.CleanupScanDefault, gc.DuplicateThreshold,
		gc.SelfServicePolicy, gc.SignupDomains, gc.AdminEmails, gc.CleanupEnabled,
		gc.CleanupIntervalHours, gc.RateLimitRPS, gc.RateLimitBurst, gc.TrustedProxyDepth,
		gc.MaxRequestBytes, gc.LogLevel, gc.WebhookURL,
	).Error; err != nil {
		return fmt.Errorf("seed instance config: %w", err)
	}

	// Personal tenants use the owner relation instead of admin (personal-owner-role).
	// Flip roles BEFORE authzseed.Backfill so it derives tenant#owner (not #admin)
	// tuples for personal tenants. Shared tenants and all system#admin tuples/roles
	// are left untouched. Idempotent + re-runnable (a second boot finds no admin
	// rows on personal tenants).
	if err := tx.Exec(
		`UPDATE tenant_users SET role = ? WHERE role = ? AND tenant_id IN (SELECT id FROM tenants WHERE type = ?)`,
		models.TenantUserRoleOwner, models.TenantUserRoleAdmin, models.TenantTypePersonal,
	).Error; err != nil {
		return fmt.Errorf("personal-owner backfill (roles): %w", err)
	}

	// Backfill authz relation tuples from existing domain rows. Same transaction,
	// idempotent, safe on every migrate. Pass 1: tuples populated but not yet
	// driving any authorization decision.
	if err := authzseed.Backfill(context.Background(), authz.NewPostgresStore(tx), tx); err != nil {
		return fmt.Errorf("authz backfill: %w", err)
	}

	// After Backfill seeds the tenant#owner tuples, drop any stale tenant#admin
	// tuples left on personal tenants by a pre-owner-role deploy, so the end state
	// is owner-only. Scoped to object_type='tenant' + relation='admin' on personal
	// tenants; never touches object_type='system', so the founding operator retains
	// system#admin. Idempotent (a re-run finds nothing to delete).
	if err := tx.Exec(
		`DELETE FROM relation_tuples WHERE object_type = ? AND relation = ? AND object_id IN (SELECT id::text FROM tenants WHERE type = ?)`,
		authz.TypeTenant, authz.RelAdmin, models.TenantTypePersonal,
	).Error; err != nil {
		return fmt.Errorf("personal-owner backfill (stale admin tuples): %w", err)
	}

	// Revoke the residual system#admin left on personal tenants' service principals
	// by a pre-owner-role deploy's seedAdminServicePrincipals (granted while personal
	// owners were role=admin). Now that owners are `owner`, it no longer re-derives,
	// but the tuple persists and would make an owner-minted svc:<tenant> key a global
	// admin. Delete it — EXCEPT where an actual API key still resolves to that svc
	// principal (nil or explicit svc subject): that is the founding/bootstrap admin
	// key (or an operator-created svc key) and must keep its reach. Idempotent.
	if err := tx.Exec(
		`DELETE FROM relation_tuples rt
		 WHERE rt.object_type = ? AND rt.object_id = ? AND rt.relation = ? AND rt.subject_type = ?
		   AND rt.subject_id LIKE 'svc:%'
		   AND substring(rt.subject_id FROM 5) IN (SELECT id::text FROM tenants WHERE type = ?)
		   AND NOT EXISTS (
		     SELECT 1 FROM api_keys k
		     WHERE k.tenant_id::text = substring(rt.subject_id FROM 5)
		       AND (k.subject_id IS NULL OR k.subject_id = rt.subject_id)
		   )`,
		authz.TypeSystem, authz.SystemObjectID, authz.RelAdmin, authz.TypeUser,
		models.TenantTypePersonal,
	).Error; err != nil {
		return fmt.Errorf("personal-owner backfill (residual svc system#admin): %w", err)
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
