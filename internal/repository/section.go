package repository

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
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
	Tier           string     `json:"relevance,omitempty"` // high | standard | low — from match structure
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
	Status        string `json:"status,omitempty"`         // needs_verification (served) | expired (withheld)
	Preview       string `json:"preview,omitempty"`        // heading-based preview of a withheld body
	StaleDays     int    `json:"age_days,omitempty"`       // age since verified_at in days
	ThresholdDays int    `json:"threshold_days,omitempty"` // the tier's threshold in days

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
	DocType     *string
	Limit       int
	// CandidatePool is the per-list SQL LIMIT (semantic + lexical) before fusion;
	// <= 0 falls back to defaultCandidatePool so direct callers stay sane.
	CandidatePool int
	// MMRLambda gates optional MMR diversity re-ranking; nil (default) leaves
	// HybridSearch byte-identical to the plain fused, score-sorted path.
	MMRLambda *float64
	// StalenessPenalty (weight in [0,1]; 0=off) down-weights candidates verified
	// past their doc_type threshold, applied post-fusion/pre-MMR. StalenessThresholds
	// maps doc_type -> day threshold; a missing entry or nil VerifiedAt = no penalty.
	StalenessPenalty    float64
	StalenessThresholds map[string]int
	// HiddenDocTypes are excluded from an unfiltered query (default_search=false),
	// suppressed when the caller names a Category or DocType filter (design D7).
	HiddenDocTypes []string
}

// Hybrid retrieval fusion constants (Reciprocal Rank Fusion).
const (
	rrfK                 = 60  // RRF damping constant (Cormack et al. 2009)
	vecOnlyFloor         = 0.4 // drop vector-only rows below this raw cosine (semantic noise)
	defaultCandidatePool = 20  // per-list SQL LIMIT fallback when SearchParams.CandidatePool <= 0
)

// nonNilArray coalesces a nil doc_type slice to a non-nil empty pq array, so a
// `doc_type <> ALL(?)` bind is '{}' (excludes nothing) — never NULL, which would
// exclude everything and fail closed.
func nonNilArray(dts []string) pq.StringArray {
	if dts == nil {
		return pq.StringArray{}
	}
	return pq.StringArray(dts)
}

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
	// Embedding backs MMR re-ranking only; never copied onto SearchResult.
	Embedding pgvector.Vector `gorm:"column:embedding"`
}

// scoredRow pairs a fused SearchResult with its candidate embedding so the two
// travel together through sorting — fuseHybridScored is the only place both
// are known at once (SearchResult itself never carries the vector).
type scoredRow struct {
	result SearchResult
	emb    pgvector.Vector
}

// rrfContrib is a candidate's Reciprocal Rank Fusion contribution from one list.
func rrfContrib(rank int) float64 { return 1.0 / float64(rrfK+rank) }

// stalenessFactor down-weights a candidate verified past its doc_type threshold:
// 1.0 within threshold, a linear ramp to a clamped floor of 1-weight at >= 2x the
// threshold. weight<=0 or thresholdDays<=0 disables it (identity).
func stalenessFactor(ageDays, thresholdDays int, weight float64) float64 {
	if weight <= 0 || thresholdDays <= 0 || ageDays <= thresholdDays {
		return 1.0
	}
	over := float64(ageDays-thresholdDays) / float64(thresholdDays)
	if over > 1 {
		over = 1
	}
	return 1 - weight*over
}

// rrfRanks reconstructs a list's 1-based native ranks from the raw scores the SQL
// already returned: rows matching `has`, ordered by `val` desc (stable), keyed by
// SectionID.
func rrfRanks(rows []hybridRow, has func(hybridRow) bool, val func(hybridRow) float64) map[uuid.UUID]int {
	type rv struct {
		id  uuid.UUID
		val float64
	}
	list := make([]rv, 0, len(rows))
	for _, r := range rows {
		if has(r) {
			list = append(list, rv{r.SectionID, val(r)})
		}
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].val != list[j].val {
			return list[i].val > list[j].val
		}
		return list[i].id.String() < list[j].id.String()
	})
	ranks := make(map[uuid.UUID]int, len(list))
	for i, e := range list {
		ranks[e.id] = i + 1
	}
	return ranks
}

// fusionTier labels a result from match structure, not a score cut: both-list is
// "high"; a single-list match is "standard" in the top half of the pool and "low"
// deeper. The top-half boundary tracks the configured poolSize.
func fusionTier(r hybridRow, vecRank, lexRank, poolSize int) string {
	if r.HasVec && r.HasLex {
		return "high"
	}
	rank := vecRank
	if r.HasLex {
		rank = lexRank
	}
	if rank <= poolSize/2 {
		return "standard"
	}
	return "low"
}

