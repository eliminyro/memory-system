package main

import (
	"log/slog"
	"os"

	"github.com/eliminyro/memory-mcp/internal/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	slog.Info("memory-mcp starting", "addr", cfg.ServerAddr)
}
