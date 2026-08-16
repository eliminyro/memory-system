package repository

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
	"gorm.io/gorm"

	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/models"
)

type SectionRepository struct {
	db *gorm.DB
}

func NewSectionRepository(db *gorm.DB) *SectionRepository {
	return &SectionRepository{db: db}
}

type SearchResult struct {
	SectionID      uuid.UUID  `json:"section_id"`
	DocumentID     uuid.UUID  `json:"document_id"`
	Heading        *string    `json:"heading,omitempty"`
	Content        string     `json:"content,omitempty"`
	Score          float64    `json:"score"`
	Tier           string     `json:"relevance,omitempty"` // high | standard | low — calibrated from Score
	Category       string     `json:"category"`
	Subcategory    *string    `json:"subcategory,omitempty"`
	Slug           string     `json:"slug"`
	DocTitle       string     `json:"doc_title"`
	DocType        string     `json:"doc_type,omitempty"`
	VerifiedAt     *time.Time `json:"verified_at,omitempty"`
	SectionCreated time.Time  `json:"-"`

	// Owning-tenant label (cross-tenant reads). TenantID comes from SQL; Name and
	// Type are resolved by the service layer for the distinct result tenants.
	TenantID   uuid.UUID `json:"tenant_id"`
	TenantName string    `json:"tenant_name,omitempty"`
	TenantType string    `json:"tenant_type,omitempty"`

	// Staleness overlay (set by service layer after fetch, not by SQL).
	Status        string   `json:"status,omitempty"`         // "needs_verification" when guarded
	Preview       string   `json:"preview,omitempty"`        // short preview of withheld content
	VerifyHints   []string `json:"verify_hints,omitempty"`   // code paths to check
	StaleDays     int      `json:"age_days,omitempty"`       // age of verified_at in days
	ThresholdDays int      `json:"threshold_days,omitempty"` // threshold for this doc_type

	// SnippetCentered is set only when search ran in snippet mode: true if the
	// window landed on a real lexical match, false for the leading-text fallback
	// (purely-semantic hit). Nil (omitted) when snippet mode is off.
	SnippetCentered *bool `json:"snippet_centered,omitempty"`
}

// SearchParams groups the inputs for hybrid search.
type SearchParams struct {
	TenantIDs   []uuid.UUID
	Embedding   pgvector.Vector
	Query       string
	Category    *string
	Subcategory *string
	Limit       int
	// MMRLambda gates optional MMR diversity re-ranking; nil (default) leaves
	// HybridSearch byte-identical to the plain fused, score-sorted path.
	MMRLambda *float64
	// UsageWeight scales the per-section recall-usage term in fusion; 0 (default)
	// skips it entirely, leaving scores byte-identical to the pre-usage path.
	UsageWeight float64
}

// Hybrid retrieval fusion constants. Lexical is weighted higher than vector: a row
// matching BOTH is genuinely relevant, while weak vector-only matches are noise.
const (
	fuseVecWeight = 0.4 // weight of vector similarity when both signals fire
	fuseLexWeight = 0.6 // weight of lexical rank when both signals fire
	scoreFloor    = 0.4 // drop fused scores below this (weak-match noise)
	tierHighMin   = 0.7 // >= this is "high"
	tierStdMin    = 0.5 // >= this is "standard"; below is "low"
)

// hybridRow is a raw candidate from the FULL OUTER JOIN (vector, lexical, or both).
// Fusion happens in Go (fuseHybrid) so it stays pure and unit-testable.
type hybridRow struct {
	SectionID      uuid.UUID  `gorm:"column:section_id"`
	DocumentID     uuid.UUID  `gorm:"column:document_id"`
	Heading        *string    `gorm:"column:heading"`
	Content        string     `gorm:"column:content"`
	VecSim         float64    `gorm:"column:vec_sim"`
	LexRank        float64    `gorm:"column:lex_rank"`
	HasVec         bool       `gorm:"column:has_vec"`
	HasLex         bool       `gorm:"column:has_lex"`
	Category       string     `gorm:"column:category"`
	Subcategory    *string    `gorm:"column:subcategory"`
	Slug           string     `gorm:"column:slug"`
	DocTitle       string     `gorm:"column:doc_title"`
	DocType        string     `gorm:"column:doc_type"`
	TenantID       uuid.UUID  `gorm:"column:tenant_id"`
	VerifiedAt     *time.Time `gorm:"column:verified_at"`
	SectionCreated time.Time  `gorm:"column:section_created"`
	// Recall counters back the optional usage-weight term (0 weight ⇒ unused).
	HitCount  int `gorm:"column:hit_count"`
	MissCount int `gorm:"column:miss_count"`
	// Embedding backs MMR re-ranking only; never copied onto SearchResult.
	Embedding pgvector.Vector `gorm:"column:embedding"`
}

