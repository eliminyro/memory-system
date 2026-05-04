package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/eliminyro/memory-system/internal/agent/claude"
	"github.com/eliminyro/memory-system/internal/agent/config"
	"github.com/eliminyro/memory-system/internal/agent/mcpclient"
	"github.com/eliminyro/memory-system/internal/agent/pipeline"
	"github.com/eliminyro/memory-system/internal/agent/secret"
	"github.com/eliminyro/memory-system/internal/agent/state"
	"github.com/eliminyro/memory-system/internal/agent/transcript"
)

func main() {
	// Log to both stderr and a persistent log file
	logFile := setupLogFile()
	if logFile != nil {
		defer func() { _ = logFile.Close() }()
	}

	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: memory-agent <command> [args]\n")
		fmt.Fprintf(os.Stderr, "Commands:\n")
		fmt.Fprintf(os.Stderr, "  capture <session.jsonl>  Extract and store learnings from a session\n")
		os.Exit(1)
	}

	// Recursion guard
	if os.Getenv("MEMORY_AGENT_INVOKED") == "1" {
		slog.Info("recursion guard: skipping (MEMORY_AGENT_INVOKED=1)")
		return
	}
	_ = os.Setenv("MEMORY_AGENT_INVOKED", "1")

	switch os.Args[1] {
	case "capture":
		if err := runCapture(os.Args[2:]); err != nil {
			slog.Error("capture failed", "error", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}

func runCapture(args []string) error {
	defCfg, err := defaultConfigPath()
	if err != nil {
		return err
	}

	fs := flag.NewFlagSet("capture", flag.ExitOnError)
	configPath := fs.String("config", defCfg, "Path to config file")
	_ = fs.Parse(args) // ExitOnError handles real failures; nothing to check here.

	if fs.NArg() < 1 {
		return fmt.Errorf("usage: memory-agent capture <session.jsonl>")
	}
	sessionPath := fs.Arg(0)

	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Check dedup state
	statePath, err := stateFilePath()
	if err != nil {
		return err
	}
	st := state.New(statePath)
	sessionID := filepath.Base(sessionPath)
	if st.AlreadyCaptured(sessionID) {
		slog.Info("session already captured, skipping", "session", sessionID)
		return nil
	}

	// Parse transcript
	session, err := transcript.Parse(sessionPath)
	if err != nil {
		return fmt.Errorf("parse transcript: %w", err)
	}
	if len(session.Messages) == 0 {
		slog.Info("empty session, skipping")
		return nil
	}

	// Create Claude client
	apiKey := cfg.APIKey
	if cfg.Auth == "api" && apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	claudeClient, err := claude.NewClient(cfg.Auth, apiKey)
	if err != nil {
		return fmt.Errorf("create claude client: %w", err)
	}

	// Create memory-mcp client
	mcpAPIKey, err := secret.Resolve(ctx, cfg.MemoryMCPAPIKey)
	if err != nil {
		return fmt.Errorf("resolve MCP API key: %w", err)
	}
	mcpClient := mcpclient.New(cfg.MemoryMCPURL, mcpAPIKey)

	// Agent 1: Extract candidates
	slog.Info("running extractor", "model", cfg.ExtractorModel(), "messages", len(session.Messages))
	candidates, err := pipeline.Extract(ctx, claudeClient, cfg.ExtractorModel(), session)
	if err != nil {
		return fmt.Errorf("extract: %w", err)
	}
	slog.Info("extracted candidates", "count", len(candidates))
	if len(candidates) == 0 {
		if err := st.MarkCaptured(sessionID); err != nil {
			slog.Warn("mark captured failed", "session", sessionID, "error", err)
		}
		return nil
	}

	// Agent 2: Review candidates
	slog.Info("running reviewer", "model", cfg.ReviewerModel(), "candidates", len(candidates))
	reviewed, err := pipeline.Review(ctx, claudeClient, cfg.ReviewerModel(), mcpClient, candidates)
	if err != nil {
		return fmt.Errorf("review: %w", err)
	}

	// Write accepted/merged candidates
	accepted, merged, rejected, err := pipeline.Write(ctx, mcpClient, reviewed)
	if err != nil {
		return fmt.Errorf("write: %w", err)
	}

	slog.Info("capture complete", "accepted", accepted, "merged", merged, "rejected", rejected)

	if err := st.MarkCaptured(sessionID); err != nil {
		slog.Warn("mark captured failed", "session", sessionID, "error", err)
	}
	return nil
}

func defaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".claude", "memory-capture.yaml"), nil
}

func stateFilePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	dir := filepath.Join(home, ".claude", "state", "memory-capture")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create state dir: %w", err)
	}
	return filepath.Join(dir, "last-capture.json"), nil
}

func setupLogFile() *os.File {
	home, err := os.UserHomeDir()
	if err != nil {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
		return nil
	}

	logDir := filepath.Join(home, ".claude", "logs")
	_ = os.MkdirAll(logDir, 0700)
	logPath := filepath.Join(logDir, "memory-capture.log")

	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
		return nil
	}

	// Write to both stderr and the log file
	w := io.MultiWriter(os.Stderr, f)
	slog.SetDefault(slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})))
	return f
}
