package authletas

import (
	"log/slog"
	"net/http"

	"github.com/eliminyro/authlet/pkg/rs"
	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/google/uuid"
)

// UserContextBridge translates authlet's validated JWT claims (under
// rs.ContextKey{}) into memory-system's auth context, so downstream handlers
// see a tenant/email/subject for JWT requests. No-op when claims are absent
// (legacy API-key path) or claims.Subject isn't a UUID — failing open lets
// unrelated bearer-shaped requests fall through. Email comes from the signed
// token's "email" custom claim (set in Setup) for parity with the API-key path.
func (w *Wiring) UserContextBridge() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			claims, ok := rs.FromContext(r.Context())
			if !ok {
				next.ServeHTTP(rw, r)
				return
			}
			tid, err := uuid.Parse(claims.Subject)
			if err != nil {
				next.ServeHTTP(rw, r)
				return
			}
			ctx := auth.WithTenantID(r.Context(), tid)
			if email, ok := claims.Extra["email"].(string); ok && email != "" {
				ctx = auth.WithEmail(ctx, email)
				// Resolve verified email to the unified subject (tenant_users.id).
				// No row -> no subject, so Pass 2 fails closed. Skipped when db is
				// nil (unit tests): tenant+email only.
				if w.db != nil {
					if uid, ok := lookupTenantUserID(r.Context(), w.db, slog.Default(), email); ok {
						ctx = auth.WithSubject(ctx, auth.Subject{Type: auth.SubjectTypeUser, ID: uid})
					}
				}
			}
			next.ServeHTTP(rw, r.WithContext(ctx))
		})
	}
}
