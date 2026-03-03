package admin

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/eliminyro/memory-mcp/internal/auth"
	apperr "github.com/eliminyro/memory-mcp/internal/errors"
	"github.com/eliminyro/memory-mcp/internal/models"
	"github.com/eliminyro/memory-mcp/internal/repository"
)

const maxNameLen = 200

type Handler struct {
	tenants *repository.TenantRepository
	keys    *repository.APIKeyRepository
}

func NewHandler(tenants *repository.TenantRepository, keys *repository.APIKeyRepository) *Handler {
	return &Handler{tenants: tenants, keys: keys}
}

// Register registers admin routes on the given mux with the provided middleware.
func (h *Handler) Register(mux *http.ServeMux, mw func(http.Handler) http.Handler) {
	mux.Handle("POST /admin/tenants", mw(http.HandlerFunc(h.CreateTenant)))
	mux.Handle("GET /admin/tenants", mw(http.HandlerFunc(h.ListTenants)))
	mux.Handle("GET /admin/tenants/{id}", mw(http.HandlerFunc(h.GetTenant)))
	mux.Handle("DELETE /admin/tenants/{id}", mw(http.HandlerFunc(h.DeleteTenant)))
	mux.Handle("POST /admin/tenants/{id}/keys", mw(http.HandlerFunc(h.CreateKey)))
	mux.Handle("GET /admin/tenants/{id}/keys", mw(http.HandlerFunc(h.ListKeys)))
	mux.Handle("DELETE /admin/keys/{id}", mw(http.HandlerFunc(h.RevokeKey)))
}

type createTenantRequest struct {
	Name string `json:"name"`
}

func (h *Handler) CreateTenant(w http.ResponseWriter, r *http.Request) {
	var req createTenantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" || len(req.Name) > maxNameLen {
		writeError(w, "name is required and must be <= 200 characters", http.StatusBadRequest)
		return
	}

	tenant := &models.Tenant{Name: req.Name}
	if err := h.tenants.Create(r.Context(), tenant); err != nil {
		slog.Error("create tenant failed", "error", err)
		writeError(w, "failed to create tenant", http.StatusInternalServerError)
		return
	}
	writeJSON(w, tenant, http.StatusCreated)
}

func (h *Handler) ListTenants(w http.ResponseWriter, r *http.Request) {
	tenants, err := h.tenants.List(r.Context())
	if err != nil {
		writeError(w, "failed to list tenants", http.StatusInternalServerError)
		return
	}
	writeJSON(w, tenants, http.StatusOK)
}

func (h *Handler) GetTenant(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, "invalid tenant id", http.StatusBadRequest)
		return
	}
	tenant, err := h.tenants.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, apperr.ErrNotFound) {
			writeError(w, "tenant not found", http.StatusNotFound)
			return
		}
		writeError(w, "failed to get tenant", http.StatusInternalServerError)
		return
	}
	writeJSON(w, tenant, http.StatusOK)
}

func (h *Handler) DeleteTenant(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, "invalid tenant id", http.StatusBadRequest)
		return
	}
	// Prevent deleting the bootstrap tenant
	if id == models.BootstrapTenantID {
		writeError(w, "cannot delete the bootstrap tenant", http.StatusForbidden)
		return
	}
	if err := h.tenants.Delete(r.Context(), id); err != nil {
		if errors.Is(err, apperr.ErrNotFound) {
			writeError(w, "tenant not found", http.StatusNotFound)
			return
		}
		slog.Error("delete tenant failed", "tenant_id", id, "error", err)
		writeError(w, "failed to delete tenant", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "deleted"}, http.StatusOK)
}

type createKeyRequest struct {
	Label string `json:"label"`
}

type createKeyResponse struct {
	ID        uuid.UUID `json:"id"`
	TenantID  uuid.UUID `json:"tenant_id"`
	Label     string    `json:"label"`
	Prefix    string    `json:"prefix"`
	Key       string    `json:"key"` // plaintext, shown only once
}

func (h *Handler) CreateKey(w http.ResponseWriter, r *http.Request) {
	tenantID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, "invalid tenant id", http.StatusBadRequest)
		return
	}

	var req createKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Label == "" || len(req.Label) > maxNameLen {
		writeError(w, "label is required and must be <= 200 characters", http.StatusBadRequest)
		return
	}

	// Verify tenant exists
	if _, err := h.tenants.GetByID(r.Context(), tenantID); err != nil {
		if errors.Is(err, apperr.ErrNotFound) {
			writeError(w, "tenant not found", http.StatusNotFound)
			return
		}
		writeError(w, "failed to verify tenant", http.StatusInternalServerError)
		return
	}

	plaintext, hash, err := auth.GenerateAPIKey()
	if err != nil {
		writeError(w, "failed to generate key", http.StatusInternalServerError)
		return
	}

	key := &models.APIKey{
		TenantID: tenantID,
		KeyHash:  hash,
		Label:    req.Label,
		Prefix:   auth.KeyPrefix(plaintext),
	}
	if err := h.keys.Create(r.Context(), key); err != nil {
		slog.Error("create api key failed", "tenant_id", tenantID, "error", err)
		writeError(w, "failed to create key", http.StatusInternalServerError)
		return
	}

	writeJSON(w, createKeyResponse{
		ID:       key.ID,
		TenantID: tenantID,
		Label:    req.Label,
		Prefix:   key.Prefix,
		Key:      plaintext,
	}, http.StatusCreated)
}

func (h *Handler) ListKeys(w http.ResponseWriter, r *http.Request) {
	tenantID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, "invalid tenant id", http.StatusBadRequest)
		return
	}
	keys, err := h.keys.ListByTenant(r.Context(), tenantID)
	if err != nil {
		writeError(w, "failed to list keys", http.StatusInternalServerError)
		return
	}
	writeJSON(w, keys, http.StatusOK)
}

func (h *Handler) RevokeKey(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, "invalid key id", http.StatusBadRequest)
		return
	}
	if err := h.keys.Revoke(r.Context(), id); err != nil {
		if errors.Is(err, apperr.ErrNotFound) {
			writeError(w, "key not found or already revoked", http.StatusNotFound)
			return
		}
		slog.Error("revoke key failed", "key_id", id, "error", err)
		writeError(w, "failed to revoke key", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "revoked"}, http.StatusOK)
}

func writeJSON(w http.ResponseWriter, v any, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, msg string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
