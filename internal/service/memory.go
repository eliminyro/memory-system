package service

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/authzseed"
	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/staleness"
)

// logOverride best-effort writes an audit row; on failure it warns and continues
// (the primary op already succeeded — dropping an audit row beats failing it).
func (s *MemoryService) logOverride(ctx context.Context, ev repository.OverrideEvent) {
	if s.overrides == nil {
		return
	}
	if err := s.overrides.Log(ctx, ev); err != nil {
		slog.Default().Warn("override audit write failed",
			"tool", ev.Tool,
			"override_type", ev.OverrideType,
			"error", err,
		)
	}
}

type MemoryService struct {
	db         *gorm.DB
	docs       *repository.DocumentRepository
	sections   *repository.SectionRepository
	embedder   EmbeddingProvider
	tenants    *repository.TenantRepository
	keys       *repository.APIKeyRepository
	lint       *repository.LintRepository
	thresholds *staleness.ThresholdStore
	overrides  *repository.OverrideLogRepository
	cleanup    *repository.CleanupQueueRepository
	// recallReceipts is nil-safe (offline CLI / tests may leave it unwired,
	// silently disabling receipt recording — see recordRecallReceipt).
	recallReceipts *repository.RecallReceiptRepository
	// authz is the relationship-tuple store; lifecycle ops seed it out-of-band.
	// Nil disables seeding.
	authz authz.Store
	// authzEngine is the single authoritative gate for admin, tenant_id override,
	// and cross-tenant/common-pool access. Nil (no authzStore) fails every Check closed.
	authzEngine *authz.Engine
	// inTx is true on a clone produced by withTx — i.e. this service is already
	// bound to a caller's transaction. Lifecycle methods that would otherwise open
	// their own transaction (GrantTenantUser) check this to avoid double-wrapping
	// and to defer atomicity to the caller's tx.
	inTx bool
	// BootstrapToken is the generated first-run token the HTTP Bootstrap path
	// compares the caller's token against (constant-time). Set once at startup in
	// cmd/server/main.go when the instance has no admin yet (design D1); empty means
	// the HTTP path refuses to bootstrap. The offline CLI bypasses it via a
	// local-admin context (design D2), so it need not be set for that path.
	BootstrapToken string
	// OAuthConfigured mirrors cfg.AuthletEnabled(): whether the authlet OAuth login
	// path is wired. Set once at construction in cmd/server/main.go. Bootstrap uses
	// it to decide admin-email seeding (design D4) — an operator email is only
	// useful when logins can actually resolve via OAuth. The offline CLI, which
	// skips config.Load, leaves it false.
	OAuthConfigured bool
	// TenantDefaults are the operator-chosen toggle defaults (staleness_mode,
	// duplicate_guard, cleanup_scan_enabled) stamped onto every tenant created
	// through the service. Set once at startup from config.TenantDefaults; a zero
	// value (unset — offline CLI / tests) leaves creation to the model/DB default.
	TenantDefaults models.TenantDefaults
	// SelfServicePolicyDefault is the operator-chosen global default self-service
	// policy ("open" | "admin_only"); a per-tenant override resolves against it.
	// Set once at startup from config.SelfServicePolicy. Empty (offline CLI /
	// tests) resolves to "open" — no lockout.
	SelfServicePolicyDefault string
	// mmrLambda is the configured MMR diversity re-rank default for Search; nil
	// (no WithMMRLambda option) leaves HybridSearch's plain fused-and-sorted path
	// unchanged, preserving existing behavior for every caller that doesn't set it.
	mmrLambda *float64
	// recallReceiptsEnabled gates receipt recording in Search (MEMORY_RECALL_RECEIPTS,
	// default true). Set explicitly in NewMemoryService so a service built
	// without WithRecallReceipts still defaults to enabled, not the bool zero value.
	recallReceiptsEnabled bool
	// snippetChars caps the match-centered window Search returns when snippet=true.
	// Set explicitly in NewMemoryService so a service built without WithSnippetChars
	// still gets the default, not the int zero value (which would blank snippets).
	snippetChars int
}

// defaultSnippetChars mirrors config MEMORY_SNIPPET_CHARS default so a service
// built without WithSnippetChars (offline CLI / tests) still windows sensibly.
const defaultSnippetChars = 400

// Option configures optional MemoryService behavior at construction time.
type Option func(*MemoryService)

// WithMMRLambda sets the default MMR diversity lambda applied to every Search
// call. Without this option mmrLambda stays nil and MMR re-ranking is off.
func WithMMRLambda(lambda float64) Option {
	return func(s *MemoryService) {
		l := lambda
		s.mmrLambda = &l
	}
}

// WithSnippetChars sets the match-centered snippet window size (chars) applied
// when Search runs in snippet mode. Without it snippetChars keeps its default.
func WithSnippetChars(chars int) Option {
	return func(s *MemoryService) {
		s.snippetChars = chars
	}
}

// WithRecallReceipts overrides the default (enabled) recall-receipt recording
// gate (MEMORY_RECALL_RECEIPTS). Recording still requires a non-nil
// recallReceipts repository; this only controls the config-level on/off switch.
func WithRecallReceipts(enabled bool) Option {
	return func(s *MemoryService) {
		s.recallReceiptsEnabled = enabled
	}
}

