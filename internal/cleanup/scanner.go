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
	"time"

	"github.com/google/uuid"

	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
)

// DuplicateThreshold is the cosine-similarity bar for enqueuing a pair. Matches
// the store-time guard in service.DuplicateThreshold — keep in sync manually,
// they are not wired as the same const because the store-time check runs
// per-section while this one runs per-doc-average.
const DuplicateThreshold = 0.70

// ScanStats summarises one sweep across all tenants.
type ScanStats struct {
	TenantsScanned int
	PairsFound     int
	PairsInserted  int
	PairsSkipped   int // already enqueued
	Errors         int
}

// Scanner runs the near-duplicate scan and keeps cleanup_queue populated.
type Scanner struct {
	lint     *repository.LintRepository
	tenants  *repository.TenantRepository
	queue    *repository.CleanupQueueRepository
	notifier *Notifier
	logger   *slog.Logger
}

// NewScanner constructs the scanner. notifier may be nil — in that case the
// scan runs silently.
func NewScanner(
	lint *repository.LintRepository,
	tenants *repository.TenantRepository,
	queue *repository.CleanupQueueRepository,
	notifier *Notifier,
	logger *slog.Logger,
) *Scanner {
	return &Scanner{
		lint:     lint,
		tenants:  tenants,
		queue:    queue,
		notifier: notifier,
		logger:   logger,
	}
}

// RunOnce performs a single sweep. Returns stats even on partial failure so
// the caller can log them; individual per-tenant errors are counted but do not
// abort the run.
func (s *Scanner) RunOnce(ctx context.Context) (ScanStats, error) {
	stats := ScanStats{}

	allTenants, err := s.tenants.List(ctx)
	if err != nil {
		return stats, fmt.Errorf("list tenants: %w", err)
	}

	for _, tenant := range allTenants {
		// Opt-in per tenant — skip anyone who didn't enable the scan.
		if !tenant.CleanupScanEnabled {
			continue
		}
		stats.TenantsScanned++
		if err := s.scanTenant(ctx, tenant.ID, &stats); err != nil {
			s.logger.Warn("cleanup scan: tenant failed", "tenant_id", tenant.ID, "error", err)
			stats.Errors++
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
	pairs, err := s.lint.FindNearDuplicatePairs(ctx, tenantID, DuplicateThreshold)
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
		// Small initial delay so startup doesn't race with migrations.
		select {
		case <-time.After(30 * time.Second):
		case <-ctx.Done():
			return
		}

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
	)
}
