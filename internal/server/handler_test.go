package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