// NewMemoryService constructs the service. Optional deps may be nil outside the
// MCP server (e.g. import CLI), disabling their features; a nil authzStore also
// disables tuple seeding and fails every authorization Check closed.
func NewMemoryService(
	db *gorm.DB,
	docs *repository.DocumentRepository,
	sections *repository.SectionRepository,
	embedder EmbeddingProvider,
	tenants *repository.TenantRepository,
	keys *repository.APIKeyRepository,
	lint *repository.LintRepository,
	thresholds *staleness.ThresholdStore,
	overrides *repository.OverrideLogRepository,
	cleanup *repository.CleanupQueueRepository,
	recallReceipts *repository.RecallReceiptRepository,
	authzStore authz.Store,
	opts ...Option,
) *MemoryService {
	var engine *authz.Engine
	if authzStore != nil {
		engine = authz.NewEngine(authzStore)
	}
	s := &MemoryService{
		db:             db,
		docs:           docs,
		sections:       sections,
		embedder:       embedder,
		tenants:        tenants,
		keys:           keys,
		lint:           lint,
		thresholds:     thresholds,
		overrides:      overrides,
		cleanup:        cleanup,
		recallReceipts: recallReceipts,
		authz:          authzStore,
		authzEngine:    engine,
		// Default true (design D7): recording is harmless and additive. A caller
		// that wants it off passes WithRecallReceipts(false).
		recallReceiptsEnabled: true,
		snippetChars:          defaultSnippetChars,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// seedTuple idempotently writes a relation tuple for a lifecycle event. Nil-safe
// and best-effort: the primary op is source of truth; a write miss is logged.
func (s *MemoryService) seedTuple(ctx context.Context, t authz.Tuple) {
	if s.authz == nil {
		return
	}
	if err := s.authz.Write(ctx, t); err != nil {
		slog.Default().Warn("authz tuple seed failed",
			"object_type", t.ObjectType,
			"object_id", t.ObjectID,
			"relation", t.Relation,
			"subject_type", t.SubjectType,
			"subject_id", t.SubjectID,
			"error", err,
		)
	}
}

// syncServicePrincipalAdmin reconciles a tenant's service-principal
// system#admin grant with whether the tenant still has any admin tenant_user.
// The svc principal (svc:<tenant>) is what a subject-less operator API key
// resolves to; its global-admin tuple is seeded add-only at boot
// (authzseed.seedAdminServicePrincipals), so runtime admin grants/removals
// must keep it in sync or the operator key keeps admin after the last admin
// is gone (H1). Counts against the passed tx so it sees the just-applied
// change. Idempotent: Write/Delete are no-ops when the tuple already matches.
func (s *MemoryService) syncServicePrincipalAdmin(ctx context.Context, tx *gorm.DB, tenantID uuid.UUID) error {
	if s.authz == nil {
		return nil
	}
	var admins int64
	if err := tx.WithContext(ctx).Model(&models.TenantUser{}).
		Where("tenant_id = ? AND role = ?", tenantID, models.TenantUserRoleAdmin).
		Count(&admins).Error; err != nil {
		return fmt.Errorf("count tenant admins: %w", err)
	}
	svc := authzseed.SystemAdmin(authz.ServicePrincipalID(tenantID.String()))
	if admins > 0 {
		return s.authz.Write(ctx, svc)
	}
	return s.authz.Delete(ctx, svc)
}

// authorize reports whether the request's subject holds relation on objType:objID.
// The single authorization choke point; fails closed on any uncertainty (nil
// engine, subjectless request, or Check error all deny).
func (s *MemoryService) authorize(ctx context.Context, objType, objID, relation string) bool {
	if s.authzEngine == nil {
		return false
	}
	subj, ok := auth.SubjectFromContext(ctx)
	if !ok || subj.ID == "" {
		return false
	}
	granted, err := s.authzEngine.Check(ctx, objType, objID, relation, subj.Type, subj.ID)
	if err != nil {
		slog.Default().Warn("authz check errored; denying",
			"object_type", objType,
			"object_id", objID,
			"relation", relation,
			"subject_id", subj.ID,
			"error", err,
		)
		return false
	}
	return granted
}

// resolveTenant resolves the effective tenant ID. A tenant_id override is
// admin-gated (only system:memory#admin may target another tenant).
func (s *MemoryService) resolveTenant(ctx context.Context, overrideID *uuid.UUID) (uuid.UUID, error) {
	tid := auth.TenantIDFromContext(ctx)
	if tid == uuid.Nil {
		return uuid.Nil, fmt.Errorf("%w: missing tenant ID in context", apperr.ErrInvalidInput)
	}
	if overrideID == nil {
		return tid, nil
	}
	if !s.isAdmin(ctx) {
		return uuid.Nil, fmt.Errorf("%w: tenant_id override requires admin privileges", apperr.ErrInvalidInput)
	}
	if _, err := s.tenants.GetByID(ctx, *overrideID); err != nil {
		return uuid.Nil, fmt.Errorf("%w: target tenant %s not found", apperr.ErrInvalidInput, overrideID)
	}
	return *overrideID, nil
}

// IsAdmin exposes the admin gate for the admin HTTP middleware (which decides
// 403-vs-proceed before dispatch; service methods re-check, so not the sole
// enforcement point). Admin = system:memory#admin via tuple Check, not tenant.Email.
func (s *MemoryService) IsAdmin(ctx context.Context) bool { return s.isAdmin(ctx) }

func (s *MemoryService) isAdmin(ctx context.Context) bool {
	// The offline memory-admin CLI (direct DB, no authenticated subject) marks its
	// context local-admin to reuse these lifecycle methods. Network paths never set it.
	if auth.IsLocalAdmin(ctx) {
		return true
	}
	return s.authorize(ctx, authz.TypeSystem, authz.SystemObjectID, authz.RelAdmin)
}

// requireSelfService is the single gate over the tenant's self-service surfaces
// (feature-toggle editing, API-key creation). A system admin always passes.
// Otherwise the caller must hold openRel when the effective policy is "open", or
// admin when it is "admin_only" — which, via the owner model (owner ⇏ admin),
// excludes personal owners while admitting shared tenant-admins + system admins,
// with no tenant-type branch. Every self-service mutation MUST route through here.
func (s *MemoryService) requireSelfService(ctx context.Context, tenant *models.Tenant, openRel string) error {
	if s.isAdmin(ctx) {
		return nil
	}
	need := openRel
	if tenant.EffectiveSelfServicePolicy(s.SelfServicePolicyDefault) == models.SelfServicePolicyAdminOnly {
		need = authz.RelAdmin
	}
	if s.authorize(ctx, authz.TypeTenant, tenant.ID.String(), need) {
		return nil
	}
	return fmt.Errorf("%w: self-service is restricted to admins on this tenant", apperr.ErrInvalidInput)
}

// tenantSettings holds per-tenant feature toggles. When the tenants repo is
// unwired (e.g. import CLI), safe defaults apply: staleness off, guard off.
type tenantSettings struct {
	StalenessMode  string
	DuplicateGuard bool
}

func (s *MemoryService) tenantSettings(ctx context.Context, tid uuid.UUID) tenantSettings {
	if s.tenants == nil {
		return tenantSettings{StalenessMode: models.StalenessModeOff}
	}
	t, err := s.tenants.GetByID(ctx, tid)
	if err != nil {
		// Fail safe: unreadable config -> staleness off; never refuse content on a glitch.
		return tenantSettings{StalenessMode: models.StalenessModeOff}
	}
	mode := t.StalenessMode
	if _, ok := models.ValidStalenessModes[mode]; !ok {
		// Fail safe: unrecognised value -> "off" (not "advisory"); never refuse content.
		mode = models.StalenessModeOff
	}
	return tenantSettings{StalenessMode: mode, DuplicateGuard: t.DuplicateGuard}
}

// readableTenants returns the set of tenant IDs the caller may READ across: the
// caller's home tenant, the common (bootstrap) pool, and every tenant for which
// the subject holds a DIRECT viewer/member/manager/owner/admin tuple confirmed by
// an authz viewer Check (owner ⇒ manager ⇒ member ⇒ viewer, so a personal owner's
// direct owner tuple resolves here).
//
// The candidate tenants come from ReadBySubject (direct tuples only), so a
// system admin is NOT expanded into every tenant — their system#admin tuple has
// object type "system" and is skipped here, keeping aggregation bounded and
// meaningful. This mirrors WritableTenants' candidate-then-Check shape but for
// the viewer relation, and is used by the READ path only; writes keep
// resolveTenant (single tenant, admin-only override).
func (s *MemoryService) readableTenants(ctx context.Context) ([]uuid.UUID, error) {
	home := auth.TenantIDFromContext(ctx)
	if home == uuid.Nil {
		return nil, fmt.Errorf("%w: missing tenant ID in context", apperr.ErrInvalidInput)
	}

	seen := make(map[uuid.UUID]struct{}, 4)
	var out []uuid.UUID
	add := func(id uuid.UUID) {
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	// Home and the common pool are always readable.
	add(home)
	add(models.BootstrapTenantID)

	if s.authz == nil {
		return out, nil
	}
	subj, ok := auth.SubjectFromContext(ctx)
	if !ok || subj.ID == "" {
		return out, nil
	}
	tuples, err := s.authz.ReadBySubject(ctx, subj.Type, subj.ID)
	if err != nil {
		return nil, err
	}
	for _, t := range tuples {
		if t.ObjectType != authz.TypeTenant {
			continue // e.g. system#admin — never expanded into every tenant
		}
		tid, err := uuid.Parse(t.ObjectID)
		if err != nil {
			continue
		}
		if _, dup := seen[tid]; dup {
			continue
		}
		// Confirm the direct tuple actually grants read (member/manager/admin all
		// rewrite to viewer); excludes stale or insufficient tuples.
		if !s.authorize(ctx, authz.TypeTenant, tid.String(), authz.RelViewer) {
			continue
		}
		add(tid)
	}
	return out, nil
}

// readScope resolves the tenant-id SET a READ should span. This replaces the
// admin-only tenant override on the read methods with two behaviors:
//
//   - overrideID == nil: aggregate over readableTenants (home + common + every
//     directly-granted readable tenant).
//   - overrideID != nil: narrow to that ONE tenant, but only when the caller may
//     read it — it is home, the common pool, or a viewer Check passes. The viewer
//     Check deliberately lets a system admin target ANY tenant (admin ⇒ viewer),
//     preserving today's admin single-tenant read, while a non-admin may target
//     only a tenant it can actually read. A non-readable target yields an EMPTY
//     scope: an empty result, never that tenant's documents, and never an error
//     that would reveal the tenant's existence.
func (s *MemoryService) readScope(ctx context.Context, overrideID *uuid.UUID) ([]uuid.UUID, error) {
	if overrideID == nil {
		return s.readableTenants(ctx)
	}
	home := auth.TenantIDFromContext(ctx)
	if home == uuid.Nil {
		return nil, fmt.Errorf("%w: missing tenant ID in context", apperr.ErrInvalidInput)
	}
	if *overrideID == home || *overrideID == models.BootstrapTenantID ||
		s.authorize(ctx, authz.TypeTenant, overrideID.String(), authz.RelViewer) {
		return []uuid.UUID{*overrideID}, nil
	}
	return nil, nil // not readable -> empty scope -> empty result, no leak
}

// resolveResultTenants fetches the distinct owning tenants present in a search
// result set in ONE lookup, labels each result with its tenant's name/type, and
// returns a per-tenant staleness-mode map (keyed by tenant id) so staleness can
// be applied under each result's own tenant mode. Fail-safe: on a lookup miss,
// labels are simply absent and modes default to off (never refuse content on a
// glitch).
func (s *MemoryService) resolveResultTenants(ctx context.Context, results []repository.SearchResult) map[uuid.UUID]string {
	modeByTenant := make(map[uuid.UUID]string)
	if len(results) == 0 || s.tenants == nil {
		return modeByTenant
	}
	seen := make(map[uuid.UUID]struct{})
	ids := make([]uuid.UUID, 0)
	for i := range results {
		id := results[i].TenantID
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	tenants, err := s.tenants.GetByIDs(ctx, ids)
	if err != nil {
		return modeByTenant
	}
	byID := make(map[uuid.UUID]models.Tenant, len(tenants))
	for _, t := range tenants {
		byID[t.ID] = t
		mode := t.StalenessMode
		if _, ok := models.ValidStalenessModes[mode]; !ok {
			mode = models.StalenessModeOff
		}
		modeByTenant[t.ID] = mode
	}
	for i := range results {
		if t, ok := byID[results[i].TenantID]; ok {
			results[i].TenantName = t.Name
			results[i].TenantType = t.Type
		}
	}
	return modeByTenant
}

// tenantModeAndLabel fetches a document's owning tenant ONCE and derives both the
// resolved staleness mode (for the staleness view) and the display name/type (for
// labeling) — replacing the two separate GetByID calls (staleness + label) that a
// single-document read previously issued for the same row. It preserves the exact
// fail-safe defaults of the old paths: an unwired tenants repo or a lookup
// miss/error yields staleness "off" and empty name/type, and an unrecognised
// staleness value degrades to "off" — never refusing the read on a config glitch.
func (s *MemoryService) tenantModeAndLabel(ctx context.Context, id uuid.UUID) (stalenessMode, name, typ string) {
	if s.tenants == nil {
		return models.StalenessModeOff, "", ""
	}
	t, err := s.tenants.GetByID(ctx, id)
	if err != nil {
		return models.StalenessModeOff, "", ""
	}
	mode := t.StalenessMode
	if _, ok := models.ValidStalenessModes[mode]; !ok {
		mode = models.StalenessModeOff
	}
	return mode, t.Name, t.Type
}

// Search input bounds shared by every read surface (MCP search_memory and the
// HTTP GET /api/search handler) so the clamp/reject limits live in one place.
const (
	// MaxSearchLimit caps the number of search results a caller may request.
	MaxSearchLimit = 100
	// MaxQueryLen caps the length of a search query string.
	MaxQueryLen = 10_000
	// DefaultListLimit is the document-list page size GET /api/documents applies
	// when limit is absent or non-positive.
	DefaultListLimit = 50
	// MaxListLimit caps the document-list page size a caller may request.
	MaxListLimit = 200
)

// Search performs hybrid semantic + keyword search, applying staleness filter.
// When forceRead is true, Reason is required and the override is audited. The
// second return value is a recall receipt id (uuid.Nil when results are empty,
// recording is disabled, or the insert failed) — see recordRecallReceipt.
func (s *MemoryService) Search(ctx context.Context, query string, category, subcategory *string, limit int, forceRead bool, reason string, overrideID *uuid.UUID, snippet bool) ([]repository.SearchResult, uuid.UUID, error) {
	if forceRead && strings.TrimSpace(reason) == "" {
		return nil, uuid.Nil, fmt.Errorf("%w: reason is required when force_read=true", apperr.ErrInvalidInput)
	}
	scope, err := s.readScope(ctx, overrideID)
	if err != nil {
		return nil, uuid.Nil, err
	}
	if len(scope) == 0 {
		return []repository.SearchResult{}, uuid.Nil, nil // non-readable filter target
	}
	embedding, err := s.embedder.Embed(ctx, query)
	if err != nil {
		return nil, uuid.Nil, fmt.Errorf("embed query: %w", err)
	}
	results, err := s.sections.HybridSearch(ctx, repository.SearchParams{
		TenantIDs:   scope,
		Embedding:   embedding,
		Query:       query,
		Category:    category,
		Subcategory: subcategory,
		Limit:       limit,
		MMRLambda:   s.mmrLambda,
	})
	if err != nil {
		return nil, uuid.Nil, err
	}
	// Label each result by its owning tenant and resolve per-tenant staleness
	// modes in one lookup, then apply staleness under each result's own mode.
	modeByTenant := s.resolveResultTenants(ctx, results)
	if s.thresholds != nil {
		results, err = applyStalenessToSearchResults(ctx, s.thresholds, results, modeByTenant, forceRead)
		if err != nil {
			return nil, uuid.Nil, err
		}
	}
	if forceRead {
		s.logOverride(ctx, repository.OverrideEvent{
			TenantID:     auth.TenantIDFromContext(ctx),
			Tool:         models.OverrideToolSearchMemory,
			OverrideType: models.OverrideTypeForceRead,
			Reason:       reason,
		})
	}
	// Post-staleness snippet step: rewrites non-withheld content in place. Runs
	// after withholding so blanked (Content=="") results are excluded by
	// construction — no withheld body can be reconstructed via ts_headline.
	if snippet {
		s.applySnippets(ctx, results, query, scope)
	}
	recallID := s.recordRecallReceipt(ctx, results, overrideID)
	return results, recallID, nil
}

// SearchResponse is the search envelope shared by the MCP and HTTP surfaces
// (design D2): recall_id is JSON null when no receipt was issued, and results
// is always a JSON array, never null.
type SearchResponse struct {
	RecallID *uuid.UUID                `json:"recall_id"`
	Results  []repository.SearchResult `json:"results"`
}

// NewSearchResponse builds the envelope from Search's return values: uuid.Nil
// becomes a nil RecallID (-> JSON null) and a nil Results slice becomes [].
func NewSearchResponse(results []repository.SearchResult, recallID uuid.UUID) SearchResponse {
	resp := SearchResponse{Results: results}
	if resp.Results == nil {
		resp.Results = []repository.SearchResult{}
	}
	if recallID != uuid.Nil {
		id := recallID
		resp.RecallID = &id
	}
	return resp
}

// recordRecallReceipt inserts a recall receipt naming the served sections,
// gated by recallReceiptsEnabled (MEMORY_RECALL_RECEIPTS) and a non-empty
// result set. The receipt's tenant is resolved via the SAME resolveTenant path
// ReportRecallOutcome uses (home tenant when overrideID is nil, the
// admin-verified override target otherwise) — NOT the read-scope aggregation
// used to FIND the results — so a later report with the same overrideID always
// resolves to this receipt (an admin searching tenant_id=X must be able to
// report against X, not their own home tenant). resolveTenant rejects a
// non-admin's overrideID outright; Search's readScope permits a narrower,
// viewer-authorized override for non-admins, so that combination simply skips
// the receipt (best-effort: recording never fails the search either way).
func (s *MemoryService) recordRecallReceipt(ctx context.Context, results []repository.SearchResult, overrideID *uuid.UUID) uuid.UUID {
	if !s.recallReceiptsEnabled || s.recallReceipts == nil || len(results) == 0 {
		return uuid.Nil
	}
	tid, err := s.resolveTenant(ctx, overrideID)
	if err != nil {
		return uuid.Nil
	}
	sectionIDs := make([]uuid.UUID, len(results))
	for i, r := range results {
		sectionIDs[i] = r.SectionID
	}
	recallID, err := s.recallReceipts.Create(ctx, tid, sectionIDs)
	if err != nil {
		slog.Default().Warn("recall receipt insert failed", "error", err)
		return uuid.Nil
	}
	return recallID
}

// ReportRecallOutcome credits hit_count (success) or miss_count (failure) on
// every section named by recallID's receipt, exactly once. The receipt must
// belong to the caller's tenant (or the admin-overridden target); a missing or
// cross-tenant id is ErrNotFound so a caller can never learn a foreign
// recall_id exists. A second report for an already-reported receipt is a no-op
// (design D4).
func (s *MemoryService) ReportRecallOutcome(ctx context.Context, recallID uuid.UUID, outcome string, overrideID *uuid.UUID) error {
	if _, ok := models.ValidRecallOutcomes[outcome]; !ok {
		return fmt.Errorf("%w: outcome must be success or failure", apperr.ErrInvalidInput)
	}
	if s.recallReceipts == nil {
		return fmt.Errorf("%w: recall %s", apperr.ErrNotFound, recallID)
	}
	tid, err := s.resolveTenant(ctx, overrideID)
	if err != nil {
		return err
	}
	return s.recallReceipts.ReportOutcome(ctx, tid, recallID, outcome)
}

// GetDocument fetches a document with all sections by path, applying the
// staleness filter. forceRead + reason override it and audit to override_log.
func (s *MemoryService) GetDocument(ctx context.Context, category string, subcategory *string, slug string, forceRead bool, reason string, overrideID *uuid.UUID) (*DocumentView, error) {
	if forceRead && strings.TrimSpace(reason) == "" {
		return nil, fmt.Errorf("%w: reason is required when force_read=true", apperr.ErrInvalidInput)
	}
	scope, err := s.readScope(ctx, overrideID)
	if err != nil {
		return nil, err
	}
	if len(scope) == 0 {
		return nil, fmt.Errorf("%w: document %s/%s", apperr.ErrNotFound, category, slug)
	}
	doc, err := s.docs.GetByPath(ctx, scope, auth.TenantIDFromContext(ctx), category, subcategory, slug)
	if err != nil {
		return nil, err
	}
	// Staleness + labeling use the doc's OWNING tenant, not the caller's home —
	// one tenant fetch drives both the mode and the display label.
	mode, name, typ := s.tenantModeAndLabel(ctx, doc.TenantID)
	view, err := buildDocumentView(ctx, s.thresholds, doc, mode, forceRead)
	if err != nil {
		return nil, err
	}
	view.TenantName, view.TenantType = name, typ
	if forceRead {
		docID := doc.ID
		s.logOverride(ctx, repository.OverrideEvent{
			TenantID:     doc.TenantID,
			Tool:         models.OverrideToolGetDocument,
			TargetID:     &docID,
			OverrideType: models.OverrideTypeForceRead,
			Reason:       reason,
		})
	}
	return &view, nil
}

// GetDocumentByID mirrors GetDocument (staleness filter + force_read audit) but
// addresses by UUID. Needed to reach a shadow doc that shares a path with another
// (e.g. cleanup_queue doc_a_id/doc_b_id), which path-keyed GetDocument can't disambiguate.
func (s *MemoryService) GetDocumentByID(ctx context.Context, id uuid.UUID, forceRead bool, reason string, overrideID *uuid.UUID) (*DocumentView, error) {
	if forceRead && strings.TrimSpace(reason) == "" {
		return nil, fmt.Errorf("%w: reason is required when force_read=true", apperr.ErrInvalidInput)
	}
	scope, err := s.readScope(ctx, overrideID)
	if err != nil {
		return nil, err
	}
	if len(scope) == 0 {
		return nil, fmt.Errorf("%w: document %s", apperr.ErrNotFound, id)
	}
	doc, err := s.docs.GetByID(ctx, scope, id)
	if err != nil {
		return nil, err
	}
	// Staleness + labeling use the doc's OWNING tenant, not the caller's home —
	// one tenant fetch drives both the mode and the display label.
	mode, name, typ := s.tenantModeAndLabel(ctx, doc.TenantID)
	view, err := buildDocumentView(ctx, s.thresholds, doc, mode, forceRead)
	if err != nil {
		return nil, err
	}
	view.TenantName, view.TenantType = name, typ
	if forceRead {
		docID := doc.ID
		s.logOverride(ctx, repository.OverrideEvent{
			TenantID:     doc.TenantID,
			Tool:         models.OverrideToolGetDocument,
			TargetID:     &docID,
			OverrideType: models.OverrideTypeForceRead,
			Reason:       reason,
		})
	}
	return &view, nil
}

// MarkVerified stamps verified_at=NOW() on a section (after an agent confirms a
// claim). Editor Check on the parent doc (finding #8): else any caller could
// verify a shared common-pool section it has no write right to.
func (s *MemoryService) MarkVerified(ctx context.Context, sectionID uuid.UUID, overrideID *uuid.UUID) error {
	tid, err := s.resolveTenant(ctx, overrideID)
	if err != nil {
		return err
	}
	section, err := s.sections.GetByID(ctx, tid, sectionID)
	if err != nil {
		return err
	}
	if !s.authorize(ctx, authz.TypeDocument, section.DocumentID.String(), authz.RelEditor) {
		return fmt.Errorf("%w: not authorized to verify section %s", apperr.ErrInvalidInput, sectionID)
	}
	return s.sections.MarkVerified(ctx, tid, sectionID)
}

// StoreResult is the outcome of StoreDocument. Status "similar_exists" means the
// save was skipped and Candidates lists the colliders (Document nil); "ok" on success.
type StoreResult struct {
	Status     string                           `json:"status"`
	Document   *models.Document                 `json:"document,omitempty"`
	Path       string                           `json:"path,omitempty"`
	Sections   int                              `json:"sections,omitempty"`
	Candidates []repository.SimilarityCandidate `json:"candidates,omitempty"`
}

// StoreDocument parses markdown into sections, embeds them (before any DB write,
// to avoid partial state), runs the duplicate guard, and stores. When the guard
// trips and force is false, no write happens and the result carries candidates.
func (s *MemoryService) StoreDocument(
	ctx context.Context,
	category string, subcategory *string, slug, content string,
	force bool, reason string,
	overrideID *uuid.UUID,
) (*StoreResult, error) {
	if force && strings.TrimSpace(reason) == "" {
		return nil, fmt.Errorf("%w: reason is required when force=true", apperr.ErrInvalidInput)
	}
	tid, err := s.resolveTenant(ctx, overrideID)
	if err != nil {
		return nil, err
	}

	title, sections := parseMarkdown(content)
	if title == "" {
		title = slug
	}

	// Embed everything before touching the DB (no partial state on failure). Use
	// the provider's native batch when available (assign back by index; length
	// must match), else fall back to a per-section Embed loop.
	sectionModels := make([]models.Section, len(sections))
	embeddings := make([]pgvector.Vector, len(sections))
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
	} else {
		for i, sec := range sections {
			embedding, err := s.embedder.Embed(ctx, sec.content)
			if err != nil {
				return nil, fmt.Errorf("embed section %d: %w", i, err)
			}
			embeddings[i] = embedding
		}
	}
	for i, sec := range sections {
		sectionModels[i] = models.Section{
			Ordinal:   i,
			Heading:   sec.heading,
			Content:   sec.content,
			Embedding: embeddings[i],
		}
	}

	// Duplicate guard: compare new embeddings against same-tenant docs, excluding
	// the target path so re-saves don't trip. Only when enabled per-tenant, not
	// forcing, and not episodic (journals are exempt from all curation).
	docType := models.InferDocType(category, subcategory, slug)
	settings := s.tenantSettings(ctx, tid)
	if settings.DuplicateGuard && !force && len(embeddings) > 0 && !models.IsEpisodic(docType) {
		candidates, err := s.sections.FindSimilarDocuments(
			ctx, tid, embeddings, models.DuplicateGuardThreshold, 5, category, subcategory, slug,
		)
		if err != nil {
			return nil, fmt.Errorf("similarity check: %w", err)
		}
		if len(candidates) > 0 {
			return &StoreResult{
				Status:     "similar_exists",
				Candidates: candidates,
			}, nil
		}
	}

	// All DB mutations in a single transaction
	doc := &models.Document{
		TenantID:    tid,
		Category:    category,
		Subcategory: subcategory,
		Slug:        slug,
		Title:       title,
		DocType:     docType,
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txDocs := repository.NewDocumentRepository(tx)
		txSections := repository.NewSectionRepository(tx)

		// Check for existing document (write path stays single-tenant + common
		// pool; the existing.TenantID == tid guard below rejects a common-pool hit).
		existing, err := txDocs.GetByPath(ctx, repository.ReadTenants(tid), tid, category, subcategory, slug)
		if err == nil && existing.TenantID == tid {
			// Update existing: delete sections first, then the doc. Preserve doc_type —
			// admin may have overridden the inferred default; don't silently revert.
			doc.ID = existing.ID
			doc.CreatedAt = existing.CreatedAt
			doc.DocType = existing.DocType
			if err := txSections.DeleteByDocumentID(ctx, existing.ID); err != nil {
				return fmt.Errorf("delete old sections: %w", err)
			}
			if err := txDocs.Save(ctx, tid, doc); err != nil {
				return fmt.Errorf("save document: %w", err)
			}
		} else {
			// Create new
			if err := txDocs.Create(ctx, doc); err != nil {
				return fmt.Errorf("create document: %w", err)
			}
		}

		// Batch insert sections under the doc ID
		for i := range sectionModels {
			sectionModels[i].DocumentID = doc.ID
		}
		if err := txSections.CreateBatch(ctx, sectionModels); err != nil {
			return fmt.Errorf("create sections: %w", err)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	doc.Sections = sectionModels

	// Lifecycle seeding: the document -> owning-tenant parent edge.
	s.seedTuple(ctx, authzseed.DocumentTenantEdge(doc.ID, tid))

	if force {
		docID := doc.ID
		s.logOverride(ctx, repository.OverrideEvent{
			TenantID:     tid,
			Tool:         models.OverrideToolStoreMemory,
			TargetID:     &docID,
			OverrideType: models.OverrideTypeForceCreate,
			Reason:       reason,
		})
	}

	return &StoreResult{
		Status:   "ok",
		Document: doc,
		Path:     doc.Path(),
		Sections: len(doc.Sections),
	}, nil
}

// UpdateSection partially updates a section: content!=nil re-embeds and sets
// content; heading!=nil sets heading (blank -> NULL). Both nil is a no-op.
func (s *MemoryService) UpdateSection(ctx context.Context, sectionID uuid.UUID, content *string, heading *string, overrideID *uuid.UUID) (*models.Section, error) {
	tid, err := s.resolveTenant(ctx, overrideID)
	if err != nil {
		return nil, err
	}

	section, err := s.sections.GetByID(ctx, tid, sectionID)
	if err != nil {
		return nil, fmt.Errorf("get section: %w", err)
	}

	if section.Document != nil && section.Document.TenantID != tid &&
		!s.authorize(ctx, authz.TypeDocument, section.DocumentID.String(), authz.RelEditor) {
		return nil, fmt.Errorf("%w: cannot update common pool section", apperr.ErrInvalidInput)
	}

	if content != nil {
		embedding, err := s.embedder.Embed(ctx, *content)
		if err != nil {
			return nil, fmt.Errorf("embed section: %w", err)
		}
		section.Content = *content
		section.Embedding = embedding
	}

	if heading != nil {
		trimmed := strings.TrimSpace(*heading)
		if trimmed == "" {
			section.Heading = nil
		} else {
			section.Heading = &trimmed
		}
	}

	if err := s.sections.Update(ctx, section); err != nil {
		return nil, fmt.Errorf("update section: %w", err)
	}

	return section, nil
}

// UpdateDocumentTitle sets a document's title. Blank titles are rejected
// (Title is NOT NULL). Refuses common-pool docs for non-admins.
func (s *MemoryService) UpdateDocumentTitle(ctx context.Context, docID uuid.UUID, title string, overrideID *uuid.UUID) (*models.Document, error) {
	tid, err := s.resolveTenant(ctx, overrideID)
	if err != nil {
		return nil, err
	}

	title = strings.TrimSpace(title)
	if title == "" {
		return nil, fmt.Errorf("%w: title is required", apperr.ErrInvalidInput)
	}

	doc, err := s.docs.GetByID(ctx, repository.ReadTenants(tid), docID)
	if err != nil {
		return nil, fmt.Errorf("get document: %w", err)
	}
	if doc.TenantID != tid && !s.authorize(ctx, authz.TypeDocument, doc.ID.String(), authz.RelEditor) {
		return nil, fmt.Errorf("%w: cannot update common pool document", apperr.ErrInvalidInput)
	}

	doc.Title = title
	if err := s.docs.Save(ctx, doc.TenantID, doc); err != nil {
		return nil, fmt.Errorf("save document: %w", err)
	}
	return doc, nil
}

// DeleteDocument removes a document and all its sections in a transaction.
// Explicitly deletes sections first (FK-safe order), does not rely on CASCADE.
func (s *MemoryService) DeleteDocument(ctx context.Context, category string, subcategory *string, slug string, overrideID *uuid.UUID) error {
	tid, err := s.resolveTenant(ctx, overrideID)
	if err != nil {
		return err
	}

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txDocs := repository.NewDocumentRepository(tx)
		txSections := repository.NewSectionRepository(tx)

		doc, err := txDocs.GetByPath(ctx, repository.ReadTenants(tid), tid, category, subcategory, slug)
		if err != nil {
			return err
		}
		if doc.TenantID != tid && !s.authorize(ctx, authz.TypeDocument, doc.ID.String(), authz.RelEditor) {
			return fmt.Errorf("%w: cannot delete common pool document", apperr.ErrInvalidInput)
		}

		if err := txSections.DeleteByDocumentID(ctx, doc.ID); err != nil {
			return fmt.Errorf("delete sections: %w", err)
		}
		if err := txDocs.Delete(ctx, doc.TenantID, doc.ID); err != nil {
			return fmt.Errorf("delete document: %w", err)
		}

		return nil
	})
}

// DeleteDocumentByID removes a document and all its sections, addressed by UUID
// and deleted from its OWNING tenant. It exists because DeleteDocument re-resolves
// a (category, subcategory, slug) path against the caller's home tenant, so an id
// that resolves to a foreign (common-pool or granted) doc would otherwise delete a
// same-path home-tenant doc instead. Here the doc is located across the caller's
// read scope, and a doc outside the caller's home tenant requires document#editor.
func (s *MemoryService) DeleteDocumentByID(ctx context.Context, id uuid.UUID, overrideID *uuid.UUID) error {
	tid, err := s.resolveTenant(ctx, overrideID)
	if err != nil {
		return err
	}
	scope, err := s.readScope(ctx, overrideID)
	if err != nil {
		return err
	}
	if len(scope) == 0 {
		return fmt.Errorf("%w: document %s", apperr.ErrNotFound, id)
	}
	doc, err := s.docs.GetByID(ctx, scope, id)
	if err != nil {
		return err
	}
	if doc.TenantID != tid && !s.authorize(ctx, authz.TypeDocument, doc.ID.String(), authz.RelEditor) {
		return fmt.Errorf("%w: not authorized to delete document %s", apperr.ErrInvalidInput, id)
	}

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txDocs := repository.NewDocumentRepository(tx)
		txSections := repository.NewSectionRepository(tx)

		if err := txSections.DeleteByDocumentID(ctx, doc.ID); err != nil {
			return fmt.Errorf("delete sections: %w", err)
		}
		if err := txDocs.Delete(ctx, doc.TenantID, doc.ID); err != nil {
			return fmt.Errorf("delete document: %w", err)
		}

		return nil
	})
}

// ListDocuments lists documents across the caller's readable tenant set,
// optionally filtered. A positive limit paginates; limit <= 0 returns the full
// list unpaginated (design D2), so the MCP list tool and CLI are unaffected.
// Each returned document already carries its owning TenantID; a nil overrideID
// aggregates, a set overrideID narrows to one readable tenant (empty result if
// not readable — never a leak).
func (s *MemoryService) ListDocuments(ctx context.Context, category, subcategory *string, overrideID *uuid.UUID, limit, offset int) ([]models.Document, error) {
	scope, err := s.readScope(ctx, overrideID)
	if err != nil {
		return nil, err
	}
	if len(scope) == 0 {
		return []models.Document{}, nil
	}
	docs, err := s.docs.List(ctx, scope, category, subcategory, limit, offset)
	if err != nil {
		return nil, err
	}
	s.labelDocumentTenants(ctx, docs)
	return docs, nil
}

// labelDocumentTenants fills the display-only TenantName/TenantType on each
// document from one batched tenant lookup, so browse/list shows the tenant name
// (not a raw UUID) — parity with search's resolveResultTenants. Best-effort:
// a nil tenant repo or lookup error leaves the labels empty.
func (s *MemoryService) labelDocumentTenants(ctx context.Context, docs []models.Document) {
	if len(docs) == 0 || s.tenants == nil {
		return
	}
	seen := make(map[uuid.UUID]struct{})
	ids := make([]uuid.UUID, 0)
	for i := range docs {
		if _, ok := seen[docs[i].TenantID]; ok {
			continue
		}
		seen[docs[i].TenantID] = struct{}{}
		ids = append(ids, docs[i].TenantID)
	}
	tenants, err := s.tenants.GetByIDs(ctx, ids)
	if err != nil {
		return
	}
	byID := make(map[uuid.UUID]models.Tenant, len(tenants))
	for _, t := range tenants {
		byID[t.ID] = t
	}
	for i := range docs {
		if t, ok := byID[docs[i].TenantID]; ok {
			docs[i].TenantName = t.Name
			docs[i].TenantType = t.Type
		}
	}
}

// --- Admin operations ---

func (s *MemoryService) requireAdmin(ctx context.Context) error {
	if !s.isAdmin(ctx) {
		return fmt.Errorf("%w: admin privileges required", apperr.ErrInvalidInput)
	}
	return nil
}

func (s *MemoryService) ListTenants(ctx context.Context) ([]models.Tenant, error) {
	if err := s.requireAdmin(ctx); err != nil {
		return nil, err
	}
	tenants, err := s.tenants.List(ctx)
	if err != nil {
		return nil, err
	}
	// Surface the resolved effective policy (override ?? global) alongside the stored value.
	for i := range tenants {
		tenants[i].EffectivePolicy = tenants[i].EffectiveSelfServicePolicy(s.SelfServicePolicyDefault)
	}
	return tenants, nil
}

// applyCreationDefaults stamps the operator-chosen toggle defaults onto a new
// tenant. GORM emits the model's struct-tag defaults ('off'/false/false) for
// zero-valued fields, silently bypassing the DB column default, so a configured
// service must write the values explicitly. A zero/invalid StalenessMode means
// the service was built without wiring config (offline CLI / tests): leave the
// fields untouched so the model/DB default (upgrade-safe 'off') applies.
func (s *MemoryService) applyCreationDefaults(t *models.Tenant) {
	if _, ok := models.ValidStalenessModes[s.TenantDefaults.StalenessMode]; !ok {
		return
	}
	t.StalenessMode = s.TenantDefaults.StalenessMode
	t.DuplicateGuard = s.TenantDefaults.DuplicateGuard
	t.CleanupScanEnabled = s.TenantDefaults.CleanupScanEnabled
}

// CreateTenant provisions a tenant. tenantType is optional (variadic so existing
// callers stay source-compatible): the first non-empty value classifies the
// tenant (models.TenantType*), defaulting to shared. The type is a DISPLAY-ONLY
// classifier — it is validated here and persisted but MUST NEVER be read by authz.
func (s *MemoryService) CreateTenant(ctx context.Context, name, email string, tenantType ...string) (*models.Tenant, error) {
	if err := s.requireAdmin(ctx); err != nil {
		return nil, err
	}
	t := models.TenantTypeShared
	if len(tenantType) > 0 && tenantType[0] != "" {
		t = tenantType[0]
	}
	if !models.IsValidTenantType(t) {
		return nil, fmt.Errorf("%w: tenant type must be personal or shared", apperr.ErrInvalidInput)
	}
	tenant := &models.Tenant{Name: name, Email: email, Type: t}
	s.applyCreationDefaults(tenant)
	if err := s.tenants.Create(ctx, tenant); err != nil {
		return nil, fmt.Errorf("create tenant: %w", err)
	}
	// Lifecycle seeding: system parent edge (enables global admins) + the
	// tenant's own service-principal membership.
	s.seedTuple(ctx, authzseed.TenantSystemEdge(tenant.ID))
	s.seedTuple(ctx, authzseed.TenantMember(tenant.ID, authz.ServicePrincipalID(tenant.ID.String())))
	return tenant, nil
}

// --- Bootstrap (token-gated, one-shot first-run provisioning) ---

// bootstrapAdvisoryLockKey is the fixed pg_advisory_xact_lock key that serializes
// concurrent Bootstrap calls so exactly one admin is provisioned (design D2). The
// value is ASCII "BOOT"; any stable constant unique among the instance's advisory
// locks works. It is currently the only advisory lock in the codebase.
const bootstrapAdvisoryLockKey int64 = 0x424F4F54 // "BOOT"

var (
	// ErrBootstrapForbidden is returned on the network path when the caller token is
	// empty, the generated BootstrapToken is empty, or the two do not match. The
	// instance fails closed and never provisions. Front-ends map this to HTTP 403.
	// (The local-admin CLI path bypasses the gate and never returns this.)
	ErrBootstrapForbidden = errors.New("bootstrap forbidden: missing or invalid token")
	// ErrAlreadyBootstrapped is returned when an admin already exists; bootstrap is
	// one-shot. Front-ends map this to HTTP 409.
	ErrAlreadyBootstrapped = errors.New("already bootstrapped: an admin already exists")
)

// BootstrapSpec describes the first tenant and admin API key to provision. Empty
// fields fall back to sensible defaults so HTTP/CLI front-ends can stay thin.
type BootstrapSpec struct {
	TenantName  string // default "admin"
	TenantEmail string // optional
	KeyLabel    string // default "admin"
	// AdminEmail, when set AND the OAuth login path is configured, is mapped to the
	// new tenant as admin (design D4) so the operator can log in via /ui without a
	// race-to-claim. Ignored when empty or when OAuth is not configured.
	AdminEmail string
}

// shouldSeedAdminEmail reports whether Bootstrap should map an operator email to
// the freshly-provisioned tenant as admin (design D4): only when an email is
// supplied AND the OAuth login path is configured — an email is useless if
// operators cannot log in via OAuth. Pure predicate so both branches are
// unit-testable without a database.
func shouldSeedAdminEmail(email string, oauthConfigured bool) bool {
	return email != "" && oauthConfigured
}

// HasAnyAdmin reports whether any subject holds system:memory#admin — the derived
// "is this instance bootstrapped?" signal (design D1: bootstrap state IS the admin
// tuple, not a separate state column). A nil authz store yields false.
func (s *MemoryService) HasAnyAdmin(ctx context.Context) (bool, error) {
	if s.authz == nil {
		return false, nil
	}
	tuples, err := s.authz.ReadByObjectRelation(ctx, authz.TypeSystem, authz.SystemObjectID, authz.RelAdmin)
	if err != nil {
		return false, err
	}
	return len(tuples) > 0, nil
}

// withTx returns a shallow copy of the service whose DB, tenant/key repositories,
// and authz store are all bound to tx, so composed lifecycle methods (CreateTenant,
// CreateAPIKey, seedTuple, HasAnyAdmin) execute inside the caller's transaction
// rather than on the pooled autocommit connection.
func (s *MemoryService) withTx(tx *gorm.DB) *MemoryService {
	clone := *s
	clone.db = tx
	clone.inTx = true
	clone.tenants = repository.NewTenantRepository(tx)
	clone.keys = repository.NewAPIKeyRepository(tx)
	txStore := authz.NewPostgresStore(tx)
	clone.authz = txStore
	clone.authzEngine = authz.NewEngine(txStore)
	return &clone
}

// Bootstrap performs token-gated, one-shot first-run provisioning. On the network
// path it verifies the caller token against the generated BootstrapToken in
// constant time (failing closed when either is empty); a local-admin context (the
// offline CLI) bypasses the token gate. It then — inside ONE transaction guarded by a Postgres
// advisory lock so concurrent callers yield exactly one admin — confirms no admin
// exists yet and provisions the first tenant + admin API key, seeding the
// system:memory#admin tuple via authzseed. The plaintext key is returned exactly
// once and is deliberately never logged.
func (s *MemoryService) Bootstrap(ctx context.Context, token string, spec BootstrapSpec) (string, *models.APIKey, error) {
	// Token gate (design D2). The offline memory-admin CLI runs under a local-admin
	// context and is inherently privileged (holding DATABASE_URL is already full
	// control), so it bypasses the token entirely. Every network caller (the HTTP
	// /bootstrap front-end) must present the generated token: fail closed if either
	// side of the compare is empty, then constant-time compare (never ==) to avoid
	// leaking the token via a timing oracle. Runs before any DB access so an
	// un-armed or un-supplied token can never provision.
	if !auth.IsLocalAdmin(ctx) {
		configured := s.BootstrapToken
		if token == "" || configured == "" {
			return "", nil, ErrBootstrapForbidden
		}
		if subtle.ConstantTimeCompare([]byte(token), []byte(configured)) != 1 {
			return "", nil, ErrBootstrapForbidden
		}
	}
	// Provisioning needs the tuple store to record admin-ness; without it we cannot
	// mint a valid admin, so fail rather than issue a key that is not an admin.
	if s.authz == nil {
		return "", nil, fmt.Errorf("bootstrap: authz store not configured")
	}

	// Founding tenant name (design D7): use the supplied name; when empty, derive
	// a sensible default from the operator email (admin email preferred, else the
	// tenant email), falling back to "admin" when no email is available.
	tenantName := spec.TenantName
	if tenantName == "" {
		switch {
		case spec.AdminEmail != "":
			tenantName = deriveTenantName(spec.AdminEmail, "admin")
		case spec.TenantEmail != "":
			tenantName = deriveTenantName(spec.TenantEmail, "admin")
		default:
			tenantName = "admin"
		}
	}
	keyLabel := spec.KeyLabel
	if keyLabel == "" {
		keyLabel = "admin"
	}

	var plaintext string
	var key *models.APIKey
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Serialize the check-and-provision (design D2). The transaction-scoped
		// advisory lock is held until this closure commits/rolls back, so a second
		// concurrent caller blocks here, then sees the committed admin below and is
		// rejected — exactly one admin results.
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", bootstrapAdvisoryLockKey).Error; err != nil {
			return fmt.Errorf("bootstrap: acquire advisory lock: %w", err)
		}
		txSvc := s.withTx(tx)

		// One-shot guard, under the lock and on the transaction's connection.
		already, err := txSvc.HasAnyAdmin(ctx)
		if err != nil {
			return fmt.Errorf("bootstrap: check existing admin: %w", err)
		}
		if already {
			return ErrAlreadyBootstrapped
		}

		// Provision with a local-admin context so the admin-gated lifecycle methods
		// pass their own requireAdmin check — the pre-auth bootstrap path has no
		// authenticated subject.
		adminCtx := auth.WithLocalAdmin(ctx)
		// The founding tenant is the admin's own personal shelf (design D7), not a
		// shared group workspace; the common `default` pool remains the shared,
		// public-read shelf and stays distinct from this tenant.
		tenant, err := txSvc.CreateTenant(adminCtx, tenantName, spec.TenantEmail, models.TenantTypePersonal)
		if err != nil {
			return fmt.Errorf("bootstrap: create tenant: %w", err)
		}
		pt, k, err := txSvc.CreateAPIKey(adminCtx, tenant.ID, keyLabel, nil, nil)
		if err != nil {
			return fmt.Errorf("bootstrap: create admin key: %w", err)
		}
		// The founding key subject is tenant#owner of the founding personal tenant
		// (design D7/personal-owner-role: the founding user is BOTH system#admin and
		// tenant#owner of it). CreateAPIKey already seeded tenant membership; upgrade
		// it to owner. Unlike the best-effort member seed, this must not silently
		// drop, so a failure rolls the whole bootstrap back.
		if err := txSvc.authz.Write(ctx, authzseed.TenantOwner(tenant.ID, authzseed.APIKeySubjectID(*k))); err != nil {
			return fmt.Errorf("bootstrap: seed founding tenant owner: %w", err)
		}
		// Seed system:memory#admin for the key's subject via authzseed. Unlike the
		// best-effort seedTuple used for tenant edges, a failure here must roll the
		// transaction back: we never commit an "admin" key that lacks the admin tuple.
		if err := txSvc.authz.Write(ctx, authzseed.SystemAdmin(authzseed.APIKeySubjectID(*k))); err != nil {
			return fmt.Errorf("bootstrap: seed system admin: %w", err)
		}
		// Admin-email seeding (design D4): when an operator email is supplied and the
		// OAuth login path is configured, map it to the new (personal) tenant as its
		// owner via the same lifecycle method the admin API uses — creating the
		// tenant_users row the authlet resolver needs plus the owner tuple. Inside the
		// transaction so the mapping is atomic with the tenant+key; a failure rolls the
		// whole bootstrap back. The admin API key is still minted and returned for the
		// API/MCP path. The founding operator's system-wide reach comes from the
		// system#admin tuple seeded just below, not from a tenant#admin tuple.
		if shouldSeedAdminEmail(spec.AdminEmail, s.OAuthConfigured) {
			tu, err := txSvc.GrantTenantUser(adminCtx, spec.AdminEmail, tenant.ID, models.TenantUserRoleOwner)
			if err != nil {
				return fmt.Errorf("bootstrap: grant admin email: %w", err)
			}
			// The founding email must hold system:memory#admin exactly like the founding
			// key, or the admin console (adminOnly/IsAdmin gate on system:memory#admin)
			// rejects it. The OAuth login path resolves the email to the tenant_users.id
			// subject (UserContextBridge), the same id GrantTenantUser used for its
			// member/admin tuples, so seed system admin on that subject. Like the key
			// seed above, a failure here rolls the whole bootstrap back.
			if err := txSvc.authz.Write(ctx, authzseed.SystemAdmin(tu.ID.String())); err != nil {
				return fmt.Errorf("bootstrap: seed admin email system admin: %w", err)
			}
		}
		plaintext, key = pt, k
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	// plaintext is intentionally never logged; it is returned to the caller once.
	return plaintext, key, nil
}

