// Package cleanup runs the server-side near-duplicate scanner and its Telegram
// notifier: on a fixed interval, walk every tenant, upsert pending queue entries.
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
	lint           *repository.LintRepository
	tenants        *repository.TenantRepository
	queue          *repository.CleanupQueueRepository
	retention      *repository.RetentionRepository
	instanceConfig *repository.InstanceConfigRepository
	thresholds     *staleness.ThresholdStore
	multiplier     int
	graceDays      int
	notifier       *Notifier
	logger         *slog.Logger

	// runMu serialises RunOnce so an overlapping ticker fire can't cause
	// concurrent upserts (which, absent a partial unique index, duplicate rows).
	runMu sync.Mutex
}

// NewScanner constructs the scanner. A nil notifier means silent scans.
func NewScanner(
	lint *repository.LintRepository,
	tenants *repository.TenantRepository,
	queue *repository.CleanupQueueRepository,
	retentionRepo *repository.RetentionRepository,
	instanceConfig *repository.InstanceConfigRepository,
	thresholds *staleness.ThresholdStore,
	multiplier int,
	graceDays int,
	notifier *Notifier,
	logger *slog.Logger,
) *Scanner {
	return &Scanner{
		lint:           lint,
		tenants:        tenants,
		queue:          queue,
		retention:      retentionRepo,
		instanceConfig: instanceConfig,
		thresholds:     thresholds,
		multiplier:     multiplier,
		graceDays:      graceDays,
		notifier:       notifier,
		logger:         logger,
	}
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

	// Read the ONE global access-retention toggle once per sweep (D9). On a read
	// error, fail safe (no access eviction) rather than block staleness retention.
	accessEnabled := false
	if s.instanceConfig != nil {
		cfg, err := s.instanceConfig.Get(ctx)
		if err != nil {
			s.logger.Warn("cleanup scan: instance config read failed; access retention disabled this sweep", "error", err)
		} else {
			accessEnabled = cfg.AccessRetentionEnabled
		}
	}

	for _, tenant := range allTenants {
		if tenant.CleanupScanEnabled {
			stats.TenantsScanned++
			if err := s.scanTenant(ctx, tenant.ID, &stats); err != nil {
				s.logger.Warn("cleanup scan: tenant failed", "tenant_id", tenant.ID, "error", err)
				stats.Errors++
			}
		}
		if retentionEligible(tenant) || accessRetentionEligible(accessEnabled, tenant) {
			if err := s.retainTenant(ctx, tenant, accessEnabled, &stats); err != nil {
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

// Start launches a goroutine calling RunOnce every `interval` until ctx is
// cancelled. The first sweep fires immediately.
func (s *Scanner) Start(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	go func() {
		// Recover per sweep so one panicking scan logs + the loop continues,
		// rather than the whole goroutine (and the scan cadence) dying.
		tick := func() {
			defer panicguard.Recover(s.logger, "cleanup scan")
			s.runAndLog(ctx)
		}
		// Migrations already ran synchronously, so no startup delay is needed.
		tick()

		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				tick()
			}
		}
	}()
}

// retentionEligible reports whether age-based retention runs for a tenant:
// staleness_mode hard, and never the bootstrap pool (curated seed, never retired).
func retentionEligible(t models.Tenant) bool {
	return t.StalenessMode == models.StalenessModeHard && t.ID != models.BootstrapTenantID
}

// accessRetentionEligible reports whether access-recency eviction runs for a
// tenant: the global toggle on, never the bootstrap pool. Independent of staleness.
func accessRetentionEligible(globalEnabled bool, t models.Tenant) bool {
	return globalEnabled && t.ID != models.BootstrapTenantID
}

func (s *Scanner) retainTenant(ctx context.Context, tenant models.Tenant, accessEnabled bool, stats *ScanStats) error {
	// Defensive guard (audit #4): refuse a destructive sweep when window params
	// collapse the cutoffs and would mass hard-delete live data. config.Load
	// rejects sub-1 values at startup; this backstops direct Scanner construction.
	if !retention.WindowSafe(s.multiplier, s.graceDays) {
		return fmt.Errorf("refusing retention sweep: unsafe window (multiplier=%d, graceDays=%d)", s.multiplier, s.graceDays)
	}

	now := time.Now()

	if retentionEligible(tenant) {
		// Partition per-doc_type cutoffs: curated doc_types archive-then-delete;
		// episodic ones (journal, ...) skip archive and roll off via direct-delete.
		curatedCutoffs := make(map[string]time.Time, len(models.ValidDocTypes))
		episodicCutoffs := make(map[string]time.Time, len(models.ValidDocTypes))
		for docType := range models.ValidDocTypes {
			days, err := s.thresholds.DaysFor(ctx, docType)
			if err != nil {
				return fmt.Errorf("threshold for %s: %w", docType, err)
			}
			cutoff := retention.ExpiryCutoff(now, days, s.multiplier)
			if models.IsEpisodic(docType) {
				episodicCutoffs[docType] = cutoff
			} else {
				curatedCutoffs[docType] = cutoff
			}
		}
		archived, err := s.retention.ArchiveExpired(ctx, tenant.ID, curatedCutoffs)
		if err != nil {
			return err
		}
		stats.DocsArchived += int(archived)

		episodicDeleted, err := s.retention.DeleteEpisodicExpired(ctx, tenant.ID, episodicCutoffs)
		if err != nil {
			return err
		}
		stats.DocsDeleted += int(episodicDeleted)
	}

	// Access-recency eviction (D3): gated by the ONE global toggle, complementary to
	// (never replacing) the staleness path above. Per-doc_type windows reuse the
	// same staleness_thresholds × multiplier cutoffs, keyed on access recency instead
	// of liveness. Archive-only here; the shared DeleteArchived below hard-deletes it
	// after grace, same as age docs.
	if accessRetentionEligible(accessEnabled, tenant) {
		accessCutoffs := make(map[string]time.Time, len(models.ValidDocTypes))
		for docType := range models.ValidDocTypes {
			if models.IsEpisodic(docType) {
				continue
			}
			days, err := s.thresholds.DaysFor(ctx, docType)
			if err != nil {
				return fmt.Errorf("threshold for %s: %w", docType, err)
			}
			accessCutoffs[docType] = retention.ExpiryCutoff(now, days, s.multiplier)
		}
		accessArchived, err := s.retention.ArchiveAccessCold(ctx, tenant.ID, accessCutoffs)
		if err != nil {
			return err
		}
		stats.DocsArchived += int(accessArchived)
	}

	// Hard-delete every archived doc (age- or access-archived) once past grace.
	deleted, err := s.retention.DeleteArchived(ctx, tenant.ID, retention.DeleteCutoff(now, s.graceDays))
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
