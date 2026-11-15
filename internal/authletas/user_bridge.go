package authletas

import (
	"encoding/json"
	"net/http"

	"github.com/eliminyro/authlet/pkg/rs"
	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/google/uuid"
)

// UserContextBridge translates authlet's validated JWT claims (under
// rs.ContextKey{}) into memory-system's auth context, so downstream handlers see
// a tenant/subject/email for JWT requests. Everything is read from the signed
// token — the bridge does no DB work. The subject is the token `sub`
// (tenant_users.id); the tenant and email come from the signed "tenant_id" and
// "email" custom claims (set in Setup's additionalClaims).
//
// It is a no-op passthrough when claims are absent (legacy API-key path). When
// claims ARE present but the "tenant_id" claim is missing/unparseable (legacy
// pre-fix tokens, or a token minted while the claims lookup failed), it rejects
// with 401 invalid_token rather than passing the request on tenant-less: a 401
// makes OAuth clients refresh or re-authorize, whereas a passthrough surfaced
// downstream as an opaque "missing tenant ID in context" invalid-input error on
// every tool call, with no signal to the client that its token was the problem.
func (w *Wiring) UserContextBridge() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			claims, ok := rs.FromContext(r.Context())
			if !ok {
				next.ServeHTTP(rw, r)
				return
			}
			tidStr, _ := claims.Extra["tenant_id"].(string)
			tid, err := uuid.Parse(tidStr)
			if err != nil || tid == uuid.Nil {
				w.writeInvalidToken(rw, "access token carries no tenant; re-authorize")
				return
			}
			ctx := auth.WithTenantID(r.Context(), tid)
			// sub IS the unified subject (tenant_users.id).
			if sub := claims.Subject; sub != "" {
				ctx = auth.WithSubject(ctx, auth.Subject{Type: auth.SubjectTypeUser, ID: sub})
			}
			if email, ok := claims.Extra["email"].(string); ok && email != "" {
				ctx = auth.WithEmail(ctx, email)
			}
			next.ServeHTTP(rw, r.WithContext(ctx))
		})
	}
}

// writeInvalidToken writes an RFC 6750 §3.1 invalid_token rejection. The
// WWW-Authenticate challenge carries error="invalid_token" so OAuth clients
// treat it as a token problem (refresh / re-authorize), not a missing token.
func (w *Wiring) writeInvalidToken(rw http.ResponseWriter, description string) {
	challenge := `Bearer realm="MCP", error="invalid_token", error_description="` + description + `"`
	if w.prmURL != "" {
		challenge += `, resource_metadata="` + w.prmURL + `"`
	}
	rw.Header().Set("WWW-Authenticate", challenge)
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(rw).Encode(map[string]string{
		"error":             "invalid_token",
		"error_description": description,
	})
}