// ResetBootstrap is the operator-only break-glass reset (design D5, spec:
// *Break-glass reset*). In ONE transaction it deletes every system:memory#admin
// tuple and the API key(s) behind each one's subject — nothing else. Tenants,
// documents, sections, and any non-admin API key are left untouched. After it
// commits, HasAnyAdmin is false again and the instance re-arms for Bootstrap
// (the next server boot generates and logs a fresh bootstrap token).
//
// This mirrors Bootstrap's seeding in reverse: Bootstrap mints an admin key and
// writes authzseed.SystemAdmin(authzseed.APIKeySubjectID(key)); this reads those
// tuples back, uses APIKeySubjectID's resolution (via the repo's FindBySubjectID)
// to find the key(s) that produced each subject, deletes the key(s), then deletes
// the tuple.
//
// Security-critical: this function has no HTTP route and must never gain one
// (spec: *Reset cannot be triggered over the network*) — the only caller is the
// boot-time MEMORY_RESET check in cmd/server/main.go (task 6.1).
func (s *MemoryService) ResetBootstrap(ctx context.Context) error {
	if s.authz == nil {
		return fmt.Errorf("reset: authz store not configured")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txSvc := s.withTx(tx)

		adminTuples, err := txSvc.authz.ReadByObjectRelation(ctx, authz.TypeSystem, authz.SystemObjectID, authz.RelAdmin)
		if err != nil {
			return fmt.Errorf("reset: read admin tuples: %w", err)
		}

		for _, t := range adminTuples {
			keys, err := txSvc.keys.FindBySubjectID(ctx, t.SubjectID)
			if err != nil {
				return fmt.Errorf("reset: find keys for admin subject %s: %w", t.SubjectID, err)
			}
			for _, k := range keys {
				if err := txSvc.keys.Delete(ctx, k.ID); err != nil {
					return fmt.Errorf("reset: delete admin key %s: %w", k.ID, err)
				}
			}
			if err := txSvc.authz.Delete(ctx, t); err != nil {
				return fmt.Errorf("reset: delete admin tuple for subject %s: %w", t.SubjectID, err)
			}
		}
		return nil
	})
}