// fuseHybridScored fuses the semantic and lexical lists by Reciprocal Rank Fusion,
// gates vector-only noise, tiers by match structure, sorts desc — UNTRUNCATED.
// Returns results with embeddings aligned 1:1 (gated rows dropped) for applyMMR.
// Stale candidates are down-weighted (weight+thresholds) before the sort so both
// the MMR and MMR-off paths consume correctly-ordered penalized scores.
func fuseHybridScored(rows []hybridRow, poolSize int, weight float64, thresholds map[string]int) ([]SearchResult, []pgvector.Vector) {
	kept := make([]hybridRow, 0, len(rows))
	for _, r := range rows {
		// Gate distant semantic-only neighbours; both-list and lexical-only survive.
		if r.HasVec && !r.HasLex && r.VecSim < vecOnlyFloor {
			continue
		}
		kept = append(kept, r)
	}

	vecRank := rrfRanks(kept, func(r hybridRow) bool { return r.HasVec }, func(r hybridRow) float64 { return r.VecSim })
	lexRank := rrfRanks(kept, func(r hybridRow) bool { return r.HasLex }, func(r hybridRow) float64 { return r.LexRank })

	scored := make([]scoredRow, len(kept))
	for i, r := range kept {
		vr, lr := vecRank[r.SectionID], lexRank[r.SectionID]
		var score float64
		if r.HasVec {
			score += rrfContrib(vr)
		}
		if r.HasLex {
			score += rrfContrib(lr)
		}
		// Down-weight stale candidates on the fused score, pre-sort.
		if weight > 0 && r.VerifiedAt != nil {
			if td, ok := thresholds[r.DocType]; ok && td > 0 {
				ageDays := int(time.Since(*r.VerifiedAt).Hours() / 24)
				score *= stalenessFactor(ageDays, td, weight)
			}
		}
		scored[i] = scoredRow{
			result: SearchResult{
				SectionID:      r.SectionID,
				DocumentID:     r.DocumentID,
				Heading:        r.Heading,
				Content:        r.Content,
				Score:          score,
				Tier:           fusionTier(r, vr, lr, poolSize),
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
		}
	}

	// RRF ties are systematic (equal 1/(k+rank) across lists); break by SectionID so
	// result-set membership at the Limit boundary is stable, not SQL-row-order dependent.
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].result.Score != scored[j].result.Score {
			return scored[i].result.Score > scored[j].result.Score
		}
		return scored[i].result.SectionID.String() < scored[j].result.SectionID.String()
	})

	out := make([]SearchResult, len(scored))
	embs := make([]pgvector.Vector, len(scored))
	for i, s := range scored {
		out[i] = s.result
		embs[i] = s.emb
	}
	return out, embs
}

