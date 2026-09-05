package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/authzseed"
	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
)

// PutSection upserts one section by path+heading through the shared merge path —
// subcategory/slug_format validation, the embed decision, chain_previous, history
// and audit all apply. Never truncates other sections; never runs the dup guard.
func (s *MemoryService) PutSection(ctx context.Context, category string, subcategory *string, slug, heading, content string, overrideID *uuid.UUID) (*StoreResult, error) {
	tid, err := s.resolveWriteScope(ctx, overrideID, authz.RelMember)
	if err != nil {
		return nil, err
	}
	docType := models.InferDocType(category, subcategory, slug)
	policy := s.policyFor(docType)
	if err := models.ValidateSubcategoryRule(policy.Subcategory, subcategory); err != nil {
		return nil, fmt.Errorf("%w: %s", apperr.ErrInvalidInput, err)
	}
	if err := models.ValidateSlugFormat(policy.SlugFormat, slug); err != nil {
		return nil, fmt.Errorf("%w: %s", apperr.ErrInvalidInput, err)
	}

	var hp *string
	if h := heading; h != "" {
		hp = &h
	}
	embeddings, err := s.embedSections(ctx, []parsedSection{{heading: hp, content: content}}, policy.Embed)
	if err != nil {
		return nil, err
	}
	incoming := []models.Section{{Ordinal: 0, Heading: hp, Content: content, Embedding: embeddings[0]}}

	// A section-level write always upserts (never truncates); append_only still
	// rejects a heading collision, replace-mode accepts it (like update_section).
	mode := models.WriteModeMergeSections
	if policy.WriteMode == models.WriteModeAppendOnly {
		mode = models.WriteModeAppendOnly
	}

	now := time.Now()
	recordHistory := s.historyEnabledFor(ctx, tid)
	var overwriteBefore *string
	var finalSections []models.Section
	created := false
	var doc *models.Document
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txDocs := repository.NewDocumentRepository(tx)
		txSections := repository.NewSectionRepository(tx)
		existing, err := txDocs.GetByPath(ctx, repository.ReadTenants(tid), tid, category, subcategory, slug)
		if err == nil && existing.TenantID == tid {
			doc = existing
			existingSections := existing.Sections
			if recordHistory {
				overwriteBefore = docBeforeSnapshot(existing)
			}
			// Detach preloaded sections so Save can't cascade-upsert them: an
			// embed=false doc's sections carry NULL embeddings that serialize to an
			// invalid '[]' vector. writeSections owns the section writes.
			doc.Sections = nil
			doc.LastAccessedAt = &now
			if err := txDocs.Save(ctx, tid, doc); err != nil {
				return fmt.Errorf("save document: %w", err)
			}
			finalSections, err = writeSections(ctx, txSections, mode, policy.Embed, existingSections, incoming, doc.ID)
			return err
		}
		doc = &models.Document{
			TenantID: tid, Category: category, Subcategory: subcategory, Slug: slug,
			Title: slug, DocType: docType, ContentHash: hashContent(content), LastAccessedAt: &now,
		}
		if err := txDocs.Create(ctx, doc); err != nil {
			return fmt.Errorf("create document: %w", err)
		}
		created = true
		incoming[0].DocumentID = doc.ID
		if policy.Embed {
			err = txSections.CreateBatch(ctx, incoming)
		} else {
			err = txSections.CreateBatchNoEmbed(ctx, incoming)
		}
		if err != nil {
			return fmt.Errorf("create section: %w", err)
		}
		finalSections = incoming
		return nil
	})
	if err != nil {
		return nil, err
	}

	doc.Sections = finalSections
	s.seedTuple(ctx, authzseed.DocumentTenantEdge(doc.ID, tid))

	if recordHistory {
		subj, email := s.actorFields(ctx)
		op, before := models.MutationOpCreate, (*string)(nil)
		if !created {
			op, before = models.MutationOpOverwrite, overwriteBefore
		}
		s.logMutation(ctx, repository.MutationEvent{
			TenantID: tid, DocumentID: doc.ID, DocumentPath: doc.Path(),
			OpType: op, ActorSubject: subj, ActorEmail: email, Before: before,
		})
	}

	var warnings []string
	if created && policy.ChainPrevious != nil {
		if w := s.autoChainPrevious(ctx, tid, doc, policy.ChainPrevious); w != "" {
			warnings = append(warnings, w)
		}
	}
	return &StoreResult{Status: "ok", Document: doc, Path: doc.Path(), Sections: len(doc.Sections), Warnings: warnings}, nil
}

