package authletas

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLooksLikeJWT(t *testing.T) {
	cases := map[string]bool{
		"a.b.c":              true,
		"header.payload.sig": true,
		"a.b":                false,
		"a.b.c.d":            false,
		"opaque-token":       false,
		"":                   false,
		".":                  false,
		"..":                 true, // edge: two dots, three (empty) segments
	}
	for in, want := range cases {
		if got := looksLikeJWT(in); got != want {
			t.Errorf("looksLikeJWT(%q) = %v, want %v", in, got, want)
		}
	}
}

// markerMW returns a middleware that sets the X-Path response header to
// label, so tests can assert which branch handled the request.
func markerMW(label string) func(http.Handler) http.Handler {
	return func(_ http.Handler) http.Handler {
		return http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
			rw.Header().Set("X-Path", label)
			rw.WriteHeader(http.StatusOK)
		})
	}
}

func TestDualAuth_BearerJWT_RoutedToBearerMW(t *testing.T) {
	w := &Wiring{BearerMW: markerMW("bearer")}
	mw := w.DualAuth(markerMW("legacy"))
	h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer aaa.bbb.ccc")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Path"); got != "bearer" {
		t.Fatalf("X-Path = %q, want bearer", got)
	}
}

func TestDualAuth_OpaqueBearer_RoutedToLegacy(t *testing.T) {
	w := &Wiring{BearerMW: markerMW("bearer")}
	mw := w.DualAuth(markerMW("legacy"))
	h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer memory_apikey_xyz")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Path"); got != "legacy" {
		t.Fatalf("X-Path = %q, want legacy", got)
	}
}

func TestDualAuth_NoAuthHeader_RoutedToLegacy(t *testing.T) {
	w := &Wiring{BearerMW: markerMW("bearer")}
	mw := w.DualAuth(markerMW("legacy"))
	h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Path"); got != "legacy" {
		t.Fatalf("X-Path = %q, want legacy", got)
	}
}
