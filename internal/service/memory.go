package service

import (
	"context"
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

// logOverride attempts to persist an audit row. If the write fails, it logs a
// warning and continues — the user's primary operation already succeeded, and
// dropping an audit row beats failing their request. The warning makes the
// drop visible so operators can alert on it.
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
	// authz is the relationship-tuple store. Lifecycle operations seed tuples
	// into it out-of-band. Nil disables seeding (e.g. tests that don't
	// exercise tuples).
	authz authz.Store
	// authzEngine evaluates authorization decisions over the tuple store: it is
	// now the single authoritative gate for admin, tenant_id override, and
	// cross-tenant/common-pool point access. Nil (when authzStore is nil) makes
	// every Check fail closed.
	authzEngine *authz.Engine
}

// NewMemoryService constructs the service. Optional deps (tenants, keys, lint,
// thresholds, overrides, authzStore) may be nil when the service is used
// outside the MCP server (e.g. the import CLI) — nil deps disable the features
// that need them. A nil authzStore disables tuple seeding AND makes every
// authorization Check fail closed.
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

// seedTuple writes a relation tuple out-of-band for a lifecycle event. Writes
// are idempotent. A nil store or a write error is non-fatal: the user's
// primary operation already succeeded, and in Pass 1 no decision depends on the
// tuple. The warning makes a drop visible so operators can alert on it.
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

// unseedTuple removes a relation tuple for a lifecycle event (user revoke, role
// downgrade). Like seedTuple it is nil-safe and best-effort: the primary DB
// operation is the source of truth; a delete miss is logged, not fatal.
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

// authorize reports whether the request's subject holds relation on
// objType:objID, per the tuple Check evaluator. It fails closed on every
// uncertainty: a nil engine (unwired), a subjectless request, or a Check error
// all deny. This is the single choke point every authorization decision in the
// service flows through.
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
// admin-gated: only a global admin (system:memory#admin) may target another
// tenant.
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

// isAdmin reports whether the request's subject is a global admin
// (system:memory#admin), resolved through the tuple Check — the mutable
// tenant.Email column has no bearing on this decision.
func (s *MemoryService) isAdmin(ctx context.Context) bool {
	// The offline memory-admin CLI is inherently privileged (direct DB access)
	// and carries no authenticated subject; it marks its context local-admin so
	// it can reuse these lifecycle methods. Network paths never set this.
	if auth.IsLocalAdmin(ctx) {
		return true
	}
	return s.authorize(ctx, authz.TypeSystem, authz.SystemObjectID, authz.RelAdmin)
}

