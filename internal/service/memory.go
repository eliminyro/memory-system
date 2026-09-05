package service

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
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
	"github.com/eliminyro/memory-system/internal/panicguard"
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

// maxHistoryEntries caps the per-document history the view returns.
const maxHistoryEntries = 200

// historyEnabledFor reports whether a mutation on tid should be recorded: the
// global toggle on AND tid is a shared tenant. Nil-safe (no config repo -> off),
// so callers can gate before capturing any snapshot (toggle-off = zero work).
func (s *MemoryService) historyEnabledFor(ctx context.Context, tid uuid.UUID) bool {
	if s.tenants == nil || !s.effectiveHistoryEnabled(ctx) {
		return false
	}
	t, err := s.tenants.GetByID(ctx, tid)
	if err != nil || t == nil {
		return false
	}
	return t.Type == models.TenantTypeShared
}

// effectiveHistoryEnabled reads the live global history toggle via the accessor;
// without one wired (offline CLI / tests) it falls back to the instance_config
// repo, else off.
func (s *MemoryService) effectiveHistoryEnabled(ctx context.Context) bool {
	if s.globalCfg != nil {
		return s.globalCfg.HistoryEnabled()
	}
	if s.instanceConfig == nil {
		return false
	}
	cfg, err := s.instanceConfig.Get(ctx)
	return err == nil && cfg.HistoryEnabled
}

// effectiveSelfServicePolicyDefault reads the live global self-service policy via
// the accessor; without one wired (offline CLI / tests) it falls back to the
// boot-seeded SelfServicePolicyDefault field.
func (s *MemoryService) effectiveSelfServicePolicyDefault() string {
	if s.globalCfg != nil {
		return s.globalCfg.SelfServicePolicy()
	}
	return s.SelfServicePolicyDefault
}

// actorFields resolves best-effort attribution from context: subject id (always)
// and email (may be empty). APIKeyID is left unset — the OverrideEvent call sites
// don't source it from context either, so history matches that attribution.
func (s *MemoryService) actorFields(ctx context.Context) (subject string, email *string) {
	if subj, ok := auth.SubjectFromContext(ctx); ok {
		subject = subj.ID
	}
	if e := auth.EmailFromContext(ctx); e != "" {
		email = &e
	}
	return subject, email
}

// logMutation best-effort writes a mutation history row; on failure it warns and
// continues (the mutation already succeeded — a dropped audit row beats failing it).
func (s *MemoryService) logMutation(ctx context.Context, ev repository.MutationEvent) {
	if s.history == nil {
		return
	}
	if err := s.history.Log(ctx, ev); err != nil {
		slog.Default().Warn("mutation history write failed",
			"op_type", ev.OpType, "document_id", ev.DocumentID, "error", err)
	}
}

// logMutationTx writes a history row inside a delete tx so a committed delete
// always carries its record. The write runs in a SAVEPOINT (nested tx) so a
// failure rolls back only itself, never aborting the outer delete tx.
func (s *MemoryService) logMutationTx(ctx context.Context, tx *gorm.DB, ev repository.MutationEvent) {
	if s.history == nil {
		return
	}
	err := tx.Transaction(func(htx *gorm.DB) error {
		return repository.NewMutationHistoryRepository(htx).Log(ctx, ev)
	})
	if err != nil {
		slog.Default().Warn("mutation history write failed (in-tx)",
			"op_type", ev.OpType, "document_id", ev.DocumentID, "error", err)
	}
}

// Before-snapshot shapes (design D1); each is JSON-encoded verbatim into
// MutationHistory.Before for the reader to interpret by op_type.
type updateSectionBefore struct {
	Content string  `json:"content"`
	Heading *string `json:"heading"`
}

type deleteSectionBefore struct {
	Content string  `json:"content"`
	Heading *string `json:"heading"`
	Ordinal int     `json:"ordinal"`
}

type updateTitleBefore struct {
	Title string `json:"title"`
}

type deleteDocSection struct {
	Heading *string `json:"heading"`
	Content string  `json:"content"`
	Ordinal int     `json:"ordinal"`
}

type deleteDocumentBefore struct {
	Title    string             `json:"title"`
	DocType  string             `json:"doc_type"`
	Sections []deleteDocSection `json:"sections"`
}

// marshalBefore JSON-encodes a snapshot to *string; a marshal error yields nil
// (the snapshot is best-effort — attribution still records without it).
func marshalBefore(v any) *string {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	str := string(b)
	return &str
}

// docBeforeSnapshot builds the delete_document Before payload from a preloaded doc.
func docBeforeSnapshot(doc *models.Document) *string {
	secs := make([]deleteDocSection, len(doc.Sections))
	for i, sec := range doc.Sections {
		secs[i] = deleteDocSection{Heading: sec.Heading, Content: sec.Content, Ordinal: sec.Ordinal}
	}
	return marshalBefore(deleteDocumentBefore{Title: doc.Title, DocType: doc.DocType, Sections: secs})
}

type MemoryService struct {
	db         *gorm.DB
	docs       *repository.DocumentRepository
	sections   *repository.SectionRepository
	embedder   EmbeddingProvider
	tenants    *repository.TenantRepository
	keys       *repository.APIKeyRepository
	lint       *repository.LintRepository
	thresholds *staleness.PolicyStore
	overrides  *repository.OverrideLogRepository
	cleanup    *repository.CleanupQueueRepository
	// edges backs the typed doc-to-doc edge overlay; nil (offline CLI / tests)
	// disables the edge methods. Wired only in the MCP server via WithEdgeRepository.
	edges *repository.EdgeRepository
	// instanceConfig + history back the mutation-history audit; both may be nil
	// (offline CLI / most tests), in which case historyEnabledFor short-circuits off.
	instanceConfig *repository.InstanceConfigRepository
	history        *repository.MutationHistoryRepository
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
	// snippetChars caps the match-centered window Search returns when snippet=true.
	// Set explicitly in NewMemoryService so a service built without WithSnippetChars
	// still gets the default, not the int zero value (which would blank snippets).
	snippetChars int
	// candidatePool is the per-list HybridSearch LIMIT Search applies. Set explicitly
	// in NewMemoryService so a service built without WithCandidatePool still gets the
	// default, not the int zero value.
	candidatePool int
	// globalCfg reads runtime global config (the duplicate-threshold default for the
	// write guard); nil (offline CLI / tests) falls back to defaultDuplicateThreshold.
	globalCfg GlobalConfig
	// metrics appends best-effort usage events (access/verify) for opted-in tenants;
	// nil (offline CLI / tests / metrics unwired) disables emission entirely.
	metrics *repository.MetricEventRepository
}

// GlobalConfig is the read-only slice of the global-config accessor the service
// reads live; *globalconfig.Accessor satisfies it.
type GlobalConfig interface {
	MMRLambda() float64
	StalenessPenalty() float64
	CandidatePool() int
	SnippetChars() int
	StalenessDefault() string
	DuplicateGuardDefault() bool
	CleanupScanDefault() bool
	HistoryEnabled() bool
	DuplicateThreshold() float64
	SelfServicePolicy() string
	RetentionGraceDays() int
}

// defaultSnippetChars mirrors config MEMORY_SNIPPET_CHARS default so a service
// built without WithSnippetChars (offline CLI / tests) still windows sensibly.
const defaultSnippetChars = 400

// defaultCandidatePool mirrors config MEMORY_CANDIDATE_POOL default so a service
// built without WithCandidatePool (offline CLI / tests) still bounds candidates.
const defaultCandidatePool = 20

// defaultDuplicateThreshold mirrors the global instance_config default so the
// write guard still has a sane cutoff when no globalCfg is wired (offline CLI / tests).
const defaultDuplicateThreshold = 0.85

// defaultRetentionGraceDays mirrors the instance_config default so the retention
// dry-run has a sane grace window when no globalCfg is wired (offline CLI / tests).
const defaultRetentionGraceDays = 30

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

// WithCandidatePool sets the per-list HybridSearch candidate LIMIT applied to
// every Search call. Without it candidatePool keeps its default.
func WithCandidatePool(n int) Option {
	return func(s *MemoryService) {
		s.candidatePool = n
	}
}

// WithEdgeRepository injects the typed-edge repo. Without it edges stays nil and
// the edge methods refuse (offline CLI / tests); wired only in the MCP server.
func WithEdgeRepository(edges *repository.EdgeRepository) Option {
	return func(s *MemoryService) {
		s.edges = edges
	}
}

// WithGlobalConfig injects the global-config accessor. Without it globalCfg stays
// nil and the write guard's threshold falls back to defaultDuplicateThreshold.
func WithGlobalConfig(gc GlobalConfig) Option {
	return func(s *MemoryService) {
		s.globalCfg = gc
	}
}