// ListDocTypePolicies returns the raw rows plus the resolved effective set for the
// admin surface (a row's NULLs mark inherited values). Instance admin only.
func (s *MemoryService) ListDocTypePolicies(ctx context.Context) ([]models.DocTypePolicy, map[string]models.EffectivePolicy, error) {
	if err := s.requireAdmin(ctx); err != nil {
		return nil, nil, err
	}
	if s.thresholds == nil {
		return nil, nil, fmt.Errorf("%w: policy store unavailable", apperr.ErrInvalidInput)
	}
	rows, err := s.thresholds.Rows(ctx)
	if err != nil {
		return nil, nil, err
	}
	eff, err := models.ResolveDocTypePolicies(rows)
	if err != nil {
		return nil, nil, err
	}
	return rows, eff, nil
}

// SetDocTypePolicy patches one policy row (instance admin only): only the
// supplied fields change, so a partial edit can't silently reset the others.
// Validates the MERGED result, persists, recomputes, and audits to override_log.
func (s *MemoryService) SetDocTypePolicy(ctx context.Context, patch models.DocTypePolicy) error {
	if err := s.requireAdmin(ctx); err != nil {
		return err
	}
	if s.thresholds == nil {
		return fmt.Errorf("%w: policy store unavailable", apperr.ErrInvalidInput)
	}
	if _, ok := models.ValidDocTypes[patch.DocType]; !ok {
		return fmt.Errorf("%w: unknown doc_type %q", apperr.ErrInvalidInput, patch.DocType)
	}
	rows, err := s.thresholds.Rows(ctx)
	if err != nil {
		return err
	}
	// Overlay the patch onto the existing row (or a fresh one), so unspecified
	// fields keep their stored value rather than resetting to NULL.
	row := models.DocTypePolicy{DocType: patch.DocType}
	for _, r := range rows {
		if r.DocType == patch.DocType {
			row = r
			break
		}
	}
	applyPolicyPatch(&row, patch)

	merged := make([]models.DocTypePolicy, 0, len(rows)+1)
	replaced := false
	for _, r := range rows {
		if r.DocType == patch.DocType {
			merged, replaced = append(merged, row), true
		} else {
			merged = append(merged, r)
		}
	}
	if !replaced {
		merged = append(merged, row)
	}
	eff, err := models.ResolveDocTypePolicies(merged)
	if err != nil {
		return fmt.Errorf("%w: %s", apperr.ErrInvalidInput, err)
	}
	for dt, p := range eff {
		if err := models.ValidateEffective(dt, p); err != nil {
			return fmt.Errorf("%w: %s", apperr.ErrInvalidInput, err)
		}
	}
	if err := s.thresholds.Upsert(ctx, row); err != nil {
		return err
	}
	if err := s.thresholds.Recompute(ctx); err != nil {
		return err
	}
	s.logOverride(ctx, repository.OverrideEvent{
		TenantID:     auth.TenantIDFromContext(ctx),
		Tool:         models.OverrideToolSetDocTypePolicy,
		OverrideType: models.OverrideTypePolicyChange,
		Reason:       "doc_type_policy: " + patch.DocType,
	})
	return nil
}