// roleElevatedTuple returns the elevated (admin/owner) access tuple a role
// implies, or ok=false for a plain member (which carries no elevated tuple —
// the member tuple is always seeded separately). Single source of truth for the
// role→relation mapping shared by GrantTenantUser, UpdateTenantUserRole, and
// RevokeTenantUser.
func roleElevatedTuple(tenantID uuid.UUID, role, subjectID string) (authz.Tuple, bool) {
	switch role {
	case models.TenantUserRoleAdmin:
		return authzseed.TenantAdmin(tenantID, subjectID), true
	case models.TenantUserRoleOwner:
		return authzseed.TenantOwner(tenantID, subjectID), true
	default:
		return authz.Tuple{}, false
	}
}

// GrantTenantUser maps a verified email to a tenant+role, creating the
// tenant_users row and seeding membership tuples (+ admin when role==admin,
// + owner when role==owner). Admin-gated; the lifecycle seam for user grants
// (no in-band tool writes tuples).
func (s *MemoryService) GrantTenantUser(ctx context.Context, email string, tenantID uuid.UUID, role string) (*models.TenantUser, error) {
	if err := s.requireAdmin(ctx); err != nil {
		return nil, err
	}
	if role == "" {
		role = models.TenantUserRoleMember
	}
	if _, ok := models.ValidTenantUserRoles[role]; !ok {
		return nil, fmt.Errorf("%w: role must be member, admin, or owner", apperr.ErrInvalidInput)
	}
	tenant, err := s.tenants.GetByID(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	// Operation-validity: a personal tenant holds a single owner. Allow the
	// creation-time first user (bootstrap / self-serve auto-provision), reject a
	// second. Shared tenants are unrestricted.
	if tenant.Type == models.TenantTypePersonal {
		var existing int64
		if err = s.db.WithContext(ctx).Model(&models.TenantUser{}).
			Where("tenant_id = ?", tenantID).Count(&existing).Error; err != nil {
			return nil, fmt.Errorf("count tenant_users: %w", err)
		}
		if existing >= 1 {
			return nil, fmt.Errorf("%w: a personal tenant may have only one user", apperr.ErrInvalidInput)
		}
	}
	tu := &models.TenantUser{Email: email, TenantID: tenantID, Role: role}

	// In-transaction path (Bootstrap / self-serve provisioning via withTx) or no
	// authz store: keep the historical best-effort seed. The caller's own transaction
	// plus its hard-failing owner/admin/system writes govern atomicity there, and a
	// nil store disables seeding entirely — so don't double-wrap or change that flow.
	if s.inTx || s.authz == nil {
		if err := s.db.WithContext(ctx).Create(tu).Error; err != nil {
			return nil, fmt.Errorf("create tenant_user: %w", err)
		}
		s.seedTuple(ctx, authzseed.TenantMember(tenantID, tu.ID.String()))
		if tuple, ok := roleElevatedTuple(tenantID, role, tu.ID.String()); ok {
			s.seedTuple(ctx, tuple)
		}
		return tu, nil
	}

	// Direct autocommit path: the tenant_users row and its access tuples must be
	// all-or-nothing. Previously the row was committed via s.db.Create and the
	// tuple seed went through best-effort seedTuple, which LOGS-and-SWALLOWS authz
	// write errors — leaving a committed user with no membership tuple (no access at
	// all) while returning success (B11). Create the row inside a transaction and
	// write the member (+admin/owner) tuple with hard-failing semantics; a tuple
	// failure rolls the row back so no access-less user is ever committed. The tuple
	// store is a separate abstraction (authz.Store), so the DB transaction guards the
	// observable invariant — no committed tenant_users row without its tuples — while
	// a rolled-back attempt may leave a harmless orphan tuple (a grant to a subject id
	// that never came to exist).
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if tenant.Type == models.TenantTypePersonal {
			// Serialize concurrent grants to the same personal tenant: lock the tenant
			// row so a second grant blocks until we commit, then re-count under the lock.
			// (No unique constraint on tenant_users.tenant_id, so the pre-tx count alone
			// is a TOCTOU.)
			if err := tx.Exec("SELECT 1 FROM tenants WHERE id = ? FOR UPDATE", tenantID).Error; err != nil {
				return fmt.Errorf("lock tenant: %w", err)
			}
			var existing int64
			if err := tx.Model(&models.TenantUser{}).Where("tenant_id = ?", tenantID).Count(&existing).Error; err != nil {
				return fmt.Errorf("count tenant_users: %w", err)
			}
			if existing >= 1 {
				return fmt.Errorf("%w: a personal tenant may have only one user", apperr.ErrInvalidInput)
			}
		}
		if err := tx.Create(tu).Error; err != nil {
			return fmt.Errorf("create tenant_user: %w", err)
		}
		if err := s.authz.Write(ctx, authzseed.TenantMember(tenantID, tu.ID.String())); err != nil {
			return fmt.Errorf("seed tenant member tuple: %w", err)
		}
		if tuple, ok := roleElevatedTuple(tenantID, role, tu.ID.String()); ok {
			if err := s.authz.Write(ctx, tuple); err != nil {
				return fmt.Errorf("seed tenant %s tuple: %w", role, err)
			}
		}
		// A runtime admin grant must seed the svc-admin tuple now, not wait for boot.
		return s.syncServicePrincipalAdmin(ctx, tx, tenantID)
	})
	if err != nil {
		return nil, err
	}
	return tu, nil
}

