package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/authletas"
	"github.com/eliminyro/memory-system/internal/authletstore"
	"github.com/eliminyro/memory-system/internal/cleanup"
	"github.com/eliminyro/memory-system/internal/config"
	"github.com/eliminyro/memory-system/internal/database"
	"github.com/eliminyro/memory-system/internal/mcp"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/server"
	"github.com/eliminyro/memory-system/internal/service"
	"github.com/eliminyro/memory-system/internal/staleness"
)

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

	if err := database.Migrate(db, cfg.EmbeddingDimensions, database.TenantColumnDefaults{
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

	// Admin email allowlist (shared between OIDC middleware and memory service)
	var allowedEmails []string
	if cfg.AdminAllowedEmails != "" {
		allowedEmails = strings.Split(cfg.AdminAllowedEmails, ",")
	}

	// Services
	memorySvc := service.NewMemoryService(db, docRepo, sectionRepo, embedder, tenantRepo, keyRepo, lintRepo, thresholdStore, overrideRepo, cleanupRepo, allowedEmails)

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

	// MCP server
	mcpServer := mcp.NewServer(memorySvc, allowedEmails)

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
		DB:            db,
		MCPServer:     mcpServer,
		KeyValidator:  keyValidator,
		AuthletWiring: authletWiring,
		Memory:        memorySvc,
		UIClientID:    cfg.UIClientID,
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