// applyPolicyPatch overlays a patch's supplied (non-nil) fields onto base.
func applyPolicyPatch(base *models.DocTypePolicy, p models.DocTypePolicy) {
	if p.VerificationAgeDays != nil {
		base.VerificationAgeDays = p.VerificationAgeDays
	}
	if p.ExpirationAgeDays != nil {
		base.ExpirationAgeDays = p.ExpirationAgeDays
	}
	if p.DuplicateGuard != nil {
		base.DuplicateGuard = p.DuplicateGuard
	}
	if p.CleanupScan != nil {
		base.CleanupScan = p.CleanupScan
	}
	if p.LintStaleCheck != nil {
		base.LintStaleCheck = p.LintStaleCheck
	}
	if p.Embed != nil {
		base.Embed = p.Embed
	}
	if p.DefaultSearch != nil {
		base.DefaultSearch = p.DefaultSearch
	}
	if p.Prunable != nil {
		base.Prunable = p.Prunable
	}
	if p.WriteMode != nil {
		base.WriteMode = p.WriteMode
	}
	if p.SlugFormat != nil {
		base.SlugFormat = p.SlugFormat
	}
	if p.Subcategory != nil {
		base.Subcategory = p.Subcategory
	}
	if len(p.Rules) > 0 {
		base.Rules = p.Rules
	}
}

// policyFor returns the effective policy for docType, falling back to the
// reference-equivalent default when no store is loaded (import CLI, unit fixtures).
func (s *MemoryService) policyFor(docType string) models.EffectivePolicy {
	if s.thresholds != nil {
		if p := s.thresholds.EffectiveFor(docType); p.WriteMode != "" {
			return p
		}
	}
	return models.DefaultEffectivePolicy()
}

// policyLintFindings surfaces two config smells: a rules key the server does not
// implement (an experimental typo), and a doc_type with every maintenance signal
// off (tasks 9.1, 9.2). Instance-wide, INFO severity.
func (s *MemoryService) policyLintFindings() []repository.LintFinding {
	if s.thresholds == nil {
		return nil
	}
	var out []repository.LintFinding
	for dt, p := range s.thresholds.All() {
		for key := range p.RawRules {
			if _, ok := models.KnownRuleKeys[key]; !ok {
				out = append(out, repository.LintFinding{
					Check:        "policy",
					Severity:     repository.LintSeverityInfo,
					DocumentPath: "doc_type_policies/" + dt,
					Message:      fmt.Sprintf("rules key %q is not implemented by the server (typo?)", key),
				})
			}
		}
		if p.VerificationAgeDays == 0 && !p.DuplicateGuard && !p.CleanupScan && !p.LintStaleCheck {
			out = append(out, repository.LintFinding{
				Check:        "policy",
				Severity:     repository.LintSeverityInfo,
				DocumentPath: "doc_type_policies/" + dt,
				Message:      "all maintenance signals (verification_age, duplicate_guard, cleanup_scan, lint_stale_check) are disabled for this doc_type",
			})
		}
	}
	return out
}

// policyDocTypes returns the doc_types whose effective policy satisfies pred, for
// the SQL-array inputs to the lint / search / scan exclusions.
func (s *MemoryService) policyDocTypes(pred func(models.EffectivePolicy) bool) []string {
	if s.thresholds == nil {
		return nil
	}
	return s.thresholds.DocTypesWhere(pred)
}

// policyAll returns the full effective policy set (retention window derivation),
// or nil when no policy store is wired (offline CLI / tests).
func (s *MemoryService) policyAll() map[string]models.EffectivePolicy {
	if s.thresholds == nil {
		return nil
	}
	return s.thresholds.All()
}

