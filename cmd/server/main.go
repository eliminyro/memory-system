package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/authletas"
	"github.com/eliminyro/memory-system/internal/authletstore"
	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/authzseed"
	"github.com/eliminyro/memory-system/internal/cleanup"
	"github.com/eliminyro/memory-system/internal/config"
	"github.com/eliminyro/memory-system/internal/database"
	"github.com/eliminyro/memory-system/internal/mcp"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/server"
	"github.com/eliminyro/memory-system/internal/service"
	"github.com/eliminyro/memory-system/internal/staleness"
)

// selfCheckBootstrapAdmins verifies every configured bootstrap admin with a
// tenant_users row passes an admin Check after seeding. A resolvable admin that
// fails means broken authz wiring / an admin lockout — fatal (error). Emails with
// no row are skipped (warn); an empty allowlist warns and returns nil.
func selfCheckBootstrapAdmins(ctx context.Context, engine *authz.Engine, db *gorm.DB, emails []string, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	configured := 0
	for _, email := range emails {
		if email == "" {
			continue
		}
		configured++
		var tu models.TenantUser
		if err := db.WithContext(ctx).Where("email = ?", email).Limit(1).Find(&tu).Error; err != nil {
			return fmt.Errorf("self-check: lookup admin %q: %w", email, err)
		}
		if tu.ID == uuid.Nil {
			logger.Warn("admin self-check: no tenant_user row yet, skipping", "email", email)
			continue
		}
		ok, err := engine.Check(ctx, authz.TypeSystem, authz.SystemObjectID, authz.RelAdmin, authz.TypeUser, tu.ID.String())
		if err != nil {
			return fmt.Errorf("self-check: admin Check for %q errored: %w", email, err)
		}
		if !ok {
			return fmt.Errorf("self-check: configured admin %q (subject %s) does not hold system:%s#%s after bootstrap",
				email, tu.ID, authz.SystemObjectID, authz.RelAdmin)
		}
		logger.Info("admin self-check passed", "email", email, "subject_id", tu.ID.String())
	}
	if configured == 0 {
		logger.Warn("no bootstrap admins configured (ADMIN_ALLOWED_EMAILS empty); admin tools will be unavailable until an admin tuple is seeded")
	}
	return nil
}