// ListTenantUsers returns the email->tenant mappings for a tenant. Admin-gated.
func (s *MemoryService) ListTenantUsers(ctx context.Context, tenantID uuid.UUID) ([]models.TenantUser, error) {
	if err := s.requireAdmin(ctx); err != nil {
		return nil, err
	}
	var users []models.TenantUser
	if err := s.db.WithContext(ctx).
		Where("tenant_id = ?", tenantID).
		Order("email ASC").
		Find(&users).Error; err != nil {
		return nil, fmt.Errorf("list tenant_users: %w", err)
	}
	return users, nil
}

// UpdateTenantUserRole changes a role and syncs the role tuple: the target role
// gets its tuple written (admin -> tenant#admin, owner -> tenant#owner) and the
// other role tuple removed, so the stored tuples always match exactly one role
// (member tuple untouched — every role is a member). Admin-gated; email is the
// unique key.
func (s *MemoryService) UpdateTenantUserRole(ctx context.Context, email, role string) (*models.TenantUser, error) {
	if err := s.requireAdmin(ctx); err != nil {
		return nil, err
	}
	if _, ok := models.ValidTenantUserRoles[role]; !ok {
		return nil, fmt.Errorf("%w: role must be member, admin, or owner", apperr.ErrInvalidInput)
	}
	var tu models.TenantUser
	if err := s.db.WithContext(ctx).Where("email = ?", email).First(&tu).Error; err != nil {
		return nil, fmt.Errorf("%w: no user mapping for %s", apperr.ErrNotFound, email)
	}
	if tu.Role == role {
		return &tu, nil // idempotent
	}

	// No authz store: keep the historical DB-only path (no tuples to reconcile).
	if s.authz == nil {
		if err := s.db.WithContext(ctx).Model(&tu).Update("role", role).Error; err != nil {
			return nil, fmt.Errorf("update tenant_user role: %w", err)
		}
		tu.Role = role
		return &tu, nil
	}

	// The role UPDATE and all tuple writes/deletes must be all-or-nothing.
	// Previously the row was committed via s.db.Update and the tuple sync went
	// through best-effort seed/unseed, which LOGS-and-SWALLOWS authz errors —
	// leaving a committed role that disagrees with its tuples in the fail-OPEN
	// direction (row says member, admin tuple retained). The tuple store is a
	// separate abstraction (authz.Store) from the gorm tx, so the DB transaction
	// guards the observable invariant: a committed role never disagrees with its
	// tuples in the fail-open direction. Any authz failure rolls the row back —
	// worst case is fail-CLOSED (row says admin, admin tuple already gone), which
	// is safe and strictly better than the old best-effort path that retained
	// privilege on a swallowed error.
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.TenantUser{}).Where("id = ?", tu.ID).Update("role", role).Error; err != nil {
			return fmt.Errorf("update tenant_user role: %w", err)
		}
		if tuple, ok := roleElevatedTuple(tu.TenantID, role, tu.ID.String()); ok {
			if err := s.authz.Write(ctx, tuple); err != nil {
				return fmt.Errorf("seed role tuple: %w", err)
			}
		}
		if role != models.TenantUserRoleAdmin {
			if err := s.authz.Delete(ctx, authzseed.TenantAdmin(tu.TenantID, tu.ID.String())); err != nil {
				return fmt.Errorf("remove admin tuple: %w", err)
			}
		}
		if role != models.TenantUserRoleOwner {
			if err := s.authz.Delete(ctx, authzseed.TenantOwner(tu.TenantID, tu.ID.String())); err != nil {
				return fmt.Errorf("remove owner tuple: %w", err)
			}
		}
		return s.syncServicePrincipalAdmin(ctx, tx, tu.TenantID)
	})
	if err != nil {
		return nil, err
	}
	tu.Role = role
	return &tu, nil
}

// RevokeTenantUser removes a user's email->tenant mapping and its membership
// tuples (member, plus admin or owner when applicable). Admin-gated. Email is the key.
func (s *MemoryService) RevokeTenantUser(ctx context.Context, email string) error {
	if err := s.requireAdmin(ctx); err != nil {
		return err
	}
	var tu models.TenantUser
	if err := s.db.WithContext(ctx).Where("email = ?", email).First(&tu).Error; err != nil {
		return fmt.Errorf("%w: no user mapping for %s", apperr.ErrNotFound, email)
	}

	// No authz store: keep the historical DB-only delete (no tuples to reconcile).
	if s.authz == nil {
		if err := s.db.WithContext(ctx).Delete(&tu).Error; err != nil {
			return fmt.Errorf("delete tenant_user: %w", err)
		}
		return nil
	}

	// The row delete and its tuple removals must be all-or-nothing (mirrors
	// UpdateTenantUserRole): a swallowed unseed used to leave live membership/admin
	// tuples for a revoked user. The gorm tx guards the DB row; any authz Delete
	// failure rolls the row back — worst case is fail-CLOSED (tuple gone, row still
	// present), which is safe. syncServicePrincipalAdmin reconciles the svc-admin
	// grant against the tx (revoking the last admin drops it).
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("id = ?", tu.ID).Delete(&models.TenantUser{}).Error; err != nil {
			return fmt.Errorf("delete tenant_user: %w", err)
		}
		if err := s.authz.Delete(ctx, authzseed.TenantMember(tu.TenantID, tu.ID.String())); err != nil {
			return fmt.Errorf("remove member tuple: %w", err)
		}
		if tuple, ok := roleElevatedTuple(tu.TenantID, tu.Role, tu.ID.String()); ok {
			if err := s.authz.Delete(ctx, tuple); err != nil {
				return fmt.Errorf("remove role tuple: %w", err)
			}
		}
		return s.syncServicePrincipalAdmin(ctx, tx, tu.TenantID)
	})
	if err != nil {
		return err
	}
	return nil
}

// --- ACL: delegated tenant/document access management ---
//
// These methods let a tenant#manager (not just a system admin) grant/revoke
// membership and per-document guest access, subject to the grant-ceiling
// matrix (design.md §6): a manager may grant/revoke viewer/member on tenants
// they manage and viewer/editor on documents owned by tenants they manage,
// but may NOT appoint or remove tenant#manager/tenant#admin — that requires
// tenant#admin or system admin.

// CanManageTenant reports whether the caller may administer tenantID's
// membership: system admin OR tenant#manager (which itself includes
// tenant#admin, since the manager relation's rewrite is this ∪ computed(admin)).
func (s *MemoryService) CanManageTenant(ctx context.Context, tenantID uuid.UUID) bool {
	return s.isAdmin(ctx) || s.authorize(ctx, authz.TypeTenant, tenantID.String(), authz.RelManager)
}

// TenantAccess pairs a tenant with the caller's effective relation on it, so
// the UI can label the caller's role (design.md §5).
type TenantAccess struct {
	Tenant   models.Tenant `json:"tenant"`
	Relation string        `json:"relation"`
}

