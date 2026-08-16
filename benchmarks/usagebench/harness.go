//go:build usagebench

package usagebench

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/authzseed"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/service"
	"github.com/eliminyro/memory-system/internal/staleness"
)

// RunConfig parameterises a full sweep. Grids default to the design D5 spec.
type RunConfig struct {
	Seed         int64
	GapFraction  float64 // gap workload; the control is always 0
	Weights      []float64
	Noises       []float64
	MMRLambda    float64
	HeldOutLimit int
	ServedDepth  int // history recall depth (receipt size); covers the pool so gap docs are credited
	JudgeWindow  int // recall-level success window J: success iff a relevant doc is in the served top-J
}

// DefaultRunConfig returns the pre-registered sweep grids (design D5, tasks 3.3).
func DefaultRunConfig() RunConfig {
	return RunConfig{
		Seed:         42,
		GapFraction:  0.4,
		Weights:      []float64{0, 0.5, 1, 2, 3, 5},
		Noises:       []float64{0, 0.1, 0.2, 0.35, 0.5},
		MMRLambda:    0.9,
		HeldOutLimit: 20,
		ServedDepth:  20,
		JudgeWindow:  5,
	}
}

// harness owns a disposable tenant and drives the real service in-process.
type harness struct {
	db       *gorm.DB
	store    authz.Store
	ctx      context.Context
	tenantID uuid.UUID
	category string
	baseSvc  *service.MemoryService // weight 0, receipts on — used for ingest + history

	secByDoc     map[string]uuid.UUID
	docBySection map[uuid.UUID]Doc
}

// newHarness provisions a fresh isolated tenant (never prod / never `pe`).
func newHarness(ctx context.Context, db *gorm.DB, store authz.Store) (*harness, error) {
	// Bootstrap/common tenant + its tuples (mirrors the service integration
	// fixture) so read-scope resolution succeeds. No docs are seeded into it.
	boot := models.Tenant{ID: models.BootstrapTenantID, Name: "common-pool"}
	if err := db.Where("id = ?", models.BootstrapTenantID).FirstOrCreate(&boot).Error; err != nil {
		return nil, fmt.Errorf("bootstrap tenant: %w", err)
	}
	if err := store.Write(ctx, authzseed.TenantSystemEdge(models.BootstrapTenantID)); err != nil {
		return nil, fmt.Errorf("bootstrap system edge: %w", err)
	}
	if err := store.Write(ctx, authzseed.CommonPoolViewerWildcard()); err != nil {
		return nil, fmt.Errorf("common pool wildcard: %w", err)
	}

	tid := uuid.New()
	ten := models.Tenant{ID: tid, Name: "usagebench-" + tid.String()[:8], Type: models.TenantTypeShared}
	if err := db.Create(&ten).Error; err != nil {
		return nil, fmt.Errorf("create bench tenant: %w", err)
	}
	if err := store.Write(ctx, authzseed.TenantSystemEdge(tid)); err != nil {
		return nil, fmt.Errorf("bench system edge: %w", err)
	}
	subj := "usagebench-user-" + uuid.NewString()
	if err := store.Write(ctx, authzseed.TenantMember(tid, subj)); err != nil {
		return nil, fmt.Errorf("bench member: %w", err)
	}

	bctx := auth.WithSubject(auth.WithTenantID(ctx, tid), auth.Subject{Type: auth.SubjectTypeUser, ID: subj})
	h := &harness{db: db, store: store, ctx: bctx, tenantID: tid, category: "usagebench"}
	h.baseSvc = h.newSvc(service.WithMMRLambda(defaultMMR))
	return h, nil
}

const defaultMMR = 0.9

func (h *harness) newSvc(opts ...service.Option) *service.MemoryService {
	return service.NewMemoryService(
		h.db,
		repository.NewDocumentRepository(h.db),
		repository.NewSectionRepository(h.db),
		service.NewFakeEmbedder(768),
		repository.NewTenantRepository(h.db),
		repository.NewAPIKeyRepository(h.db),
		repository.NewLintRepository(h.db),
		staleness.NewThresholdStore(h.db),
		repository.NewOverrideLogRepository(h.db),
		repository.NewCleanupQueueRepository(h.db),
		repository.NewRecallReceiptRepository(h.db),
		h.store,
		opts...,
	)
}