// relevanceTier calibrates a fused score into a label the model can act on.
func relevanceTier(score float64) string {
	switch {
	case score >= tierHighMin:
		return "high"
	case score >= tierStdMin:
		return "standard"
	default:
		return "low"
	}
}

// Usage-term constants: Laplace smoothing α and saturating confidence κ. Both
// are fixed here (not config) — tunable later if the benchmark motivates it.
const (
	usageAlpha = 1.0
	usageKappa = 5.0
)

// usageTerm is the bounded, confidence-damped success-rate adjustment added to a
// floor-surviving candidate's score when weight > 0: (2p̂−1)·conf·weight, with
// p̂ Laplace-smoothed and conf saturating in n=hit+miss. n=0 ⇒ 0 (cold-start
// neutral; denominators are never zero), bounded by ±weight.
func usageTerm(hit, miss int, weight float64) float64 {
	n := float64(hit + miss)
	p := (float64(hit) + usageAlpha) / (n + 2*usageAlpha)
	conf := n / (n + usageKappa)
	return weight * (2*p - 1) * conf
}

// scoredRow pairs a fused SearchResult with its candidate embedding so the two
// travel together through sorting — fuseHybridScored is the only place both
// are known at once (SearchResult itself never carries the vector).
type scoredRow struct {
	result SearchResult
	emb    pgvector.Vector
}

// fuseHybridScored normalizes lexical ranks to the batch max, fuses with vector
// similarity, drops sub-floor results, tiers, and sorts desc — UNTRUNCATED.
// Lexical-only matches are kept (the point of the FULL OUTER JOIN) but weighted by
// lexical weight alone, so they top out at "standard" tier. Returns results
// alongside their embeddings (aligned 1:1, sub-floor rows dropped from both) for
// callers that need them (applyMMR); fuseHybrid discards the embeddings.
//
// usageWeight > 0 adds each floor-surviving candidate's recall-usage term to the
// ranking score (consumed by the sort and MMR relevance). The floor still gates
// the BASE score, and the tier is calibrated from the BASE score — usage only
// re-orders survivors, it never resurrects a sub-floor row nor re-tiers one.
// usageWeight == 0 skips the term entirely ⇒ output byte-identical to pre-change.
func fuseHybridScored(rows []hybridRow, usageWeight float64) ([]SearchResult, []pgvector.Vector) {
	var maxLex float64
	for _, r := range rows {
		if r.HasLex && r.LexRank > maxLex {
			maxLex = r.LexRank
		}
	}

	scored := make([]scoredRow, 0, len(rows))
	for _, r := range rows {
		vec := r.VecSim
		if vec < 0 {
			vec = 0 // cosine similarity can go negative; clamp.
		}
		var lex float64
		if r.HasLex && maxLex > 0 {
			lex = r.LexRank / maxLex
		}

		var score float64
		switch {
		case r.HasVec && r.HasLex:
			// A lexical co-match is a boost, never a penalty: a row matching BOTH
			// signals must not score below what its vector match alone would give.
			// Otherwise a weak normalized lexical rank drags a strong vector match
			// down (and can push it under scoreFloor, dropping a genuinely relevant
			// result) — the opposite of "matched both ⇒ more relevant".
			score = max(vec, fuseVecWeight*vec+fuseLexWeight*lex)
		case r.HasVec:
			score = vec
		default: // lexical-only
			score = fuseLexWeight * lex
		}
		if score < scoreFloor {
			continue
		}

		tier := relevanceTier(score) // from BASE score; usage re-orders, never re-tiers.
		if usageWeight > 0 {
			score += usageTerm(r.HitCount, r.MissCount, usageWeight)
		}

		scored = append(scored, scoredRow{
			result: SearchResult{
				SectionID:      r.SectionID,
				DocumentID:     r.DocumentID,
				Heading:        r.Heading,
				Content:        r.Content,
				Score:          score,
				Tier:           tier,
				Category:       r.Category,
				Subcategory:    r.Subcategory,
				Slug:           r.Slug,
				DocTitle:       r.DocTitle,
				DocType:        r.DocType,
				TenantID:       r.TenantID,
				VerifiedAt:     r.VerifiedAt,
				SectionCreated: r.SectionCreated,
			},
			emb: r.Embedding,
		})
	}

	sort.SliceStable(scored, func(i, j int) bool { return scored[i].result.Score > scored[j].result.Score })

	out := make([]SearchResult, len(scored))
	embs := make([]pgvector.Vector, len(scored))
	for i, s := range scored {
		out[i] = s.result
		embs[i] = s.emb
	}
	return out, embs
}

