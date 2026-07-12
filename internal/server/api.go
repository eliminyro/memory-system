package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/service"
)

// apiHandler holds the dependencies the JSON API needs. Every handler runs
// behind the bearer middleware + UserContextBridge, so the request context
// already carries the tenant; handlers pass overrideID = nil and let the
// service resolve the tenant from context, exactly like the MCP tools.
type apiHandler struct {
	memory *service.MemoryService
}

// apiMux builds the /api router. Routes are registered without the /api
// prefix because the caller strips it via http.StripPrefix.
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

	// Admin surface: /api/admin/* (after the caller strips /api, seen here as
	// /admin/*). Gated by adminOnly so non-admins get a clean 403; the shared
	// bearer + UserContextBridge stack already put the subject in context.
	admin := &adminAPIHandler{memory: h.memory}
	m.Handle("/admin/", adminOnly(h.memory)(http.StripPrefix("/admin", admin.mux())))
	return m
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr maps apperr sentinels to an HTTP status + a JSON {error} body. Only
// the mapped, safe-to-expose sentinels (ErrNotFound / ErrInvalidInput) put
// their message on the wire. Any other error is an internal fault: the full
// error is logged server-side and the client gets a generic body, so raw
// internal strings (SQL, driver, connection-string fragments) never leak to
// /api callers.
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
	results, err := h.memory.Search(r.Context(), query, category, subcategory, limit, false, "", nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, results)
}

func (h *apiHandler) getDocument(w http.ResponseWriter, r *http.Request) {
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
	docs, err := h.memory.ListDocuments(r.Context(), category, subcategory, nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, docs)
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
	entries, err := h.memory.GenerateIndex(r.Context(), depth, category, nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entries)
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

// deleteDocument resolves the doc by id (to its path) then deletes via the
// audited by-path DeleteDocument — which also refuses common-pool docs.
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