// ingest stores every corpus doc (one section each) then overwrites the section
// embedding with its designed vector, so the geometry is exact (design D4).
func (h *harness) ingest(c *Corpus) error {
	h.secByDoc = make(map[string]uuid.UUID, len(c.Docs))
	h.docBySection = make(map[uuid.UUID]Doc, len(c.Docs))
	for _, d := range c.Docs {
		subcat := d.Subcat
		content := "# " + d.ID + "\n\n## body\n" + d.Text
		res, err := h.baseSvc.StoreDocument(h.ctx, h.category, &subcat, d.ID, content, true, "usagebench seed", nil)
		if err != nil {
			return fmt.Errorf("store %s: %w", d.ID, err)
		}
		if res.Document == nil || len(res.Document.Sections) == 0 {
			return fmt.Errorf("store %s: no section returned", d.ID)
		}
		secID := res.Document.Sections[0].ID
		if err := h.db.Model(&models.Section{}).
			Where("id = ?", secID).
			Update("embedding", pgvector.NewVector(d.Embedding)).Error; err != nil {
			return fmt.Errorf("override embedding for %s: %w", d.ID, err)
		}
		h.secByDoc[d.ID] = secID
		h.docBySection[secID] = d
	}
	return nil
}

// resetCounts zeroes usage on this tenant's sections before a noise level's
// history phase, so each noise level starts from a clean slate on one ingest.
func (h *harness) resetCounts() error {
	return h.db.Exec(
		`UPDATE sections SET hit_count = 0, miss_count = 0
		 WHERE document_id IN (SELECT id FROM documents WHERE tenant_id = ?)`,
		h.tenantID,
	).Error
}

// judge is the simulated agent's noisy call on a whole recall: a truly-useful
// recall is reported failed with prob fn; a truly-useless one reported succeeded
// with prob fp.
func judge(success bool, fp, fn float64, rng *rand.Rand) bool {
	if success {
		return rng.Float64() >= fn
	}
	return rng.Float64() < fp
}

// runHistory generates usage the ONLY faithful way (design D8): per history
// query it runs ONE realistic recall at servedDepth, forms ONE recall-level
// judgment (success iff a truly-relevant doc — topic membership — appears in the
// served top-J), flips it with FP/FN noise, and calls ReportRecallOutcome ONCE.
// That single call credits ALL served sections together, exactly as the real
// mechanism does. The resulting signal is success-rate-under-co-service:
// relevant docs (served in successful primary recalls) accrue hits; distractors
// (co-served on primary AND served alone on failing secondary recalls) accrue a
// lower hit rate. Counts are NEVER injected onto gold docs.
func (h *harness) runHistory(c *Corpus, fp, fn float64, rng *rand.Rand, servedDepth, judgeWindow int) error {
	for _, q := range c.History {
		served, rid, err := h.baseSvc.Search(h.ctx, q.Text, &h.category, nil, servedDepth, false, "", nil, false)
		if err != nil {
			return fmt.Errorf("history recall %s: %w", q.ID, err)
		}
		if rid == uuid.Nil || len(served) == 0 {
			continue // empty recall ⇒ no receipt ⇒ nothing to credit
		}
		trueSuccess := false
		for i, r := range served {
			if i >= judgeWindow {
				break
			}
			if d, ok := h.docBySection[r.SectionID]; ok && d.Topic == q.Topic {
				trueSuccess = true
				break
			}
		}
		outcome := models.RecallOutcomeFailure
		if judge(trueSuccess, fp, fn, rng) {
			outcome = models.RecallOutcomeSuccess
		}
		if err := h.baseSvc.ReportRecallOutcome(h.ctx, rid, outcome, nil); err != nil {
			return fmt.Errorf("report outcome %s: %w", q.ID, err)
		}
	}
	return nil
}