// WritableTenants lists the tenants the caller may administer: every tenant
// for a system admin (labeled RelAdmin), else the tenants where a direct
// tuple on the caller's subject resolves to tenant#manager once confirmed by
// Check. ReadBySubject returns only DIRECT tuples, so a member/viewer tuple on
// a tenant the caller does not otherwise manage is a candidate that Check
// then correctly excludes, while a direct tenant#admin or tenant#owner tuple is
// correctly included via the manager<-admin / manager<-owner rewrites. A
// directly-owned personal tenant is labeled RelOwner; other managed tenants
// RelManager (design.md §4).
func (s *MemoryService) WritableTenants(ctx context.Context) ([]TenantAccess, error) {
	if s.isAdmin(ctx) {
		tenants, err := s.tenants.List(ctx)
		if err != nil {
			return nil, err
		}
		result := make([]TenantAccess, len(tenants))
		for i, t := range tenants {
			result[i] = TenantAccess{Tenant: t, Relation: authz.RelAdmin}
		}
		return result, nil
	}
	if s.authz == nil {
		return nil, nil
	}
	subj, ok := auth.SubjectFromContext(ctx)
	if !ok || subj.ID == "" {
		return nil, nil
	}
	tuples, err := s.authz.ReadBySubject(ctx, subj.Type, subj.ID)
	if err != nil {
		return nil, err
	}
	// Pre-scan direct owner tuples so the label is owner-accurate regardless of the
	// order ReadBySubject returns a tenant's (member/owner/...) direct tuples in.
	ownerTenants := make(map[uuid.UUID]struct{})
	for _, t := range tuples {
		if t.ObjectType == authz.TypeTenant && t.Relation == authz.RelOwner {
			if tid, perr := uuid.Parse(t.ObjectID); perr == nil {
				ownerTenants[tid] = struct{}{}
			}
		}
	}
	// First pass: collect the distinct tenant ids that pass the manager Check,
	// remembering each one's owner-vs-manager label and a stable order (the order
	// ReadBySubject returned the tuples in). Then resolve them all in ONE GetByIDs
	// instead of a per-candidate GetByID (N+1).
	seen := make(map[uuid.UUID]struct{}, len(tuples))
	orderedIDs := make([]uuid.UUID, 0, len(tuples))
	labelByID := make(map[uuid.UUID]string, len(tuples))
	for _, t := range tuples {
		if t.ObjectType != authz.TypeTenant {
			continue
		}
		tid, err := uuid.Parse(t.ObjectID)
		if err != nil {
			continue
		}
		if _, dup := seen[tid]; dup {
			continue
		}
		seen[tid] = struct{}{}
		if !s.authorize(ctx, authz.TypeTenant, tid.String(), authz.RelManager) {
			continue
		}
		label := authz.RelManager
		if _, owned := ownerTenants[tid]; owned {
			label = authz.RelOwner
		}
		orderedIDs = append(orderedIDs, tid)
		labelByID[tid] = label
	}
	if len(orderedIDs) == 0 {
		return nil, nil
	}
	tenants, err := s.tenants.GetByIDs(ctx, orderedIDs)
	if err != nil {
		return nil, err
	}
	byID := make(map[uuid.UUID]models.Tenant, len(tenants))
	for _, t := range tenants {
		byID[t.ID] = t
	}
	// Build in the collected order (GetByIDs may return rows unordered). A tuple
	// pointing at a deleted tenant is simply absent from byID; skip it rather
	// than fail the whole list.
	var result []TenantAccess
	for _, tid := range orderedIDs {
		tenant, ok := byID[tid]
		if !ok {
			continue
		}
		result = append(result, TenantAccess{Tenant: tenant, Relation: labelByID[tid]})
	}
	return result, nil
}

// resolveSubjectByEmail looks up the tenant_users row for email — the ACL
// subject-id convention is tenant_user.ID.String() (design.md §3). Email is
// globally unique. Not found is an error; the ACL surface never auto-creates
// tenant_users rows (that's GrantTenantUser's job).
func (s *MemoryService) resolveSubjectByEmail(ctx context.Context, email string) (*models.TenantUser, error) {
	var tu models.TenantUser
	if err := s.db.WithContext(ctx).Where("email = ?", email).First(&tu).Error; err != nil {
		return nil, fmt.Errorf("%w: no user mapping for %s", apperr.ErrNotFound, email)
	}
	return &tu, nil
}

// subjectEmails resolves many tenant_users subject ids to emails in one query.
// Ids with no row are simply absent from the map (same skip semantics as the
// per-id lookup, whose error → the caller skips the grant). Non-UUID
// subjects (e.g. "svc:<tenant>" service principals) never parse and are absent
// too. The map is keyed by the original input id string so callers can look up
// by the tuple's SubjectID directly.
func (s *MemoryService) subjectEmails(ctx context.Context, ids []string) (map[string]string, error) {
	out := make(map[string]string, len(ids))
	byUUID := make(map[uuid.UUID][]string, len(ids))
	uuids := make([]uuid.UUID, 0, len(ids))
	for _, raw := range ids {
		id, err := uuid.Parse(raw)
		if err != nil {
			continue
		}
		if _, ok := byUUID[id]; !ok {
			uuids = append(uuids, id)
		}
		byUUID[id] = append(byUUID[id], raw)
	}
	if len(uuids) == 0 {
		return out, nil
	}
	var users []models.TenantUser
	if err := s.db.WithContext(ctx).Where("id IN ?", uuids).Find(&users).Error; err != nil {
		return nil, err
	}
	for _, u := range users {
		for _, raw := range byUUID[u.ID] {
			out[raw] = u.Email
		}
	}
	return out, nil
}

// documentTenantID resolves a document's owning tenant by ID alone (no tenant
// scoping), so ACL grant checks can authorize CanManageTenant(doc.TenantID)
// without first knowing the caller's own tenant context — a manager may
// administer a tenant that isn't the tenant their own request happens to be
// scoped to. Deliberately archived-INCLUSIVE (unlike the read-path repo
// methods): GrantDocumentAccess/RevokeDocumentAccess/ListDocumentGrants must
// still reach an archived document's guest tuples, or a manager loses the
// ability to list/revoke lingering document#viewer/#editor grants once a doc
// is archived — those tuples would then silently reactivate if the doc is
// later un-archived. Granting on an archived doc is harmless (the tuple just
// sits dormant), so one archived-inclusive lookup suits all three callers.
func (s *MemoryService) documentTenantID(ctx context.Context, docID uuid.UUID) (uuid.UUID, error) {
	var doc models.Document
	if err := s.db.WithContext(ctx).
		Select("tenant_id").
		Where("id = ?", docID).
		First(&doc).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return uuid.Nil, fmt.Errorf("%w: document %s", apperr.ErrNotFound, docID)
		}
		return uuid.Nil, err
	}
	return doc.TenantID, nil
}

// validTenantGrantRelations is the accepted relation set for
// GrantTenantAccess/RevokeTenantAccess (design.md §5).
var validTenantGrantRelations = map[string]struct{}{
	authz.RelViewer:  {},
	authz.RelMember:  {},
	authz.RelManager: {},
}

// canGrantTenantRelation enforces the grant-ceiling matrix (design.md §6):
// viewer/member require the caller already manage the tenant (manager+);
// manager requires tenant#admin or system admin — a manager may never appoint
// another manager or admin. relation is expected to already be validated
// against validTenantGrantRelations; an unrecognized value denies.
func (s *MemoryService) canGrantTenantRelation(ctx context.Context, tenantID uuid.UUID, relation string) bool {
	switch relation {
	case authz.RelManager:
		return s.isAdmin(ctx) || s.authorize(ctx, authz.TypeTenant, tenantID.String(), authz.RelAdmin)
	case authz.RelViewer, authz.RelMember:
		return s.CanManageTenant(ctx, tenantID)
	default:
		return false
	}
}

// tenantGrantTuple builds the authz tuple for relation on tenantID/subjectID
// via the authzseed constructors. relation must already be validated.
func tenantGrantTuple(tenantID uuid.UUID, subjectID, relation string) authz.Tuple {
	switch relation {
	case authz.RelViewer:
		return authzseed.TenantViewer(tenantID, subjectID)
	case authz.RelManager:
		return authzseed.TenantManager(tenantID, subjectID)
	default: // authz.RelMember
		return authzseed.TenantMember(tenantID, subjectID)
	}
}

// GrantTenantAccess grants email the given relation (viewer, member, or
// manager) on tenantID, enforcing the grant-ceiling matrix (design.md §6).
// email must already have a tenant_users row; the ACL surface does not
// auto-create one.
func (s *MemoryService) GrantTenantAccess(ctx context.Context, tenantID uuid.UUID, email, relation string) error {
	if _, ok := validTenantGrantRelations[relation]; !ok {
		return fmt.Errorf("%w: relation must be viewer, member, or manager", apperr.ErrInvalidRelation)
	}
	if !s.canGrantTenantRelation(ctx, tenantID, relation) {
		return fmt.Errorf("%w: not authorized to grant %s on tenant %s", apperr.ErrInvalidInput, relation, tenantID)
	}
	// Operation-validity: a personal tenant takes no tenant-level grants (single
	// owner). Document-level guest grants stay allowed (see GrantDocumentAccess).
	// Checked after the ceiling so an unauthorized caller still short-circuits
	// before any tenant read.
	tenant, err := s.tenants.GetByID(ctx, tenantID)
	if err != nil {
		return err
	}
	if tenant.Type == models.TenantTypePersonal {
		return fmt.Errorf("%w: cannot grant tenant-level access on a personal tenant", apperr.ErrInvalidInput)
	}
	tu, err := s.resolveSubjectByEmail(ctx, email)
	if err != nil {
		return err
	}
	if s.authz == nil {
		return fmt.Errorf("grant tenant access: authz store not configured")
	}
	if err := s.authz.Write(ctx, tenantGrantTuple(tenantID, tu.ID.String(), relation)); err != nil {
		return fmt.Errorf("grant tenant access: %w", err)
	}
	return nil
}

// RevokeTenantAccess removes email's relation grant on tenantID, subject to
// the same grant-ceiling matrix as GrantTenantAccess.
func (s *MemoryService) RevokeTenantAccess(ctx context.Context, tenantID uuid.UUID, email, relation string) error {
	if _, ok := validTenantGrantRelations[relation]; !ok {
		return fmt.Errorf("%w: relation must be viewer, member, or manager", apperr.ErrInvalidRelation)
	}
	if !s.canGrantTenantRelation(ctx, tenantID, relation) {
		return fmt.Errorf("%w: not authorized to revoke %s on tenant %s", apperr.ErrInvalidInput, relation, tenantID)
	}
	tu, err := s.resolveSubjectByEmail(ctx, email)
	if err != nil {
		return err
	}
	if s.authz == nil {
		return fmt.Errorf("revoke tenant access: authz store not configured")
	}
	if err := s.authz.Delete(ctx, tenantGrantTuple(tenantID, tu.ID.String(), relation)); err != nil {
		return fmt.Errorf("revoke tenant access: %w", err)
	}
	return nil
}

// validDocumentGrantRelations is the accepted relation set for
// GrantDocumentAccess/RevokeDocumentAccess (design.md §5).
var validDocumentGrantRelations = map[string]struct{}{
	authz.RelViewer: {},
	authz.RelEditor: {},
}

// documentGrantTuple builds the authz tuple for relation on docID/subjectID
// via the authzseed constructors. relation must already be validated.
func documentGrantTuple(docID uuid.UUID, subjectID, relation string) authz.Tuple {
	if relation == authz.RelEditor {
		return authzseed.DocumentEditor(docID, subjectID)
	}
	return authzseed.DocumentViewer(docID, subjectID)
}

// GrantDocumentAccess grants email per-document guest access (viewer or
// editor) to docID. The caller must manage the document's owning tenant
// (CanManageTenant(doc.TenantID)) — document guest sharing is bounded by
// tenant management, not by holding a grant on the document itself.
func (s *MemoryService) GrantDocumentAccess(ctx context.Context, docID uuid.UUID, email, relation string) error {
	if _, ok := validDocumentGrantRelations[relation]; !ok {
		return fmt.Errorf("%w: relation must be viewer or editor", apperr.ErrInvalidRelation)
	}
	tenantID, err := s.documentTenantID(ctx, docID)
	if err != nil {
		return err
	}
	if !s.CanManageTenant(ctx, tenantID) {
		return fmt.Errorf("%w: not authorized to manage document %s", apperr.ErrInvalidInput, docID)
	}
	tu, err := s.resolveSubjectByEmail(ctx, email)
	if err != nil {
		return err
	}
	if s.authz == nil {
		return fmt.Errorf("grant document access: authz store not configured")
	}
	if err := s.authz.Write(ctx, documentGrantTuple(docID, tu.ID.String(), relation)); err != nil {
		return fmt.Errorf("grant document access: %w", err)
	}
	return nil
}

// RevokeDocumentAccess removes email's per-document guest grant on docID,
// subject to the same tenant-management requirement as GrantDocumentAccess.
func (s *MemoryService) RevokeDocumentAccess(ctx context.Context, docID uuid.UUID, email, relation string) error {
	if _, ok := validDocumentGrantRelations[relation]; !ok {
		return fmt.Errorf("%w: relation must be viewer or editor", apperr.ErrInvalidRelation)
	}
	tenantID, err := s.documentTenantID(ctx, docID)
	if err != nil {
		return err
	}
	if !s.CanManageTenant(ctx, tenantID) {
		return fmt.Errorf("%w: not authorized to manage document %s", apperr.ErrInvalidInput, docID)
	}
	tu, err := s.resolveSubjectByEmail(ctx, email)
	if err != nil {
		return err
	}
	if s.authz == nil {
		return fmt.Errorf("revoke document access: authz store not configured")
	}
	if err := s.authz.Delete(ctx, documentGrantTuple(docID, tu.ID.String(), relation)); err != nil {
		return fmt.Errorf("revoke document access: %w", err)
	}
	return nil
}

// Grant is one relation-tuple rendered for display: SubjectID resolved to
// Email via tenant_users (design.md §5).
type Grant struct {
	Email     string `json:"email"`
	SubjectID string `json:"subject_id"`
	Relation  string `json:"relation"`
}

