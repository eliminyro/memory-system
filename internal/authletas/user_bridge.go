package authletas

import (
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
// It is a no-op passthrough (original context, no auth attached) when claims are
// absent (legacy API-key path) or the "tenant_id" claim is missing/unparseable.
// That fails OPEN for unrelated bearer-shaped requests and fails SECURE for
// legacy pre-fix tokens (which lack the tenant_id claim), forcing re-auth rather
// than trusting a stale `sub`.
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
			if err != nil {
				next.ServeHTTP(rw, r)
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
