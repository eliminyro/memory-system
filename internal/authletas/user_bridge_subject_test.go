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
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// openBridgeTestDB returns a sqlite tenant_users table whose id column is a
// string (matching the production uuid text id the bridge reads as the
// subject).
func openBridgeTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	type turow struct {
		ID       string `gorm:"column:id;primaryKey"`
		Email    string `gorm:"column:email"`
		TenantID string `gorm:"column:tenant_id"`
		Role     string `gorm:"column:role"`
	}
	if err := db.Table("tenant_users").AutoMigrate(&turow{}); err != nil {
		t.Fatal(err)
	}
	return db
}

// runBridge drives UserContextBridge with a JWT-claims context and reports the
// resolved auth.Subject.
func runBridge(t *testing.T, w *Wiring, tid uuid.UUID, email string) (auth.Subject, bool) {
	t.Helper()
	var subj auth.Subject
	var ok bool
	inner := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		subj, ok = auth.SubjectFromContext(r.Context())
	})
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	extra := map[string]any{}
	if email != "" {
		extra["email"] = email
	}
	ctx := context.WithValue(req.Context(), rs.ContextKey{}, jwt.Claims{Subject: tid.String(), Extra: extra})
	req = req.WithContext(ctx)
	w.UserContextBridge()(inner).ServeHTTP(httptest.NewRecorder(), req)
	return subj, ok
}

func TestUserContextBridge_ResolvesSubjectFromEmail(t *testing.T) {
	db := openBridgeTestDB(t)
	tid := uuid.New()
	uid := uuid.New().String()
	if err := db.Exec(
		"INSERT INTO tenant_users (id, email, tenant_id, role) VALUES (?, ?, ?, 'member')",
		uid, "pe@avantistudios.ai", tid.String(),
	).Error; err != nil {
		t.Fatal(err)
	}

	subj, ok := runBridge(t, &Wiring{db: db}, tid, "pe@avantistudios.ai")
	if !ok {
		t.Fatal("expected subject to be resolved from verified email")
	}
	if subj.Type != auth.SubjectTypeUser || subj.ID != uid {
		t.Fatalf("subject = %+v, want {user %s}", subj, uid)
	}
}

func TestUserContextBridge_NoTenantUserRowNoSubject(t *testing.T) {
	db := openBridgeTestDB(t)
	if _, ok := runBridge(t, &Wiring{db: db}, uuid.New(), "stranger@example.com"); ok {
		t.Fatal("expected no subject when no tenant_user row exists (fail closed)")
	}
}

func TestUserContextBridge_NilDBNoSubject(t *testing.T) {
	if _, ok := runBridge(t, &Wiring{}, uuid.New(), "pe@avantistudios.ai"); ok {
		t.Fatal("expected no subject when db is nil")
	}
}
