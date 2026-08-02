package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eliminyro/memory-system/internal/version"
)

func TestHealthHandler(t *testing.T) {
	rec := httptest.NewRecorder()
	healthHandler(rec, httptest.NewRequest(http.MethodGet, "/~/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Body.String(); got != "ok" {
		t.Fatalf("body = %q, want %q", got, "ok")
	}
}

// bypassGlobalBodyCap is the pure predicate deciding which route skips the
// global MaxRequestBytes wrapper. Table-tested directly since standing up the
// full auth stack (authletas.Setup needs a live DB + Google OIDC discovery)
// isn't feasible in a unit test.
func TestBypassGlobalBodyCap(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		want   bool
	}{
		{"import upload exempted", http.MethodPost, "/api/admin/import", true},
		{"wrong method not exempted", http.MethodGet, "/api/admin/import", false},
		{"import status not exempted", http.MethodGet, "/api/admin/import/some-id", false},
		{"other admin route not exempted", http.MethodPost, "/api/admin/tenants", false},
		{"other api route not exempted", http.MethodPost, "/api/documents", false},
		{"unauthenticated route not exempted", http.MethodPost, "/mcp", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := bypassGlobalBodyCap(c.method, c.path); got != c.want {
				t.Errorf("bypassGlobalBodyCap(%q, %q) = %v, want %v", c.method, c.path, got, c.want)
			}
		})
	}
}

// withGlobalBodyCap is exactly what NewHandler composes into its middleware
// stack. Exercising it directly (with a plain echo handler standing in for the
// mux) gives a real, non-flaky assertion of the exemption's effect — no auth
// stack required — without needing the full NewHandler/mux/authlet wiring.
func TestWithGlobalBodyCap_ExemptsOnlyImportUpload(t *testing.T) {
	const cap = 10 // bytes; deliberately tiny
	oversized := strings.Repeat("x", 1000)

	echo := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				w.WriteHeader(http.StatusRequestEntityTooLarge)
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("X-Read-Bytes", strings.Repeat("1", len(b))) // cheap length signal
		w.WriteHeader(http.StatusOK)
	})
	handler := withGlobalBodyCap(echo, cap)

	t.Run("import upload bypasses the global cap", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/admin/import", strings.NewReader(oversized))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code == http.StatusRequestEntityTooLarge {
			t.Fatalf("import upload rejected by global cap, status = %d", rec.Code)
		}
		if got := len(rec.Header().Get("X-Read-Bytes")); got != len(oversized) {
			t.Fatalf("read %d bytes, want the full %d-byte body past the global cap", got, len(oversized))
		}
	})

	t.Run("non-import route stays capped", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/documents", strings.NewReader(oversized))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want %d (global cap should still apply)", rec.Code, http.StatusRequestEntityTooLarge)
		}
	})
}

func TestVersionHandler(t *testing.T) {
	rec := httptest.NewRecorder()
	versionHandler(rec, httptest.NewRequest(http.MethodGet, "/~/version", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q, want application/json", ct)
	}

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if body["version"] != version.Version {
		t.Fatalf("version = %q, want %q", body["version"], version.Version)
	}
	if body["commit"] != version.Commit {
		t.Fatalf("commit = %q, want %q", body["commit"], version.Commit)
	}
}
