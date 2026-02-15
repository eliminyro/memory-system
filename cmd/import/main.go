package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/eliminyro/memory-mcp/internal/config"
	"github.com/eliminyro/memory-mcp/internal/database"
	"github.com/eliminyro/memory-mcp/internal/repository"
	"github.com/eliminyro/memory-mcp/internal/service"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: import <memory-directory>\n")
		fmt.Fprintf(os.Stderr, "Example: import ~/.claude/context/memory\n")
		os.Exit(1)
	}

	memoryDir := os.Args[1]

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

	if err := database.Migrate(db); err != nil {
		slog.Error("failed to run migrations", "error", err)
		os.Exit(1)
	}

	docRepo := repository.NewDocumentRepository(db)
	sectionRepo := repository.NewSectionRepository(db)
	embedder := service.NewEmbedder(cfg.OllamaURL, cfg.OllamaModel)
	memorySvc := service.NewMemoryService(db, docRepo, sectionRepo, embedder)

	ctx := context.Background()
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

		_, err = memorySvc.StoreDocument(ctx, category, subcategory, slug, string(content))
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
// and "preferences/workflow.md" -> ("preferences", nil, "workflow")
// and "work-status.md" -> ("misc", nil, "work-status")
func parsePath(rel string) (category string, subcategory *string, slug string) {
	rel = strings.TrimSuffix(rel, ".md")
	parts := strings.Split(filepath.ToSlash(rel), "/")

	switch len(parts) {
	case 3:
		// learnings/go/gorm
		sub := parts[1]
		return parts[0], &sub, parts[2]
	case 2:
		// preferences/workflow
		return parts[0], nil, parts[1]
	case 1:
		// work-status (top-level)
		return "misc", nil, parts[0]
	default:
		return "", nil, ""
	}
}
