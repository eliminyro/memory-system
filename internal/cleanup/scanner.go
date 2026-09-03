// Package cleanup runs the server-side near-duplicate scanner and its webhook
// notifier: on the configured interval, walk every tenant, upsert pending entries.
// Merging happens client-side in a scheduled agent — this package calls no LLMs.
package cleanup

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/panicguard"
	"github.com/eliminyro/memory-system/internal/repository"
)

// ScanStats summarises one sweep across all tenants.
type ScanStats struct {
	TenantsScanned int
	PairsFound     int
	PairsInserted  int
	PairsSkipped   int // already enqueued
	Errors         int
	HistoryPruned  int
}

// GlobalConfig is the live global-config slice the cleanup pipeline reads each
// cycle; *globalconfig.Accessor satisfies it.
type GlobalConfig interface {
	CleanupEnabled() bool
	CleanupIntervalHours() int
	HistoryRetentionDays() int
	WebhookURL() string
}

// PolicySource supplies the doc_types the scanner skips (cleanup_scan=false);
// *staleness.PolicyStore satisfies it.
type PolicySource interface {
	DocTypesWhere(func(models.EffectivePolicy) bool) []string
}

// Scanner runs the near-duplicate scan and keeps cleanup_queue populated.
type Scanner struct {
	lint     *repository.LintRepository
	tenants  *repository.TenantRepository
	queue    *repository.CleanupQueueRepository
	history  *repository.MutationHistoryRepository
	policies PolicySource
	gc       GlobalConfig
	notifier *Notifier
	logger   *slog.Logger

	// runMu serialises RunOnce so an overlapping ticker fire can't cause
	// concurrent upserts (which, absent a partial unique index, duplicate rows).
	runMu sync.Mutex
}

// NewScanner constructs the scanner. gc supplies the live retention window,
// interval, and enabled flag; a nil notifier means silent scans.
func NewScanner(
	lint *repository.LintRepository,
	tenants *repository.TenantRepository,
	queue *repository.CleanupQueueRepository,
	history *repository.MutationHistoryRepository,
	policies PolicySource,
	gc GlobalConfig,
	notifier *Notifier,
	logger *slog.Logger,
) *Scanner {
	return &Scanner{
		lint:     lint,
		tenants:  tenants,
		queue:    queue,
		history:  history,
		policies: policies,
		gc:       gc,
		notifier: notifier,
		logger:   logger,
	}
}

// scanExcluded returns the doc_types the scanner skips (cleanup_scan=false).
func (s *Scanner) scanExcluded() []string {
	if s.policies == nil {
		return nil
	}
	return s.policies.DocTypesWhere(func(p models.EffectivePolicy) bool { return !p.CleanupScan })
}

// RunOnce performs a single sweep. Returns stats even on partial failure;
// per-tenant errors are counted, not fatal.
func (s *Scanner) RunOnce(ctx context.Context) (ScanStats, error) {
	s.runMu.Lock()
	defer s.runMu.Unlock()

	stats := ScanStats{}

	allTenants, err := s.tenants.List(ctx)
	if err != nil {
		return stats, fmt.Errorf("list tenants: %w", err)
	}

	for _, tenant := range allTenants {
		if tenant.CleanupScanEnabled {
			stats.TenantsScanned++
			if err := s.scanTenant(ctx, tenant.ID, &stats); err != nil {
				s.logger.Warn("cleanup scan: tenant failed", "tenant_id", tenant.ID, "error", err)
				stats.Errors++
			}
		}
	}

	// Prune mutation_history past its live retention window (global, not per-tenant).
	if days := s.gc.HistoryRetentionDays(); s.history != nil && days > 0 {
		cutoff := time.Now().AddDate(0, 0, -days)
		if pruned, err := s.history.PruneOlderThan(ctx, cutoff); err != nil {
			s.logger.Warn("history prune failed", "error", err)
			stats.Errors++
		} else {
			stats.HistoryPruned += int(pruned)
		}
	}

	if s.notifier != nil {
		if err := s.notifier.SendScanSummary(ctx, stats); err != nil {
			s.logger.Warn("cleanup scan: notifier failed", "error", err)
		}
	}

	return stats, nil
}

func (s *Scanner) scanTenant(ctx context.Context, tenantID uuid.UUID, stats *ScanStats) error {
	pairs, err := s.lint.FindNearDuplicatePairs(ctx, tenantID, models.ScanThreshold, s.scanExcluded())
	if err != nil {
		return err
	}
	for _, p := range pairs {
		stats.PairsFound++
		inserted, err := s.queue.Upsert(ctx, &models.CleanupQueue{
			TenantID:   tenantID,
			DocAID:     p.DocAID,
			DocBID:     p.DocBID,
			Similarity: p.Similarity,
		})
		if err != nil {
			s.logger.Warn("cleanup scan: upsert failed", "pair", p, "error", err)
			stats.Errors++
			continue
		}
		if inserted {
			stats.PairsInserted++
		} else {
			stats.PairsSkipped++
		}
	}
	return nil
}

// Start launches the scan loop until ctx is cancelled. Each cycle reads
// cleanup_enabled and cleanup_interval_hours live from the accessor, so a change
// applies at the next fire (next-cycle, not instant). The first sweep fires now.
func (s *Scanner) Start(ctx context.Context) {
	go func() {
		// Recover per sweep so one panicking scan logs + the loop continues,
		// rather than the whole goroutine (and the scan cadence) dying.
		tick := func() {
			defer panicguard.Recover(s.logger, "cleanup scan")
			if !s.gc.CleanupEnabled() {
				return
			}
			s.runAndLog(ctx)
		}
		tick() // migrations already ran synchronously, so no startup delay

		for {
			interval := time.Duration(s.gc.CleanupIntervalHours()) * time.Hour
			if interval <= 0 {
				interval = 24 * time.Hour
			}
			t := time.NewTimer(interval)
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
				tick()
			}
		}
	}()
}

func (s *Scanner) runAndLog(ctx context.Context) {
	stats, err := s.RunOnce(ctx)
	if err != nil {
		s.logger.Error("cleanup scan failed", "error", err)
		return
	}
	s.logger.Info("cleanup scan complete",
		"tenants", stats.TenantsScanned,
		"pairs_found", stats.PairsFound,
		"pairs_inserted", stats.PairsInserted,
		"pairs_skipped", stats.PairsSkipped,
		"errors", stats.Errors,
		"history_pruned", stats.HistoryPruned,
	)
}
