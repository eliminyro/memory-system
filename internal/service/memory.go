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
	// authz is the relationship-tuple store; lifecycle ops seed it out-of-band.
	// Nil disables seeding.
	authz authz.Store
	// authzEngine is the single authoritative gate for admin, tenant_id override,
	// and cross-tenant/common-pool access. Nil (no authzStore) fails every Check closed.
	authzEngine *authz.Engine
	// BootstrapToken is the configured BOOTSTRAP_TOKEN the first-run Bootstrap path
	// compares the caller's token against (constant-time). Set once at construction
	// in cmd/server/main.go from config; empty means the instance refuses to bootstrap.
	BootstrapToken string
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
	authzStore authz.Store,
) *MemoryService {
	var engine *authz.Engine
	if authzStore != nil {
		engine = authz.NewEngine(authzStore)
	}
	return &MemoryService{
		db:          db,
		docs:        docs,
		sections:    sections,
		embedder:    embedder,
		tenants:     tenants,
		keys:        keys,
		lint:        lint,
		thresholds:  thresholds,
		overrides:   overrides,
		cleanup:     cleanup,
		authz:       authzStore,
		authzEngine: engine,
	}
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

// unseedTuple removes a relation tuple for a lifecycle event (revoke, downgrade).
// Nil-safe and best-effort like seedTuple; a delete miss is logged, not fatal.
func (s *MemoryService) unseedTuple(ctx context.Context, t authz.Tuple) {
	if s.authz == nil {
		return
	}
	if err := s.authz.Delete(ctx, t); err != nil {
		slog.Default().Warn("authz tuple unseed failed",
			"object_type", t.ObjectType,
			"object_id", t.ObjectID,
			"relation", t.Relation,
			"subject_type", t.SubjectType,
			"subject_id", t.SubjectID,
			"error", err,
		)
	}
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

// Search performs hybrid semantic + keyword search, applying staleness filter.
// When forceRead is true, Reason is required and the override is audited.
func (s *MemoryService) Search(ctx context.Context, query string, category, subcategory *string, limit int, forceRead bool, reason string, overrideID *uuid.UUID) ([]repository.SearchResult, error) {
	if forceRead && strings.TrimSpace(reason) == "" {
		return nil, fmt.Errorf("%w: reason is required when force_read=true", apperr.ErrInvalidInput)
	}
	tid, err := s.resolveTenant(ctx, overrideID)
	if err != nil {
		return nil, err
	}
	embedding, err := s.embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	results, err := s.sections.HybridSearch(ctx, repository.SearchParams{
		TenantID:    tid,
		Embedding:   embedding,
		Query:       query,
		Category:    category,
		Subcategory: subcategory,
		Limit:       limit,
	})
	if err != nil {
		return nil, err
	}
	settings := s.tenantSettings(ctx, tid)
	if s.thresholds != nil {
		results, err = applyStalenessToSearchResults(ctx, s.thresholds, results, settings.StalenessMode, forceRead)
		if err != nil {
			return nil, err
		}
	}
	if forceRead {
		s.logOverride(ctx, repository.OverrideEvent{
			TenantID:     tid,
			Tool:         models.OverrideToolSearchMemory,
			OverrideType: models.OverrideTypeForceRead,
			Reason:       reason,
		})
	}
	return results, nil
}

// GetDocument fetches a document with all sections by path, applying the
// staleness filter. forceRead + reason override it and audit to override_log.
func (s *MemoryService) GetDocument(ctx context.Context, category string, subcategory *string, slug string, forceRead bool, reason string, overrideID *uuid.UUID) (*DocumentView, error) {
	if forceRead && strings.TrimSpace(reason) == "" {
		return nil, fmt.Errorf("%w: reason is required when force_read=true", apperr.ErrInvalidInput)
	}
	tid, err := s.resolveTenant(ctx, overrideID)
	if err != nil {
		return nil, err
	}
	doc, err := s.docs.GetByPath(ctx, tid, category, subcategory, slug)
	if err != nil {
		return nil, err
	}
	settings := s.tenantSettings(ctx, tid)
	view, err := buildDocumentView(ctx, s.thresholds, doc, settings.StalenessMode, forceRead)
	if err != nil {
		return nil, err
	}
	if forceRead {
		docID := doc.ID
		s.logOverride(ctx, repository.OverrideEvent{
			TenantID:     tid,
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
	tid, err := s.resolveTenant(ctx, overrideID)
	if err != nil {
		return nil, err
	}
	doc, err := s.docs.GetByID(ctx, tid, id)
	if err != nil {
		return nil, err
	}
	settings := s.tenantSettings(ctx, tid)
	view, err := buildDocumentView(ctx, s.thresholds, doc, settings.StalenessMode, forceRead)
	if err != nil {
		return nil, err
	}
	if forceRead {
		docID := doc.ID
		s.logOverride(ctx, repository.OverrideEvent{
			TenantID:     tid,
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

	// Embed everything before touching the DB (no partial state on failure)
	sectionModels := make([]models.Section, len(sections))
	embeddings := make([]pgvector.Vector, len(sections))
	for i, sec := range sections {
		embedding, err := s.embedder.Embed(ctx, sec.content)
		if err != nil {
			return nil, fmt.Errorf("embed section %d: %w", i, err)
		}
		embeddings[i] = embedding
		sectionModels[i] = models.Section{
			Ordinal:   i,
			Heading:   sec.heading,
			Content:   sec.content,
			Embedding: embedding,
		}
	}

	// Duplicate guard: compare new embeddings against same-tenant docs, excluding
	// the target path so re-saves don't trip. Only when enabled per-tenant and not forcing.
	settings := s.tenantSettings(ctx, tid)
	if settings.DuplicateGuard && !force && len(embeddings) > 0 {
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
		DocType:     models.InferDocType(category, subcategory, slug),
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txDocs := repository.NewDocumentRepository(tx)
		txSections := repository.NewSectionRepository(tx)

		// Check for existing document (only in this tenant, not common pool)
		existing, err := txDocs.GetByPath(ctx, tid, category, subcategory, slug)
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

	doc, err := s.docs.GetByID(ctx, tid, docID)
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

		doc, err := txDocs.GetByPath(ctx, tid, category, subcategory, slug)
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

// ListDocuments lists documents, optionally filtered.
func (s *MemoryService) ListDocuments(ctx context.Context, category, subcategory *string, overrideID *uuid.UUID) ([]models.Document, error) {
	tid, err := s.resolveTenant(ctx, overrideID)
	if err != nil {
		return nil, err
	}
	return s.docs.List(ctx, tid, category, subcategory)
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
	return s.tenants.List(ctx)
}

func (s *MemoryService) CreateTenant(ctx context.Context, name, email string) (*models.Tenant, error) {
	if err := s.requireAdmin(ctx); err != nil {
		return nil, err
	}
	tenant := &models.Tenant{Name: name, Email: email}
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
	// ErrBootstrapForbidden is returned when the caller token is empty, the
	// configured BOOTSTRAP_TOKEN is empty, or the two do not match. The instance
	// fails closed and never provisions. Front-ends map this to HTTP 403.
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
	clone.tenants = repository.NewTenantRepository(tx)
	clone.keys = repository.NewAPIKeyRepository(tx)
	txStore := authz.NewPostgresStore(tx)
	clone.authz = txStore
	clone.authzEngine = authz.NewEngine(txStore)
	return &clone
}

// Bootstrap performs token-gated, one-shot first-run provisioning. It verifies the
// caller token against the configured BOOTSTRAP_TOKEN in constant time (failing
// closed when either is empty), then — inside ONE transaction guarded by a Postgres
// advisory lock so concurrent callers yield exactly one admin — confirms no admin
// exists yet and provisions the first tenant + admin API key, seeding the
// system:memory#admin tuple via authzseed. The plaintext key is returned exactly
// once and is deliberately never logged.
func (s *MemoryService) Bootstrap(ctx context.Context, token string, spec BootstrapSpec) (string, *models.APIKey, error) {
	// Fail closed: refuse if either side of the compare is empty (design D4). This
	// runs before any DB access so an un-configured or un-supplied token can never
	// provision.
	configured := s.BootstrapToken
	if token == "" || configured == "" {
		return "", nil, ErrBootstrapForbidden
	}
	// Constant-time compare (never ==) to avoid leaking the token via a timing oracle.
	if subtle.ConstantTimeCompare([]byte(token), []byte(configured)) != 1 {
		return "", nil, ErrBootstrapForbidden
	}
	// Provisioning needs the tuple store to record admin-ness; without it we cannot
	// mint a valid admin, so fail rather than issue a key that is not an admin.
	if s.authz == nil {
		return "", nil, fmt.Errorf("bootstrap: authz store not configured")
	}

	tenantName := spec.TenantName
	if tenantName == "" {
		tenantName = "admin"
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
		tenant, err := txSvc.CreateTenant(adminCtx, tenantName, spec.TenantEmail)
		if err != nil {
			return fmt.Errorf("bootstrap: create tenant: %w", err)
		}
		pt, k, err := txSvc.CreateAPIKey(adminCtx, tenant.ID, keyLabel, nil, nil)
		if err != nil {
			return fmt.Errorf("bootstrap: create admin key: %w", err)
		}
		// Seed system:memory#admin for the key's subject via authzseed. Unlike the
		// best-effort seedTuple used for tenant edges, a failure here must roll the
		// transaction back: we never commit an "admin" key that lacks the admin tuple.
		if err := txSvc.authz.Write(ctx, authzseed.SystemAdmin(authzseed.APIKeySubjectID(*k))); err != nil {
			return fmt.Errorf("bootstrap: seed system admin: %w", err)
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
// (still gated by BOOTSTRAP_TOKEN).
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

// GrantTenantUser maps a verified email to a tenant+role, creating the
// tenant_users row and seeding membership tuples (+ admin when role==admin).
// Admin-gated; the lifecycle seam for user grants (no in-band tool writes tuples).
func (s *MemoryService) GrantTenantUser(ctx context.Context, email string, tenantID uuid.UUID, role string) (*models.TenantUser, error) {
	if err := s.requireAdmin(ctx); err != nil {
		return nil, err
	}
	if role == "" {
		role = models.TenantUserRoleMember
	}
	if _, ok := models.ValidTenantUserRoles[role]; !ok {
		return nil, fmt.Errorf("%w: role must be member or admin", apperr.ErrInvalidInput)
	}
	if _, err := s.tenants.GetByID(ctx, tenantID); err != nil {
		return nil, err
	}
	tu := &models.TenantUser{Email: email, TenantID: tenantID, Role: role}
	if err := s.db.WithContext(ctx).Create(tu).Error; err != nil {
		return nil, fmt.Errorf("create tenant_user: %w", err)
	}
	s.seedTuple(ctx, authzseed.TenantMember(tenantID, tu.ID.String()))
	if role == models.TenantUserRoleAdmin {
		s.seedTuple(ctx, authzseed.TenantAdmin(tenantID, tu.ID.String()))
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

// UpdateTenantUserRole changes a role and syncs the admin tuple: grant writes
// tenant:<T>#admin, downgrade deletes it (member tuple untouched — admin is a
// superset). Admin-gated; email is the unique key.
func (s *MemoryService) UpdateTenantUserRole(ctx context.Context, email, role string) (*models.TenantUser, error) {
	if err := s.requireAdmin(ctx); err != nil {
		return nil, err
	}
	if _, ok := models.ValidTenantUserRoles[role]; !ok {
		return nil, fmt.Errorf("%w: role must be member or admin", apperr.ErrInvalidInput)
	}
	var tu models.TenantUser
	if err := s.db.WithContext(ctx).Where("email = ?", email).First(&tu).Error; err != nil {
		return nil, fmt.Errorf("%w: no user mapping for %s", apperr.ErrNotFound, email)
	}
	if tu.Role == role {
		return &tu, nil // idempotent
	}
	if err := s.db.WithContext(ctx).Model(&tu).Update("role", role).Error; err != nil {
		return nil, fmt.Errorf("update tenant_user role: %w", err)
	}
	adminTuple := authzseed.TenantAdmin(tu.TenantID, tu.ID.String())
	if role == models.TenantUserRoleAdmin {
		s.seedTuple(ctx, adminTuple)
	} else {
		s.unseedTuple(ctx, adminTuple)
	}
	tu.Role = role
	return &tu, nil
}

// RevokeTenantUser removes a user's email->tenant mapping and its membership
// tuples (member, and admin when applicable). Admin-gated. Email is the key.
func (s *MemoryService) RevokeTenantUser(ctx context.Context, email string) error {
	if err := s.requireAdmin(ctx); err != nil {
		return err
	}
	var tu models.TenantUser
	if err := s.db.WithContext(ctx).Where("email = ?", email).First(&tu).Error; err != nil {
		return fmt.Errorf("%w: no user mapping for %s", apperr.ErrNotFound, email)
	}
	if err := s.db.WithContext(ctx).Delete(&tu).Error; err != nil {
		return fmt.Errorf("delete tenant_user: %w", err)
	}
	s.unseedTuple(ctx, authzseed.TenantMember(tu.TenantID, tu.ID.String()))
	if tu.Role == models.TenantUserRoleAdmin {
		s.unseedTuple(ctx, authzseed.TenantAdmin(tu.TenantID, tu.ID.String()))
	}
	return nil
}

// UpdateTenantFields bundles the optional patches admin/self tools may apply.
// Any nil pointer leaves the corresponding column unchanged.
type UpdateTenantFields struct {
	Name               *string
	Email              *string
	StalenessMode      *string
	DuplicateGuard     *bool
	CleanupScanEnabled *bool
}

// UpdateTenant is the admin-only patcher. It can touch any field.
func (s *MemoryService) UpdateTenant(ctx context.Context, id uuid.UUID, fields UpdateTenantFields) (*models.Tenant, error) {
	if err := s.requireAdmin(ctx); err != nil {
		return nil, err
	}
	return s.applyTenantPatch(ctx, id, fields)
}

// UpdateMyTenantSettings lets any authenticated caller adjust their own tenant's
// toggles (staleness, duplicate guard, cleanup scan); name/email stay admin-only.
// Every call is audited to override_log (compromised-key trail).
func (s *MemoryService) UpdateMyTenantSettings(ctx context.Context, stalenessMode *string, duplicateGuard *bool, cleanupScanEnabled *bool) (*models.Tenant, error) {
	tid := auth.TenantIDFromContext(ctx)
	if tid == uuid.Nil {
		return nil, fmt.Errorf("%w: missing tenant ID in context", apperr.ErrInvalidInput)
	}
	// No fields = status read: return the current row without touching the DB or
	// audit log (avoids a "noop" override_log entry and an updated_at bump).
	if stalenessMode == nil && duplicateGuard == nil && cleanupScanEnabled == nil {
		return s.tenants.GetByID(ctx, tid)
	}
	tenant, err := s.applyTenantPatch(ctx, tid, UpdateTenantFields{
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
	if err := s.requireAdmin(ctx); err != nil {
		return "", nil, err
	}
	if _, err := s.tenants.GetByID(ctx, tenantID); err != nil {
		return "", nil, err
	}
	if subjectID != nil && *subjectID == "" {
		subjectID = nil
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
	// Issue the replacement first so a failure leaves the old key intact.
	plaintext, newKey, err := s.CreateAPIKey(ctx, old.TenantID, old.Label, old.SubjectID, nil)
	if err != nil {
		return "", nil, err
	}
	if grace > 0 {
		expiry := time.Now().Add(grace)
		if err := s.keys.SetExpiry(ctx, old.ID, &expiry); err != nil {
			return "", nil, fmt.Errorf("set grace expiry on old key: %w", err)
		}
	} else if err := s.keys.Revoke(ctx, old.ID); err != nil {
		return "", nil, fmt.Errorf("revoke old key: %w", err)
	}
	return plaintext, newKey, nil
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

func (s *MemoryService) GenerateIndex(ctx context.Context, depth string, category *string, overrideID *uuid.UUID) ([]repository.IndexEntry, error) {
	tid, err := s.resolveTenant(ctx, overrideID)
	if err != nil {
		return nil, err
	}
	return s.docs.GenerateIndex(ctx, tid, repository.IndexDepth(depth), category)
}

// GetRelated returns documents semantically related to the target. Viewer Check
// on the caller-supplied target (finding #9, IDOR) blocks probing another tenant's
// docs; viewer-level is deliberate — denies private cross-tenant targets but
// allows relating over world-readable common-pool docs.
func (s *MemoryService) GetRelated(ctx context.Context, documentID uuid.UUID, limit int, overrideID *uuid.UUID) ([]repository.RelatedResult, error) {
	tid, err := s.resolveTenant(ctx, overrideID)
	if err != nil {
		return nil, err
	}
	if !s.authorize(ctx, authz.TypeDocument, documentID.String(), authz.RelViewer) {
		return nil, fmt.Errorf("%w: not authorized for document %s", apperr.ErrInvalidInput, documentID)
	}
	return s.sections.GetRelated(ctx, tid, documentID, limit)
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