// tenantSettings loads the per-tenant feature toggles. When the tenants repo
// is not wired (e.g. import CLI) it returns safe defaults: staleness off,
// duplicate guard off. The common/bootstrap pool is treated as the fetching
// tenant's own, so settings are always the requester's.
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
		// Fail safe: if we can't read tenant config, act as if staleness is off
		// so we never accidentally refuse content due to an infra glitch.
		return tenantSettings{StalenessMode: models.StalenessModeOff}
	}
	mode := t.StalenessMode
	if _, ok := models.ValidStalenessModes[mode]; !ok {
		// Fail safe: an unrecognised value (data corruption, bad migration) flips
		// to "off" rather than "advisory" — same rationale as the surrounding
		// branches: never refuse content because of an infra glitch.
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

// GetDocument fetches a document with all sections by path, applying staleness
// filter to each section. forceRead + reason override the filter and log to
// override_log.
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

// GetDocumentByID fetches a document by its UUID. Mirrors GetDocument's
// behaviour (staleness filter + force_read audit) but addresses by ID
// instead of path. Needed when a caller has a document UUID (e.g. a row
// from cleanup_queue referencing doc_a_id / doc_b_id) and must access the
// shadow doc at a path where another doc also lives — path-keyed
// GetDocument can only return one of them.
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

// MarkVerified stamps verified_at = NOW() on a section. Agents call this after
// confirming a claim against current source. Routed through an editor Check on
// the section's parent document (finding #8): without it, any caller could
// stamp verified_at on a shared common-pool section it has no write right to.
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

// StoreResult is the outcome of StoreDocument. When Status is "similar_exists"
// the candidates list describes existing docs that collided; Document is nil
// and the save did not happen. On success Status is "ok".
type StoreResult struct {
	Status     string                           `json:"status"`
	Document   *models.Document                 `json:"document,omitempty"`
	Path       string                           `json:"path,omitempty"`
	Sections   int                              `json:"sections,omitempty"`
	Candidates []repository.SimilarityCandidate `json:"candidates,omitempty"`
}

// StoreDocument parses markdown content into sections, embeds them, runs a
// duplicate-guard check against existing docs in the same tenant, and stores.
// Embeddings are generated before any DB mutations to avoid partial state on
// failure. When the guard trips and force is false, no write happens and the
// result carries candidates instead.
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

	// Generate all embeddings BEFORE touching the database
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

	// Duplicate-guard: compare new section embeddings against existing docs
	// in the same tenant (excluding the target path so re-saves of the same
	// doc never trip the guard). Only runs when enabled per-tenant AND not forcing.
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
			// Update existing: delete sections first, then update document.
			// Preserve the existing doc_type — admin may have changed it from
			// the inferred default, and an update shouldn't silently revert.
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

		// Set document ID on all sections and batch insert
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

// UpdateSection partially updates a section: re-embeds and sets content when
// content != nil; sets the heading when heading != nil (blank/whitespace ->
// NULL). At least one of content/heading should be non-nil; both nil is a
// no-op that returns the section unchanged.
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

// GrantTenantUser maps a verified email to a tenant with a role, creating the
// tenant_users row and seeding its membership tuples (+ admin when role ==
// admin). Admin-gated, matching the other tenant-lifecycle operations. This is
// the lifecycle seam for user grants; no in-band tool writes tuples.
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

// UpdateTenantUserRole changes a user's role and syncs the admin tuple: granting
// admin writes tenant:<T>#admin; downgrading to member deletes it (the member
// tuple is untouched — admin is a superset). Admin-gated. Email is the key
// (globally unique).
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

// UpdateMyTenantSettings lets any authenticated caller adjust the feature
// toggles on their own tenant (staleness mode, duplicate guard, cleanup scan).
// Name/email stay off limits to non-admins. Every call is audited to
// override_log so a compromised API key silencing enforcement leaves a trail.
func (s *MemoryService) UpdateMyTenantSettings(ctx context.Context, stalenessMode *string, duplicateGuard *bool, cleanupScanEnabled *bool) (*models.Tenant, error) {
	tid := auth.TenantIDFromContext(ctx)
	if tid == uuid.Nil {
		return nil, fmt.Errorf("%w: missing tenant ID in context", apperr.ErrInvalidInput)
	}
	// Treat a call with no fields as a status read: return the current row
	// without touching the DB or the audit log. Callers (including UI / CLI
	// probes) use this shape to inspect current settings without leaving a
	// "noop" override_log entry or bumping updated_at.
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

// formatSettingsChange renders the requested patch as a compact string for
// the override_log.reason column.
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

// CreateAPIKey mints a key for a tenant. subjectID pins the key to a specific
// unified authorization subject; nil/empty makes it the tenant service
// principal ("svc:<tenant_id>"). Either way the key's subject is granted
// membership on the tenant (idempotent — svc membership is already seeded at
// tenant create).
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

// RotateAPIKey issues a replacement key for the same tenant/label/subject as an
// existing key and retires the predecessor. With grace == 0 the old key is
// revoked immediately; with grace > 0 the old key's expiry is set to now+grace
// so a client can swap without downtime. Returns the new plaintext exactly once.
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

// GetRelated returns documents semantically related to the target. Routed
// through a viewer Check on the caller-supplied target document (finding #9,
// IDOR): without it, a caller could probe another tenant's documents for
// related content. Read-level (viewer) is deliberate — it still denies
// cross-tenant private targets but allows relating over world-readable
// common-pool documents.
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

// parseMarkdown splits markdown by ## headings into sections.
// Returns the title (from # heading) and sections.
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

	// If no sections were created (no ## headings), put everything in one section
	if len(sections) == 0 && strings.TrimSpace(content) != "" {
		sections = append(sections, parsedSection{
			heading: nil,
			content: strings.TrimSpace(content),
		})
	}

	return title, sections
}
