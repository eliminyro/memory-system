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

// selfCheckBootstrapAdmins verifies that every configured bootstrap admin that
// resolves to a tenant_users row actually passes an admin Check after seeding.
// A resolvable admin that fails the Check means the authz wiring is broken and
// the deploy would lock everyone out of admin — that is fatal (returns an
// error). Emails with no tenant_users row are skipped with a warning (there was
// nothing to seed; the grant happens once the user exists). An empty allowlist
// warns and returns nil.
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

	// Wire log level
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

	// Admin email allowlist — bootstrap-only now. It is read at startup to seed
	// system:memory#admin tuples (below) and is never consulted per request:
	// every authorization decision flows through the tuple Check.
	var allowedEmails []string
	if cfg.AdminAllowedEmails != "" {
		allowedEmails = strings.Split(cfg.AdminAllowedEmails, ",")
	}
	for i := range allowedEmails {
		allowedEmails[i] = strings.TrimSpace(allowedEmails[i])
	}

	// Authorization: relation-tuple store + Check engine. The engine is the
	// single authoritative gate — admin split, tenant_id override, and all
	// cross-tenant/common-pool point access resolve through it.
	authzStore := authz.NewPostgresStore(db)
	authzEngine := authz.NewEngine(authzStore)

	// Bootstrap the global-admin allowlist into system:memory#admin tuples.
	// Read ADMIN_ALLOWED_EMAILS at startup only; idempotent, skips emails with
	// no tenant_user row.
	if err := authzseed.BootstrapAdmins(context.Background(), authzStore, db, allowedEmails, slog.Default()); err != nil {
		slog.Error("failed to bootstrap admin tuples", "error", err)
		os.Exit(1)
	}

	// Startup self-check (task 6.3): every configured bootstrap admin that has a
	// tenant_user row MUST pass an admin Check. If one was seeded but does not
	// resolve to admin, the authorization wiring is broken and continuing would
	// silently ship a lockout — refuse to serve. Emails with no tenant_user row
	// (nothing to seed yet) log a warning and are skipped, mirroring
	// BootstrapAdmins. No admin configured at all: warn and continue.
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

	// Cleanup pipeline — nightly near-duplicate scan + Telegram summary.
	// Notifier is nil when Telegram creds aren't set, in which case the scanner
	// runs silently.
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

	// authlet OAuth 2.1 / OIDC AS. When both Google client envs are set,
	// the operator has opted into OAuth and any Setup error (malformed
	// master key, OIDC discovery failure, etc) is fatal — the legacy
	// silent-downgrade behavior masked deployment bugs. When the Google
	// envs are unset, /mcp serves API-key auth only and Setup is skipped.
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
		// Start the AS cleanup goroutine (expires codes / refresh tokens /
		// DCR clients and rotates signing keys). Returns a channel that
		// closes on rootCtx cancellation; we don't wait on it here — the
		// http.Server shutdown is the bound for our exit.
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
