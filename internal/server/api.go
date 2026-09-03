package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/eliminyro/memory-system/internal/auth"
	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/service"
)

// apiHandler backs the JSON API. Every handler runs behind bearer + UserContextBridge,
// so write handlers pass overrideID = nil and let the service resolve the tenant from
// context. Read handlers additionally honor an optional ?tenant_id=<uuid> filter
// (absent = aggregate across the caller's readable tenants; set = scope to that tenant).
type apiHandler struct {
	memory *service.MemoryService

	// importJobs + maxUploadBytes back the admin import surface (/api/admin/import),
	// threaded down to adminAPIHandler in mux().
	importJobs     importJobStore
	maxUploadBytes int64

	// instanceConfig + globalCfg back the admin global-config surface
	// (/api/admin/config), threaded down to adminAPIHandler in mux();
	// setLogLevel applies a log_level PATCH to the running LevelVar.
	instanceConfig instanceConfigStore
	globalCfg      configRefresher
	setLogLevel    func(level string)
}

// mux builds the /api router; routes omit the /api prefix (caller strips it).
func (h *apiHandler) mux() *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc("GET /index", h.getIndex)
	m.HandleFunc("GET /search", h.getSearch)
	m.HandleFunc("GET /documents", h.listDocuments)
	m.HandleFunc("POST /documents", h.createDocument)
	m.HandleFunc("PUT /sections", h.putSection)
	m.HandleFunc("GET /documents/{id}", h.getDocument)
	m.HandleFunc("GET /documents/{id}/history", h.getDocumentHistory)
	m.HandleFunc("POST /documents/{id}/edges", h.createEdge)
	m.HandleFunc("GET /documents/{id}/edges", h.listDocumentEdges)
	m.HandleFunc("DELETE /documents/{id}/edges/{edgeId}", h.deleteEdge)
	m.HandleFunc("GET /resume", h.resume)
	m.HandleFunc("PATCH /sections/{id}", h.patchSection)
	m.HandleFunc("PATCH /documents/{id}", h.patchDocument)
	m.HandleFunc("POST /sections/{id}/verify", h.verifySection)
	m.HandleFunc("DELETE /documents/{id}", h.deleteDocument)
	m.HandleFunc("DELETE /sections/{id}", h.deleteSection)

	// GET /tenants/writable backs the delegated-manager UI probe (design.md
	// §7/§9): every tenant for a system admin, else the tenants the caller
	// manages. Not adminOnly — a manager who isn't a system admin needs this.
	m.HandleFunc("GET /tenants/writable", h.listWritableTenants)

	// GET /tenants?type=<t>&q=<filter> lists/searches tenants by display type
	// (design.md §5), reusing the WritableTenants authz shape: all tenants of
	// that type for a system admin, else only the tenants the caller manages.
	// Not adminOnly — delegated managers use it too.
	m.HandleFunc("GET /tenants", h.listTenants)

	// Per-tenant settings read/write. NOT adminOnly: a delegated manager may read
	// (CanManageTenant) and, when the self-service policy allows, write the toggles.
	// Authorization is enforced in the service (UpdateTenantSettings); the response
	// DTO leaks only the toggles + resolved policy, never name/email.
	m.HandleFunc("GET /tenants/{id}/settings", h.getTenantSettings)
	m.HandleFunc("PATCH /tenants/{id}/settings", h.patchTenantSettings)

	// Relational import surface (design.md §8): sysadmin may target any
	// tenant, otherwise the caller must manage the target tenant
	// (CanManageTenant). Not adminOnly — mechanics shared with the admin
	// surface below via enqueueImportShared/importStatusShared.
	m.HandleFunc("POST /import", h.enqueueImport)
	m.HandleFunc("GET /import/{id}", h.importStatus)

	// ACL surface /acl/* (i.e. /api/acl/*): NOT adminOnly, since a delegated
	// tenant#manager (not a system admin) must reach it too. Authorization is
	// enforced by the service methods themselves (design.md §7).
	acl := &aclAPIHandler{memory: h.memory}
	m.Handle("/acl/", http.StripPrefix("/acl", acl.mux()))

	// Admin surface /admin/* (i.e. /api/admin/*), gated by adminOnly so non-admins
	// get a clean 403; the shared bearer + UserContextBridge stack set the subject.
	admin := &adminAPIHandler{memory: h.memory, importJobs: h.importJobs, maxUploadBytes: h.maxUploadBytes, instanceConfig: h.instanceConfig, globalCfg: h.globalCfg, setLogLevel: h.setLogLevel}
	m.Handle("/admin/", adminOnly(h.memory)(http.StripPrefix("/admin", admin.mux())))
	return m
}

