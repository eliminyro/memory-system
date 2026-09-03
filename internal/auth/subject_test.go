package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func TestSubjectContextRoundTrip(t *testing.T) {
	ctx := context.Background()
	if _, ok := SubjectFromContext(ctx); ok {
		t.Fatal("empty context should carry no subject")
	}
	want := Subject{Type: SubjectTypeUser, ID: "tu-123"}
	ctx = WithSubject(ctx, want)
	got, ok := SubjectFromContext(ctx)
	if !ok {
		t.Fatal("subject not found after WithSubject")
	}
	if got != want {
		t.Fatalf("subject = %+v, want %+v", got, want)
	}
}

// keySubjectID is the API-key subject resolver: explicit subject_id wins, else
// the tenant service principal svc:<tenant_id>.
func TestKeySubjectID(t *testing.T) {
	tid := uuid.New()
	svc := "svc:" + tid.String()

	if got := keySubjectID(nil, tid); got != svc {
		t.Fatalf("nil subject: got %q, want %q", got, svc)
	}
	empty := ""
	if got := keySubjectID(&empty, tid); got != svc {
		t.Fatalf("empty subject: got %q, want %q", got, svc)
	}
	explicit := "tu-abc"
	if got := keySubjectID(&explicit, tid); got != explicit {
		t.Fatalf("explicit subject: got %q, want %q", got, explicit)
	}
}

// The middleware attaches a unified Subject from the resolved KeyInfo.
func TestAPIKeyMiddlewareSetsSubject(t *testing.T) {
	tid := uuid.New()
	mw := APIKeyMiddleware(stubValidator{info: KeyInfo{TenantID: tid, Email: "x@y", SubjectID: "tu-42"}})
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer ok")
	rec := httptest.NewRecorder()

	var got Subject
	var ok bool
	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok = SubjectFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)

	if !ok {
		t.Fatal("subject not set in context")
	}
	if got.Type != SubjectTypeUser || got.ID != "tu-42" {
		t.Fatalf("subject = %+v, want {user tu-42}", got)
	}
}

// An empty SubjectID (should not happen in practice) leaves no subject rather
// than attaching a blank one.
func TestAPIKeyMiddlewareNoSubjectWhenEmpty(t *testing.T) {
	mw := APIKeyMiddleware(stubValidator{info: KeyInfo{TenantID: uuid.New(), Email: "x@y"}})
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer ok")
	rec := httptest.NewRecorder()

	var ok bool
	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, ok = SubjectFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)

	if ok {
		t.Fatal("expected no subject when SubjectID is empty")
	}
}
