package auth

import (
	"context"
	"encoding/json"
	"net/http"
)

// KeyValidator resolves an API key to its tenant identity. Defined as an
// interface so middleware can be tested without a real database.
type KeyValidator interface {
	ValidateKey(ctx context.Context, key string) (KeyInfo, error)
}

// APIKeyMiddleware extracts a Bearer token from the Authorization header,
// validates it, and injects the tenant ID into the request context.
//
// On rejection, the response is RFC 6750 §3 shaped: status 401, body
// application/json with error/error_description fields. The WWW-Authenticate
// challenge is added by an outer middleware.
func APIKeyMiddleware(validator KeyValidator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key, err := BearerToken(r)
			if err != nil {
				writeOAuthError(w, "invalid_request", err.Error())
				return
			}

			info, err := validator.ValidateKey(r.Context(), key)
			if err != nil {
				writeOAuthError(w, "invalid_token", "invalid or revoked API key")
				return
			}

			ctx := WithTenantID(r.Context(), info.TenantID)
			ctx = WithEmail(ctx, info.Email)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func writeOAuthError(w http.ResponseWriter, code, description string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":             code,
		"error_description": description,
	})
}