// listWritableTenants returns the tenants the caller may administer, each
// paired with the caller's effective relation (design.md §5): every tenant
// (labeled admin) for a system admin, else the tenants where the caller holds
// tenant#manager.
func (h *apiHandler) listWritableTenants(w http.ResponseWriter, r *http.Request) {
	tenants, err := h.memory.WritableTenants(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, jsonList(tenants))
}

// listTenants serves GET /tenants (design.md §5). It delegates to
// listTenantsByType with the real service; the type/q filtering and authz shape
// live in the service, the response projection in listTenantsByType.
func (h *apiHandler) listTenants(w http.ResponseWriter, r *http.Request) {
	listTenantsByType(w, r, h.memory)
}

// enqueueImport is the relational (non-admin) counterpart of
// adminAPIHandler.enqueueImport (design.md §8): same upload/enqueue
// mechanics (enqueueImportShared), but the resolved target tenant is
// authorized via canImportInto instead of being trusted unconditionally.
func (h *apiHandler) enqueueImport(w http.ResponseWriter, r *http.Request) {
	enqueueImportShared(w, r, h.importJobs, h.maxUploadBytes, h.canImportInto, h.hasAnyImportTarget)
}

// hasAnyImportTarget reports whether the caller could import ANYWHERE — a
// cheap identity-level gate run before the (up to 32MiB) archive is buffered,
// so a caller who manages no tenant is refused without the memory cost. It does
// NOT authorize the specific target tenant (that stays canImportInto after the
// body is parsed for tenant_id); a caller who manages some tenant but targets
// one they don't manage still buffers once (bounded by MaxBytesReader + the
// rate limiter).
func (h *apiHandler) hasAnyImportTarget(ctx context.Context) bool {
	if h.memory.IsAdmin(ctx) {
		return true
	}
	ts, err := h.memory.WritableTenants(ctx)
	return err == nil && len(ts) > 0
}

// importStatus is the relational counterpart of adminAPIHandler.importStatus
// (design.md §8): same status-lookup mechanics (importStatusShared), gated by
// canImportInto on the resolved target tenant.
func (h *apiHandler) importStatus(w http.ResponseWriter, r *http.Request) {
	importStatusShared(w, r, h.importJobs, h.canImportInto)
}

// canImportInto authorizes the relational import surface (design.md §8): a
// system admin may target any tenant; otherwise the caller must manage the
// target tenant (tenant#manager, which includes tenant#admin via the manager
// relation's rewrite — design.md §1a). Everyone else is refused: 403, no job.
func (h *apiHandler) canImportInto(ctx context.Context, tenantID uuid.UUID) bool {
	return h.memory.IsAdmin(ctx) || h.memory.CanManageTenant(ctx, tenantID)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes a JSON {"error": msg} body with the given status.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// pathUUID parses the {name} path segment as a UUID. On failure it writes a 400
// with an "invalid <label> id" body and returns ok=false; the caller must return
// immediately when ok is false.
func pathUUID(w http.ResponseWriter, r *http.Request, name, label string) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid "+label+" id")
		return uuid.Nil, false
	}
	return id, true
}