// fuseHybrid is the plain fuse-then-truncate path (MMR off) — a thin wrapper
// around fuseHybridScored that keeps its own signature stable for callers.
func fuseHybrid(rows []hybridRow, limit, poolSize int, weight float64, thresholds map[string]int) []SearchResult {
	out, _ := fuseHybridScored(rows, poolSize, weight, thresholds)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

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
func applyMMR(scored []SearchResult, embs []pgvector.Vector, lambda float64, limit, poolSize int) []SearchResult {
	if lambda >= 0.999 {
		out := scored
		if limit > 0 && len(out) > limit {
			out = out[:limit]
		}
		return out
	}

	// MMR is O(n²); bound the candidate set to the two pools (2*poolSize) so cost
	// tracks the configured pool without capping recall (SQL already yields <= this).
	maxCandidates := 2 * poolSize
	if maxCandidates <= 0 {
		maxCandidates = 2 * defaultCandidatePool
	}
	pool, poolEmbs := scored, embs
	if len(pool) > maxCandidates {
		pool, poolEmbs = pool[:maxCandidates], poolEmbs[:maxCandidates]
	}

	n := len(pool)
	k := n
	if limit > 0 && limit < n {
		k = limit
	}

	picked := make([]bool, n)
	maxSim := make([]float64, n) // running max cosine to already-selected, per candidate
	out := make([]SearchResult, 0, k)

	// Min-max normalize the pool's raw RRF Score into [0,1] so lambda balances
	// relevance against cosine diversity on one scale (an all-equal pool maps
	// every relevance to 1). Without this the tiny RRF range makes lambda inert.
	rel := make([]float64, n)
	if n > 0 {
		lo, hi := pool[0].Score, pool[0].Score
		for i := 1; i < n; i++ {
			if pool[i].Score < lo {
				lo = pool[i].Score
			}
			if pool[i].Score > hi {
				hi = pool[i].Score
			}
		}
		span := hi - lo
		for i := range pool {
			if span > 0 {
				rel[i] = (pool[i].Score - lo) / span
			} else {
				rel[i] = 1
			}
		}
	}

	for len(out) < k {
		best := -1
		var bestMMR float64
		for i := 0; i < n; i++ {
			if picked[i] {
				continue
			}
			mmr := lambda*rel[i] - (1-lambda)*maxSim[i]
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
	pool := p.CandidatePool
	if pool <= 0 {
		pool = defaultCandidatePool
	}

	sql := `
		WITH semantic AS (
			SELECT s.id, s.document_id, s.heading, s.content, s.verified_at,
				   s.created_at AS section_created, s.embedding,
				   1 - (s.embedding <=> ?::vector) AS vec_sim
			FROM sections s
			JOIN documents d ON d.id = s.document_id
			WHERE d.tenant_id IN ?
			  AND d.archived_at IS NULL
			  AND (?::text IS NULL OR d.category = ?)
			  AND (?::text IS NULL OR d.subcategory = ?)
			  AND (?::text IS NULL OR d.doc_type = ?)
			  AND (?::text IS NOT NULL OR ?::text IS NOT NULL OR d.doc_type <> ALL(?))
			  AND s.embedding IS NOT NULL
			ORDER BY s.embedding <=> ?::vector, s.id
			LIMIT ?
		),
		keyword AS (
			SELECT s.id, s.document_id, s.heading, s.content, s.verified_at,
				   s.created_at AS section_created, s.embedding,
				   ts_rank(s.tsv, plainto_tsquery('english', ?)) AS lex_rank
			FROM sections s
			JOIN documents d ON d.id = s.document_id
			WHERE d.tenant_id IN ?
			  AND d.archived_at IS NULL
			  AND (?::text IS NULL OR d.category = ?)
			  AND (?::text IS NULL OR d.subcategory = ?)
			  AND (?::text IS NULL OR d.doc_type = ?)
			  AND (?::text IS NOT NULL OR ?::text IS NOT NULL OR d.doc_type <> ALL(?))
			  AND s.tsv @@ plainto_tsquery('english', ?)
			ORDER BY lex_rank DESC, s.id
			LIMIT ?
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

	hidden := nonNilArray(p.HiddenDocTypes)
	args := []any{
		vec, tenants, p.Category, p.Category, p.Subcategory, p.Subcategory, p.DocType, p.DocType, p.Category, p.DocType, hidden, vec, pool, // semantic
		p.Query, tenants, p.Category, p.Category, p.Subcategory, p.Subcategory, p.DocType, p.DocType, p.Category, p.DocType, hidden, p.Query, pool, // keyword
	}

	var rows []hybridRow
	if err := r.db.WithContext(ctx).Raw(sql, args...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("hybrid search: %w", err)
	}
	if p.MMRLambda == nil {
		return fuseHybrid(rows, p.Limit, pool, p.StalenessPenalty, p.StalenessThresholds), nil
	}
	scored, embs := fuseHybridScored(rows, pool, p.StalenessPenalty, p.StalenessThresholds)
	return applyMMR(scored, embs, *p.MMRLambda, p.Limit, pool), nil
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
			  -- Target has no embedding => avg is NULL => every similarity NULL and
			  -- rows would rank arbitrarily; short-circuit to no candidates.
			  AND (SELECT avg_embedding FROM target_avg) IS NOT NULL
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

// Delete removes one section by id, returning rows-affected so the caller can
// map 0 -> ErrNotFound. Scope is gated upstream by GetByID.
func (r *SectionRepository) Delete(ctx context.Context, id uuid.UUID) (int64, error) {
	result := r.db.WithContext(ctx).Delete(&models.Section{}, id)
	return result.RowsAffected, result.Error
}

// CountByDocumentID returns how many sections a document currently has.
func (r *SectionRepository) CountByDocumentID(ctx context.Context, docID uuid.UUID) (int64, error) {
	var n int64
	err := r.db.WithContext(ctx).Model(&models.Section{}).
		Where("document_id = ?", docID).Count(&n).Error
	return n, err
}

func (r *SectionRepository) CreateBatch(ctx context.Context, sections []models.Section) error {
	if len(sections) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Create(&sections).Error
}

// CreateBatchNoEmbed inserts sections with a NULL embedding, for doc_types whose
// policy embed is false (no vector generated or stored).
func (r *SectionRepository) CreateBatchNoEmbed(ctx context.Context, sections []models.Section) error {
	if len(sections) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Omit("Embedding").Create(&sections).Error
}

// ListByDocumentID returns a document's sections ordered by ordinal.
func (r *SectionRepository) ListByDocumentID(ctx context.Context, docID uuid.UUID) ([]models.Section, error) {
	var sections []models.Section
	if err := r.db.WithContext(ctx).Where("document_id = ?", docID).Order("ordinal ASC").Find(&sections).Error; err != nil {
		return nil, err
	}
	return sections, nil
}

// UpdateSectionContent replaces one section's content, writing the embedding when
// withEmbed is set and NULLing it otherwise (embed=false doc_types).
func (r *SectionRepository) UpdateSectionContent(ctx context.Context, id uuid.UUID, content string, embedding pgvector.Vector, withEmbed bool) error {
	updates := map[string]any{"content": content, "updated_at": time.Now()}
	if withEmbed {
		updates["embedding"] = embedding
	} else {
		updates["embedding"] = nil
	}
	return r.db.WithContext(ctx).Model(&models.Section{ID: id}).Updates(updates).Error
}

func (r *SectionRepository) Update(ctx context.Context, section *models.Section) error {
	return r.db.WithContext(ctx).Save(section).Error
}

// UpdateOmitEmbedding saves a section's columns EXCEPT embedding, leaving the
// stored vector untouched (NULL for embed=false doc_types like prompts, whose
// zero-value would otherwise serialize to an invalid '[]' and surface in search).
func (r *SectionRepository) UpdateOmitEmbedding(ctx context.Context, section *models.Section) error {
	return r.db.WithContext(ctx).Omit("Embedding").Save(section).Error
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

// StalenessCount is one gauge cell: the current count of stale (or expired)
// sections for a tenant × doc_type, feeding the metrics gauges.
type StalenessCount struct {
	TenantID uuid.UUID `json:"tenant_id"`
	DocType  string    `json:"doc_type"`
	Count    int64     `json:"count"`
}

// CountStaleByTenant counts, per tenant × doc_type, live sections whose age
// (NOW − COALESCE(verified_at, created_at)) exceeds the doc_type's verification
// window. days maps doc_type→verification_age_days; non-positive windows are skipped.
func (r *SectionRepository) CountStaleByTenant(ctx context.Context, days map[string]int) ([]StalenessCount, error) {
	return r.countAgedSections(ctx, days, false)
}

// CountExpiredByTenant counts the same over each doc_type's expiration window, but
// only for hard-mode tenants — matching read-time withholding (expired is hard-only).
func (r *SectionRepository) CountExpiredByTenant(ctx context.Context, days map[string]int) ([]StalenessCount, error) {
	return r.countAgedSections(ctx, days, true)
}

// countAgedSections runs one grouped COUNT per doc_type window. Stale counts raw
// corpus health across all tenants (usable on the default off-mode); hardOnly
// restricts to hard-mode tenants (the expired gauge, matching read-gating).
func (r *SectionRepository) countAgedSections(ctx context.Context, days map[string]int, hardOnly bool) ([]StalenessCount, error) {
	out := make([]StalenessCount, 0, len(days))
	for docType, d := range days {
		if d <= 0 {
			continue
		}
		q := r.db.WithContext(ctx).
			Table("sections AS s").
			Select("doc.tenant_id AS tenant_id, doc.doc_type AS doc_type, COUNT(*) AS count").
			Joins("JOIN documents doc ON doc.id = s.document_id")
		if hardOnly {
			q = q.Joins("JOIN tenants t ON t.id = doc.tenant_id AND t.staleness_mode = ?", models.StalenessModeHard)
		}
		var rows []StalenessCount
		if err := q.
			Where("doc.doc_type = ? AND doc.archived_at IS NULL", docType).
			Where("NOW() - COALESCE(s.verified_at, s.created_at) > make_interval(days => ?)", d).
			Group("doc.tenant_id, doc.doc_type").
			Scan(&rows).Error; err != nil {
			return nil, fmt.Errorf("count aged sections (%s): %w", docType, err)
		}
		out = append(out, rows...)
	}
	return out, nil
}