// WithMetricEventRepository injects the metric-events repo. Without it metrics
// stays nil and no usage events are emitted (offline CLI / tests).
func WithMetricEventRepository(metrics *repository.MetricEventRepository) Option {
	return func(s *MemoryService) {
		s.metrics = metrics
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
	thresholds *staleness.PolicyStore,
	overrides *repository.OverrideLogRepository,
	cleanup *repository.CleanupQueueRepository,
	instanceConfig *repository.InstanceConfigRepository,
	history *repository.MutationHistoryRepository,
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
		instanceConfig: instanceConfig,
		history:        history,
		authz:          authzStore,
		authzEngine:    engine,
		snippetChars:   defaultSnippetChars,
		candidatePool:  defaultCandidatePool,
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

// resolveWriteScope mirrors resolveTenant but admits a tenant_id override for a
// caller holding floor+ on the target (grant-aware writes), not only admins.
// Own-tenant/admin paths stay identical; fails closed via authorize.
func (s *MemoryService) resolveWriteScope(ctx context.Context, overrideID *uuid.UUID, floor string) (uuid.UUID, error) {
	tid := auth.TenantIDFromContext(ctx)
	if tid == uuid.Nil {
		return uuid.Nil, fmt.Errorf("%w: missing tenant ID in context", apperr.ErrInvalidInput)
	}
	if overrideID == nil {
		return tid, nil
	}
	if !s.isAdmin(ctx) && !s.authorize(ctx, authz.TypeTenant, overrideID.String(), floor) {
		return uuid.Nil, fmt.Errorf("%w: write access required on tenant %s", apperr.ErrInvalidInput, overrideID)
	}
	if _, err := s.tenants.GetByID(ctx, *overrideID); err != nil {
		return uuid.Nil, fmt.Errorf("%w: target tenant %s not found", apperr.ErrInvalidInput, overrideID)
	}
	return *overrideID, nil
}

// IsAdmin exposes the admin gate for the admin HTTP middleware (which decides
// 403-vs-proceed before dispatch; service methods re-check, so not the sole
// enforcement point). Admin = system:memory#admin via tuple Check, never an email.
func (s *MemoryService) IsAdmin(ctx context.Context) bool { return s.isAdmin(ctx) }

func (s *MemoryService) isAdmin(ctx context.Context) bool {
	// A local-admin context (in-process, no authenticated subject) reuses these
	// lifecycle methods. Network paths never set it.
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
	if tenant.EffectiveSelfServicePolicy(s.effectiveSelfServicePolicyDefault()) == models.SelfServicePolicyAdminOnly {
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
	StalenessMode      string
	DuplicateGuard     bool
	DuplicateThreshold float64
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
	return tenantSettings{StalenessMode: mode, DuplicateGuard: t.DuplicateGuard, DuplicateThreshold: s.effectiveDuplicateThreshold(t.DuplicateThreshold)}
}

// effectiveDuplicateThreshold resolves the write-guard cutoff: a valid per-tenant
// override wins, else the global default (globalCfg), else defaultDuplicateThreshold
// when no accessor is wired (offline CLI / tests).
func (s *MemoryService) effectiveDuplicateThreshold(override *float64) float64 {
	if override != nil && *override > 0 {
		return *override
	}
	if s.globalCfg != nil {
		if v := s.globalCfg.DuplicateThreshold(); v > 0 {
			return v
		}
	}
	return defaultDuplicateThreshold
}

// effectiveMMRLambda resolves the Search MMR lambda: the live global value via
// the accessor, else the construction-time fallback (nil = MMR off).
func (s *MemoryService) effectiveMMRLambda() *float64 {
	if s.globalCfg != nil {
		l := s.globalCfg.MMRLambda()
		return &l
	}
	return s.mmrLambda
}

// effectiveStalenessPenalty resolves the Search staleness weight: the live global
// value via the accessor, else 0 (off) when no accessor is wired (CLI/tests).
func (s *MemoryService) effectiveStalenessPenalty() float64 {
	if s.globalCfg != nil {
		return s.globalCfg.StalenessPenalty()
	}
	return 0
}

// effectiveCandidatePool resolves the per-list HybridSearch LIMIT: the live
// global value, else the construction-time fallback.
func (s *MemoryService) effectiveCandidatePool() int {
	if s.globalCfg != nil {
		if v := s.globalCfg.CandidatePool(); v > 0 {
			return v
		}
	}
	return s.candidatePool
}

// effectiveSnippetChars resolves the snippet window size: the live global value,
// else the construction-time fallback.
func (s *MemoryService) effectiveSnippetChars() int {
	if s.globalCfg != nil {
		if v := s.globalCfg.SnippetChars(); v > 0 {
			return v
		}
	}
	return s.snippetChars
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
	if *overrideID == home || *overrideID == models.BootstrapTenantID {
		return []uuid.UUID{*overrideID}, nil
	}
	if s.authorize(ctx, authz.TypeTenant, overrideID.String(), authz.RelViewer) {
		// Permitted via a grant or, for an admin, admin⇒viewer. If the target is
		// outside the caller's own read scope this is a cross-tenant admin read — audit it.
		s.auditCrossTenantRead(ctx, *overrideID)
		return []uuid.UUID{*overrideID}, nil
	}
	return nil, nil // not readable -> empty scope -> empty result, no leak
}

// promptTargeted reports whether a read explicitly names prompts.
func promptTargeted(category, docType *string) bool {
	return (category != nil && *category == "prompts") || (docType != nil && *docType == models.DocTypePrompt)
}

// readScopeForPrompts narrows a read to the home tenant when it targets prompts —
// prompt documents are own-tenant-only and never resolve from the common pool or a
// grant (design D4); every other read keeps the normal aggregate scope.
func (s *MemoryService) readScopeForPrompts(ctx context.Context, category, docType *string, overrideID *uuid.UUID) ([]uuid.UUID, error) {
	if promptTargeted(category, docType) {
		home := auth.TenantIDFromContext(ctx)
		if home == uuid.Nil {
			return nil, fmt.Errorf("%w: missing tenant ID in context", apperr.ErrInvalidInput)
		}
		return []uuid.UUID{home}, nil
	}
	return s.readScope(ctx, overrideID)
}

// dropForeignPrompts removes prompt documents not owned by home — the safety net
// behind readScopeForPrompts on unfiltered/by-id paths, so a prompt never leaks
// from the common pool or a granted tenant (design D4).
func dropForeignPrompts(docs []models.Document, home uuid.UUID) []models.Document {
	out := docs[:0]
	for _, d := range docs {
		if d.DocType == models.DocTypePrompt && d.TenantID != home {
			continue
		}
		out = append(out, d)
	}
	return out
}

// auditCrossTenantRead writes a best-effort override_log row when a caller resolves
// a read scope to a tenant outside their own membership/grant set (an admin using
// the tenant_id override). Cheap: skipped when no sink or the target is in-scope.
func (s *MemoryService) auditCrossTenantRead(ctx context.Context, target uuid.UUID) {
	if s.overrides == nil {
		return
	}
	readable, err := s.readableTenants(ctx)
	if err != nil {
		return
	}
	for _, id := range readable {
		if id == target {
			return // genuine grant/membership, not a cross-tenant admin read
		}
	}
	s.logOverride(ctx, repository.OverrideEvent{
		TenantID:     target,
		Tool:         models.OverrideToolReadScope,
		OverrideType: models.OverrideTypeCrossTenantRead,
	})
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
// When forceRead is true, Reason is required and the override is audited.
func (s *MemoryService) Search(ctx context.Context, query string, category, subcategory, docType *string, limit int, forceRead bool, reason string, overrideID *uuid.UUID, snippet bool) ([]repository.SearchResult, error) {
	if forceRead && strings.TrimSpace(reason) == "" {
		return nil, fmt.Errorf("%w: reason is required when force_read=true", apperr.ErrInvalidInput)
	}
	// A prompt-targeted search is home-only (D4); otherwise default_search=false
	// keeps prompts out of an unfiltered query on both arms.
	scope, err := s.readScopeForPrompts(ctx, category, docType, overrideID)
	if err != nil {
		return nil, err
	}
	if len(scope) == 0 {
		return []repository.SearchResult{}, nil // non-readable filter target
	}
	embedding, err := s.embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	// Only load the threshold map when the penalty is actually on.
	penalty := s.effectiveStalenessPenalty()
	var stalenessThresholds map[string]int
	if penalty > 0 && s.thresholds != nil {
		stalenessThresholds = s.thresholds.DaysByDocType()
	}
	results, err := s.sections.HybridSearch(ctx, repository.SearchParams{
		TenantIDs:           scope,
		Embedding:           embedding,
		Query:               query,
		Category:            category,
		Subcategory:         subcategory,
		DocType:             docType,
		Limit:               limit,
		CandidatePool:       s.effectiveCandidatePool(),
		MMRLambda:           s.effectiveMMRLambda(),
		StalenessPenalty:    penalty,
		StalenessThresholds: stalenessThresholds,
		HiddenDocTypes:      s.policyDocTypes(func(p models.EffectivePolicy) bool { return !p.DefaultSearch }),
	})
	if err != nil {
		return nil, err
	}
	// Label each result by its owning tenant and resolve per-tenant staleness
	// modes in one lookup, then apply staleness under each result's own mode.
	modeByTenant := s.resolveResultTenants(ctx, results)
	if s.thresholds != nil {
		// force_read reveals an expired body only for an admin; a non-admin's
		// force_read is still audited below but leaves the body withheld.
		results, err = applyStalenessToSearchResults(ctx, s.thresholds, results, modeByTenant, forceRead && s.isAdmin(ctx))
		if err != nil {
			return nil, err
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
	// Async, best-effort serve-bump (D2) + access metric emit for opted-in tenants:
	// both off the request ctx, never blocking or failing the search (bounded inside).
	if len(results) > 0 {
		s.bumpAndRecord(ctx, distinctResultDocIDs(results), accessEvents(results))
	}
	return results, nil
}

// bumpAccessed fires a best-effort, day-guarded access-recency bump for docIDs on
// a detached, deadline-bounded context so it never blocks the request, outlives it
// unbounded, or fails the caller. TouchAccessed's day guard caps it to 1 write/day.
func (s *MemoryService) bumpAccessed(ctx context.Context, docIDs ...uuid.UUID) {
	s.bumpAndRecord(ctx, docIDs, nil)
}

// bumpAndRecord runs the serve-bump and, for opted-in tenants, appends the given
// metric events — both inside one detached, deadline-bounded, panic-guarded
// goroutine so a slow or failing write never blocks or breaks the caller.
func (s *MemoryService) bumpAndRecord(ctx context.Context, docIDs []uuid.UUID, events []models.MetricEvent) {
	bump := s.docs != nil && len(docIDs) > 0
	if !bump && len(events) == 0 {
		return
	}
	detached := context.WithoutCancel(ctx)
	go func() {
		defer panicguard.Recover(nil, "access-recency bump")
		bumpCtx, cancel := context.WithTimeout(detached, 5*time.Second)
		defer cancel()
		if bump {
			if err := s.docs.TouchAccessed(bumpCtx, docIDs); err != nil {
				slog.Default().Warn("access-recency bump failed", "error", err)
			}
		}
		s.recordMetricEvents(bumpCtx, events)
	}()
}

// recordMetricEvents appends events for tenants with metrics_enabled, best-effort:
// an append error or an unreadable tenant flag drops the event, never the op.
func (s *MemoryService) recordMetricEvents(ctx context.Context, events []models.MetricEvent) {
	if s.metrics == nil || len(events) == 0 {
		return
	}
	enabled := s.metricsEnabledTenants(ctx, distinctEventTenants(events))
	for i := range events {
		if !enabled[events[i].TenantID] {
			continue
		}
		if err := s.metrics.Append(ctx, &events[i]); err != nil {
			slog.Default().Warn("metric event append failed", "event_type", events[i].EventType, "error", err)
		}
	}
}

// metricsEnabledTenants returns which of ids have metrics_enabled; a lookup miss or
// error yields an empty set (fail-safe: record nothing on a config glitch).
func (s *MemoryService) metricsEnabledTenants(ctx context.Context, ids []uuid.UUID) map[uuid.UUID]bool {
	out := make(map[uuid.UUID]bool, len(ids))
	if s.tenants == nil || len(ids) == 0 {
		return out
	}
	tenants, err := s.tenants.GetByIDs(ctx, ids)
	if err != nil {
		return out
	}
	for _, t := range tenants {
		if t.MetricsEnabled {
			out[t.ID] = true
		}
	}
	return out
}

// distinctEventTenants collects the unique tenant IDs across events.
func distinctEventTenants(events []models.MetricEvent) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{}, len(events))
	ids := make([]uuid.UUID, 0, len(events))
	for i := range events {
		if _, ok := seen[events[i].TenantID]; ok {
			continue
		}
		seen[events[i].TenantID] = struct{}{}
		ids = append(ids, events[i].TenantID)
	}
	return ids
}

// distinctResultDocIDs collects the unique owning document IDs of search results,
// preserving first-seen order, for the access-recency serve-bump.
func distinctResultDocIDs(results []repository.SearchResult) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{}, len(results))
	ids := make([]uuid.UUID, 0, len(results))
	for _, r := range results {
		if _, ok := seen[r.DocumentID]; ok {
			continue
		}
		seen[r.DocumentID] = struct{}{}
		ids = append(ids, r.DocumentID)
	}
	return ids
}

// accessEvents builds one access metric event per distinct served document,
// carrying its owning tenant, doc_type, and id (metrics_enabled gating and the
// append happen off the request path in recordMetricEvents).
func accessEvents(results []repository.SearchResult) []models.MetricEvent {
	seen := make(map[uuid.UUID]struct{}, len(results))
	events := make([]models.MetricEvent, 0, len(results))
	for i := range results {
		docID := results[i].DocumentID
		if _, ok := seen[docID]; ok {
			continue
		}
		seen[docID] = struct{}{}
		id := docID
		events = append(events, models.MetricEvent{
			EventType: models.MetricEventAccess,
			TenantID:  results[i].TenantID,
			DocID:     &id,
			DocType:   results[i].DocType,
		})
	}
	return events
}

// SearchResponse is the search envelope shared by the MCP and HTTP surfaces:
// results is always a JSON array, never null.
type SearchResponse struct {
	Results []repository.SearchResult `json:"results"`
}

// NewSearchResponse builds the envelope from Search's results: a nil slice
// becomes [] so the wire shape is always a JSON array.
func NewSearchResponse(results []repository.SearchResult) SearchResponse {
	resp := SearchResponse{Results: results}
	if resp.Results == nil {
		resp.Results = []repository.SearchResult{}
	}
	return resp
}

// GetDocument fetches a document with all sections by path, applying the
// staleness filter. forceRead + reason override it and audit to override_log.
func (s *MemoryService) GetDocument(ctx context.Context, category string, subcategory *string, slug string, forceRead bool, reason string, overrideID *uuid.UUID) (*DocumentView, error) {
	if forceRead && strings.TrimSpace(reason) == "" {
		return nil, fmt.Errorf("%w: reason is required when force_read=true", apperr.ErrInvalidInput)
	}
	scope, err := s.readScopeForPrompts(ctx, &category, nil, overrideID)
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
	// A prompt is own-tenant-only (D4); parity with GetDocumentByID for the case a
	// prompt shares a non-prompts path in a granted tenant.
	if doc.DocType == models.DocTypePrompt && doc.TenantID != auth.TenantIDFromContext(ctx) {
		return nil, fmt.Errorf("%w: document %s/%s", apperr.ErrNotFound, category, slug)
	}
	// Staleness + labeling use the doc's OWNING tenant, not the caller's home —
	// one tenant fetch drives both the mode and the display label.
	mode, name, typ := s.tenantModeAndLabel(ctx, doc.TenantID)
	view, err := buildDocumentView(ctx, s.thresholds, doc, mode, forceRead && s.isAdmin(ctx))
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
	// A read is a liveness signal: keep the doc off the access-cold path.
	s.bumpAccessed(ctx, doc.ID)
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
	// A prompt is own-tenant-only (D4): a by-id fetch of another tenant's prompt
	// is not-found, never a leak.
	if doc.DocType == models.DocTypePrompt && doc.TenantID != auth.TenantIDFromContext(ctx) {
		return nil, fmt.Errorf("%w: document %s", apperr.ErrNotFound, id)
	}
	// Staleness + labeling use the doc's OWNING tenant, not the caller's home —
	// one tenant fetch drives both the mode and the display label.
	mode, name, typ := s.tenantModeAndLabel(ctx, doc.TenantID)
	view, err := buildDocumentView(ctx, s.thresholds, doc, mode, forceRead && s.isAdmin(ctx))
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
	// A read is a liveness signal: keep the doc off the access-cold path.
	s.bumpAccessed(ctx, doc.ID)
	return &view, nil
}

// GetDocumentHistory lists a doc's mutation history newest-first. Live doc: gated
// like GetDocumentByID (keeps per-doc guest access). Deleted doc: visible to readers
// of its owning tenant (audit survives). Neither readable ⇒ ErrNotFound, no leak.
func (s *MemoryService) GetDocumentHistory(ctx context.Context, docID uuid.UUID, overrideID *uuid.UUID) ([]models.MutationHistory, error) {
	scope, err := s.readScope(ctx, overrideID)
	if err != nil {
		return nil, err
	}
	if len(scope) == 0 {
		return nil, fmt.Errorf("%w: document %s", apperr.ErrNotFound, docID)
	}
	_, docErr := s.docs.GetByID(ctx, scope, docID)
	if docErr != nil && !errors.Is(docErr, apperr.ErrNotFound) {
		return nil, docErr
	}
	if s.history == nil {
		if docErr != nil {
			return nil, docErr
		}
		return []models.MutationHistory{}, nil
	}
	rows, err := s.history.ListByDocument(ctx, docID, maxHistoryEntries)
	if err != nil {
		return nil, err
	}
	if docErr == nil {
		return rows, nil
	}
	// Doc gone/unreadable: expose only if its history's owning tenant is readable.
	if len(rows) == 0 || !slices.Contains(scope, rows[0].TenantID) {
		return nil, fmt.Errorf("%w: document %s", apperr.ErrNotFound, docID)
	}
	return rows, nil
}

// MarkVerified stamps verified_at=NOW() on a section (after an agent confirms a
// claim). Editor Check on the parent doc (finding #8): else any caller could
// verify a shared common-pool section it has no write right to.
func (s *MemoryService) MarkVerified(ctx context.Context, sectionID uuid.UUID, overrideID *uuid.UUID) error {
	tid, err := s.resolveWriteScope(ctx, overrideID, authz.RelMember)
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
	if err := s.sections.MarkVerified(ctx, tid, sectionID); err != nil {
		return err
	}
	// An asserted verification is auditable: it unlocks an expired section for
	// everyone, so record who claimed the content is current.
	secID := sectionID
	s.logOverride(ctx, repository.OverrideEvent{
		TenantID:     tid,
		Tool:         models.OverrideToolMarkVerified,
		TargetID:     &secID,
		OverrideType: models.OverrideTypeVerification,
		Reason:       "section verified",
	})
	// Verifying is a liveness signal: keep the doc off the access-cold path, and
	// record a best-effort verify event for opted-in tenants (detached goroutine).
	s.recordVerify(ctx, tid, section.DocumentID)
	return nil
}

// recordVerify bumps the verified doc's access-recency and, for an opted-in tenant,
// appends a verify event — all in one detached goroutine, gated before the doc_type
// lookup so a non-metrics verify pays nothing on its critical path.
func (s *MemoryService) recordVerify(ctx context.Context, tenantID, docID uuid.UUID) {
	detached := context.WithoutCancel(ctx)
	go func() {
		defer panicguard.Recover(nil, "verify record")
		c, cancel := context.WithTimeout(detached, 5*time.Second)
		defer cancel()
		if s.docs != nil {
			if err := s.docs.TouchAccessed(c, []uuid.UUID{docID}); err != nil {
				slog.Default().Warn("access-recency bump failed", "error", err)
			}
		}
		if s.metrics == nil || s.docs == nil || !s.metricsEnabledTenants(c, []uuid.UUID{tenantID})[tenantID] {
			return
		}
		doc, err := s.docs.GetByID(c, []uuid.UUID{tenantID}, docID)
		if err != nil || doc == nil {
			return
		}
		id := docID
		if err := s.metrics.Append(c, &models.MetricEvent{EventType: models.MetricEventVerify, TenantID: tenantID, DocID: &id, DocType: doc.DocType}); err != nil {
			slog.Default().Warn("metric event append failed", "event_type", models.MetricEventVerify, "error", err)
		}
	}()
}

// StoreResult is the outcome of StoreDocument. Status "similar_exists" means the
// save was skipped and Candidates lists the colliders (Document nil); "ok" on success.
type StoreResult struct {
	Status     string                           `json:"status"`
	Document   *models.Document                 `json:"document,omitempty"`
	Path       string                           `json:"path,omitempty"`
	Sections   int                              `json:"sections,omitempty"`
	Candidates []repository.SimilarityCandidate `json:"candidates,omitempty"`
	// Warnings carries non-fatal issues (e.g. a best-effort handoff auto-chain
	// link that failed) so a valuable write still succeeds while surfacing them.
	Warnings []string `json:"warnings,omitempty"`
}

// StoreDocument parses markdown into sections, embeds them (before any DB write,
// to avoid partial state), runs the duplicate guard, and stores. When the guard
// trips and force is false, no write happens and the result carries candidates.
// pin distinguishes unset (nil, keep current on upsert / default false on
// create) from an explicit true/false. It sets documents.pinned, which exempts
// the doc from access-recency eviction (D4).
// StoreDocument stores or updates a whole document (no scope override).
func (s *MemoryService) StoreDocument(
	ctx context.Context,
	category string, subcategory *string, slug, content string,
	force bool, reason string,
	overrideID *uuid.UUID,
	pin *bool,
) (*StoreResult, error) {
	return s.StoreDocumentScoped(ctx, category, subcategory, slug, content, force, reason, overrideID, pin, nil)
}

// StoreDocumentScoped is StoreDocument plus an optional scope: nil leaves an
// existing value unchanged, a non-nil value sets it (empty string clears).
// Allowed on any doc_type.
func (s *MemoryService) StoreDocumentScoped(
	ctx context.Context,
	category string, subcategory *string, slug, content string,
	force bool, reason string,
	overrideID *uuid.UUID,
	pin *bool,
	scope *string,
) (*StoreResult, error) {
	if force && strings.TrimSpace(reason) == "" {
		return nil, fmt.Errorf("%w: reason is required when force=true", apperr.ErrInvalidInput)
	}
	tid, err := s.resolveWriteScope(ctx, overrideID, authz.RelMember)
	if err != nil {
		return nil, err
	}

	// 2. Classify from the client slug; 3. load the doc_type's effective rules.
	docType := models.InferDocType(category, subcategory, slug)
	policy := s.policyFor(docType)

	// 4. Identity — validate subcategory then slug_format; never rewrite the slug.
	if err := models.ValidateSubcategoryRule(policy.Subcategory, subcategory); err != nil {
		return nil, fmt.Errorf("%w: %s", apperr.ErrInvalidInput, err)
	}
	if err := models.ValidateSlugFormat(policy.SlugFormat, slug); err != nil {
		return nil, fmt.Errorf("%w: %s", apperr.ErrInvalidInput, err)
	}

	// 5. Content.
	title, sections := parseMarkdown(content)
	if title == "" {
		title = slug
	}

	// 9. Embed before the DB (no partial state); skipped when the doc_type doesn't embed.
	embeddings, err := s.embedSections(ctx, sections, policy.Embed)
	if err != nil {
		return nil, err
	}
	sectionModels := make([]models.Section, len(sections))
	for i, sec := range sections {
		sectionModels[i] = models.Section{
			Ordinal:   i,
			Heading:   sec.heading,
			Content:   sec.content,
			Embedding: embeddings[i],
		}
	}

	// 8. Duplicate guard — tenant toggle AND the doc_type rule. Validation
	// guarantees duplicate_guard implies write_mode=replace, so re-saves compare
	// whole-document centroids as today; skipped entirely when embed is off.
	contentHash := hashContent(content)
	settings := s.tenantSettings(ctx, tid)
	if settings.DuplicateGuard && !force && policy.DuplicateGuard && policy.Embed && len(embeddings) > 0 {
		hit, err := s.sections.FindByContentHash(ctx, tid, contentHash, category, subcategory, slug)
		if err != nil {
			return nil, fmt.Errorf("content-hash check: %w", err)
		}
		if hit != nil {
			hit.Similarity = 1.0
			return &StoreResult{Status: "similar_exists", Candidates: []repository.SimilarityCandidate{*hit}}, nil
		}
		candidates, err := s.sections.FindSimilarDocuments(
			ctx, tid, centroid(embeddings), settings.DuplicateThreshold, 5, category, subcategory, slug,
		)
		if err != nil {
			return nil, fmt.Errorf("similarity check: %w", err)
		}
		if len(candidates) > 0 {
			return &StoreResult{Status: "similar_exists", Candidates: candidates}, nil
		}
	}

	// A write stamps last_accessed_at on create and upsert so access-retention
	// never evicts a just-updated doc.
	now := time.Now()
	doc := &models.Document{
		TenantID:       tid,
		Category:       category,
		Subcategory:    subcategory,
		Slug:           slug,
		Title:          title,
		DocType:        docType,
		ContentHash:    contentHash,
		Pinned:         pin != nil && *pin,
		LastAccessedAt: &now,
		Scope:          scope,
	}

	recordHistory := s.historyEnabledFor(ctx, tid)
	var overwriteBefore *string
	var finalSections []models.Section
	created := false
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txDocs := repository.NewDocumentRepository(tx)
		txSections := repository.NewSectionRepository(tx)

		// 6. Resolve target (single-tenant + common pool; the tid guard rejects a
		// common-pool hit).
		existing, err := txDocs.GetByPath(ctx, repository.ReadTenants(tid), tid, category, subcategory, slug)
		if err == nil && existing.TenantID == tid {
			if recordHistory {
				overwriteBefore = docBeforeSnapshot(existing)
			}
			doc.ID = existing.ID
			doc.CreatedAt = existing.CreatedAt
			doc.DocType = existing.DocType // preserve an admin override; don't revert
			doc.Pinned = existing.Pinned
			if pin != nil {
				doc.Pinned = *pin
			}
			// scope: unset preserves the current value; set updates or clears.
			if scope == nil {
				doc.Scope = existing.Scope
			}
			if err := txDocs.Save(ctx, tid, doc); err != nil {
				return fmt.Errorf("save document: %w", err)
			}
			// 7. Write mode.
			finalSections, err = writeSections(ctx, txSections, policy.WriteMode, policy.Embed, existing.Sections, sectionModels, doc.ID)
			if err != nil {
				return err
			}
		} else {
			if err := txDocs.Create(ctx, doc); err != nil {
				return fmt.Errorf("create document: %w", err)
			}
			created = true
			for i := range sectionModels {
				sectionModels[i].DocumentID = doc.ID
			}
			if policy.Embed {
				err = txSections.CreateBatch(ctx, sectionModels)
			} else {
				err = txSections.CreateBatchNoEmbed(ctx, sectionModels)
			}
			if err != nil {
				return fmt.Errorf("create sections: %w", err)
			}
			finalSections = sectionModels
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	doc.Sections = finalSections

	// Lifecycle seeding: the document -> owning-tenant parent edge.
	s.seedTuple(ctx, authzseed.DocumentTenantEdge(doc.ID, tid))

	// 11. History and audit. Best-effort.
	if recordHistory {
		subj, email := s.actorFields(ctx)
		op, before := models.MutationOpCreate, (*string)(nil)
		if !created {
			op, before = models.MutationOpOverwrite, overwriteBefore
		}
		s.logMutation(ctx, repository.MutationEvent{
			TenantID:     tid,
			DocumentID:   doc.ID,
			DocumentPath: doc.Path(),
			OpType:       op,
			ActorSubject: subj,
			ActorEmail:   email,
			Before:       before,
		})
	}

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

	// 10. Post-write links — chain_previous (handoff, and any doc_type carrying it).
	var warnings []string
	if created && policy.ChainPrevious != nil {
		if w := s.autoChainPrevious(ctx, tid, doc, policy.ChainPrevious); w != "" {
			warnings = append(warnings, w)
		}
	}

	return &StoreResult{
		Status:   "ok",
		Document: doc,
		Path:     doc.Path(),
		Sections: len(doc.Sections),
		Warnings: warnings,
	}, nil
}

// autoChainPrevious best-effort links a new document to the prior latest of its
// doc_type in subcategory scope, via the rule's edge_type (new -> prior). Never
// fails the store: errors are logged and returned as a warning. Create-only.
func (s *MemoryService) autoChainPrevious(ctx context.Context, tid uuid.UUID, doc *models.Document, chain *models.ChainPrevious) string {
	if s.edges == nil || s.docs == nil {
		return ""
	}
	prior, err := s.docs.LatestByDocType(ctx, []uuid.UUID{tid}, doc.DocType, doc.Subcategory, &doc.ID)
	if err != nil {
		slog.Default().Warn("auto-chain lookup failed", "document_id", doc.ID, "error", err)
		return fmt.Sprintf("auto-chain skipped: prior lookup failed: %v", err)
	}
	if prior == nil {
		return ""
	}
	subj, _ := s.actorFields(ctx)
	edge := &models.Edge{
		TenantID:         tid,
		SourceDocumentID: doc.ID,
		TargetDocumentID: prior.ID,
		EdgeType:         chain.EdgeType,
		ActorSubject:     subj,
	}
	if _, _, err := s.edges.Create(ctx, edge); err != nil {
		slog.Default().Warn("auto-chain link failed", "source", doc.ID, "target", prior.ID, "error", err)
		return fmt.Sprintf("auto-chain link to %s failed: %v", prior.Path(), err)
	}
	return ""
}

// hashContent returns hex(sha256(content)) for the write-guard exact-dup check.
func hashContent(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// centroid returns the component-wise mean of embs (all same dim). Cosine ignores
// magnitude, so no renormalization. Zero-value Vector for empty input.
func centroid(embs []pgvector.Vector) pgvector.Vector {
	if len(embs) == 0 {
		return pgvector.Vector{}
	}
	sum := make([]float32, len(embs[0].Slice()))
	for _, e := range embs {
		for i, v := range e.Slice() {
			sum[i] += v
		}
	}
	n := float32(len(embs))
	for i := range sum {
		sum[i] /= n
	}
	return pgvector.NewVector(sum)
}

// UpdateSection partially updates a section: content!=nil re-embeds and sets
// content; heading!=nil sets heading (blank -> NULL). Both nil is a no-op.
func (s *MemoryService) UpdateSection(ctx context.Context, sectionID uuid.UUID, content *string, heading *string, overrideID *uuid.UUID) (*models.Section, error) {
	tid, err := s.resolveWriteScope(ctx, overrideID, authz.RelMember)
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

	// Snapshot prior state on the doc's OWNING tenant (a common-pool edit's home
	// tid differs) BEFORE overwriting; short-circuits when the toggle is off.
	ownerTID, docPath := tid, ""
	if section.Document != nil {
		ownerTID, docPath = section.Document.TenantID, section.Document.Path()
	}
	recordHistory := s.historyEnabledFor(ctx, ownerTID)
	var beforeSnap *string
	if recordHistory {
		beforeSnap = marshalBefore(updateSectionBefore{Content: section.Content, Heading: section.Heading})
	}

	// embed=false doc_types (prompts) must keep NULL embeddings, or an edit re-embeds
	// them and they leak into search / get_related across granted tenants.
	embed := true
	if section.Document != nil {
		embed = s.policyFor(section.Document.DocType).Embed
	}

	if content != nil {
		if embed {
			embedding, err := s.embedder.Embed(ctx, *content)
			if err != nil {
				return nil, fmt.Errorf("embed section: %w", err)
			}
			section.Embedding = embedding
		} else {
			section.Embedding = pgvector.Vector{}
		}
		section.Content = *content
	}

	if heading != nil {
		trimmed := strings.TrimSpace(*heading)
		if trimmed == "" {
			section.Heading = nil
		} else {
			section.Heading = &trimmed
		}
	}

	// embed=false: leave the stored embedding NULL — a full Save would write the
	// zero-value vector as an invalid '[]'.
	var updateErr error
	if embed {
		updateErr = s.sections.Update(ctx, section)
	} else {
		updateErr = s.sections.UpdateOmitEmbedding(ctx, section)
	}
	if updateErr != nil {
		return nil, fmt.Errorf("update section: %w", updateErr)
	}

	if recordHistory {
		subj, email := s.actorFields(ctx)
		sid := section.ID
		s.logMutation(ctx, repository.MutationEvent{
			TenantID:     ownerTID,
			DocumentID:   section.DocumentID,
			SectionID:    &sid,
			DocumentPath: docPath,
			OpType:       models.MutationOpUpdateSection,
			ActorSubject: subj,
			ActorEmail:   email,
			Before:       beforeSnap,
		})
	}

	// Updating is a liveness signal: keep the doc off the access-cold path.
	s.bumpAccessed(ctx, section.DocumentID)
	return section, nil
}

// UpdateDocumentTitle sets a document's title. Blank titles are rejected
// (Title is NOT NULL). Refuses common-pool docs for non-admins.
func (s *MemoryService) UpdateDocumentTitle(ctx context.Context, docID uuid.UUID, title string, overrideID *uuid.UUID) (*models.Document, error) {
	tid, err := s.resolveWriteScope(ctx, overrideID, authz.RelMember)
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

	// Snapshot the prior title BEFORE overwriting; short-circuits when off.
	recordHistory := s.historyEnabledFor(ctx, doc.TenantID)
	var beforeSnap *string
	if recordHistory {
		beforeSnap = marshalBefore(updateTitleBefore{Title: doc.Title})
	}

	doc.Title = title
	if err := s.docs.Save(ctx, doc.TenantID, doc); err != nil {
		return nil, fmt.Errorf("save document: %w", err)
	}

	if recordHistory {
		subj, email := s.actorFields(ctx)
		s.logMutation(ctx, repository.MutationEvent{
			TenantID:     doc.TenantID,
			DocumentID:   doc.ID,
			DocumentPath: doc.Path(),
			OpType:       models.MutationOpUpdateTitle,
			ActorSubject: subj,
			ActorEmail:   email,
			Before:       beforeSnap,
		})
	}
	// Updating is a liveness signal: keep the doc off the access-cold path.
	s.bumpAccessed(ctx, doc.ID)
	return doc, nil
}

// DeleteDocument removes a document and all its sections in a transaction.
// Explicitly deletes sections first (FK-safe order), does not rely on CASCADE.
func (s *MemoryService) DeleteDocument(ctx context.Context, category string, subcategory *string, slug string, overrideID *uuid.UUID) error {
	tid, err := s.resolveWriteScope(ctx, overrideID, authz.RelManager)
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
		// The fetch spans the common pool too, so a foreign (non-tid) doc still
		// needs manager+ on its OWNING tenant — never a mere member/editor.
		if doc.TenantID != tid && !s.CanManageTenant(ctx, doc.TenantID) {
			return fmt.Errorf("%w: manager grant required to delete document", apperr.ErrInvalidInput)
		}

		// Snapshot the whole doc (preloaded sections) BEFORE the rows are gone.
		recordHistory := s.historyEnabledFor(ctx, doc.TenantID)
		var beforeSnap *string
		if recordHistory {
			beforeSnap = docBeforeSnapshot(doc)
		}

		if err := txSections.DeleteByDocumentID(ctx, doc.ID); err != nil {
			return fmt.Errorf("delete sections: %w", err)
		}
		if err := txDocs.Delete(ctx, doc.TenantID, doc.ID); err != nil {
			return fmt.Errorf("delete document: %w", err)
		}

		if recordHistory {
			subj, email := s.actorFields(ctx)
			s.logMutationTx(ctx, tx, repository.MutationEvent{
				TenantID:     doc.TenantID,
				DocumentID:   doc.ID,
				DocumentPath: doc.Path(),
				OpType:       models.MutationOpDeleteDocument,
				ActorSubject: subj,
				ActorEmail:   email,
				Before:       beforeSnap,
			})
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
	tid, err := s.resolveWriteScope(ctx, overrideID, authz.RelManager)
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
	// The read scope spans other readable tenants, so a foreign (non-tid) doc
	// still needs manager+ on its OWNING tenant — never a mere member/editor.
	if doc.TenantID != tid && !s.CanManageTenant(ctx, doc.TenantID) {
		return fmt.Errorf("%w: manager grant required to delete document %s", apperr.ErrInvalidInput, id)
	}

	// Snapshot BEFORE the tx (doc + sections still preloaded); short-circuits when off.
	recordHistory := s.historyEnabledFor(ctx, doc.TenantID)
	var beforeSnap *string
	if recordHistory {
		beforeSnap = docBeforeSnapshot(doc)
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

		if recordHistory {
			subj, email := s.actorFields(ctx)
			s.logMutationTx(ctx, tx, repository.MutationEvent{
				TenantID:     doc.TenantID,
				DocumentID:   doc.ID,
				DocumentPath: doc.Path(),
				OpType:       models.MutationOpDeleteDocument,
				ActorSubject: subj,
				ActorEmail:   email,
				Before:       beforeSnap,
			})
		}
		return nil
	})
}

// DeleteSection removes one section by id, authorized exactly like UpdateSection.
// Deleting a document's last remaining section also deletes the now-empty parent
// document (same delete path as DeleteDocument). Writes no deletion_event.
func (s *MemoryService) DeleteSection(ctx context.Context, sectionID uuid.UUID, overrideID *uuid.UUID) error {
	tid, err := s.resolveWriteScope(ctx, overrideID, authz.RelManager)
	if err != nil {
		return err
	}

	section, err := s.sections.GetByID(ctx, tid, sectionID)
	if err != nil {
		return fmt.Errorf("get section: %w", err)
	}

	// The fetch spans the common pool too, so a foreign (non-tid) section still
	// needs manager+ on its doc's OWNING tenant — never a mere member/editor.
	if section.Document != nil && section.Document.TenantID != tid &&
		!s.CanManageTenant(ctx, section.Document.TenantID) {
		return fmt.Errorf("%w: manager grant required to delete section", apperr.ErrInvalidInput)
	}

	// The doc-delete filters by owning tenant; use the section's document tenant
	// (a granted manager's tid differs from a common-pool doc's tenant).
	docTenant := tid
	docPath := ""
	if section.Document != nil {
		docTenant = section.Document.TenantID
		docPath = section.Document.Path()
	}

	// Snapshot the removed section BEFORE the tx; short-circuits when off.
	recordHistory := s.historyEnabledFor(ctx, docTenant)
	var beforeSnap *string
	if recordHistory {
		beforeSnap = marshalBefore(deleteSectionBefore{Content: section.Content, Heading: section.Heading, Ordinal: section.Ordinal})
	}

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txDocs := repository.NewDocumentRepository(tx)
		txSections := repository.NewSectionRepository(tx)

		// Lock the parent doc row so concurrent section deletes on the same doc
		// serialize — otherwise both could see remaining==1 and skip the empty-doc
		// delete, orphaning a contentless document. No row (doc already gone) is fine.
		var locked int
		if err := tx.Raw("SELECT 1 FROM documents WHERE id = ? FOR UPDATE", section.DocumentID).
			Scan(&locked).Error; err != nil {
			return fmt.Errorf("lock document: %w", err)
		}

		rows, err := txSections.Delete(ctx, sectionID)
		if err != nil {
			return fmt.Errorf("delete section: %w", err)
		}
		if rows == 0 {
			return fmt.Errorf("%w: section %s", apperr.ErrNotFound, sectionID)
		}

		if recordHistory {
			subj, email := s.actorFields(ctx)
			sid := section.ID
			s.logMutationTx(ctx, tx, repository.MutationEvent{
				TenantID:     docTenant,
				DocumentID:   section.DocumentID,
				SectionID:    &sid,
				DocumentPath: docPath,
				OpType:       models.MutationOpDeleteSection,
				ActorSubject: subj,
				ActorEmail:   email,
				Before:       beforeSnap,
			})
		}

		remaining, err := txSections.CountByDocumentID(ctx, section.DocumentID)
		if err != nil {
			return fmt.Errorf("count sections: %w", err)
		}
		if remaining == 0 {
			if err := txDocs.Delete(ctx, docTenant, section.DocumentID); err != nil {
				return fmt.Errorf("delete empty document: %w", err)
			}
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
func (s *MemoryService) ListDocuments(ctx context.Context, category, subcategory *string, overrideID *uuid.UUID, opts ListOptions) ([]models.Document, error) {
	// category=prompts is home-only; an unfiltered list drops foreign prompts below (D4).
	scope, err := s.readScopeForPrompts(ctx, category, nil, overrideID)
	if err != nil {
		return nil, err
	}
	if len(scope) == 0 {
		return []models.Document{}, nil
	}
	docs, err := s.docs.List(ctx, scope, category, subcategory, opts.SlugPrefix, opts.OrderBy, opts.Order, opts.Limit, opts.Offset)
	if err != nil {
		return nil, err
	}
	docs = dropForeignPrompts(docs, auth.TenantIDFromContext(ctx))
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
		tenants[i].EffectivePolicy = tenants[i].EffectiveSelfServicePolicy(s.effectiveSelfServicePolicyDefault())
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
	mode := s.TenantDefaults.StalenessMode
	guard := s.TenantDefaults.DuplicateGuard
	scan := s.TenantDefaults.CleanupScanEnabled
	if s.globalCfg != nil {
		mode = s.globalCfg.StalenessDefault()
		guard = s.globalCfg.DuplicateGuardDefault()
		scan = s.globalCfg.CleanupScanDefault()
	}
	if _, ok := models.ValidStalenessModes[mode]; !ok {
		return
	}
	t.StalenessMode = mode
	t.DuplicateGuard = guard
	t.CleanupScanEnabled = scan
}

// CreateTenant provisions a tenant. tenantType (variadic, default shared) is a
// DISPLAY-ONLY classifier — validated + persisted but NEVER read by authz.
// ownerEmail provisions the owner tenant_users mapping for type=personal only.
func (s *MemoryService) CreateTenant(ctx context.Context, name, ownerEmail string, tenantType ...string) (*models.Tenant, error) {
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

	create := func(txSvc *MemoryService) (*models.Tenant, error) {
		tenant := &models.Tenant{Name: name, Type: t}
		txSvc.applyCreationDefaults(tenant)
		if err := txSvc.tenants.Create(ctx, tenant); err != nil {
			return nil, fmt.Errorf("create tenant: %w", err)
		}
		// Lifecycle seeding: system parent edge (enables global admins) + the
		// tenant's own service-principal membership.
		txSvc.seedTuple(ctx, authzseed.TenantSystemEdge(tenant.ID))
		txSvc.seedTuple(ctx, authzseed.TenantMember(tenant.ID, authz.ServicePrincipalID(tenant.ID.String())))
		// A personal tenant always carries its owner mapping (subject adopted on
		// the owner's first login), so it can never exist identity-less.
		if t == models.TenantTypePersonal && ownerEmail != "" {
			if _, err := txSvc.GrantTenantUser(ctx, ownerEmail, tenant.ID, models.TenantUserRoleOwner); err != nil {
				if isEmailUniqueViolation(err) {
					// Double-wrap: ErrInvalidInput so admin edges map it to 400, and err
					// so self-serve provisioning's isEmailUniqueViolation race recovery
					// still detects it.
					return nil, fmt.Errorf("%w: owner_email %q already belongs to another tenant: %w", apperr.ErrInvalidInput, ownerEmail, err)
				}
				return nil, err
			}
		}
		return tenant, nil
	}

	// Already inside a caller's transaction (Bootstrap / self-serve provisioning):
	// run inline so the tenant + owner row share that transaction's atomicity.
	if s.inTx {
		return create(s)
	}
	var tenant *models.Tenant
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var e error
		tenant, e = create(s.withTx(tx))
		return e
	})
	if err != nil {
		return nil, err
	}
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
	// Token gate (design D2). An in-process local-admin context is inherently
	// privileged and bypasses the token entirely. Every network caller (the HTTP
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
		// The founding tenant is the admin's own personal shelf (design D7); the
		// common `default` pool stays the shared public-read shelf. Owner mapping
		// comes from the AdminEmail grant below, so no ownerEmail is passed here.
		tenant, err := txSvc.CreateTenant(adminCtx, tenantName, "", models.TenantTypePersonal)
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
	Type               *string
	StalenessMode      *string
	DuplicateGuard     *bool
	DuplicateThreshold *float64
	// ClearDuplicateThreshold clears the per-tenant override to NULL (inherit the
	// global default); it wins over DuplicateThreshold when both are set.
	ClearDuplicateThreshold bool
	CleanupScanEnabled      *bool
	MetricsEnabled          *bool
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
	tenant.EffectivePolicy = tenant.EffectiveSelfServicePolicy(s.effectiveSelfServicePolicyDefault())
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
func (s *MemoryService) UpdateMyTenantSettings(ctx context.Context, stalenessMode *string, duplicateGuard *bool, duplicateThreshold *float64, clearDuplicateThreshold bool, cleanupScanEnabled *bool, metricsEnabled *bool) (*models.Tenant, error) {
	tid := auth.TenantIDFromContext(ctx)
	if tid == uuid.Nil {
		return nil, fmt.Errorf("%w: missing tenant ID in context", apperr.ErrInvalidInput)
	}
	// No fields = status read: return the current row without touching the DB or
	// audit log (avoids a "noop" override_log entry and an updated_at bump). A
	// read is always allowed regardless of the self-service policy.
	if stalenessMode == nil && duplicateGuard == nil && duplicateThreshold == nil && !clearDuplicateThreshold && cleanupScanEnabled == nil && metricsEnabled == nil {
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
		StalenessMode:           stalenessMode,
		DuplicateGuard:          duplicateGuard,
		DuplicateThreshold:      duplicateThreshold,
		ClearDuplicateThreshold: clearDuplicateThreshold,
		CleanupScanEnabled:      cleanupScanEnabled,
		MetricsEnabled:          metricsEnabled,
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
		Reason:       formatSettingsChange(stalenessMode, duplicateGuard, duplicateThreshold, clearDuplicateThreshold, cleanupScanEnabled, metricsEnabled),
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
func (s *MemoryService) UpdateTenantSettings(ctx context.Context, tenantID uuid.UUID, stalenessMode *string, duplicateGuard *bool, duplicateThreshold *float64, clearDuplicateThreshold bool, cleanupScanEnabled *bool, metricsEnabled *bool) (*models.Tenant, error) {
	tenant, err := s.tenants.GetByID(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	// No fields = settings read: gate on manage rights, populate the derived
	// effective policy, and return without touching the DB or the audit log.
	if stalenessMode == nil && duplicateGuard == nil && duplicateThreshold == nil && !clearDuplicateThreshold && cleanupScanEnabled == nil && metricsEnabled == nil {
		if !s.CanManageTenant(ctx, tenantID) {
			return nil, fmt.Errorf("%w: not authorized to view this tenant's settings", apperr.ErrInvalidInput)
		}
		tenant.EffectivePolicy = tenant.EffectiveSelfServicePolicy(s.effectiveSelfServicePolicyDefault())
		return tenant, nil
	}
	// Write: self-service gate at manager level (admin_only escalates to admin).
	if err := s.requireSelfService(ctx, tenant, authz.RelManager); err != nil {
		return nil, err
	}
	tenant, err = s.applyTenantPatch(ctx, tenantID, UpdateTenantFields{
		StalenessMode:           stalenessMode,
		DuplicateGuard:          duplicateGuard,
		DuplicateThreshold:      duplicateThreshold,
		ClearDuplicateThreshold: clearDuplicateThreshold,
		CleanupScanEnabled:      cleanupScanEnabled,
		MetricsEnabled:          metricsEnabled,
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
		Reason:       formatSettingsChange(stalenessMode, duplicateGuard, duplicateThreshold, clearDuplicateThreshold, cleanupScanEnabled, metricsEnabled),
	})
	tenant.EffectivePolicy = tenant.EffectiveSelfServicePolicy(s.effectiveSelfServicePolicyDefault())
	return tenant, nil
}

// formatSettingsChange renders the patch as a compact override_log.reason string.
func formatSettingsChange(stalenessMode *string, duplicateGuard *bool, duplicateThreshold *float64, clearDuplicateThreshold bool, cleanupScanEnabled *bool, metricsEnabled *bool) string {
	parts := make([]string, 0, 5)
	if stalenessMode != nil {
		parts = append(parts, "staleness_mode="+*stalenessMode)
	}
	if duplicateGuard != nil {
		parts = append(parts, fmt.Sprintf("duplicate_guard=%t", *duplicateGuard))
	}
	if clearDuplicateThreshold {
		parts = append(parts, "duplicate_threshold=inherit")
	} else if duplicateThreshold != nil {
		parts = append(parts, fmt.Sprintf("duplicate_threshold=%g", *duplicateThreshold))
	}
	if cleanupScanEnabled != nil {
		parts = append(parts, fmt.Sprintf("cleanup_scan_enabled=%t", *cleanupScanEnabled))
	}
	if metricsEnabled != nil {
		parts = append(parts, fmt.Sprintf("metrics_enabled=%t", *metricsEnabled))
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
	if fields.ClearDuplicateThreshold {
		tenant.DuplicateThreshold = nil // clear override -> inherit global default
	} else if fields.DuplicateThreshold != nil {
		v := *fields.DuplicateThreshold
		if v <= 0 || v > 1 {
			return nil, fmt.Errorf("%w: duplicate_threshold must be in (0, 1]", apperr.ErrInvalidInput)
		}
		tenant.DuplicateThreshold = &v
	}
	if fields.CleanupScanEnabled != nil {
		tenant.CleanupScanEnabled = *fields.CleanupScanEnabled
	}
	if fields.MetricsEnabled != nil {
		tenant.MetricsEnabled = *fields.MetricsEnabled
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

// EdgeResult is the outcome of CreateEdge: the created (or, on an idempotent
// re-create, the existing) edge and whether THIS call archived the target.
type EdgeResult struct {
	Edge           *models.Edge `json:"edge"`
	TargetArchived bool         `json:"target_archived"`
}

// CreateEdge records a directed typed edge from source to target. supersedes
// archives the target atomically with the insert (reason "superseded"); an
// idempotent re-create returns the existing edge and runs no second side effect.
func (s *MemoryService) CreateEdge(ctx context.Context, sourceID, targetID uuid.UUID, edgeType string, overrideID *uuid.UUID) (*EdgeResult, error) {
	if s.edges == nil || s.docs == nil {
		return nil, fmt.Errorf("%w: edges are not configured", apperr.ErrInvalidInput)
	}
	if _, ok := models.ValidEdgeTypes[edgeType]; !ok {
		return nil, fmt.Errorf("%w: unknown edge type %q", apperr.ErrInvalidInput, edgeType)
	}
	if sourceID == targetID {
		return nil, fmt.Errorf("%w: source and target must differ", apperr.ErrInvalidInput)
	}
	tid, err := s.resolveWriteScope(ctx, overrideID, authz.RelMember)
	if err != nil {
		return nil, err
	}
	// Resolve both endpoints WITHOUT the archived filter (a supersedes target may
	// already be archived), scoped to the write tenant + common pool; then require
	// both in one tenant (v1 same-tenant).
	scope := repository.ReadTenants(tid)
	source, err := s.docs.GetByIDIncludingArchived(ctx, scope, sourceID)
	if err != nil {
		return nil, err
	}
	target, err := s.docs.GetByIDIncludingArchived(ctx, scope, targetID)
	if err != nil {
		return nil, err
	}
	if source.TenantID != target.TenantID {
		return nil, fmt.Errorf("%w: cross-tenant edges are not allowed", apperr.ErrInvalidInput)
	}
	if !s.authorize(ctx, authz.TypeDocument, sourceID.String(), authz.RelEditor) {
		return nil, fmt.Errorf("%w: not authorized to add edges from document %s", apperr.ErrInvalidInput, sourceID)
	}
	targetRel := authz.RelViewer
	if edgeType == models.EdgeSupersedes {
		targetRel = authz.RelEditor
	}
	if !s.authorize(ctx, authz.TypeDocument, targetID.String(), targetRel) {
		return nil, fmt.Errorf("%w: not authorized on target document %s", apperr.ErrInvalidInput, targetID)
	}

	subj, _ := s.actorFields(ctx)
	edge := &models.Edge{
		TenantID:         source.TenantID,
		SourceDocumentID: sourceID,
		TargetDocumentID: targetID,
		EdgeType:         edgeType,
		ActorSubject:     subj,
	}

	var result EdgeResult
	if edgeType != models.EdgeSupersedes {
		created, _, cerr := s.edges.Create(ctx, edge)
		if cerr != nil {
			return nil, cerr
		}
		result.Edge = created
		return &result, nil
	}
	// supersedes: insert + target archive in ONE tx so a committed edge always
	// implies a committed archive. A conflict (edge already exists) skips the
	// archive; ArchiveByID's IS NULL guard no-ops an already-archived target.
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txEdges := repository.NewEdgeRepository(tx)
		txDocs := repository.NewDocumentRepository(tx)
		created, wasNew, cerr := txEdges.Create(ctx, edge)
		if cerr != nil {
			return cerr
		}
		result.Edge = created
		if wasNew {
			n, aerr := txDocs.ArchiveByID(ctx, targetID, models.ArchiveReasonSuperseded)
			if aerr != nil {
				return aerr
			}
			result.TargetArchived = n > 0
			// Purge the tombstone's content (sections + embeddings + FTS) in the same
			// tx as the archive; idempotent (deletes 0 when already purged).
			txSections := repository.NewSectionRepository(tx)
			if derr := txSections.DeleteByDocumentID(ctx, targetID); derr != nil {
				return derr
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// ListDocumentEdges returns a document's edges in both directions, gated by read
// access on the doc (same gate as GetRelated). Includes edges to archived
// endpoints so the supersede trail stays visible from the live source.
func (s *MemoryService) ListDocumentEdges(ctx context.Context, docID uuid.UUID, overrideID *uuid.UUID) ([]repository.EdgeListItem, error) {
	if s.edges == nil {
		return nil, fmt.Errorf("%w: edges are not configured", apperr.ErrInvalidInput)
	}
	scope, err := s.readScope(ctx, overrideID)
	if err != nil {
		return nil, err
	}
	if len(scope) == 0 {
		return nil, fmt.Errorf("%w: document %s", apperr.ErrNotFound, docID)
	}
	if !s.authorize(ctx, authz.TypeDocument, docID.String(), authz.RelViewer) {
		return nil, fmt.Errorf("%w: not authorized for document %s", apperr.ErrInvalidInput, docID)
	}
	// Restrict the OTHER endpoint to the caller's read scope so a doc-only guest
	// can't see a sibling's path/title in a tenant they cannot read.
	return s.edges.ListByDocument(ctx, docID, scope)
}

const (
	defaultResumeDepth = 10
	maxResumeDepth     = 100
)

// HandoffRef identifies one handoff in a resume chain (no section content).
type HandoffRef struct {
	ID       uuid.UUID `json:"id"`
	Path     string    `json:"path"`
	Title    string    `json:"title"`
	Archived bool      `json:"archived"`
}

// ResumeResult is the outcome of Resume: the latest handoff (full content) and
// the ordered prior-handoff chain it continues from (newest first, bounded).
type ResumeResult struct {
	Latest *models.Document `json:"latest"`
	Chain  []HandoffRef     `json:"chain,omitempty"`
}

// Resume returns the latest handoff for a project plus, when depth > 1, the
// ordered continues_from chain of prior handoffs (bounded). Read-scope gated:
// an empty scope or a project with no handoff yields an empty result, no error.
func (s *MemoryService) Resume(ctx context.Context, subcategory *string, overrideID *uuid.UUID, depth int) (ResumeResult, error) {
	if s.edges == nil || s.docs == nil {
		return ResumeResult{}, fmt.Errorf("%w: edges are not configured", apperr.ErrInvalidInput)
	}
	scope, err := s.readScope(ctx, overrideID)
	if err != nil {
		return ResumeResult{}, err
	}
	if len(scope) == 0 {
		return ResumeResult{}, nil // non-readable scope -> empty, no leak
	}
	latest, err := s.docs.LatestHandoff(ctx, scope, subcategory, subcategory == nil, nil)
	if err != nil {
		return ResumeResult{}, err
	}
	if latest == nil {
		return ResumeResult{}, nil // no handoff -> empty, not an error
	}
	result := ResumeResult{Latest: latest}
	if depth <= 0 {
		depth = defaultResumeDepth
	}
	if depth > maxResumeDepth {
		depth = maxResumeDepth
	}
	if depth > 1 {
		result.Chain = s.walkHandoffChain(ctx, latest.ID, depth-1, scope)
	}
	return result, nil
}

// walkHandoffChain follows continues_from outgoing edges from startID, newest to
// oldest, collecting up to maxRefs prior refs. The seen-set breaks any cycle so a
// corrupt chain bounds rather than loops.
func (s *MemoryService) walkHandoffChain(ctx context.Context, startID uuid.UUID, maxRefs int, scope []uuid.UUID) []HandoffRef {
	chain := make([]HandoffRef, 0, maxRefs)
	seen := map[uuid.UUID]bool{startID: true}
	current := startID
	for len(chain) < maxRefs {
		edges, err := s.edges.ListByDocument(ctx, current, scope)
		if err != nil {
			slog.Default().Warn("resume chain walk failed", "document_id", current, "error", err)
			break
		}
		prior, ok := priorHandoffEdge(edges)
		if !ok || seen[prior.OtherDocumentID] {
			break
		}
		chain = append(chain, HandoffRef{
			ID:       prior.OtherDocumentID,
			Path:     prior.OtherDocumentPath,
			Title:    prior.OtherDocumentTitle,
			Archived: prior.OtherDocumentArchived,
		})
		seen[prior.OtherDocumentID] = true
		current = prior.OtherDocumentID
	}
	return chain
}

// priorHandoffEdge returns the outgoing continues_from edge (the prior handoff).
func priorHandoffEdge(edges []repository.EdgeListItem) (repository.EdgeListItem, bool) {
	for _, e := range edges {
		if e.EdgeType == models.EdgeContinuesFrom && e.Direction == "outgoing" {
			return e, true
		}
	}
	return repository.EdgeListItem{}, false
}

// DeleteEdge removes an edge, gated by editor on its source doc under the member
// write floor. Deleting a supersedes edge does NOT un-archive its former target.
func (s *MemoryService) DeleteEdge(ctx context.Context, edgeID uuid.UUID, overrideID *uuid.UUID) error {
	if s.edges == nil {
		return fmt.Errorf("%w: edges are not configured", apperr.ErrInvalidInput)
	}
	if _, err := s.resolveWriteScope(ctx, overrideID, authz.RelMember); err != nil {
		return err
	}
	edge, err := s.edges.GetByID(ctx, edgeID)
	if err != nil {
		return err
	}
	if !s.authorize(ctx, authz.TypeDocument, edge.SourceDocumentID.String(), authz.RelEditor) {
		return fmt.Errorf("%w: not authorized to delete edge %s", apperr.ErrInvalidInput, edgeID)
	}
	n, err := s.edges.Delete(ctx, edgeID)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w: edge %s", apperr.ErrNotFound, edgeID)
	}
	return nil
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

	// Rule-driven exclusions: lint stale-check skips lint_stale_check=false types,
	// the near-duplicate check skips cleanup_scan=false types (design D8, task 6.4).
	staleExcl := s.policyDocTypes(func(p models.EffectivePolicy) bool { return !p.LintStaleCheck })
	dupExcl := s.policyDocTypes(func(p models.EffectivePolicy) bool { return !p.CleanupScan })

	type lintCheck struct {
		name string
		fn   func(context.Context, uuid.UUID, repository.LintThresholds) ([]repository.LintFinding, error)
	}
	allChecks := []lintCheck{
		{"stale", func(ctx context.Context, tid uuid.UUID, th repository.LintThresholds) ([]repository.LintFinding, error) {
			return s.lint.CheckStale(ctx, tid, th, staleExcl)
		}},
		{"sparse", s.lint.CheckSparse},
		{"near_duplicate", func(ctx context.Context, tid uuid.UUID, th repository.LintThresholds) ([]repository.LintFinding, error) {
			return s.lint.CheckNearDuplicates(ctx, tid, th, dupExcl)
		}},
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
	// Instance-wide policy findings (unknown rule keys, all-maintenance-off types).
	if _, ok := requested["policy"]; len(checks) == 0 || ok {
		findings = append(findings, s.policyLintFindings()...)
	}
	// Retention dry-run: preview would-be-evicted docs; never deletes, toggle-independent.
	if _, ok := requested["retention"]; len(checks) == 0 || ok {
		grace := defaultRetentionGraceDays
		if s.globalCfg != nil {
			grace = s.globalCfg.RetentionGraceDays()
		}
		cutoffs := repository.BuildRetentionCutoffs(s.policyAll(), grace)
		retFindings, err := repository.NewRetentionRepository(s.db).CandidateFindings(ctx, tid, cutoffs)
		if err != nil {
			return nil, err
		}
		findings = append(findings, retFindings...)
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
