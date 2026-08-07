package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"

	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/service"
)

// aclService is the subset of *service.MemoryService the /api/acl surface
// needs (design.md §7). An interface — mirroring importJobStore in
// admin_api.go — so handler unit tests can stub authorization / email
// resolution outcomes without a database; *service.MemoryService satisfies it
// structurally, so production wiring just assigns the real service.
type aclService interface {
	GrantTenantAccess(ctx context.Context, tenantID uuid.UUID, email, relation string) error
	RevokeTenantAccess(ctx context.Context, tenantID uuid.UUID, email, relation string) error
	ListTenantGrants(ctx context.Context, tenantID uuid.UUID) ([]service.Grant, error)
	GrantDocumentAccess(ctx context.Context, docID uuid.UUID, email, relation string) error
	RevokeDocumentAccess(ctx context.Context, docID uuid.UUID, email, relation string) error
	ListDocumentGrants(ctx context.Context, docID uuid.UUID) ([]service.Grant, error)
}

// aclAPIHandler backs /api/acl/*, mounted on the main /api mux behind bearer +
// UserContextBridge but deliberately NOT adminOnly (design.md §7): a
// delegated tenant#manager who is not a system admin must reach these routes
// too. Authorization is enforced by the service methods themselves (the
// grant-ceiling matrix, design.md §6), which fail closed; every handler here
// just maps the resulting error to a status code (writeACLErr).
type aclAPIHandler struct {
	memory aclService
}

func (h *aclAPIHandler) mux() *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc("GET /tenants/{id}/grants", h.listTenantGrants)
	m.HandleFunc("POST /tenants/{id}/grants", h.grantTenantAccess)
	m.HandleFunc("DELETE /tenants/{id}/grants", h.revokeTenantAccess)
	m.HandleFunc("GET /documents/{id}/grants", h.listDocumentGrants)
	m.HandleFunc("POST /documents/{id}/grants", h.grantDocumentAccess)
	m.HandleFunc("DELETE /documents/{id}/grants", h.revokeDocumentAccess)
	return m
}

// aclGrantBody is the {email, relation} payload shared by every grant/revoke
// request (design.md §7); DELETE requests carry it as a body too, same as
// POST, so the relation being revoked is explicit rather than "all relations".
type aclGrantBody struct {
	Email    string `json:"email"`
	Relation string `json:"relation"`
}

func decodeGrantBody(r *http.Request) (aclGrantBody, error) {
	var body aclGrantBody
	err := json.NewDecoder(r.Body).Decode(&body)
	return body, err
}

// writeACLErr maps a service error to the ACL surface's status codes
// (design.md §7). Relation validation lives solely in the service now, which
// returns the distinct apperr.ErrInvalidRelation for a malformed relation;
// that is checked first so a bad relation stays a 400 while a grant-ceiling /
// CanManageTenant denial (a bare apperr.ErrInvalidInput) maps to 403.
// apperr.ErrNotFound (unknown email, or a missing document for the document
// routes) -> 404; anything else falls through to the shared writeErr (500,
// logged, generic body — never leaks internals).
func writeACLErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, apperr.ErrInvalidRelation):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, apperr.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, apperr.ErrInvalidInput):
		writeError(w, http.StatusForbidden, err.Error())
	default:
		writeErr(w, err)
	}
}

func (h *aclAPIHandler) listTenantGrants(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "id", "tenant")
	if !ok {
		return
	}
	grants, err := h.memory.ListTenantGrants(r.Context(), id)
	if err != nil {
		writeACLErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, jsonList(grants))
}

func (h *aclAPIHandler) grantTenantAccess(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "id", "tenant")
	if !ok {
		return
	}
	body, err := decodeGrantBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := h.memory.GrantTenantAccess(r.Context(), id, body.Email, body.Relation); err != nil {
		writeACLErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"tenant_id": id.String(), "email": body.Email, "relation": body.Relation})
}

func (h *aclAPIHandler) revokeTenantAccess(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "id", "tenant")
	if !ok {
		return
	}
	body, err := decodeGrantBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := h.memory.RevokeTenantAccess(r.Context(), id, body.Email, body.Relation); err != nil {
		writeACLErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *aclAPIHandler) listDocumentGrants(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "id", "document")
	if !ok {
		return
	}
	grants, err := h.memory.ListDocumentGrants(r.Context(), id)
	if err != nil {
		writeACLErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, jsonList(grants))
}

func (h *aclAPIHandler) grantDocumentAccess(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "id", "document")
	if !ok {
		return
	}
	body, err := decodeGrantBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := h.memory.GrantDocumentAccess(r.Context(), id, body.Email, body.Relation); err != nil {
		writeACLErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"document_id": id.String(), "email": body.Email, "relation": body.Relation})
}

func (h *aclAPIHandler) revokeDocumentAccess(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "id", "document")
	if !ok {
		return
	}
	body, err := decodeGrantBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := h.memory.RevokeDocumentAccess(r.Context(), id, body.Email, body.Relation); err != nil {
		writeACLErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