// decodeJSON decodes the request body into dst. On failure it writes a 400
// "invalid body" and returns false; the caller must return immediately when it
// returns false. Handlers with a deliberately optional/tolerant body must NOT use
// this — they ignore the decode error rather than 400 on it.
func decodeJSON[T any](w http.ResponseWriter, r *http.Request, dst *T) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return false
	}
	return true
}

// optFloat distinguishes an omitted JSON key (Present=false) from an explicit
// null (Present=true, Value=nil) from a number, so a PATCH can tell "leave
// unchanged" apart from "clear to inherit the global default".
type optFloat struct {
	Present bool
	Value   *float64
}

func (o *optFloat) UnmarshalJSON(b []byte) error {
	o.Present = true
	if string(b) == "null" {
		o.Value = nil
		return nil
	}
	var f float64
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	o.Value = &f
	return nil
}

// jsonList coerces a nil slice to a non-nil empty slice so list endpoints always
// marshal to a JSON array ([]), never null. A null body breaks the /ui client on an
// empty corpus (design D2); this keeps the "a list endpoint always returns an array"
// contract at the HTTP boundary.
func jsonList[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// writeErr maps apperr sentinels to HTTP status + JSON {error}. Only the safe
// sentinels (ErrNotFound / ErrInvalidInput) put their message on the wire; any
// other error is logged and returns a generic body so internal strings (SQL,
// driver, connection-string fragments) never leak to /api callers.
func writeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, apperr.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, apperr.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		slog.Error("api: internal error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

// tenantFilter parses the optional ?tenant_id=<uuid> read filter (PR-A2
// cross-tenant-reads). Absent/empty ⇒ nil (aggregate across the caller's
// readable tenants); a valid UUID ⇒ &id (scope to that tenant if the caller can
// read it); a malformed UUID ⇒ error, so the handler returns 400 rather than
// silently falling back to the aggregate.
func tenantFilter(r *http.Request) (*uuid.UUID, error) {
	return parseOptionalTenantID(r.URL.Query().Get("tenant_id"))
}

// parseOptionalTenantID parses an optional tenant_id override shared by every
// server site that accepts one (read filter, createDocument body, import
// enqueue/status): empty ⇒ (nil, nil) — no override, fall back to the caller's
// context tenant; a valid UUID ⇒ (&id, nil); a malformed value ⇒ (nil, err) so
// the handler returns 400 rather than silently targeting the wrong scope (B15).
func parseOptionalTenantID(raw string) (*uuid.UUID, error) {
	if raw == "" {
		return nil, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

func (h *apiHandler) getSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	query := q.Get("q")
	if query == "" {
		writeError(w, http.StatusBadRequest, "q is required")
		return
	}
	// Mirror MCP search_memory's input bounds (shared service consts): reject an
	// over-long query and clamp the limit so an unbounded ?limit can't be abused.
	if len(query) > service.MaxQueryLen {
		writeError(w, http.StatusBadRequest, "q exceeds maximum length")
		return
	}
	var category, subcategory, docType *string
	if c := q.Get("category"); c != "" {
		category = &c
	}
	if s := q.Get("subcategory"); s != "" {
		subcategory = &s
	}
	if dt := strings.TrimSpace(q.Get("doc_type")); dt != "" {
		docType = &dt
	}
	limit := 10
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 {
		limit = n
	}
	if limit > service.MaxSearchLimit {
		limit = service.MaxSearchLimit
	}
	tenantID, err := tenantFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid tenant_id")
		return
	}
	results, err := h.memory.Search(r.Context(), query, category, subcategory, docType, limit, false, "", tenantID, false)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, service.NewSearchResponse(results))
}

func (h *apiHandler) getDocument(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "id", "document")
	if !ok {
		return
	}
	tenantID, err := tenantFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid tenant_id")
		return
	}
	var doc *service.DocumentView
	if r.URL.Query().Get("expand") == "true" {
		doc, err = h.memory.GetDocumentByIDExpanded(r.Context(), id, false, "", r.URL.Query().Get("scope"), tenantID)
	} else {
		doc, err = h.memory.GetDocumentByID(r.Context(), id, false, "", tenantID)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

func (h *apiHandler) getDocumentHistory(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "id", "document")
	if !ok {
		return
	}
	tenantID, err := tenantFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid tenant_id")
		return
	}
	entries, err := h.memory.GetDocumentHistory(r.Context(), id, tenantID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

// createEdge backs POST /documents/{id}/edges: {id} is the source document, the
// body carries target_document_id + edge_type. Write handler — overrideID nil, so
// the service resolves the tenant from context (mirrors patchSection/deleteSection).
func (h *apiHandler) createEdge(w http.ResponseWriter, r *http.Request) {
	sourceID, ok := pathUUID(w, r, "id", "document")
	if !ok {
		return
	}
	var body struct {
		TargetDocumentID string `json:"target_document_id"`
		EdgeType         string `json:"edge_type"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	targetID, err := uuid.Parse(strings.TrimSpace(body.TargetDocumentID))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid target_document_id")
		return
	}
	result, err := h.memory.CreateEdge(r.Context(), sourceID, targetID, strings.TrimSpace(body.EdgeType), nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (h *apiHandler) listDocumentEdges(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "id", "document")
	if !ok {
		return
	}
	tenantID, err := tenantFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid tenant_id")
		return
	}
	edges, err := h.memory.ListDocumentEdges(r.Context(), id, tenantID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, jsonList(edges))
}

// resume backs GET /resume: latest handoff for ?project=<subcategory> plus the
// continues_from chain to ?depth. Read-scope gated by the service.
func (h *apiHandler) resume(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var project *string
	if p := strings.TrimSpace(q.Get("project")); p != "" {
		project = &p
	}
	depth := 0
	if n, err := strconv.Atoi(q.Get("depth")); err == nil && n > 0 {
		depth = n
	}
	tenantID, err := tenantFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid tenant_id")
		return
	}
	result, err := h.memory.Resume(r.Context(), project, tenantID, depth)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *apiHandler) deleteEdge(w http.ResponseWriter, r *http.Request) {
	edgeID, ok := pathUUID(w, r, "edgeId", "edge")
	if !ok {
		return
	}
	if err := h.memory.DeleteEdge(r.Context(), edgeID, nil); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// putSection upserts one section by path+heading (PUT /api/sections), the REST
// twin of the MCP put_section tool through the same service method.
func (h *apiHandler) putSection(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TenantID    string  `json:"tenant_id"`
		Category    string  `json:"category"`
		Subcategory *string `json:"subcategory"`
		Slug        string  `json:"slug"`
		Heading     string  `json:"heading"`
		Content     string  `json:"content"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	category, slug := strings.TrimSpace(body.Category), strings.TrimSpace(body.Slug)
	heading, content := strings.TrimSpace(body.Heading), strings.TrimSpace(body.Content)
	if category == "" || slug == "" || heading == "" || content == "" {
		writeError(w, http.StatusBadRequest, "category, slug, heading and content are required")
		return
	}
	subcategory := body.Subcategory
	if subcategory != nil && strings.TrimSpace(*subcategory) == "" {
		subcategory = nil
	}
	if err := models.ValidateDocumentPath(category, slug, subcategory); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	override, err := parseOptionalTenantID(body.TenantID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid tenant_id")
		return
	}
	result, err := h.memory.PutSection(r.Context(), category, subcategory, slug, heading, content, override)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": result.Document.ID, "path": result.Path, "sections": result.Sections})
}

func (h *apiHandler) listDocuments(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var category, subcategory *string
	if c := q.Get("category"); c != "" {
		category = &c
	}
	if s := q.Get("subcategory"); s != "" {
		subcategory = &s
	}
	tenantID, err := tenantFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid tenant_id")
		return
	}
	// Paginate the browse path: default page when absent/invalid, clamp to the
	// max, floor offset at 0. The MCP tool is unbounded by default; here we always
	// pass a positive limit, so offset-without-limit never trips (design D2/D4).
	limit := service.DefaultListLimit
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 {
		limit = n
	}
	if limit > service.MaxListLimit {
		limit = service.MaxListLimit
	}
	offset := 0
	if n, err := strconv.Atoi(q.Get("offset")); err == nil && n > 0 {
		offset = n
	}
	var slugPrefix, orderBy, order *string
	if v := q.Get("slug_prefix"); v != "" {
		slugPrefix = &v
	}
	if v := q.Get("order_by"); v != "" {
		orderBy = &v
	}
	if v := q.Get("order"); v != "" {
		order = &v
	}
	opts, err := service.ValidateListOptions(slugPrefix, orderBy, order, &limit, &offset)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	docs, err := h.memory.ListDocuments(r.Context(), category, subcategory, tenantID, opts)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, jsonList(docs))
}

