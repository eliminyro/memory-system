package auth

import (
	"net/http"
	"strings"

	"google.golang.org/api/idtoken"
)

// OIDCMiddleware validates Google ID tokens from the Authorization header.
// If audience is empty, validation is skipped (local dev mode).
func OIDCMiddleware(audience string, allowedEmails []string) func(http.Handler) http.Handler {
	emailSet := make(map[string]struct{}, len(allowedEmails))
	for _, e := range allowedEmails {
		emailSet[strings.TrimSpace(e)] = struct{}{}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip validation in local dev mode
			if audience == "" {
				next.ServeHTTP(w, r)
				return
			}

			token, err := BearerToken(r)
			if err != nil {
				http.Error(w, err.Error(), http.StatusUnauthorized)
				return
			}

			payload, err := idtoken.Validate(r.Context(), token, audience)
			if err != nil {
				http.Error(w, "invalid ID token", http.StatusUnauthorized)
				return
			}

			email, _ := payload.Claims["email"].(string)
			email = strings.TrimSpace(email)
			if email == "" {
				http.Error(w, "token missing email claim", http.StatusForbidden)
				return
			}

			if len(emailSet) > 0 {
				if _, ok := emailSet[email]; !ok {
					http.Error(w, "email not authorized", http.StatusForbidden)
					return
				}
			}

			next.ServeHTTP(w, r)
		})
	}
}
