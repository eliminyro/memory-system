package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/service"
)

// adminAPIHandler backs /api/admin/*, mounted behind bearer + UserContextBridge
// plus the adminOnly gate, so every handler runs with an authenticated admin
// subject. Reads are metadata-only (APIKey hash is `json:"-"`); newly issued or
// rotated keys return the plaintext exactly once.
type adminAPIHandler struct {
	memory *service.MemoryService

	// importJobs backs POST/GET /admin/import; maxUploadBytes caps the archive
	// accepted by the enqueue endpoint (config.ImportMaxUploadBytes).
	importJobs     importJobStore
	maxUploadBytes int64
}

// importJobStore is the slice of the import_jobs repository the admin HTTP
// surface needs (enqueue + tenant-scoped status). An interface so handler unit
// tests can stub it without a database.
type importJobStore interface {
	Create(ctx context.Context, job *models.ImportJob) error
	GetByID(ctx context.Context, id, tenantID uuid.UUID) (*models.ImportJob, error)
}

// adminOnly refuses non-admins with a clean 403 before dispatch. Service methods
// re-check (defense in depth) but surface denial as 400; the web page needs a
// true 403 to gate its UI.
func adminOnly(svc *service.MemoryService) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !svc.IsAdmin(r.Context()) {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "admin privileges required"})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (h *adminAPIHandler) mux() *http.ServeMux {
	m := http.NewServeMux()
	// UI probe for whether to show the admin section; only reachable past
	// adminOnly, so a 200 means "you are an admin".
	m.HandleFunc("GET /whoami", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"admin": true})
	})
	m.HandleFunc("GET /tenants", h.listTenants)
	m.HandleFunc("POST /tenants", h.createTenant)
	m.HandleFunc("GET /tenants/{id}/users", h.listUsers)
	m.HandleFunc("GET /tenants/{id}/keys", h.listKeys)
	m.HandleFunc("POST /tenants/{id}/keys", h.createKey)
	m.HandleFunc("POST /keys/{id}/rotate", h.rotateKey)
	m.HandleFunc("DELETE /keys/{id}", h.revokeKey)
	m.HandleFunc("POST /users", h.grantUser)
	m.HandleFunc("PATCH /users", h.updateUserRole)
	m.HandleFunc("DELETE /users", h.revokeUser)
	m.HandleFunc("POST /import", h.enqueueImport)
	m.HandleFunc("GET /import/{id}", h.importStatus)
	return m
}

func (h *adminAPIHandler) listTenants(w http.ResponseWriter, r *http.Request) {
	tenants, err := h.memory.ListTenants(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tenants)
}

func (h *adminAPIHandler) createTenant(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	tenant, err := h.memory.CreateTenant(r.Context(), body.Name, body.Email)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, tenant)
}

func (h *adminAPIHandler) listUsers(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid tenant id"})
		return
	}
	users, err := h.memory.ListTenantUsers(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, users)
}

func (h *adminAPIHandler) listKeys(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid tenant id"})
		return
	}
	keys, err := h.memory.ListAPIKeys(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, keys)
}

