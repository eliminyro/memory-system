package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
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
	m.HandleFunc("PATCH /tenants/{id}", h.updateTenant)
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

// createTenant accepts an optional "type" in the body alongside name/email and
// forwards it to CreateTenant (default shared when omitted). The decode +
// create mechanics live in createTenantWithType so a handler unit test can
// drive them with a stubbed tenantCreator.
func (h *adminAPIHandler) createTenant(w http.ResponseWriter, r *http.Request) {
	createTenantWithType(w, r, h.memory)
}

func (h *adminAPIHandler) listUsers(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "id", "tenant")
	if !ok {
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
	id, ok := pathUUID(w, r, "id", "tenant")
	if !ok {
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
	tenantID, ok := pathUUID(w, r, "id", "tenant")
	if !ok {
		return
	}
	var body struct {
		Label         string  `json:"label"`
		SubjectID     *string `json:"subject_id"`
		ExpiresInDays *int    `json:"expires_in_days"`
	}
	if !decodeJSON(w, r, &body) {
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
	id, ok := pathUUID(w, r, "id", "key")
	if !ok {
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
	id, ok := pathUUID(w, r, "id", "key")
	if !ok {
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
	if !decodeJSON(w, r, &body) {
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

// updateTenant is the admin-only tenant patcher (PATCH /admin/tenants/{id}). The
// UI wires it to the self-service-lock rocker, so it accepts only
// self_service_policy (open | admin_only | inherit — the last clears the override
// to NULL) and returns the settings projection. The /admin mux is adminOnly-gated
// and UpdateTenant re-checks requireAdmin (defense in depth). The lock stays
// admin-only by design — never wired to the self-service settings surface.
func (h *adminAPIHandler) updateTenant(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "id", "tenant")
	if !ok {
		return
	}
	var body struct {
		SelfServicePolicy *string `json:"self_service_policy"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	t, err := h.memory.UpdateTenant(r.Context(), id, service.UpdateTenantFields{SelfServicePolicy: body.SelfServicePolicy})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settingsResponse(t))
}

func (h *adminAPIHandler) updateUserRole(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if !decodeJSON(w, r, &body) {
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

// importAuthorizer decides whether the caller may target tenantID for an
// import operation (enqueue or status), given the resolved target tenant.
// Shared between the admin and relational import surfaces (design.md §8) via
// enqueueImportShared / importStatusShared, so the upload/status mechanics
// aren't duplicated between adminAPIHandler and apiHandler.
type importAuthorizer func(ctx context.Context, tenantID uuid.UUID) bool

// allowAnyTenant is the admin surface's importAuthorizer: unrestricted,
// because the whole /api/admin subtree already sits behind adminOnly before
// this ever runs (a system admin may direct an import at any tenant).
func allowAnyTenant(context.Context, uuid.UUID) bool { return true }

// enqueueImport accepts a zip archive upload (multipart form file field
// "archive"), persists it as a queued import_jobs row, and returns the job id
// (202). The in-process worker ingests it off the request path (design D7).
// Non-admins never reach here — adminOnly returns 403 before dispatch — so the
// target tenant is trusted unconditionally (allowAnyTenant); see
// enqueueImportShared for the upload/authorize/enqueue mechanics shared with
// the relational /api/import surface (apiHandler.enqueueImport, design.md §8).
func (h *adminAPIHandler) enqueueImport(w http.ResponseWriter, r *http.Request) {
	enqueueImportShared(w, r, h.importJobs, h.maxUploadBytes, allowAnyTenant, nil)
}

// enqueueImportShared implements the archive-upload + enqueue mechanics
// common to POST /api/admin/import and POST /api/import: extract the
// multipart "archive" field, resolve the target tenant (context default,
// optionally overridden by a "tenant_id" form field), authorize the resolved
// target via authorize, then persist a queued import_jobs row. Oversized
// uploads are rejected 413 via http.MaxBytesReader; a missing/malformed
// tenant or archive is 400; an authorize failure is 403.
func enqueueImportShared(w http.ResponseWriter, r *http.Request, jobs importJobStore, maxUploadBytes int64, authorize importAuthorizer, preAuthorize func(context.Context) bool) {
	// Cheap identity-level gate BEFORE the (up to maxUploadBytes) archive is
	// buffered, so a caller who could never import anywhere is refused without
	// the memory cost. The specific target tenant is still authorized by
	// `authorize` after the body is parsed for tenant_id.
	if preAuthorize != nil && !preAuthorize(r.Context()) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not authorized to import"})
		return
	}
	// Cap the whole request body: MaxBytesReader makes reads past the limit fail
	// with *http.MaxBytesError, surfaced below as 413. Passing the same cap as
	// ParseMultipartForm's maxMemory keeps the file part in memory (no temp-file
	// spill — the image runs on a read-only rootfs). Parsing the form here also
	// makes the optional tenant_id field readable via r.FormValue below.
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	archive, err := readArchiveUpload(r, maxUploadBytes)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "archive exceeds the upload size limit"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `invalid archive upload (expected multipart form file field "archive")`})
		return
	}

	tenantID := auth.TenantIDFromContext(r.Context())
	override, err := parseOptionalTenantID(strings.TrimSpace(r.FormValue("tenant_id")))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid tenant_id"})
		return
	}
	if override != nil {
		tenantID = *override
	}
	if tenantID == uuid.Nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no target tenant: pass tenant_id or authenticate with a tenant-scoped key"})
		return
	}
	if !authorize(r.Context(), tenantID) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not authorized to import into this tenant"})
		return
	}

	job := &models.ImportJob{
		TenantID: tenantID,
		Status:   models.ImportJobStatusQueued,
		Archive:  archive,
	}
	if err := jobs.Create(r.Context(), job); err != nil {
		writeErr(w, err)
		return
	}
	// tenant_id is echoed so a client that targeted another tenant can poll its
	// status: importStatus scopes GetByID by tenant (see its ?tenant_id handling).
	writeJSON(w, http.StatusAccepted, map[string]any{"id": job.ID, "status": job.Status, "tenant_id": job.TenantID})
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

// importStatus returns an import job's state and progress counters. Non-admins
// never reach here — adminOnly returns 403 before dispatch — so the target
// tenant is trusted unconditionally (allowAnyTenant); see importStatusShared
// for the lookup/authorize mechanics shared with the relational
// GET /api/import/{id} surface (apiHandler.importStatus, design.md §8).
func (h *adminAPIHandler) importStatus(w http.ResponseWriter, r *http.Request) {
	importStatusShared(w, r, h.importJobs, allowAnyTenant)
}

// importStatusShared implements the job-status lookup mechanics common to
// GET /api/admin/import/{id} and GET /api/import/{id}: resolve the job id and
// the target tenant (context default, optionally overridden by a "tenant_id"
// query param — echoed by enqueueImportShared), authorize the resolved target
// via authorize, then fetch the tenant-scoped job. GetByID is tenant-scoped,
// so the archive bytes never leave the server (json:"-") and an unknown or
// wrong-tenant id yields 404 via writeErr.
func importStatusShared(w http.ResponseWriter, r *http.Request, jobs importJobStore, authorize importAuthorizer) {
	id, ok := pathUUID(w, r, "id", "job")
	if !ok {
		return
	}
	tenantID := auth.TenantIDFromContext(r.Context())
	// A malformed tenant_id is a 400, never a silent fallback to the context
	// tenant: a typo'd override in a status poll must not quietly target the
	// wrong scope and surface as a misleading 404 (B15).
	override, err := parseOptionalTenantID(strings.TrimSpace(r.URL.Query().Get("tenant_id")))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid tenant_id"})
		return
	}
	if override != nil {
		tenantID = *override
	}
	if !authorize(r.Context(), tenantID) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not authorized to view this tenant's import jobs"})
		return
	}
	job, err := jobs.GetByID(r.Context(), id, tenantID)
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
