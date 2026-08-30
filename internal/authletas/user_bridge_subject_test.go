package authletas

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/eliminyro/authlet/pkg/jwt"
	"github.com/eliminyro/authlet/pkg/rs"
	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/google/uuid"
)

// bridgeResult captures the auth context a request carried past the bridge.
type bridgeResult struct {
	subject   auth.Subject
	subjectOK bool
	tenant    uuid.UUID
	email     string
}

// runBridge drives UserContextBridge with a JWT-claims context (sub plus the
// signed tenant_id/email custom claims) and reports the resolved auth context.
// The bridge is DB-free: it reads everything from the signed token.
func runBridge(t *testing.T, sub string, tid uuid.UUID, email string) bridgeResult {
	t.Helper()
	var res bridgeResult
	inner := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		res.subject, res.subjectOK = auth.SubjectFromContext(r.Context())
		res.tenant = auth.TenantIDFromContext(r.Context())
		res.email = auth.EmailFromContext(r.Context())
	})
	extra := map[string]any{}
	if tid != uuid.Nil {
		extra["tenant_id"] = tid.String()
	}
	if email != "" {
		extra["email"] = email
	}
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	ctx := context.WithValue(req.Context(), rs.ContextKey{}, jwt.Claims{Subject: sub, Extra: extra})
	req = req.WithContext(ctx)
	(&Wiring{}).UserContextBridge()(inner).ServeHTTP(httptest.NewRecorder(), req)
	return res
}

// TestUserContextBridge_ResolvesSubjectFromSub: sub becomes auth.Subject.ID
// directly (no DB) when a valid tenant_id claim is present.
func TestUserContextBridge_ResolvesSubjectFromSub(t *testing.T) {
	tid := uuid.New()
	uid := uuid.New().String()
	res := runBridge(t, uid, tid, "admin@example.com")
	if !res.subjectOK {
		t.Fatal("expected subject to be resolved from sub")
	}
	if res.subject.Type != auth.SubjectTypeUser || res.subject.ID != uid {
		t.Fatalf("subject = %+v, want {user %s}", res.subject, uid)
	}
	if res.tenant != tid {
		t.Fatalf("tenant = %s, want %s", res.tenant, tid)
	}
	if res.email != "admin@example.com" {
		t.Fatalf("email = %q, want admin@example.com", res.email)
	}
}

// TestUserContextBridge_EmptySubNoSubject: a valid tenant_id but empty sub sets
// the tenant yet attaches no subject (Pass 2 fails closed).
func TestUserContextBridge_EmptySubNoSubject(t *testing.T) {
	tid := uuid.New()
	res := runBridge(t, "", tid, "")
	if res.subjectOK {
		t.Fatal("expected no subject when sub is empty")
	}
	if res.tenant != tid {
		t.Fatalf("tenant = %s, want %s", res.tenant, tid)
	}
}

// TestUserContextBridge_MultiUserTenant_SubjectIsPerUser is the bridge-level
// regression for B1: member B's token (sub=idB, tenant_id=T, email=b@x) must
// yield Subject.ID == idB, TenantID == T, email == b@x — the per-user identity,
// never a co-tenant's.
func TestUserContextBridge_MultiUserTenant_SubjectIsPerUser(t *testing.T) {
	tenant := uuid.New()
	const idB = "user-b-id"
	res := runBridge(t, idB, tenant, "b@x")
	if !res.subjectOK {
		t.Fatal("expected subject from sub")
	}
	if res.subject.ID != idB {
		t.Fatalf("subject id = %q, want %q", res.subject.ID, idB)
	}
	if res.tenant != tenant {
		t.Fatalf("tenant = %s, want %s", res.tenant, tenant)
	}
	if res.email != "b@x" {
		t.Fatalf("email = %q, want b@x", res.email)
	}
}
