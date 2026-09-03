package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

type stubValidator struct {
	info KeyInfo
	err  error
}

func (s stubValidator) ValidateKey(ctx context.Context, key string) (KeyInfo, error) {
	return s.info, s.err
}

// 401s from APIKeyMiddleware must be RFC 6750-shaped JSON (error/
// error_description), which stricter clients require.
func TestAPIKeyMiddleware401IsJSON(t *testing.T) {
	cases := []struct {
		name      string
		authValue string
		validator stubValidator
		wantError string
	}{
		{
			name:      "no header",
			authValue: "",
			wantError: "invalid_request",
		},
		{
			name:      "malformed header",
			authValue: "Token abc",
			wantError: "invalid_request",
		},
		{
			name:      "rejected key",
			authValue: "Bearer foo",
			validator: stubValidator{err: errors.New("not found")},
			wantError: "invalid_token",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mw := APIKeyMiddleware(tc.validator)
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			if tc.authValue != "" {
				req.Header.Set("Authorization", tc.authValue)
			}
			rec := httptest.NewRecorder()
			mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Fatal("inner handler should not be called on 401")
			})).ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d want 401", rec.Code)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Fatalf("Content-Type=%q want application/json", ct)
			}
			var body struct {
				Error            string `json:"error"`
				ErrorDescription string `json:"error_description"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("body not JSON: %v (raw=%q)", err, rec.Body.String())
			}
			if body.Error != tc.wantError {
				t.Fatalf("error=%q want %q", body.Error, tc.wantError)
			}
			if body.ErrorDescription == "" {
				t.Fatal("error_description must be non-empty")
			}
		})
	}
}

// Successful auth still propagates tenant context and 200s through.
func TestAPIKeyMiddlewareAllowsValidKey(t *testing.T) {
	tid := uuid.New()
	mw := APIKeyMiddleware(stubValidator{info: KeyInfo{TenantID: tid, Email: "x@y"}})
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer ok")
	rec := httptest.NewRecorder()

	called := false
	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if got := TenantIDFromContext(r.Context()); got != tid {
			t.Fatalf("tenant id=%v want %v", got, tid)
		}
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)

	if !called {
		t.Fatal("inner handler not called")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rec.Code)
	}
}
