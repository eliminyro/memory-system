package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	apperr "github.com/eliminyro/memory-system/internal/errors"
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
}

// mux builds the /api router; routes omit the /api prefix (caller strips it).
func (h *apiHandler) mux() *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc("GET /index", h.getIndex)
	m.HandleFunc("GET /search", h.getSearch)
	m.HandleFunc("GET /documents", h.listDocuments)
	m.HandleFunc("GET /documents/{id}", h.getDocument)
	m.HandleFunc("PATCH /sections/{id}", h.patchSection)
	m.HandleFunc("PATCH /documents/{id}", h.patchDocument)
	m.HandleFunc("POST /sections/{id}/verify", h.verifySection)
	m.HandleFunc("DELETE /documents/{id}", h.deleteDocument)

	// GET /tenants/writable backs the delegated-manager UI probe (design.md
	// §7/§9): every tenant for a system admin, else the tenants the caller
	// manages. Not adminOnly — a manager who isn't a system admin needs this.
	m.HandleFunc("GET /tenants/writable", h.listWritableTenants)

	// GET /tenants?type=<t>&q=<filter> lists/searches tenants by display type
	// (design.md §5), reusing the WritableTenants authz shape: all tenants of
	// that type for a system admin, else only the tenants the caller manages.
	// Not adminOnly — delegated managers use it too.
	m.HandleFunc("GET /tenants", h.listTenants)

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
	admin := &adminAPIHandler{memory: h.memory, importJobs: h.importJobs, maxUploadBytes: h.maxUploadBytes}
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
	enqueueImportShared(w, r, h.importJobs, h.maxUploadBytes, h.canImportInto)
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
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, apperr.ErrInvalidInput):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	default:
		slog.Error("api: internal error", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
	}
}

// tenantFilter parses the optional ?tenant_id=<uuid> read filter (PR-A2
// cross-tenant-reads). Absent/empty ⇒ nil (aggregate across the caller's
// readable tenants); a valid UUID ⇒ &id (scope to that tenant if the caller can
// read it); a malformed UUID ⇒ error, so the handler returns 400 rather than
// silently falling back to the aggregate.
func tenantFilter(r *http.Request) (*uuid.UUID, error) {
	v := r.URL.Query().Get("tenant_id")
	if v == "" {
		return nil, nil
	}
	id, err := uuid.Parse(v)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

func (h *apiHandler) getSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	query := q.Get("q")
	if query == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "q is required"})
		return
	}
	var category, subcategory *string
	if c := q.Get("category"); c != "" {
		category = &c
	}
	if s := q.Get("subcategory"); s != "" {
		subcategory = &s
	}
	limit := 10
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 {
		limit = n
	}
	tenantID, err := tenantFilter(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid tenant_id"})
		return
	}
	results, err := h.memory.Search(r.Context(), query, category, subcategory, limit, false, "", tenantID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, jsonList(results))
}

func (h *apiHandler) getDocument(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid document id"})
		return
	}
	tenantID, err := tenantFilter(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid tenant_id"})
		return
	}
	doc, err := h.memory.GetDocumentByID(r.Context(), id, false, "", tenantID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, doc)
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
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid tenant_id"})
		return
	}
	docs, err := h.memory.ListDocuments(r.Context(), category, subcategory, tenantID)
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
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid tenant_id"})
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
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid section id"})
		return
	}
	var body struct {
		Content *string `json:"content"`
		Heading *string `json:"heading"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	if body.Content == nil && body.Heading == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "content or heading is required"})
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
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid document id"})
		return
	}
	var body struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
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
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid section id"})
		return
	}
	if err := h.memory.MarkVerified(r.Context(), id, nil); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deleteDocument resolves the doc by id to its path, then deletes via the audited
// by-path DeleteDocument (which also refuses common-pool docs).
func (h *apiHandler) deleteDocument(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid document id"})
		return
	}
	doc, err := h.memory.GetDocumentByID(r.Context(), id, false, "", nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.memory.DeleteDocument(r.Context(), doc.Category, doc.Subcategory, doc.Slug, nil); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
