package authletas

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWWWAuth401_InjectsChallengeOnUnauth(t *testing.T) {
	const prm = "https://mem.example.test/.well-known/oauth-protected-resource/mcp"
	w := &Wiring{prmURL: prm}
	mw := w.WWWAuth401()

	inner := http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(http.StatusUnauthorized)
		_, _ = rw.Write([]byte(`{"code":"UNAUTHORIZED","error":"authentication required"}`))
	})

	rec := httptest.NewRecorder()
	mw(inner).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: want 401, got %d", rec.Code)
	}
	got := rec.Header().Get("WWW-Authenticate")
	if got == "" {
		t.Fatalf("WWW-Authenticate header missing on 401")
	}
	if !strings.Contains(got, `resource_metadata="`+prm+`"`) {
		t.Fatalf("WWW-Authenticate missing PRM URL; got %q", got)
	}
	if !strings.HasPrefix(got, "Bearer") {
		t.Fatalf("WWW-Authenticate scheme: want Bearer, got %q", got)
	}
}

func TestWWWAuth401_DoesNotTouchSuccessResponses(t *testing.T) {
	w := &Wiring{}
	mw := w.WWWAuth401()

	inner := http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(http.StatusOK)
		_, _ = rw.Write([]byte(`{"ok":true}`))
	})

	rec := httptest.NewRecorder()
	mw(inner).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != "" {
		t.Fatalf("WWW-Authenticate must not be set on 200; got %q", got)
	}
}

func TestWWWAuth401_PreservesUpstreamChallenge(t *testing.T) {
	w := &Wiring{}
	mw := w.WWWAuth401()

	existing := `Bearer error="invalid_token", error_description="token expired", resource_metadata="https://example/.well-known/oauth-protected-resource/mcp"`
	inner := http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("WWW-Authenticate", existing)
		rw.WriteHeader(http.StatusUnauthorized)
	})

	rec := httptest.NewRecorder()
	mw(inner).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))

	if got := rec.Header().Get("WWW-Authenticate"); got != existing {
		t.Fatalf("WWW-Authenticate overwritten; want %q, got %q", existing, got)
	}
}

func TestWWWAuth401_HandlesImplicit200OnWrite(t *testing.T) {
	w := &Wiring{}
	mw := w.WWWAuth401()

	inner := http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		_, _ = rw.Write([]byte(`{"ok":true}`))
	})

	rec := httptest.NewRecorder()
	mw(inner).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mcp", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != "" {
		t.Fatalf("WWW-Authenticate must not appear on implicit 200; got %q", got)
	}
}