// listGrants is the shared body of ListTenantGrants/ListDocumentGrants: for each
// relation in order, read the direct tuples on objectType:objectID, keep the
// concrete-user subjects, resolve their emails in ONE batch query, and render one
// Grant per resolvable subject in tuple order. A public wildcard USER subject
// (user:*) is INCLUDED as an auditable grant (SubjectID "*", a recognizable
// label, no email lookup) so an ACL listing/UI can surface public access rather
// than silently dropping it — display/audit only, authz behavior is unchanged.
// Usersets and non-user subjects are still skipped. Subjects with no resolvable
// email (stale tenant_users, service principals) are skipped rather than failing
// the list — matching the per-id skip-on-error behavior. Callers own
// the authz==nil guard and the CanManageTenant gate before delegating here.
func (s *MemoryService) listGrants(ctx context.Context, objectType, objectID string, relations []string) ([]Grant, error) {
	type pending struct {
		subjectID string
		relation  string
	}
	var items []pending
	var ids []string
	var wildcards []Grant
	for _, rel := range relations {
		tuples, err := s.authz.ReadByObjectRelation(ctx, objectType, objectID, rel)
		if err != nil {
			return nil, err
		}
		for _, t := range tuples {
			if t.SubjectType != authz.TypeUser || t.IsUserset() {
				continue
			}
			if t.IsWildcard() {
				// Public wildcard grant (user:*): surface it as auditable, bypassing
				// the email lookup (there is no user to resolve).
				wildcards = append(wildcards, Grant{
					Email:     "(public wildcard)",
					SubjectID: authz.Wildcard,
					Relation:  rel,
				})
				continue
			}
			items = append(items, pending{subjectID: t.SubjectID, relation: rel})
			ids = append(ids, t.SubjectID)
		}
	}
	emails, err := s.subjectEmails(ctx, ids)
	if err != nil {
		return nil, err
	}
	var grants []Grant
	for _, it := range items {
		email, ok := emails[it.subjectID]
		if !ok {
			continue
		}
		grants = append(grants, Grant{Email: email, SubjectID: it.subjectID, Relation: it.relation})
	}
	grants = append(grants, wildcards...)
	return grants, nil
}

// ListTenantGrants lists every direct viewer/member/manager grant on tenantID
// (tenant#admin is out of scope here — that's the GrantTenantUser admin
// flow). Caller must CanManageTenant(tenantID). A public wildcard (user:*) grant
// is included as an auditable entry; usersets and subjects with no resolvable
// email (stale tenant_users, service principals) are skipped rather than failing
// the whole list.
func (s *MemoryService) ListTenantGrants(ctx context.Context, tenantID uuid.UUID) ([]Grant, error) {
	if !s.CanManageTenant(ctx, tenantID) {
		return nil, fmt.Errorf("%w: not authorized to list grants for tenant %s", apperr.ErrInvalidInput, tenantID)
	}
	if s.authz == nil {
		return nil, nil
	}
	return s.listGrants(ctx, authz.TypeTenant, tenantID.String(), []string{authz.RelViewer, authz.RelMember, authz.RelManager})
}

// ListDocumentGrants lists every direct per-document guest viewer/editor grant
// on docID (tenant-inherited access is out of scope — only explicit guest
// shares). Caller must CanManageTenant(doc.TenantID).
func (s *MemoryService) ListDocumentGrants(ctx context.Context, docID uuid.UUID) ([]Grant, error) {
	tenantID, err := s.documentTenantID(ctx, docID)
	if err != nil {
		return nil, err
	}
	if !s.CanManageTenant(ctx, tenantID) {
		return nil, fmt.Errorf("%w: not authorized to list grants for document %s", apperr.ErrInvalidInput, docID)
	}
	if s.authz == nil {
		return nil, nil
	}
	return s.listGrants(ctx, authz.TypeDocument, docID.String(), []string{authz.RelViewer, authz.RelEditor})
}

// UpdateTenantFields bundles the optional patches admin/self tools may apply.
// Any nil pointer leaves the corresponding column unchanged.
type UpdateTenantFields struct {
	Name               *string
	Email              *string
	Type               *string
	StalenessMode      *string
	DuplicateGuard     *bool
	CleanupScanEnabled *bool
	// SelfServicePolicy accepts "open" | "admin_only" | "inherit" (the last clears
	// the per-tenant override to NULL). Admin-only — never wired to self-service.
	SelfServicePolicy *string
}

// UpdateTenant is the admin-only patcher. It can touch any field.
func (s *MemoryService) UpdateTenant(ctx context.Context, id uuid.UUID, fields UpdateTenantFields) (*models.Tenant, error) {
	if err := s.requireAdmin(ctx); err != nil {
		return nil, err
	}
	tenant, err := s.applyTenantPatch(ctx, id, fields)
	if err != nil {
		return nil, err
	}
	tenant.EffectivePolicy = tenant.EffectiveSelfServicePolicy(s.SelfServicePolicyDefault)
	return tenant, nil
}

// UpdateMyTenantSettings edits the caller's OWN tenant's toggles (staleness,
// duplicate guard, cleanup scan); name/email stay admin-only. A field-less call
// is a status read and is always allowed. Writes require MANAGE rights (manager)
// via requireSelfService, NOT bare membership: these toggles arm destructive
// behavior — staleness_mode="hard" arms the retention sweep that archives then
// hard-deletes documents — so they are not member-level self-service. A personal
// tenant's owner still passes (owner ⇒ manager), keeping personal self-service
// intact; a shared tenant's plain member is refused. This matches the by-id
// sibling UpdateTenantSettings (also manager). Every call is audited to
// override_log (compromised-key trail).
func (s *MemoryService) UpdateMyTenantSettings(ctx context.Context, stalenessMode *string, duplicateGuard *bool, cleanupScanEnabled *bool) (*models.Tenant, error) {
	tid := auth.TenantIDFromContext(ctx)
	if tid == uuid.Nil {
		return nil, fmt.Errorf("%w: missing tenant ID in context", apperr.ErrInvalidInput)
	}
	// No fields = status read: return the current row without touching the DB or
	// audit log (avoids a "noop" override_log entry and an updated_at bump). A
	// read is always allowed regardless of the self-service policy.
	if stalenessMode == nil && duplicateGuard == nil && cleanupScanEnabled == nil {
		return s.tenants.GetByID(ctx, tid)
	}
	// Self-service gate: toggle edits require manager (admin_only escalates to
	// admin). Manager-level because these toggles arm destructive retention, so
	// they are not bare-member self-service; personal owners still pass via
	// owner ⇒ manager.
	tenant, err := s.tenants.GetByID(ctx, tid)
	if err != nil {
		return nil, err
	}
	if err := s.requireSelfService(ctx, tenant, authz.RelManager); err != nil {
		return nil, err
	}
	tenant, err = s.applyTenantPatch(ctx, tid, UpdateTenantFields{
		StalenessMode:      stalenessMode,
		DuplicateGuard:     duplicateGuard,
		CleanupScanEnabled: cleanupScanEnabled,
	})
	if err != nil {
		return nil, err
	}
	target := tid
	s.logOverride(ctx, repository.OverrideEvent{
		TenantID:     tid,
		Tool:         models.OverrideToolUpdateMyTenantSettings,
		TargetID:     &target,
		OverrideType: models.OverrideTypeSettingsChange,
		Reason:       formatSettingsChange(stalenessMode, duplicateGuard, cleanupScanEnabled),
	})
	return tenant, nil
}

// UpdateTenantSettings is the tenant-targeted analogue of UpdateMyTenantSettings:
// it reads or edits ANOTHER tenant's toggles by id (the ctx-only sibling always
// targets the caller's home tenant). With all three field pointers nil it is a
// READ, gated by CanManageTenant (system admin OR tenant#manager). Otherwise it is
// a WRITE, gated by the tenant's self-service policy at manager level
// (requireSelfService: open ⇒ manager, admin_only ⇒ admin, system-admin bypass) —
// managers manage settings, the lock escalates to admins. Writes are audited.
func (s *MemoryService) UpdateTenantSettings(ctx context.Context, tenantID uuid.UUID, stalenessMode *string, duplicateGuard *bool, cleanupScanEnabled *bool) (*models.Tenant, error) {
	tenant, err := s.tenants.GetByID(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	// No fields = settings read: gate on manage rights, populate the derived
	// effective policy, and return without touching the DB or the audit log.
	if stalenessMode == nil && duplicateGuard == nil && cleanupScanEnabled == nil {
		if !s.CanManageTenant(ctx, tenantID) {
			return nil, fmt.Errorf("%w: not authorized to view this tenant's settings", apperr.ErrInvalidInput)
		}
		tenant.EffectivePolicy = tenant.EffectiveSelfServicePolicy(s.SelfServicePolicyDefault)
		return tenant, nil
	}
	// Write: self-service gate at manager level (admin_only escalates to admin).
	if err := s.requireSelfService(ctx, tenant, authz.RelManager); err != nil {
		return nil, err
	}
	tenant, err = s.applyTenantPatch(ctx, tenantID, UpdateTenantFields{
		StalenessMode:      stalenessMode,
		DuplicateGuard:     duplicateGuard,
		CleanupScanEnabled: cleanupScanEnabled,
	})
	if err != nil {
		return nil, err
	}
	target := tenantID
	s.logOverride(ctx, repository.OverrideEvent{
		TenantID:     tenantID,
		Tool:         models.OverrideToolUpdateTenantSettings,
		TargetID:     &target,
		OverrideType: models.OverrideTypeSettingsChange,
		Reason:       formatSettingsChange(stalenessMode, duplicateGuard, cleanupScanEnabled),
	})
	tenant.EffectivePolicy = tenant.EffectiveSelfServicePolicy(s.SelfServicePolicyDefault)
	return tenant, nil
}

// formatSettingsChange renders the patch as a compact override_log.reason string.
func formatSettingsChange(stalenessMode *string, duplicateGuard *bool, cleanupScanEnabled *bool) string {
	parts := make([]string, 0, 3)
	if stalenessMode != nil {
		parts = append(parts, "staleness_mode="+*stalenessMode)
	}
	if duplicateGuard != nil {
		parts = append(parts, fmt.Sprintf("duplicate_guard=%t", *duplicateGuard))
	}
	if cleanupScanEnabled != nil {
		parts = append(parts, fmt.Sprintf("cleanup_scan_enabled=%t", *cleanupScanEnabled))
	}
	if len(parts) == 0 {
		return "noop"
	}
	return strings.Join(parts, ", ")
}

func (s *MemoryService) applyTenantPatch(ctx context.Context, id uuid.UUID, fields UpdateTenantFields) (*models.Tenant, error) {
	// Validate the type patch before the DB read so bad input is rejected cheaply
	// (and the admin update path stays unit-testable without a database). Type is
	// a DISPLAY-ONLY classifier and MUST NEVER be read by authz.
	if fields.Type != nil && !models.IsValidTenantType(*fields.Type) {
		return nil, fmt.Errorf("%w: tenant type must be personal or shared", apperr.ErrInvalidInput)
	}
	if fields.SelfServicePolicy != nil {
		switch *fields.SelfServicePolicy {
		case models.SelfServicePolicyOpen, models.SelfServicePolicyAdminOnly, "inherit":
		default:
			return nil, fmt.Errorf("%w: self_service_policy must be open, admin_only, or inherit", apperr.ErrInvalidInput)
		}
	}
	tenant, err := s.tenants.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if fields.Name != nil {
		tenant.Name = *fields.Name
	}
	if fields.Email != nil {
		tenant.Email = *fields.Email
	}
	if fields.Type != nil {
		tenant.Type = *fields.Type
	}
	if fields.StalenessMode != nil {
		if _, ok := models.ValidStalenessModes[*fields.StalenessMode]; !ok {
			return nil, fmt.Errorf("%w: staleness_mode must be off, advisory, or hard", apperr.ErrInvalidInput)
		}
		tenant.StalenessMode = *fields.StalenessMode
	}
	if fields.DuplicateGuard != nil {
		tenant.DuplicateGuard = *fields.DuplicateGuard
	}
	if fields.CleanupScanEnabled != nil {
		tenant.CleanupScanEnabled = *fields.CleanupScanEnabled
	}
	if fields.SelfServicePolicy != nil {
		// "inherit" clears the override to NULL; Save persists a nil pointer as NULL.
		if *fields.SelfServicePolicy == "inherit" {
			tenant.SelfServicePolicy = nil
		} else {
			v := *fields.SelfServicePolicy
			tenant.SelfServicePolicy = &v
		}
	}
	if err := s.tenants.Update(ctx, tenant); err != nil {
		return nil, fmt.Errorf("update tenant: %w", err)
	}
	return tenant, nil
}

func (s *MemoryService) DeleteTenant(ctx context.Context, id uuid.UUID) error {
	if err := s.requireAdmin(ctx); err != nil {
		return err
	}
	if id == models.BootstrapTenantID {
		return fmt.Errorf("%w: cannot delete the bootstrap tenant", apperr.ErrInvalidInput)
	}
	return s.tenants.Delete(ctx, id)
}

// CreateAPIKey mints a key for a tenant. subjectID pins the key to an authorization
// subject; nil/empty defaults to the tenant service principal ("svc:<tenant_id>").
// The subject is granted tenant membership (idempotent).
func (s *MemoryService) CreateAPIKey(ctx context.Context, tenantID uuid.UUID, label string, subjectID *string, expiresAt *time.Time) (string, *models.APIKey, error) {
	tenant, err := s.tenants.GetByID(ctx, tenantID)
	if err != nil {
		return "", nil, err
	}
	// Authorization (personal-owner-role D4 + tenant-self-service-lock): a system
	// admin may mint a key for any (personal) tenant and pin it to any subject. A
	// non-system-admin must satisfy the tenant's self-service policy — "open" ⇒
	// manager (personal owners qualify via owner ⇒ manager, enabling owner
	// self-service), "admin_only" ⇒ admin (excludes personal owners) — and may only
	// mint a key for their OWN subject (the foreign-subject pin below is admin-only).
	if err := s.requireSelfService(ctx, tenant, authz.RelManager); err != nil {
		return "", nil, err
	}
	isSysAdmin := s.isAdmin(ctx)
	// Operation-validity (not an access decision — type never gates Check): API
	// keys are a personal-tenant affordance. A shared tenant is reached via its
	// members' own identities + ACL, so a tenant-scoped key has no purpose.
	if tenant.Type == models.TenantTypeShared {
		return "", nil, fmt.Errorf("%w: API keys cannot be created for a shared tenant", apperr.ErrInvalidInput)
	}
	if subjectID != nil && *subjectID == "" {
		subjectID = nil
	}
	// A system admin may pin to any subject (incl. nil ⇒ the tenant service
	// principal). A self-serving non-admin (personal owner) is scoped to their OWN
	// subject and may NEVER mint a key for the service principal or a foreign
	// subject: svc:<tenant> can carry residual elevated grants (e.g. a prior
	// seedAdminServicePrincipals system#admin), so an unpinned/svc key here would
	// be a privilege escalation. Pinning to the caller's own owner subject yields
	// an owner-scoped key with no more power than the caller already holds.
	if !isSysAdmin {
		subj, ok := auth.SubjectFromContext(ctx)
		if !ok || subj.ID == "" {
			return "", nil, fmt.Errorf("%w: cannot determine caller subject for key creation", apperr.ErrInvalidInput)
		}
		if subjectID != nil && *subjectID != subj.ID {
			return "", nil, fmt.Errorf("%w: only a system admin may pin an API key to another subject", apperr.ErrInvalidInput)
		}
		subjectID = &subj.ID
	}
	// A wildcard subject would seed a user:* tuple — public membership. Even
	// though Check no longer honors user:* on privileged relations (subject
	// typing), this is the only reachable write path that could create such a
	// tuple, so reject it at the source. Only a system admin could reach here
	// with "*" (a non-admin's subject is forced to their own id above).
	if subjectID != nil && *subjectID == authz.Wildcard {
		return "", nil, fmt.Errorf(`%w: subject_id cannot be the wildcard "*"`, apperr.ErrInvalidInput)
	}
	plaintext, hash, err := auth.GenerateAPIKey()
	if err != nil {
		return "", nil, fmt.Errorf("generate key: %w", err)
	}
	key := &models.APIKey{
		TenantID:  tenantID,
		KeyHash:   hash,
		Label:     label,
		Prefix:    auth.KeyPrefix(plaintext),
		SubjectID: subjectID,
		ExpiresAt: expiresAt,
	}
	if err := s.keys.Create(ctx, key); err != nil {
		return "", nil, fmt.Errorf("create key: %w", err)
	}
	// Lifecycle seeding: ensure the key's subject is a member of the tenant.
	s.seedTuple(ctx, authzseed.TenantMember(tenantID, authzseed.APIKeySubjectID(*key)))
	return plaintext, key, nil
}

// RotateAPIKey issues a replacement for an existing key's tenant/label/subject and
// retires the predecessor: grace==0 revokes it now, grace>0 sets expiry to now+grace
// for a zero-downtime swap. Returns the new plaintext exactly once.
func (s *MemoryService) RotateAPIKey(ctx context.Context, keyID uuid.UUID, grace time.Duration) (string, *models.APIKey, error) {
	if err := s.requireAdmin(ctx); err != nil {
		return "", nil, err
	}
	old, err := s.keys.GetByID(ctx, keyID)
	if err != nil {
		return "", nil, err
	}
	// Rotation must be atomic: previously the replacement was minted via a
	// committed s.CreateAPIKey and only THEN was the predecessor retired via a
	// separate autocommit write. A retire failure (e.g. old key already revoked ->
	// ErrNotFound) left the new key committed while the caller was told rotation
	// failed — an orphaned, unauditable live credential — and concurrent rotations
	// both minted a key. Wrap create + retire in ONE transaction, locking the
	// predecessor FOR UPDATE so concurrent rotations serialize and a retire failure
	// rolls back the new key. s.CreateAPIKey / s.keys.* are autocommit against the
	// pooled DB, so inline the writes on tx-bound repos (mirrors the B11 pattern).
	var newKey *models.APIKey
	var rotatedPlaintext string
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txKeys := repository.NewAPIKeyRepository(tx)
		// Lock the predecessor so concurrent rotations of the same key serialize
		// and only one replacement is ever minted.
		var locked models.APIKey
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, "id = ?", old.ID).Error; err != nil {
			return fmt.Errorf("lock old key: %w", err)
		}
		// Mint the replacement (same tenant/label/subject as old).
		plaintext, hash, gerr := auth.GenerateAPIKey()
		if gerr != nil {
			return fmt.Errorf("generate key: %w", gerr)
		}
		newKey = &models.APIKey{
			TenantID:  old.TenantID,
			KeyHash:   hash,
			Label:     old.Label,
			Prefix:    auth.KeyPrefix(plaintext),
			SubjectID: old.SubjectID,
			ExpiresAt: nil,
		}
		if err := txKeys.Create(ctx, newKey); err != nil {
			return fmt.Errorf("create key: %w", err)
		}
		// Seed the member tuple with HARD-FAILING authz.Write (like B11) so a tuple
		// failure rolls back the new key. (old.SubjectID is a real subject, never "*".)
		if s.authz != nil {
			if err := s.authz.Write(ctx, authzseed.TenantMember(old.TenantID, authzseed.APIKeySubjectID(*newKey))); err != nil {
				return fmt.Errorf("seed key member tuple: %w", err)
			}
		}
		// Retire the predecessor inside the same tx.
		if grace > 0 {
			expiry := time.Now().Add(grace)
			if err := txKeys.SetExpiry(ctx, old.ID, &expiry); err != nil {
				return fmt.Errorf("set grace expiry on old key: %w", err)
			}
		} else if err := txKeys.Revoke(ctx, old.ID); err != nil {
			return fmt.Errorf("revoke old key: %w", err)
		}
		rotatedPlaintext = plaintext
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	return rotatedPlaintext, newKey, nil
}