// runHeldOut measures held-out retrieval for one weighted service over the
// disjoint held-out slice (relevance = topic membership).
func (h *harness) runHeldOut(c *Corpus, svc *service.MemoryService, limit int) (Metrics, error) {
	ms := make([]Metrics, 0, len(c.HeldOut))
	for _, q := range c.HeldOut {
		results, _, err := svc.Search(h.ctx, q.Text, &h.category, nil, limit, false, "", nil, false)
		if err != nil {
			return Metrics{}, fmt.Errorf("held-out recall %s: %w", q.ID, err)
		}
		ranked := make([]string, len(results))
		for i, r := range results {
			ranked[i] = r.SectionID.String()
		}
		ms = append(ms, evaluate(ranked, h.goldSections(c, q.Topic)))
	}
	return meanMetrics(ms), nil
}

func (h *harness) goldSections(c *Corpus, topic int) []string {
	ids := c.RelevantDocIDs(topic)
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if sec, ok := h.secByDoc[id]; ok {
			out = append(out, sec.String())
		}
	}
	return out
}

// assertDisjoint enforces the non-circularity invariant: history and held-out
// query IDs must not overlap (design D7 / spec).
func assertDisjoint(c *Corpus) error {
	hist := make(map[string]struct{}, len(c.History))
	for _, q := range c.History {
		hist[q.ID] = struct{}{}
	}
	for _, q := range c.HeldOut {
		if _, clash := hist[q.ID]; clash {
			return fmt.Errorf("history and held-out share query id %q", q.ID)
		}
	}
	return nil
}

// Run executes the full workload × noise × weight matrix and returns Results
// with a computed D5 verdict. Each workload uses a fresh isolated tenant.
func Run(ctx context.Context, db *gorm.DB, store authz.Store, cfg RunConfig) (Results, error) {
	workloads := []struct {
		name string
		gf   float64
	}{
		{WorkloadGap, cfg.GapFraction},
		{WorkloadNoGap, 0},
	}

	var cells []Cell
	for wi, wl := range workloads {
		h, err := newHarness(ctx, db, store)
		if err != nil {
			return Results{}, err
		}
		params := DefaultParams(cfg.Seed, wl.gf)
		corpus, err := Generate(params)
		if err != nil {
			return Results{}, err
		}
		if err := assertDisjoint(corpus); err != nil {
			return Results{}, err
		}
		if err := h.ingest(corpus); err != nil {
			return Results{}, err
		}
		for ni, noise := range cfg.Noises {
			if err := h.resetCounts(); err != nil {
				return Results{}, fmt.Errorf("reset counts: %w", err)
			}
			rng := rand.New(rand.NewSource(cfg.Seed*10007 + int64(wi)*1009 + int64(ni)))
			if err := h.runHistory(corpus, noise, noise, rng, cfg.ServedDepth, cfg.JudgeWindow); err != nil {
				return Results{}, err
			}
			for _, w := range cfg.Weights {
				svc := h.newSvc(service.WithMMRLambda(cfg.MMRLambda), service.WithUsageWeight(w), service.WithRecallReceipts(false))
				m, err := h.runHeldOut(corpus, svc, cfg.HeldOutLimit)
				if err != nil {
					return Results{}, err
				}
				cells = append(cells, Cell{Workload: wl.name, Noise: noise, Weight: w, Metrics: m})
			}
		}
	}

	res := Results{
		Run: RunMeta{
			Status:           "complete",
			Seed:             cfg.Seed,
			Dim:              768,
			EmbedderProvider: "fake",
			Weights:          cfg.Weights,
			Noises:           cfg.Noises,
			Commit:           gitCommit(),
			Timestamp:        time.Now().UTC().Format(time.RFC3339),
		},
		Params:  DefaultParams(cfg.Seed, cfg.GapFraction),
		Cells:   cells,
		Verdict: ComputeVerdict(cells),
	}
	return res, nil
}
