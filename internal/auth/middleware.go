package auth

import (
	"net/http"
)

// APIKeyMiddleware extracts a Bearer token from the Authorization header,
// validates it, and injects the tenant ID into the request context.
func APIKeyMiddleware(validator *APIKeyValidator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key, err := BearerToken(r)
			if err != nil {
				http.Error(w, err.Error(), http.StatusUnauthorized)
				return
			}

			tenantID, err := validator.ValidateKey(r.Context(), key)
			if err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}

			ctx := WithTenantID(r.Context(), tenantID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