func (h *apiHandler) getIndex(w http.ResponseWriter, r *http.Request) {
	depth := r.URL.Query().Get("depth")
	if depth == "" {
		depth = "summary"
	}
	var category *string
	if c := r.URL.Query().Get("category"); c != "" {
		category = &c
	}
	tenantID, err := tenantFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid tenant_id")
		return
	}
	entries, err := h.memory.GenerateIndex(r.Context(), depth, category, tenantID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, jsonList(entries))
}

func (h *apiHandler) patchSection(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "id", "section")
	if !ok {
		return
	}
	var body struct {
		Content *string `json:"content"`
		Heading *string `json:"heading"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Content == nil && body.Heading == nil {
		writeError(w, http.StatusBadRequest, "content or heading is required")
		return
	}
	section, err := h.memory.UpdateSection(r.Context(), id, body.Content, body.Heading, nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, section)
}

func (h *apiHandler) patchDocument(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "id", "document")
	if !ok {
		return
	}
	var body struct {
		Title string `json:"title"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	doc, err := h.memory.UpdateDocumentTitle(r.Context(), id, body.Title, nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

func (h *apiHandler) verifySection(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "id", "section")
	if !ok {
		return
	}
	if err := h.memory.MarkVerified(r.Context(), id, nil); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deleteDocument deletes by id against the doc's OWNING tenant (DeleteDocumentByID),
// not by re-resolving the path against the caller's home tenant — otherwise a
// foreign (common-pool or granted) id whose path collides with a home-tenant doc
// would silently delete the wrong document.
func (h *apiHandler) deleteDocument(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "id", "document")
	if !ok {
		return
	}
	if err := h.memory.DeleteDocumentByID(r.Context(), id, nil); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deleteSection removes one section by id (last-section deletes the empty parent
// document too). Authz mirrors patchSection: no admin override at the HTTP layer.
func (h *apiHandler) deleteSection(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "id", "section")
	if !ok {
		return
	}
	if err := h.memory.DeleteSection(r.Context(), id, nil); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// createDocument backs POST /documents. It mirrors the relational import
// surface's authz (canImportInto: system admin OR manages the target tenant) and,
// exactly like ImportDocuments, injects the resolved tenant into the context so
// StoreDocument targets it with overrideID nil. force stays false, so the tenant's
// duplicate guard is honored: a near/exact duplicate returns 409, not a 201.
func (h *apiHandler) createDocument(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TenantID    string  `json:"tenant_id"`
		Category    string  `json:"category"`
		Subcategory *string `json:"subcategory"`
		Slug        string  `json:"slug"`
		Content     string  `json:"content"`
		Scope       *string `json:"scope"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	category := strings.TrimSpace(body.Category)
	slug := strings.TrimSpace(body.Slug)
	content := strings.TrimSpace(body.Content)
	if category == "" || slug == "" || content == "" {
		writeError(w, http.StatusBadRequest, "category, slug and content are required")
		return
	}
	subcategory := body.Subcategory
	if subcategory != nil && strings.TrimSpace(*subcategory) == "" {
		subcategory = nil
	}
	// Enforce the shared path contract MCP store_memory also enforces (B8): the
	// HTTP surface previously skipped it, letting malformed slugs through and
	// turning an over-long category into a Postgres 500 instead of a 400.
	if err := models.ValidateDocumentPath(category, slug, subcategory); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx := r.Context()
	tenantID := auth.TenantIDFromContext(ctx)
	override, err := parseOptionalTenantID(body.TenantID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid tenant_id")
		return
	}
	if override != nil {
		tenantID = *override
	}
	if !h.canImportInto(ctx, tenantID) {
		writeError(w, http.StatusForbidden, "not authorized to write to this tenant")
		return
	}
	ctx = auth.WithTenantID(ctx, tenantID)
	res, err := h.memory.StoreDocumentScoped(ctx, category, subcategory, slug, content, false, "", nil, nil, body.Scope)
	if err != nil {
		writeErr(w, err)
		return
	}
	if res.Status == "similar_exists" {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":      "a similar document already exists",
			"status":     res.Status,
			"candidates": res.Candidates,
		})
		return
	}
	writeJSON(w, http.StatusCreated, res.Document)
}

// tenantSettingsResponse is the GET/PATCH /tenants/{id}/settings body: only the
// per-tenant retention toggles + resolved self-service policy, so this
// non-adminOnly surface never leaks name/email or other tenant fields.
type tenantSettingsResponse struct {
	ID                         uuid.UUID `json:"id"`
	StalenessMode              string    `json:"staleness_mode"`
	DuplicateGuard             bool      `json:"duplicate_guard"`
	DuplicateThreshold         *float64  `json:"duplicate_threshold"`
	CleanupScanEnabled         bool      `json:"cleanup_scan_enabled"`
	SelfServicePolicy          *string   `json:"self_service_policy"`
	EffectiveSelfServicePolicy string    `json:"effective_self_service_policy"`
}

func settingsResponse(t *models.Tenant) tenantSettingsResponse {
	return tenantSettingsResponse{
		ID:                         t.ID,
		StalenessMode:              t.StalenessMode,
		DuplicateGuard:             t.DuplicateGuard,
		DuplicateThreshold:         t.DuplicateThreshold,
		CleanupScanEnabled:         t.CleanupScanEnabled,
		SelfServicePolicy:          t.SelfServicePolicy,
		EffectiveSelfServicePolicy: t.EffectivePolicy,
	}
}

// getTenantSettings backs GET /tenants/{id}/settings: the read path of
// UpdateTenantSettings (all field pointers nil), gated by CanManageTenant. A
// read denial surfaces as 400 (ErrInvalidInput) via writeErr — deliberately not
// writeACLErr, which would misclassify a validation error as 403.
func (h *apiHandler) getTenantSettings(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "id", "tenant")
	if !ok {
		return
	}
	t, err := h.memory.UpdateTenantSettings(r.Context(), id, nil, nil, nil, false, nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settingsResponse(t))
}

// patchTenantSettings backs PATCH /tenants/{id}/settings: the write path of
// UpdateTenantSettings, gated by the tenant's self-service policy (manager, or
// admin when locked). The self-service-lock denial surfaces as 400 via writeErr.
func (h *apiHandler) patchTenantSettings(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "id", "tenant")
	if !ok {
		return
	}
	var body struct {
		StalenessMode      *string  `json:"staleness_mode"`
		DuplicateGuard     *bool    `json:"duplicate_guard"`
		DuplicateThreshold optFloat `json:"duplicate_threshold"`
		CleanupScanEnabled *bool    `json:"cleanup_scan_enabled"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	// Presence-aware: omitted = unchanged, explicit null = clear to inherit global.
	clearThreshold := body.DuplicateThreshold.Present && body.DuplicateThreshold.Value == nil
	t, err := h.memory.UpdateTenantSettings(r.Context(), id, body.StalenessMode, body.DuplicateGuard, body.DuplicateThreshold.Value, clearThreshold, body.CleanupScanEnabled)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settingsResponse(t))
}