// fuseHybrid is the plain fuse-then-truncate path (MMR off). Thin wrapper
// around fuseHybridScored, threading the usage weight through.
func fuseHybrid(rows []hybridRow, usageWeight float64, limit int) []SearchResult {
	out, _ := fuseHybridScored(rows, usageWeight)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// mmrMaxCandidates guards a pathologically large pool; the real CTEs cap at
// 20+20=40 candidates so this never triggers in normal operation.
const mmrMaxCandidates = 128

// cosineSimilarity is the unit-normalized dot product of two embeddings. A
// zero-norm vector (e.g. a lexical-only candidate with no real embedding) is
// treated as maximally dissimilar — similarity 0, not an error.
func cosineSimilarity(a, b pgvector.Vector) float64 {
	av, bv := a.Slice(), b.Slice()
	if len(av) == 0 || len(av) != len(bv) {
		return 0
	}
	var dot, na, nb float64
	for i := range av {
		dot += float64(av[i]) * float64(bv[i])
		na += float64(av[i]) * float64(av[i])
		nb += float64(bv[i]) * float64(bv[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// applyMMR greedily re-ranks the fused, score-sorted, UNTRUNCATED scored list
// by maximizing lambda*relevance - (1-lambda)*maxCosineToSelected at each
// step (first pick has no penalty), then truncates to limit (limit<=0 means
// no truncation). embs[i] must be the embedding of scored[i] (aligned, same
// order as fuseHybridScored's output). lambda>=0.999 is an escape hatch that
// returns the score-sorted order truncated — identical to the default path.
// Every returned result is a member of scored (no fabrication), unique (no
// duplicates), with count = min(len(scored), limit).
func applyMMR(scored []SearchResult, embs []pgvector.Vector, lambda float64, limit int) []SearchResult {
	if lambda >= 0.999 {
		out := scored
		if limit > 0 && len(out) > limit {
			out = out[:limit]
		}
		return out
	}

	pool, poolEmbs := scored, embs
	if len(pool) > mmrMaxCandidates {
		pool, poolEmbs = pool[:mmrMaxCandidates], poolEmbs[:mmrMaxCandidates]
	}

	n := len(pool)
	k := n
	if limit > 0 && limit < n {
		k = limit
	}

	picked := make([]bool, n)
	maxSim := make([]float64, n) // running max cosine to already-selected, per candidate
	out := make([]SearchResult, 0, k)

	for len(out) < k {
		best := -1
		var bestMMR float64
		for i := 0; i < n; i++ {
			if picked[i] {
				continue
			}
			mmr := lambda*pool[i].Score - (1-lambda)*maxSim[i]
			if best == -1 || mmr > bestMMR {
				best, bestMMR = i, mmr
			}
		}
		picked[best] = true
		out = append(out, pool[best])
		for i := 0; i < n; i++ {
			if picked[i] {
				continue
			}
			if sim := cosineSimilarity(poolEmbs[best], poolEmbs[i]); sim > maxSim[i] {
				maxSim[i] = sim
			}
		}
	}
	return out
}

// HybridSearch gathers vector and lexical candidates (scope-filtered, capped) via
// a FULL OUTER JOIN so lexical-only matches are recalled, then fuses them in Go.
// Scope filters run inside both CTEs, before ranking.
func (r *SectionRepository) HybridSearch(ctx context.Context, p SearchParams) ([]SearchResult, error) {
	if p.Limit <= 0 {
		p.Limit = 10
	}

	tenants := p.TenantIDs
	vec := p.Embedding.String()

	sql := `
		WITH semantic AS (
			SELECT s.id, s.document_id, s.heading, s.content, s.verified_at,
				   s.created_at AS section_created, s.embedding,
				   s.hit_count, s.miss_count,
				   1 - (s.embedding <=> ?::vector) AS vec_sim
			FROM sections s
			JOIN documents d ON d.id = s.document_id
			WHERE d.tenant_id IN ?
			  AND d.archived_at IS NULL
			  AND (?::text IS NULL OR d.category = ?)
			  AND (?::text IS NULL OR d.subcategory = ?)
			  AND s.embedding IS NOT NULL
			ORDER BY s.embedding <=> ?::vector
			LIMIT 20
		),
		keyword AS (
			SELECT s.id, s.document_id, s.heading, s.content, s.verified_at,
				   s.created_at AS section_created, s.embedding,
				   s.hit_count, s.miss_count,
				   ts_rank(s.tsv, plainto_tsquery('english', ?)) AS lex_rank
			FROM sections s
			JOIN documents d ON d.id = s.document_id
			WHERE d.tenant_id IN ?
			  AND d.archived_at IS NULL
			  AND (?::text IS NULL OR d.category = ?)
			  AND (?::text IS NULL OR d.subcategory = ?)
			  AND s.tsv @@ plainto_tsquery('english', ?)
			ORDER BY lex_rank DESC
			LIMIT 20
		)
		SELECT COALESCE(sem.id, kw.id)                       AS section_id,
			   COALESCE(sem.document_id, kw.document_id)     AS document_id,
			   COALESCE(sem.heading, kw.heading)             AS heading,
			   COALESCE(sem.content, kw.content)             AS content,
			   COALESCE(sem.vec_sim, 0)                      AS vec_sim,
			   COALESCE(kw.lex_rank, 0)                      AS lex_rank,
			   (sem.id IS NOT NULL)                          AS has_vec,
			   (kw.id IS NOT NULL)                           AS has_lex,
			   d.category, d.subcategory, d.slug, d.title    AS doc_title,
			   d.doc_type, d.tenant_id,
			   COALESCE(sem.hit_count, kw.hit_count, 0)      AS hit_count,
			   COALESCE(sem.miss_count, kw.miss_count, 0)    AS miss_count,
			   COALESCE(sem.verified_at, kw.verified_at)     AS verified_at,
			   COALESCE(sem.section_created, kw.section_created) AS section_created,
			   -- '[0]'::vector fallback: guards the Go-side pgvector.Vector scan
			   -- against a legacy NULL-embedding row (only sem is filtered
			   -- NOT NULL); norm-zero so cosineSimilarity treats it as 0 anyway.
			   COALESCE(sem.embedding, kw.embedding, '[0]'::vector) AS embedding
		FROM semantic sem
		FULL OUTER JOIN keyword kw ON kw.id = sem.id
		JOIN documents d ON d.id = COALESCE(sem.document_id, kw.document_id)
	`

	args := []any{
		vec, tenants, p.Category, p.Category, p.Subcategory, p.Subcategory, vec, // semantic
		p.Query, tenants, p.Category, p.Category, p.Subcategory, p.Subcategory, p.Query, // keyword
	}

	var rows []hybridRow
	if err := r.db.WithContext(ctx).Raw(sql, args...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("hybrid search: %w", err)
	}
	if p.MMRLambda == nil {
		return fuseHybrid(rows, p.UsageWeight, p.Limit), nil
	}
	scored, embs := fuseHybridScored(rows, p.UsageWeight)
	return applyMMR(scored, embs, *p.MMRLambda, p.Limit), nil
}

// Snippet highlight sentinels: PUA runes ts_headline wraps around matched terms.
// Their presence signals a centered match; absence signals the leading-text
// fallback. Shared with the service-side strip/detect helpers — single source.
const (
	SnippetStartSel = ""
	SnippetStopSel  = ""
)

// hlSnippet is the raw ts_headline scan row (text still carries the sentinels).
type hlSnippet struct {
	ID   uuid.UUID `gorm:"column:id"`
	Snip string    `gorm:"column:snip"`
}

// Snippets returns per-section ts_headline windows for the given section IDs,
// keyed by id. snippetChars sets the word budget (MaxWords ~= chars/6, floor 8);
// the d.tenant_id filter is defense-in-depth (IDs already came from a scoped
// result). Empty sectionIDs -> empty map, no query. Values still contain the PUA
// sentinels so the caller can detect centering before stripping.
func (r *SectionRepository) Snippets(ctx context.Context, tenantIDs []uuid.UUID, query string, sectionIDs []uuid.UUID, snippetChars int) (map[uuid.UUID]string, error) {
	out := make(map[uuid.UUID]string, len(sectionIDs))
	if len(sectionIDs) == 0 {
		return out, nil
	}
	maxWords := snippetChars / 6
	if maxWords < 8 {
		maxWords = 8
	}
	opts := fmt.Sprintf("StartSel=%s, StopSel=%s, MaxWords=%d, MinWords=%d, ShortWord=3, MaxFragments=0, HighlightAll=FALSE",
		SnippetStartSel, SnippetStopSel, maxWords, maxWords/2)
	const sql = `
		SELECT s.id, ts_headline('english', s.content, plainto_tsquery('english', ?), ?) AS snip
		FROM sections s
		JOIN documents d ON d.id = s.document_id
		WHERE s.id IN ? AND d.tenant_id IN ?`
	var rows []hlSnippet
	if err := r.db.WithContext(ctx).Raw(sql, query, opts, sectionIDs, tenantIDs).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("snippets: %w", err)
	}
	for _, row := range rows {
		out[row.ID] = row.Snip
	}
	return out, nil
}

type RelatedResult struct {
	DocumentID  uuid.UUID `json:"document_id"`
	Category    string    `json:"category"`
	Subcategory *string   `json:"subcategory,omitempty"`
	Slug        string    `json:"slug"`
	DocTitle    string    `json:"doc_title"`
	Similarity  float64   `json:"similarity"`

	// Owning-tenant label (cross-tenant reads). TenantID comes from SQL; Name and
	// Type are resolved by the service layer for the distinct result tenants.
	TenantID   uuid.UUID `json:"tenant_id"`
	TenantName string    `json:"tenant_name,omitempty"`
	TenantType string    `json:"tenant_type,omitempty"`
}

// GetRelated returns documents semantically related to documentID, restricted to
// the caller's readable tenant set (tenant_id IN tenantIDs) so no result can
// leak a tenant outside that set. The service layer computes tenantIDs via
// readScope/readableTenants and resolves the per-result tenant labels.
func (r *SectionRepository) GetRelated(ctx context.Context, tenantIDs []uuid.UUID, documentID uuid.UUID, limit int) ([]RelatedResult, error) {
	if limit <= 0 {
		limit = 5
	}

	sql := `
		WITH target_avg AS (
			SELECT AVG(embedding) AS avg_embedding
			FROM sections
			WHERE document_id = ?
			  AND embedding IS NOT NULL
		),
		candidates AS (
			SELECT s.document_id,
				   1 - (s.embedding <=> (SELECT avg_embedding FROM target_avg)) AS similarity
			FROM sections s
			JOIN documents d ON d.id = s.document_id
			WHERE s.document_id != ?
			  AND d.tenant_id IN ?
			  AND d.archived_at IS NULL
			  AND s.embedding IS NOT NULL
		)
		SELECT c.document_id, d.tenant_id, d.category, d.subcategory, d.slug, d.title AS doc_title,
			   AVG(c.similarity) AS similarity
		FROM candidates c
		JOIN documents d ON d.id = c.document_id
		GROUP BY c.document_id, d.tenant_id, d.category, d.subcategory, d.slug, d.title
		ORDER BY similarity DESC
		LIMIT ?
	`

	args := []any{documentID, documentID, tenantIDs, limit}

	var results []RelatedResult
	if err := r.db.WithContext(ctx).Raw(sql, args...).Scan(&results).Error; err != nil {
		return nil, fmt.Errorf("get related: %w", err)
	}
	return results, nil
}

func (r *SectionRepository) DeleteByDocumentID(ctx context.Context, docID uuid.UUID) error {
	return r.db.WithContext(ctx).Where("document_id = ?", docID).Delete(&models.Section{}).Error
}

func (r *SectionRepository) CreateBatch(ctx context.Context, sections []models.Section) error {
	if len(sections) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Create(&sections).Error
}

func (r *SectionRepository) Update(ctx context.Context, section *models.Section) error {
	return r.db.WithContext(ctx).Save(section).Error
}

func (r *SectionRepository) GetByID(ctx context.Context, tenantID uuid.UUID, id uuid.UUID) (*models.Section, error) {
	var section models.Section
	if err := r.db.WithContext(ctx).
		Joins("JOIN documents d ON d.id = sections.document_id").
		Where("d.tenant_id IN ?", readTenants(tenantID)).
		Preload("Document").
		First(&section, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: section %s", apperr.ErrNotFound, id)
		}
		return nil, err
	}
	return &section, nil
}

// MarkVerified sets verified_at = NOW() if the section belongs to one of the
// caller's accessible tenants. ErrNotFound if not in scope.
func (r *SectionRepository) MarkVerified(ctx context.Context, tenantID uuid.UUID, id uuid.UUID) error {
	result := r.db.WithContext(ctx).
		Model(&models.Section{}).
		Where(`id = ? AND document_id IN (SELECT id FROM documents WHERE tenant_id IN ?)`, id, readTenants(tenantID)).
		Update("verified_at", gorm.Expr("NOW()"))
	if result.Error != nil {
		return fmt.Errorf("mark verified: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("%w: section %s", apperr.ErrNotFound, id)
	}
	return nil
}