func (h *adminAPIHandler) createKey(w http.ResponseWriter, r *http.Request) {
	tenantID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid tenant id"})
		return
	}
	var body struct {
		Label         string  `json:"label"`
		SubjectID     *string `json:"subject_id"`
		ExpiresInDays *int    `json:"expires_in_days"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	var expiresAt *time.Time
	if body.ExpiresInDays != nil {
		if *body.ExpiresInDays <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "expires_in_days must be > 0"})
			return
		}
		t := time.Now().Add(time.Duration(*body.ExpiresInDays) * 24 * time.Hour)
		expiresAt = &t
	}
	plaintext, key, err := h.memory.CreateAPIKey(r.Context(), tenantID, body.Label, body.SubjectID, expiresAt)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, keyIssueResponse(plaintext, key))
}

func (h *adminAPIHandler) rotateKey(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid key id"})
		return
	}
	var body struct {
		GraceHours *int `json:"grace_hours"`
	}
	// Body is optional for rotate; ignore decode errors on an empty body.
	_ = json.NewDecoder(r.Body).Decode(&body)
	var grace time.Duration
	if body.GraceHours != nil {
		if *body.GraceHours < 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "grace_hours must be >= 0"})
			return
		}
		grace = time.Duration(*body.GraceHours) * time.Hour
	}
	plaintext, key, err := h.memory.RotateAPIKey(r.Context(), id, grace)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, keyIssueResponse(plaintext, key))
}

func (h *adminAPIHandler) revokeKey(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid key id"})
		return
	}
	if err := h.memory.RevokeAPIKey(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *adminAPIHandler) grantUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		TenantID string `json:"tenant_id"`
		Role     string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	tenantID, err := uuid.Parse(body.TenantID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid tenant_id"})
		return
	}
	tu, err := h.memory.GrantTenantUser(r.Context(), body.Email, tenantID, body.Role)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, tu)
}

func (h *adminAPIHandler) updateUserRole(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	tu, err := h.memory.UpdateTenantUserRole(r.Context(), body.Email, body.Role)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tu)
}

func (h *adminAPIHandler) revokeUser(w http.ResponseWriter, r *http.Request) {
	email := r.URL.Query().Get("email")
	if email == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "email query parameter is required"})
		return
	}
	if err := h.memory.RevokeTenantUser(r.Context(), email); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// enqueueImport accepts a zip archive upload (multipart form file field
// "archive"), persists it as a queued import_jobs row for the authenticated
// admin's tenant, and returns the job id (202). The in-process worker ingests it
// off the request path (design D7). Oversized uploads are rejected 413 via
// http.MaxBytesReader; a missing tenant or malformed form is 400. Non-admins
// never reach here — adminOnly returns 403 before dispatch.
func (h *adminAPIHandler) enqueueImport(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDFromContext(r.Context())
	if tenantID == uuid.Nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no tenant in context"})
		return
	}

	// Cap the whole request body: MaxBytesReader makes reads past the limit fail
	// with *http.MaxBytesError, surfaced below as 413. Passing the same cap as
	// ParseMultipartForm's maxMemory keeps the file part in memory (no temp-file
	// spill — the image runs on a read-only rootfs).
	r.Body = http.MaxBytesReader(w, r.Body, h.maxUploadBytes)
	archive, err := readArchiveUpload(r, h.maxUploadBytes)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "archive exceeds the upload size limit"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `invalid archive upload (expected multipart form file field "archive")`})
		return
	}

	job := &models.ImportJob{
		TenantID: tenantID,
		Status:   models.ImportJobStatusQueued,
		Archive:  archive,
	}
	if err := h.importJobs.Create(r.Context(), job); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": job.ID, "status": job.Status})
}

// readArchiveUpload extracts the uploaded archive bytes from the "archive"
// multipart form file. maxMemory bounds in-memory buffering so
// ParseMultipartForm never spills a part to a temp file.
func readArchiveUpload(r *http.Request, maxMemory int64) ([]byte, error) {
	if err := r.ParseMultipartForm(maxMemory); err != nil {
		return nil, err
	}
	file, _, err := r.FormFile("archive")
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return io.ReadAll(file)
}

// importStatus returns an import job's state and progress counters. A job is
// visible only within the tenant that owns it (GetByID is tenant-scoped), so an
// unknown or cross-tenant id yields 404 via writeErr. The archive bytes never
// leave the server (json:"-" on the model).
func (h *adminAPIHandler) importStatus(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid job id"})
		return
	}
	job, err := h.importJobs.GetByID(r.Context(), id, auth.TenantIDFromContext(r.Context()))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

// keyIssueResponse is the one-time create/rotate payload: plaintext (shown once)
// plus non-secret metadata.
func keyIssueResponse(plaintext string, key *models.APIKey) map[string]any {
	return map[string]any{
		"id":         key.ID,
		"tenant_id":  key.TenantID,
		"label":      key.Label,
		"prefix":     key.Prefix,
		"expires_at": key.ExpiresAt,
		"key":        plaintext,
	}
}