func main() {
	// --opts overrides MEMORY_DEFAULT_OPTS. Format:
	//   --opts staleness=off,duplicate_guard=false,cleanup_scan_enabled=false
	optsFlag := flag.String("opts", "", "tenant-toggle defaults (key=value,...): staleness, duplicate_guard, cleanup_scan_enabled")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	// CLI flag wins over env var.
	if *optsFlag != "" {
		td, err := config.ParseTenantDefaults(*optsFlag)
		if err != nil {
			slog.Error("invalid --opts", "error", err)
			os.Exit(1)
		}
		cfg.TenantDefaults = td
	}

	var level slog.Level
	_ = level.UnmarshalText([]byte(cfg.LogLevel))
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	db, err := database.Connect(cfg.DatabaseURL)
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}

	if err := database.Migrate(db, cfg.EmbeddingProvider, cfg.EmbeddingModel(), cfg.EmbeddingDimensions, database.TenantColumnDefaults{
		StalenessMode:      cfg.TenantDefaults.StalenessMode,
		DuplicateGuard:     cfg.TenantDefaults.DuplicateGuard,
		CleanupScanEnabled: cfg.TenantDefaults.CleanupScanEnabled,
	}); err != nil {
		slog.Error("failed to run migrations", "error", err)
		os.Exit(1)
	}

	// Repositories
	docRepo := repository.NewDocumentRepository(db)
	sectionRepo := repository.NewSectionRepository(db)
	tenantRepo := repository.NewTenantRepository(db)
	keyRepo := repository.NewAPIKeyRepository(db)
	lintRepo := repository.NewLintRepository(db)
	overrideRepo := repository.NewOverrideLogRepository(db)
	cleanupRepo := repository.NewCleanupQueueRepository(db)
	retentionRepo := repository.NewRetentionRepository(db)

	// Staleness threshold cache — loads from staleness_thresholds table.
	thresholdStore := staleness.NewThresholdStore(db)

	// Embedding provider
	embedder, err := service.NewEmbeddingProvider(cfg.EmbeddingProvider, cfg.EmbeddingCfg())
	if err != nil {
		slog.Error("failed to create embedding provider", "error", err)
		os.Exit(1)
	}

	// Admin email allowlist — bootstrap-only: read at startup to seed
	// system:memory#admin tuples, never consulted per request (all decisions go through tuple Check).
	var allowedEmails []string
	if cfg.AdminAllowedEmails != "" {
		allowedEmails = strings.Split(cfg.AdminAllowedEmails, ",")
	}
	for i := range allowedEmails {
		allowedEmails[i] = strings.TrimSpace(allowedEmails[i])
	}

	// Authorization: relation-tuple store + Check engine — the single gate for
	// admin split, tenant_id override, and cross-tenant/common-pool access.
	authzStore := authz.NewPostgresStore(db)
	authzEngine := authz.NewEngine(authzStore)

	// Bootstrap the admin allowlist into system:memory#admin tuples. Startup-only,
	// idempotent, skips emails with no tenant_user row.
	if err := authzseed.BootstrapAdmins(context.Background(), authzStore, db, allowedEmails, slog.Default()); err != nil {
		slog.Error("failed to bootstrap admin tuples", "error", err)
		os.Exit(1)
	}

	// Startup self-check (task 6.3): a seeded admin that fails an admin Check means
	// broken authz wiring / a lockout — refuse to serve. See selfCheckBootstrapAdmins.
	if err := selfCheckBootstrapAdmins(context.Background(), authzEngine, db, allowedEmails, slog.Default()); err != nil {
		slog.Error("admin authorization self-check failed; refusing to serve", "error", err)
		os.Exit(1)
	}

	// Services
	memorySvc := service.NewMemoryService(db, docRepo, sectionRepo, embedder, tenantRepo, keyRepo, lintRepo, thresholdStore, overrideRepo, cleanupRepo, authzStore)

	// Root context for background work — cancelled on SIGINT/SIGTERM so the
	// cleanup scanner and HTTP server shut down together.
	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Cleanup pipeline — nightly near-duplicate scan + Telegram summary. Notifier
	// is nil (silent) when Telegram creds are unset.
	cleanupNotifier := cleanup.NewNotifier(cfg.TelegramBotToken, cfg.TelegramChatID)
	cleanupScanner := cleanup.NewScanner(
		lintRepo, tenantRepo, cleanupRepo, retentionRepo, thresholdStore,
		cfg.RetentionMultiplier, cfg.DeleteGraceDays,
		cleanupNotifier, slog.Default(),
	)
	if cfg.CleanupEnabled {
		cleanupScanner.Start(rootCtx, time.Duration(cfg.CleanupIntervalHours)*time.Hour)
	}

	// MCP server — admin split resolves through the same Check engine.
	mcpServer := mcp.NewServer(memorySvc, authzEngine)

	// Auth
	keyValidator := auth.NewAPIKeyValidator(db)

	// authlet OAuth 2.1 / OIDC AS. Both Google envs set = opt-in: any Setup error
	// is fatal (silent downgrade masked deploy bugs). Unset = /mcp is API-key-only,
	// Setup skipped.
	var authletWiring *authletas.Wiring
	if cfg.AuthletEnabled() {
		authletWiring, err = authletas.Setup(
			rootCtx,
			db,
			authletstore.New(db),
			cfg.GoogleClientID,
			cfg.GoogleClientSecret,
			cfg.PublicBaseURL,
			slog.Default(),
		)
		if err != nil {
			slog.Error("authlet setup failed", "error", err)
			os.Exit(1)
		}
		// AS cleanup goroutine: expires codes/tokens/DCR clients, rotates signing
		// keys. Returns a channel closed on ctx cancel; we don't wait on it
		// (http.Server shutdown bounds our exit).
		_ = authletWiring.RunCleanup(rootCtx)
	}

	handler := server.NewHandler(server.Deps{
		DB:              db,
		MCPServer:       mcpServer,
		KeyValidator:    keyValidator,
		AuthletWiring:   authletWiring,
		Memory:          memorySvc,
		UIClientID:      cfg.UIClientID,
		PublicBaseURL:   cfg.PublicBaseURL,
		MaxRequestBytes: cfg.MaxRequestBytes,
		RateLimitRPS:    cfg.RateLimitRPS,
		RateLimitBurst:  cfg.RateLimitBurst,
	})

	srv := &http.Server{
		Addr:         cfg.ServerAddr,
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
	}

	go func() {
		slog.Info("memory-mcp listening", "addr", cfg.ServerAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	<-rootCtx.Done()
	slog.Info("shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("server shutdown error", "error", err)
	}
}
