package main

import (
	"log/slog"
	"os"

	"github.com/eliminyro/memory-mcp/internal/config"
	"github.com/eliminyro/memory-mcp/internal/database"
)

func main() {
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

	slog.Info("memory-mcp starting", "addr", cfg.ServerAddr)
	_ = db // will be used in next task
}
