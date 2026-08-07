package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/eliminyro/authlet/pkg/idp"
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
	"github.com/eliminyro/memory-system/internal/panicguard"
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

// resetBootstrapper is the subset of *service.MemoryService that
// maybeResetBootstrap needs, narrowed so the boot-time decision is
// unit-testable without a database.
type resetBootstrapper interface {
	ResetBootstrap(ctx context.Context) error
}

// maybeResetBootstrap implements the break-glass reset (task 6.1, design D5):
// a boot-time-only signal, never a route. When reset (cfg.MemoryReset /
// MEMORY_RESET) is set, it calls ResetBootstrap to clear the admin-key set +
// system#admin tuple(s) so bootstrap re-arms; all tenants, documents, and
// memories are preserved (ResetBootstrap only touches keys/tuples — see
// internal/service/memory.go). No HTTP handler reads cfg.MemoryReset, so this
// call is the only trigger path — it cannot be invoked over the network.
func maybeResetBootstrap(ctx context.Context, reset bool, svc resetBootstrapper, logger *slog.Logger) error {
	if !reset {
		return nil
	}
	if err := svc.ResetBootstrap(ctx); err != nil {
		return fmt.Errorf("reset bootstrap: %w", err)
	}
	logger.Warn("MEMORY_RESET set: cleared admin set, bootstrap re-armed")
	return nil
}