func (s *MemoryService) ListAPIKeys(ctx context.Context, tenantID uuid.UUID) ([]models.APIKey, error) {
	if err := s.requireAdmin(ctx); err != nil {
		return nil, err
	}
	return s.keys.ListByTenant(ctx, tenantID)
}

func (s *MemoryService) RevokeAPIKey(ctx context.Context, id uuid.UUID) error {
	if err := s.requireAdmin(ctx); err != nil {
		return err
	}
	return s.keys.Revoke(ctx, id)
}

// DeleteAPIKey permanently removes a key row (hard delete, no audit trace). It is
// admin-only and restricted to dead keys — already revoked or past expiry — since
// the UI surfaces it only on those rows, for cleanup. An active key must be
// revoked (or expire) first.
func (s *MemoryService) DeleteAPIKey(ctx context.Context, id uuid.UUID) error {
	if err := s.requireAdmin(ctx); err != nil {
		return err
	}
	key, err := s.keys.GetByID(ctx, id)
	if err != nil {
		return err
	}
	expired := key.ExpiresAt != nil && key.ExpiresAt.Before(time.Now())
	if key.RevokedAt == nil && !expired {
		return fmt.Errorf("%w: only revoked or expired keys can be deleted", apperr.ErrInvalidInput)
	}
	return s.keys.Delete(ctx, id)
}

// PurgeDeadKeys hard-deletes keys that have been dead — revoked or expired — for
// longer than ttl. It runs from the scheduled system sweep with no user context, so
// it performs no per-request authz. ttl <= 0 is a no-op. Returns the count removed.
func (s *MemoryService) PurgeDeadKeys(ctx context.Context, ttl time.Duration) (int64, error) {
	if ttl <= 0 {
		return 0, nil
	}
	return s.keys.PurgeDeadBefore(ctx, time.Now().Add(-ttl))
}

// GenerateIndex builds the browse catalog aggregated across the caller's
// readable tenant SET (home + common pool + directly-granted tenants), the same
// no-leak scope as search/list/get/get_related. The optional tenant_id filter
// narrows to one readable tenant; a non-readable filter yields an empty scope ->
// empty index (no existence leak).
func (s *MemoryService) GenerateIndex(ctx context.Context, depth string, category *string, overrideID *uuid.UUID) ([]repository.IndexEntry, error) {
	scope, err := s.readScope(ctx, overrideID)
	if err != nil {
		return nil, err
	}
	if len(scope) == 0 {
		return []repository.IndexEntry{}, nil // non-readable filter target
	}
	return s.docs.GenerateIndex(ctx, scope, repository.IndexDepth(depth), category)
}

// GetRelated returns documents semantically related to the target, aggregated
// across the caller's readable tenant SET (home + common pool + directly-granted
// tenants) exactly like search/list/get, and labels each result by its owning
// tenant. A related document is returned only when its owning tenant is in that
// set — the same no-leak guarantee as the other reads. The optional tenant_id
// filter narrows to one readable tenant; a non-readable filter yields an empty
// scope -> empty result (no existence leak), consistent with the other reads.
//
// The viewer Check on the caller-supplied target (finding #9, IDOR) still blocks
// probing another tenant's docs; viewer-level is deliberate — denies private
// cross-tenant targets but allows relating over world-readable common-pool docs.
func (s *MemoryService) GetRelated(ctx context.Context, documentID uuid.UUID, limit int, overrideID *uuid.UUID) ([]repository.RelatedResult, error) {
	scope, err := s.readScope(ctx, overrideID)
	if err != nil {
		return nil, err
	}
	if len(scope) == 0 {
		return []repository.RelatedResult{}, nil // non-readable filter target
	}
	if !s.authorize(ctx, authz.TypeDocument, documentID.String(), authz.RelViewer) {
		return nil, fmt.Errorf("%w: not authorized for document %s", apperr.ErrInvalidInput, documentID)
	}
	results, err := s.sections.GetRelated(ctx, scope, documentID, limit)
	if err != nil {
		return nil, err
	}
	s.labelRelatedResults(ctx, results)
	return results, nil
}

// labelRelatedResults fills each related result's owning-tenant name/type in ONE
// lookup over the distinct result tenants (mirrors resolveResultTenants without
// the staleness overlay — no N+1). Fail-safe: on a lookup miss labels are simply
// absent, never failing the read on a glitch.
func (s *MemoryService) labelRelatedResults(ctx context.Context, results []repository.RelatedResult) {
	if len(results) == 0 || s.tenants == nil {
		return
	}
	seen := make(map[uuid.UUID]struct{})
	ids := make([]uuid.UUID, 0)
	for i := range results {
		id := results[i].TenantID
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	tenants, err := s.tenants.GetByIDs(ctx, ids)
	if err != nil {
		return
	}
	byID := make(map[uuid.UUID]models.Tenant, len(tenants))
	for _, t := range tenants {
		byID[t.ID] = t
	}
	for i := range results {
		if t, ok := byID[results[i].TenantID]; ok {
			results[i].TenantName = t.Name
			results[i].TenantType = t.Type
		}
	}
}

func (s *MemoryService) LintMemory(ctx context.Context, checks []string, thresholds *repository.LintThresholds, overrideID *uuid.UUID) ([]repository.LintFinding, error) {
	tid, err := s.resolveTenant(ctx, overrideID)
	if err != nil {
		return nil, err
	}

	t := repository.DefaultLintThresholds()
	if thresholds != nil {
		t = *thresholds
	}

	type lintCheck struct {
		name string
		fn   func(context.Context, uuid.UUID, repository.LintThresholds) ([]repository.LintFinding, error)
	}
	allChecks := []lintCheck{
		{"stale", s.lint.CheckStale},
		{"sparse", s.lint.CheckSparse},
		{"near_duplicate", s.lint.CheckNearDuplicates},
		{"empty_category", s.lint.CheckEmptyCategories},
	}

	// Filter to requested checks if specified
	requested := make(map[string]struct{}, len(checks))
	for _, c := range checks {
		requested[c] = struct{}{}
	}

	var findings []repository.LintFinding
	for _, check := range allChecks {
		if len(checks) > 0 {
			if _, ok := requested[check.name]; !ok {
				continue
			}
		}
		results, err := check.fn(ctx, tid, t)
		if err != nil {
			return nil, err
		}
		findings = append(findings, results...)
	}
	return findings, nil
}

// parsedSection holds a parsed markdown section.
type parsedSection struct {
	heading *string
	content string
}

// parseMarkdown splits markdown by ## headings into sections, returning the
// title (from the # heading) and the sections.
func parseMarkdown(content string) (string, []parsedSection) {
	lines := strings.Split(content, "\n")
	var title string
	var sections []parsedSection
	var currentHeading *string
	var currentLines []string

	flush := func() {
		text := strings.TrimSpace(strings.Join(currentLines, "\n"))
		if text != "" {
			sections = append(sections, parsedSection{
				heading: currentHeading,
				content: text,
			})
		}
		currentLines = nil
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Extract title from # heading (first one only)
		if strings.HasPrefix(trimmed, "# ") && !strings.HasPrefix(trimmed, "## ") && title == "" {
			title = strings.TrimPrefix(trimmed, "# ")
			continue
		}

		// Split on ## headings
		if strings.HasPrefix(trimmed, "## ") {
			flush()
			h := strings.TrimPrefix(trimmed, "## ")
			currentHeading = &h
			continue
		}

		currentLines = append(currentLines, line)
	}

	flush()

	// No ## headings: put everything in one section
	if len(sections) == 0 && strings.TrimSpace(content) != "" {
		sections = append(sections, parsedSection{
			heading: nil,
			content: strings.TrimSpace(content),
		})
	}

	return title, sections
}
