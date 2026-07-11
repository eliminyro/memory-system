package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/config"
	"github.com/eliminyro/memory-system/internal/database"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/service"
)

func main() {
	tenantFlag := flag.String("tenant", "", "Tenant UUID or name (required)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: import --tenant <uuid|name> [flags] <memory-directory>\n")
		fmt.Fprintf(os.Stderr, "Example: import --tenant default ~/.claude/context/memory\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() < 1 {
		flag.Usage()
		os.Exit(1)
	}

	// --tenant is mandatory: refuse to silently default to the shared
	// bootstrap/common pool, which would pollute every tenant's read scope.
	if *tenantFlag == "" {
		slog.Error("--tenant is required; refusing to default to the shared bootstrap/common pool")
		flag.Usage()
		os.Exit(1)
	}
	memoryDir := flag.Arg(0)

	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

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

	// Resolve the required tenant flag: UUID first, then name lookup.
	var tenantID uuid.UUID
	if parsed, err := uuid.Parse(*tenantFlag); err == nil {
		tenantID = parsed
	} else {
		tenantRepo := repository.NewTenantRepository(db)
		tenants, err := tenantRepo.List(context.Background())
		if err != nil {
			slog.Error("failed to list tenants", "error", err)
			os.Exit(1)
		}
		found := false
		for _, t := range tenants {
			if t.Name == *tenantFlag {
				tenantID = t.ID
				found = true
				break
			}
		}
		if !found {
			slog.Error("tenant not found", "name", *tenantFlag)
			os.Exit(1)
		}
	}
	slog.Info("importing for tenant", "tenant_id", tenantID)

	docRepo := repository.NewDocumentRepository(db)
	sectionRepo := repository.NewSectionRepository(db)

	embedder, err := service.NewEmbeddingProvider(cfg.EmbeddingProvider, cfg.EmbeddingCfg())
	if err != nil {
		slog.Error("failed to create embedding provider", "error", err)
		os.Exit(1)
	}

	// Real tuple store so bulk-imported documents get their document#tenant
	// parent edge seeded during import.
	memorySvc := service.NewMemoryService(db, docRepo, sectionRepo, embedder, nil, nil, nil, nil, nil, nil, authz.NewPostgresStore(db))

	// Inject tenant ID into context
	ctx := auth.WithTenantID(context.Background(), tenantID)
	var imported, failed int

	err = filepath.Walk(memoryDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		if strings.Contains(path, "CLAUDE.md") {
			return nil // Skip CLAUDE.md instruction files
		}

		rel, _ := filepath.Rel(memoryDir, path)
		category, subcategory, slug := parsePath(rel)

		if category == "" || slug == "" {
			slog.Warn("skipping unparseable path", "path", rel)
			return nil
		}

		content, err := os.ReadFile(path)
		if err != nil {
			slog.Error("failed to read file", "path", path, "error", err)
			failed++
			return nil
		}

		slog.Info("importing", "path", rel, "category", category, "subcategory", subcategory, "slug", slug)

		// Bulk import bypasses the duplicate guard — operator's intent is to
		// ingest files as-authored, not to prompt on every near-duplicate.
		_, err = memorySvc.StoreDocument(ctx, category, subcategory, slug, string(content), true, "bulk import", nil)
		if err != nil {
			slog.Error("failed to import", "path", rel, "error", err)
			failed++
			return nil
		}

		imported++
		return nil
	})

	if err != nil {
		slog.Error("walk error", "error", err)
		os.Exit(1)
	}

	slog.Info("import complete", "imported", imported, "failed", failed)
}

// parsePath converts "learnings/go/gorm.md" -> ("learnings", "go", "gorm")
func parsePath(rel string) (category string, subcategory *string, slug string) {
	rel = strings.TrimSuffix(rel, ".md")
	rel = filepath.ToSlash(rel)
	return models.ParsePath(rel)
}
