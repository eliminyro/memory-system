package mcp

import (
	"context"
	"testing"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/authzseed"
)

// TestAdminSplitByCheck proves the admin/regular surface split is decided by a
// system:memory#admin Check on the subject, not an email allowlist. In-memory store.
func TestAdminSplitByCheck(t *testing.T) {
	store := authz.NewMemoryStore()
	ctx := context.Background()

	adminID := "admin-subject"
	if err := store.Write(ctx, authzseed.SystemAdmin(adminID)); err != nil {
		t.Fatalf("seed admin tuple: %v", err)
	}
	engine := authz.NewEngine(store)

	// nil memory service is fine: the admin split only consults the checker.
	srv := NewServer(nil, engine)

	cases := []struct {
		name string
		ctx  context.Context
		want bool
	}{
		{
			name: "global admin subject",
			ctx:  auth.WithSubject(ctx, auth.Subject{Type: auth.SubjectTypeUser, ID: adminID}),
			want: true,
		},
		{
			name: "non-admin subject",
			ctx:  auth.WithSubject(ctx, auth.Subject{Type: auth.SubjectTypeUser, ID: "regular-user"}),
			want: false,
		},
		{
			name: "subjectless request (fail closed)",
			ctx:  ctx,
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := srv.isAdmin(tc.ctx); got != tc.want {
				t.Fatalf("isAdmin = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestAdminSplitNilCheckerFailsClosed: with no checker wired, every request
// falls back to the regular (non-admin) surface.
func TestAdminSplitNilCheckerFailsClosed(t *testing.T) {
	srv := NewServer(nil, nil)
	admin := auth.WithSubject(context.Background(), auth.Subject{Type: auth.SubjectTypeUser, ID: "anyone"})
	if srv.isAdmin(admin) {
		t.Fatal("nil checker must never grant admin")
	}
}
