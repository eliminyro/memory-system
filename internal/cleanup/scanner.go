// Package cleanup hosts the server-side duplicate-detection scanner and its
// Telegram notifier. The scanner runs on a fixed interval, walks every tenant,
// runs the near-duplicate query, and upserts pending queue entries. Actual
// merging happens client-side in a scheduled agent — this package does not
// call LLMs.
package cleanup

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/retention"
	"github.com/eliminyro/memory-system/internal/staleness"
)

// ScanStats summarises one sweep across all tenants.
type ScanStats struct {
	TenantsScanned int
	PairsFound     int
	PairsInserted  int
	PairsSkipped   int // already enqueued
	Errors         int
	DocsArchived   int
	DocsDeleted    int
}

// Scanner runs the near-duplicate scan and keeps cleanup_queue populated.
type Scanner struct {
	lint       *repository.LintRepository
	tenants    *repository.TenantRepository
	queue      *repository.CleanupQueueRepository
	retention  *repository.RetentionRepository
	thresholds *staleness.ThresholdStore
	multiplier int
	graceDays  int
	notifier   *Notifier
	logger     *slog.Logger

	// runMu serialises RunOnce so a ticker fire that lands while the previous
	// sweep is still draining won't produce concurrent upserts (which, absent
	// a partial unique index, could insert duplicate pending rows).
	runMu sync.Mutex
}

// NewScanner constructs the scanner. notifier may be nil — in that case the
// scan runs silently.
func NewScanner(
	lint *repository.LintRepository,
	tenants *repository.TenantRepository,
	queue *repository.CleanupQueueRepository,
	retentionRepo *repository.RetentionRepository,
	thresholds *staleness.ThresholdStore,
	multiplier int,
	graceDays int,
	notifier *Notifier,
	logger *slog.Logger,
) *Scanner {
	return &Scanner{
		lint:       lint,
		tenants:    tenants,
		queue:      queue,
		retention:  retentionRepo,
		thresholds: thresholds,
		multiplier: multiplier,
		graceDays:  graceDays,
		notifier:   notifier,
		logger:     logger,
	}
}

// RunOnce performs a single sweep. Returns stats even on partial failure so
// the caller can log them; individual per-tenant errors are counted but do not
// abort the run.
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
		if retentionEligible(tenant) {
			if err := s.retainTenant(ctx, tenant.ID, &stats); err != nil {
				s.logger.Warn("retention sweep: tenant failed", "tenant_id", tenant.ID, "error", err)
				stats.Errors++
			}
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
	pairs, err := s.lint.FindNearDuplicatePairs(ctx, tenantID, models.ScanThreshold)
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

// Start launches a goroutine that calls RunOnce every `interval` until ctx is
// cancelled. The first sweep fires immediately (minus a small jitter for
// process-restart scenarios).
func (s *Scanner) Start(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	go func() {
		// Migrations already ran synchronously before we got here, so no
		// startup delay is needed before the first sweep.
		s.runAndLog(ctx)

		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.runAndLog(ctx)
			}
		}
	}()
}

// retentionEligible reports whether the retention sweep should run for a
// tenant: opted into hard staleness enforcement, and never the shared
// bootstrap pool (its seed docs are curated, never auto-retired).
func retentionEligible(t models.Tenant) bool {
	return t.StalenessMode == models.StalenessModeHard && t.ID != models.BootstrapTenantID
}

func (s *Scanner) retainTenant(ctx context.Context, tenantID uuid.UUID, stats *ScanStats) error {
	now := time.Now()
	cutoffs := make(map[string]time.Time, len(models.ValidDocTypes))
	for docType := range models.ValidDocTypes {
		days, err := s.thresholds.DaysFor(ctx, docType)
		if err != nil {
			return fmt.Errorf("threshold for %s: %w", docType, err)
		}
		cutoffs[docType] = retention.ExpiryCutoff(now, days, s.multiplier)
	}

	archived, err := s.retention.ArchiveExpired(ctx, tenantID, cutoffs)
	if err != nil {
		return err
	}
	stats.DocsArchived += int(archived)

	deleted, err := s.retention.DeleteArchived(ctx, tenantID, retention.DeleteCutoff(now, s.graceDays))
	if err != nil {
		return err
	}
	stats.DocsDeleted += int(deleted)
	return nil
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
		"docs_archived", stats.DocsArchived,
		"docs_deleted", stats.DocsDeleted,
	)
}