// generateBootstrapToken returns a hex-encoded 32-byte random token from
// crypto/rand for the first-run HTTP bootstrap gate. Held only in memory and
// regenerated every boot (design D1); never persisted.
func generateBootstrapToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate bootstrap token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// armBootstrapToken is the boot-time decision seam (design D1), kept pure and
// DB-free — the caller performs the HasAnyAdmin lookup and passes the result in.
// On an un-bootstrapped instance (hasAdmin == false) it generates a token, logs
// it at WARN so it is unmissable in `docker logs`, and returns it for
// MemoryService.BootstrapToken. When an admin already exists it generates and
// logs nothing and returns "", leaving the HTTP bootstrap path failing closed.
func armBootstrapToken(hasAdmin bool, logger *slog.Logger) (string, error) {
	if hasAdmin {
		return "", nil
	}
	token, err := generateBootstrapToken()
	if err != nil {
		return "", err
	}
	logger.Warn("instance not bootstrapped: POST this one-time token to /bootstrap (or run `memory-admin bootstrap`) to provision the first admin; regenerated every boot, never persisted",
		"bootstrap_token", token)
	return token, nil
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
	importJobRepo := repository.NewImportJobRepository(db)

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
	// Admin-email seeding (design D4) is only meaningful when OAuth logins resolve.
	memorySvc.OAuthConfigured = cfg.AuthletEnabled()
	// Toggle defaults stamped onto every tenant created through the service.
	memorySvc.TenantDefaults = cfg.TenantDefaults
	// Global default self-service policy; per-tenant overrides resolve against it.
	memorySvc.SelfServicePolicyDefault = cfg.SelfServicePolicy

	// Root context for background work — cancelled on SIGINT/SIGTERM so the
	// cleanup scanner and HTTP server shut down together.
	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Break-glass reset (task 6.1): must run BEFORE the HasAnyAdmin / bootstrap-token
	// arming below. MEMORY_RESET clears the admin set + system#admin tuple(s), so if
	// this ran after the check, hasAdmin would reflect the pre-reset state and the
	// token would be armed to "" (nothing logged) while the /bootstrap gate is left
	// open — HTTP bootstrap unusable until the next restart. Running it here makes
	// hasAdmin and the generated+logged token reflect the POST-reset state. DB is
	// connected, migrations ran, and memorySvc is constructed above; this runs before
	// anything else starts (scanners, authlet, HTTP server). See maybeResetBootstrap.
	if err := maybeResetBootstrap(rootCtx, cfg.MemoryReset, memorySvc, slog.Default()); err != nil {
		slog.Error("MEMORY_RESET: reset failed; refusing to start", "error", err)
		os.Exit(1)
	}

	// First-run provisioning (design D1): on an un-bootstrapped instance generate a
	// one-time bootstrap token and log it at WARN; when an admin already exists,
	// generate/log nothing and leave BootstrapToken empty so the HTTP path fails closed.
	hasAdmin, err := memorySvc.HasAnyAdmin(context.Background())
	if err != nil {
		slog.Error("failed to check bootstrap state", "error", err)
		os.Exit(1)
	}
	bootstrapToken, err := armBootstrapToken(hasAdmin, slog.Default())
	if err != nil {
		slog.Error("failed to arm bootstrap token", "error", err)
		os.Exit(1)
	}
	memorySvc.BootstrapToken = bootstrapToken

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

	// Async document-import worker (design D7). Same background-goroutine lifecycle
	// as the cleanup scanner, bound to rootCtx: it sweeps interrupted jobs on start
	// (D9), then drains import_jobs honoring IMPORT_WORKER_CONCURRENCY.
	importWorker := service.NewImportWorker(
		importJobRepo, memorySvc.ImportDocuments,
		cfg.ImportWorkerConcurrency, 2*time.Second, slog.Default(),
	)
	importWorker.Start(rootCtx)

	// MCP server — admin split resolves through the same Check engine.
	mcpServer := mcp.NewServer(memorySvc, authzEngine)

	// Auth
	keyValidator := auth.NewAPIKeyValidator(db)

	// authlet OAuth 2.1 / OIDC AS. Both Google envs set = opt-in: any Setup error
	// is fatal (silent downgrade masked deploy bugs). Unset = /mcp is API-key-only,
	// Setup skipped.
	var authletWiring *authletas.Wiring
	if cfg.AuthletEnabled() {
		authletStore := authletstore.New(db)

		// Idempotently register the public PKCE OAuth client the /ui SPA
		// authenticates as, so no operator hand-inserts an oauth_clients row.
		// Safe here: database.Migrate (above) created the authlet tables. Logs
		// at INFO when it creates or updates the client (no secret — public).
		if err := authletstore.SeedUIClient(rootCtx, authletStore, cfg.UIClientID, cfg.PublicBaseURL, slog.Default()); err != nil {
			slog.Error("failed to seed UI OAuth client", "error", err)
			os.Exit(1)
		}

		authletWiring, err = authletas.Setup(
			rootCtx,
			db,
			authletStore,
			cfg.GoogleClientID,
			cfg.GoogleClientSecret,
			cfg.PublicBaseURL,
			slog.Default(),
		)
		if err != nil {
			slog.Error("authlet setup failed", "error", err)
			os.Exit(1)
		}
		// Self-serve signup: on a verified-email login with no tenant_users row,
		// auto-provision a personal tenant, gated by SIGNUP_ALLOWED_DOMAINS. The
		// service owns the gate + creation (leaf authletas can't reach it); we
		// translate its not-allowed sentinel into the authletas one so the
		// resolver maps it to a 403. hd is best-effort from Raw (idp.Claims has
		// no typed field); empty ⇒ email-domain match only.
		authletWiring.Resolver.Provision = func(ctx context.Context, c idp.Claims) (string, error) {
			hd, _ := c.Raw["hd"].(string)
			tid, provErr := memorySvc.ProvisionPersonalTenant(ctx, c.Email, c.Name, hd, cfg.SignupAllowedDomains)
			if errors.Is(provErr, service.ErrSignupNotAllowed) {
				return "", authletas.ErrProvisionNotAllowed
			}
			return tid, provErr
		}
		// Loud operator safety net: an empty allow-list means ANY verified
		// identity can self-provision. A garbled restrictive value also
		// normalizes to empty, so warn rather than fail closed here.
		if len(cfg.SignupAllowedDomains) == 0 {
			slog.Warn("self-serve signup is PUBLIC: any verified OAuth identity can provision a tenant — set SIGNUP_ALLOWED_DOMAINS to restrict",
				"event", "signup.public")
		}
		// AS cleanup goroutine: expires codes/tokens/DCR clients, rotates signing
		// keys. Returns a channel closed on ctx cancel; we don't wait on it
		// (http.Server shutdown bounds our exit).
		_ = authletWiring.RunCleanup(rootCtx)
	}

	handler := server.NewHandler(server.Deps{
		DB:                         db,
		MCPServer:                  mcpServer,
		KeyValidator:               keyValidator,
		AuthletWiring:              authletWiring,
		Memory:                     memorySvc,
		UIClientID:                 cfg.UIClientID,
		PublicBaseURL:              cfg.PublicBaseURL,
		MaxRequestBytes:            cfg.MaxRequestBytes,
		RateLimitRPS:               cfg.RateLimitRPS,
		RateLimitBurst:             cfg.RateLimitBurst,
		RateLimitTrustedProxyDepth: cfg.RateLimitTrustedProxyDepth,

		ImportJobs:           importJobRepo,
		ImportMaxUploadBytes: cfg.ImportMaxUploadBytes,
	})

	srv := &http.Server{
		Addr:         cfg.ServerAddr,
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
	}

	go func() {
		defer panicguard.Recover(slog.Default(), "http server goroutine")
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