// embedSections embeds the parsed sections, or returns zero vectors when the
// doc_type does not embed (the caller then stores NULL embeddings).
func (s *MemoryService) embedSections(ctx context.Context, sections []parsedSection, doEmbed bool) ([]pgvector.Vector, error) {
	embeddings := make([]pgvector.Vector, len(sections))
	if !doEmbed {
		return embeddings, nil
	}
	if batcher, ok := s.embedder.(BatchEmbedder); ok && len(sections) > 0 {
		texts := make([]string, len(sections))
		for i, sec := range sections {
			texts[i] = sec.content
		}
		vecs, err := batcher.EmbedBatch(ctx, texts)
		if err != nil {
			return nil, fmt.Errorf("embed sections: %w", err)
		}
		if len(vecs) != len(sections) {
			return nil, fmt.Errorf("embed sections: got %d vectors for %d sections", len(vecs), len(sections))
		}
		copy(embeddings, vecs)
		return embeddings, nil
	}
	for i, sec := range sections {
		embedding, err := s.embedder.Embed(ctx, sec.content)
		if err != nil {
			return nil, fmt.Errorf("embed section %d: %w", i, err)
		}
		embeddings[i] = embedding
	}
	return embeddings, nil
}

// headingKey normalizes a section heading for merge matching (nil = the preamble).
func headingKey(h *string) string {
	if h == nil {
		return "\x00preamble"
	}
	return *h
}

// writeSections applies write_mode to an existing document's sections: replace
// deletes then inserts; merge_sections upserts by heading; append_only rejects a
// collision. Returns the final ordered section slice for the response.
func writeSections(ctx context.Context, txSections *repository.SectionRepository, mode models.WriteMode, embed bool, existing, incoming []models.Section, docID uuid.UUID) ([]models.Section, error) {
	insert := func(secs []models.Section) error {
		for i := range secs {
			secs[i].DocumentID = docID
		}
		if embed {
			return txSections.CreateBatch(ctx, secs)
		}
		return txSections.CreateBatchNoEmbed(ctx, secs)
	}

	if mode == models.WriteModeReplace || mode == "" {
		if err := txSections.DeleteByDocumentID(ctx, docID); err != nil {
			return nil, fmt.Errorf("delete old sections: %w", err)
		}
		if err := insert(incoming); err != nil {
			return nil, fmt.Errorf("create sections: %w", err)
		}
		return incoming, nil
	}

	// merge_sections / append_only: upsert by heading, positional within a heading
	// group so duplicate headings pair 1:1 with existing rows instead of collapsing
	// onto the last one (which would drop content and leave a sibling stale).
	byHeading := make(map[string][]models.Section, len(existing))
	maxOrd := -1
	for _, e := range existing {
		k := headingKey(e.Heading)
		byHeading[k] = append(byHeading[k], e)
		if e.Ordinal > maxOrd {
			maxOrd = e.Ordinal
		}
	}
	consumed := make(map[string]int, len(byHeading))
	updatedByID := make(map[uuid.UUID]models.Section)
	var inserts []models.Section
	next := maxOrd + 1
	for _, in := range incoming {
		k := headingKey(in.Heading)
		grp := byHeading[k]
		if idx := consumed[k]; idx < len(grp) {
			if mode == models.WriteModeAppendOnly {
				return nil, fmt.Errorf("%w: section %q already exists (append_only)", apperr.ErrInvalidInput, k)
			}
			e := grp[idx]
			consumed[k] = idx + 1
			if err := txSections.UpdateSectionContent(ctx, e.ID, in.Content, in.Embedding, embed); err != nil {
				return nil, fmt.Errorf("update section: %w", err)
			}
			e.Content, e.Embedding = in.Content, in.Embedding
			updatedByID[e.ID] = e
			continue
		}
		in.Ordinal = next
		next++
		inserts = append(inserts, in)
	}
	if err := insert(inserts); err != nil {
		return nil, fmt.Errorf("create sections: %w", err)
	}
	final := make([]models.Section, 0, len(existing)+len(inserts))
	for _, e := range existing {
		if u, ok := updatedByID[e.ID]; ok {
			final = append(final, u)
		} else {
			final = append(final, e)
		}
	}
	return append(final, inserts...), nil
}
