// Package staleness holds the effective doc_type policy store. Read-time
// needs-verification is content/event-driven (the section flag), not age-based;
// this package no longer computes freshness from a clock.
package staleness

import (
	"context"
	"fmt"
	"sort"
	"sync/atomic"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/eliminyro/memory-system/internal/models"
)

// PolicyStore holds the effective doc_type policy set behind an atomic snapshot,
// loaded once at boot and recomputed on admin write (no TTL, no per-request DB
// hit). config-invalidation reloads it on other replicas via Recompute.
type PolicyStore struct {
	db   *gorm.DB
	snap atomic.Pointer[map[string]models.EffectivePolicy]
}

// NewPolicyStore returns a store seeded with the resolved default policies, so an
// un-Loaded store still serves the seeded rules; call Load at boot to pick up
// admin-edited rows.
func NewPolicyStore(db *gorm.DB) *PolicyStore {
	s := &PolicyStore{db: db}
	seed := make(map[string]models.EffectivePolicy, len(models.DefaultEffectivePolicies))
	for k, v := range models.DefaultEffectivePolicies {
		seed[k] = v
	}
	s.snap.Store(&seed)
	return s
}

// NewPolicyStoreFromEffective serves a pre-resolved policy set directly, skipping
// the DB load — for in-process callers and tests that resolve policies themselves.
func NewPolicyStoreFromEffective(eff map[string]models.EffectivePolicy) *PolicyStore {
	s := &PolicyStore{}
	m := make(map[string]models.EffectivePolicy, len(eff))
	for k, v := range eff {
		m[k] = v
	}
	s.snap.Store(&m)
	return s
}

// Load reads every policy row, resolves NULL inheritance, validates the merged
// set, and swaps the snapshot. A validation failure returns an error (boot stops)
// and leaves the current snapshot untouched.
func (s *PolicyStore) Load(ctx context.Context) error {
	var rows []models.DocTypePolicy
	if err := s.db.WithContext(ctx).Find(&rows).Error; err != nil {
		return fmt.Errorf("load doc_type_policies: %w", err)
	}
	eff, err := models.ResolveDocTypePolicies(rows)
	if err != nil {
		return err
	}
	for dt, p := range eff {
		if err := models.ValidateEffective(dt, p); err != nil {
			return err
		}
	}
	s.snap.Store(&eff)
	return nil
}

// Recompute reloads after an admin write; satisfies the config-invalidation
// ReloadFunc signature.
func (s *PolicyStore) Recompute(ctx context.Context) error { return s.Load(ctx) }

// Rows returns the raw policy rows (for the admin read + merged validation).
func (s *PolicyStore) Rows(ctx context.Context) ([]models.DocTypePolicy, error) {
	var rows []models.DocTypePolicy
	if err := s.db.WithContext(ctx).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("read doc_type_policies: %w", err)
	}
	return rows, nil
}

// Upsert writes one policy row (insert or update by doc_type), firing the trigger.
func (s *PolicyStore) Upsert(ctx context.Context, row models.DocTypePolicy) error {
	if len(row.Rules) == 0 {
		row.Rules = []byte("{}")
	}
	if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "doc_type"}},
		UpdateAll: true,
	}).Create(&row).Error; err != nil {
		return fmt.Errorf("upsert doc_type_policy %q: %w", row.DocType, err)
	}
	return nil
}

// EffectiveFor returns a doc_type's effective policy, falling back to the
// reference row for an unknown doc_type.
func (s *PolicyStore) EffectiveFor(docType string) models.EffectivePolicy {
	m := *s.snap.Load()
	if p, ok := m[docType]; ok {
		return p
	}
	return m[models.DocTypeReference]
}

// All returns a copy of the effective set keyed by doc_type.
func (s *PolicyStore) All() map[string]models.EffectivePolicy {
	m := *s.snap.Load()
	out := make(map[string]models.EffectivePolicy, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// DocTypesWhere returns the doc_types whose effective policy satisfies pred, as a
// sorted slice — the SQL-array inputs (default_search, cleanup_scan, lint) that
// replace the old compiled-in EpisodicDocTypes bindings.
func (s *PolicyStore) DocTypesWhere(pred func(models.EffectivePolicy) bool) []string {
	m := *s.snap.Load()
	out := make([]string, 0, len(m))
	for dt, p := range m {
		if pred(p) {
			out = append(out, dt)
		}
	}
	sort.Strings(out)
	return out
}
