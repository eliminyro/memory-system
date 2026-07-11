package authletas

import (
	"log/slog"
	"net/http"

	"github.com/eliminyro/authlet/pkg/rs"
	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/google/uuid"
)

// UserContextBridge translates the validated JWT claims attached by
// authlet's bearer middleware (stored under rs.ContextKey{}) into
// memory-system's auth context. Downstream handlers read tenant
// identity via auth.TenantIDFromContext (and email via
// auth.EmailFromContext); without this bridge they would see no
// tenant for JWT-authenticated requests.
//
// No-op when authlet claims are absent (legacy API-key path on the
// same chain — that middleware already populates auth.WithTenantID
// from the API key row) or when claims.Subject isn't a UUID. The AS
// only mints JWTs with sub set to a memory-system tenant UUID via
// MemoryUserResolver, so the parse should never fail for tokens this
// AS issued; failing open here lets unrelated bearer-shaped requests
// fall through to the next handler without surprises.
//
// Email is populated for parity with the API-key path so handlers
// logging caller identity see a non-empty email on JWT requests. The
// email is in the access token's "email" custom claim (set in Setup
// via AdditionalClaims). authlet's jwt.Verify populates Claims.Extra
// with all non-standard claims, so claims.Extra["email"] is the
// validated value from the signed token.
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
				// Resolve the verified email to the unified authorization
				// subject (tenant_users.id). No row -> no subject attached, so
				// Pass 2 fails closed. Skipped when db is nil (unit tests that
				// construct Wiring directly), preserving the tenant+email-only
				// behavior.
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
