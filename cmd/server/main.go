package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/cleanup"
	"github.com/eliminyro/memory-system/internal/config"
	"github.com/eliminyro/memory-system/internal/database"
	"github.com/eliminyro/memory-system/internal/mcp"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/service"
	"github.com/eliminyro/memory-system/internal/staleness"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	// Wire log level
	var level slog.Level
	level.UnmarshalText([]byte(cfg.LogLevel))
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	db, err := database.Connect(cfg.DatabaseURL)
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}

	if err := database.Migrate(db, cfg.EmbeddingDimensions); err != nil {
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
	cleanupScanner := cleanup.NewScanner(lintRepo, tenantRepo, cleanupRepo, cleanupNotifier, slog.Default())
	if cfg.CleanupEnabled {
		cleanupScanner.Start(rootCtx, time.Duration(cfg.CleanupIntervalHours)*time.Hour)
	}

	// MCP server
	mcpServer := mcp.NewServer(memorySvc, allowedEmails)

	// Auth
	keyValidator := auth.NewAPIKeyValidator(db)
	apiKeyMW := auth.APIKeyMiddleware(keyValidator)

	// HTTP server
	mux := http.NewServeMux()

	// MCP endpoints (API key auth)
	mcpHTTP := mcpServer.HTTPHandler()
	mux.Handle("/mcp", apiKeyMW(mcpHTTP))
	mux.Handle("/mcp/", apiKeyMW(mcpHTTP))

	// Health
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		sqlDB, err := db.DB()
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("db: " + err.Error()))
			return
		}
		if err := sqlDB.PingContext(r.Context()); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("db ping: " + err.Error()))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ready"))
	})

	srv := &http.Server{
		Addr:         cfg.ServerAddr,
		Handler:      mux,
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
	srv.Shutdown(shutdownCtx)
}
