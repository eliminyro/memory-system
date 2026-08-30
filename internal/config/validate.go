package config

import (
	"fmt"
	"log/slog"

	"github.com/eliminyro/memory-system/internal/models"
)

// Per-field bound checks for the runtime globals, shared by config.Load (boot)
// and the admin config API (PATCH). Each mirrors an instance_config column so a
// partial update can validate exactly the supplied fields.

func ValidateMMRLambda(v float64) error {
	if v <= 0 || v > 1 {
		return fmt.Errorf("MEMORY_MMR_LAMBDA must be in (0, 1], got %v", v)
	}
	return nil
}

// ValidateStalenessPenalty allows [0, 1]; 0 is valid (off), unlike MMR lambda.
func ValidateStalenessPenalty(v float64) error {
	if v < 0 || v > 1 {
		return fmt.Errorf("MEMORY_STALENESS_PENALTY must be in [0, 1], got %v", v)
	}
	return nil
}

func ValidateCandidatePool(v int) error {
	if v < 1 || v > maxCandidatePool {
		return fmt.Errorf("MEMORY_CANDIDATE_POOL must be in [1, %d], got %d", maxCandidatePool, v)
	}
	return nil
}

func ValidateSnippetChars(v int) error {
	if v <= 0 {
		return fmt.Errorf("MEMORY_SNIPPET_CHARS must be > 0, got %d", v)
	}
	return nil
}

func ValidateHistoryRetentionDays(v int) error {
	if v < 0 {
		return fmt.Errorf("MEMORY_HISTORY_RETENTION_DAYS must be >= 0 (0 = keep full history), got %d", v)
	}
	return nil
}

func ValidateSelfServicePolicy(v string) error {
	if !models.IsValidSelfServicePolicy(v) {
		return fmt.Errorf("MEMORY_SELF_SERVICE_POLICY must be open or admin_only, got %q", v)
	}
	return nil
}

// ValidateLogLevel accepts the slog level names (debug|info|warn|error). Not
// called by Load — main parses the level leniently — but the admin API rejects
// a bad value here.
func ValidateLogLevel(v string) error {
	var l slog.Level
	if err := l.UnmarshalText([]byte(v)); err != nil {
		return fmt.Errorf("log_level must be one of debug, info, warn, error, got %q", v)
	}
	return nil
}

func ValidateMaxRequestBytes(v int64) error {
	if v < 0 {
		return fmt.Errorf("MAX_REQUEST_BYTES must be >= 0, got %d", v)
	}
	return nil
}

// ValidateRateLimit enforces the burst>=1 rule whenever throttling is on (rps>0).
func ValidateRateLimit(rps float64, burst int) error {
	if rps > 0 && burst < 1 {
		return fmt.Errorf("RATE_LIMIT_BURST must be >= 1 when RATE_LIMIT_RPS > 0, got %d", burst)
	}
	return nil
}

func ValidateStalenessDefault(v string) error {
	if _, ok := models.ValidStalenessModes[v]; !ok {
		return fmt.Errorf("staleness_default must be off, advisory or hard, got %q", v)
	}
	return nil
}

func ValidateDuplicateThreshold(v float64) error {
	if v <= 0 || v > 1 {
		return fmt.Errorf("duplicate_threshold must be in (0, 1], got %v", v)
	}
	return nil
}

func ValidateRateLimitRPS(v float64) error {
	if v < 0 {
		return fmt.Errorf("rate_limit_rps must be >= 0, got %v", v)
	}
	return nil
}

func ValidateRateLimitBurst(v int) error {
	if v < 1 {
		return fmt.Errorf("rate_limit_burst must be >= 1, got %d", v)
	}
	return nil
}

func ValidateTrustedProxyDepth(v int) error {
	if v < 0 {
		return fmt.Errorf("trusted_proxy_depth must be >= 0, got %d", v)
	}
	return nil
}

func ValidateCleanupIntervalHours(v int) error {
	if v < 1 {
		return fmt.Errorf("cleanup_interval_hours must be >= 1, got %d", v)
	}
	return nil
}
