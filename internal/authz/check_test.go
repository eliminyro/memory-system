package authz

import (
	"context"
	"errors"
	"testing"
	"time"
)

// seedWorld builds a small authorization world exercising every rewrite path:
//
//	system:memory#admin@user:gadmin            -> global admin
//	tenant:t1  (parent system, member alice, admin t1admin)
//	tenant:t2  (parent system, member bob)
//	tenant:tc  (common pool: parent system, viewer user:*, admin cadmin)
//	document:d1   belongs to t1
//	document:d2   belongs to t2
//	document:dc   belongs to tc (common pool)
//	document:dshare belongs to t2, direct viewer grant to alice
//	document:dgrp belongs to t2, viewer granted to the userset tenant:t1#member
func seedWorld(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	writes := []Tuple{
		// global admin
		tup(TypeSystem, SystemObjectID, RelAdmin, TypeUser, "gadmin", ""),

		// tenant t1
		tup(TypeTenant, "t1", RelSystem, TypeSystem, SystemObjectID, ""),
		tup(TypeTenant, "t1", RelMember, TypeUser, "alice", ""),
		tup(TypeTenant, "t1", RelAdmin, TypeUser, "t1admin", ""),

		// tenant t2
		tup(TypeTenant, "t2", RelSystem, TypeSystem, SystemObjectID, ""),
		tup(TypeTenant, "t2", RelMember, TypeUser, "bob", ""),

		// common pool tenant tc
		tup(TypeTenant, "tc", RelSystem, TypeSystem, SystemObjectID, ""),
		tup(TypeTenant, "tc", RelViewer, TypeUser, Wildcard, ""), // public read
		tup(TypeTenant, "tc", RelAdmin, TypeUser, "cadmin", ""),  // only admins write

		// documents -> tenant parent edges
		tup(TypeDocument, "d1", RelTenant, TypeTenant, "t1", ""),
		tup(TypeDocument, "d2", RelTenant, TypeTenant, "t2", ""),
		tup(TypeDocument, "dc", RelTenant, TypeTenant, "tc", ""),

		// direct cross-tenant grant: alice can view dshare (owned by t2)
		tup(TypeDocument, "dshare", RelTenant, TypeTenant, "t2", ""),
		tup(TypeDocument, "dshare", RelViewer, TypeUser, "alice", ""),

		// userset grant: everyone who is a member of t1 can view dgrp (owned by t2)
		tup(TypeDocument, "dgrp", RelTenant, TypeTenant, "t2", ""),
		tup(TypeDocument, "dgrp", RelViewer, TypeTenant, "t1", RelMember),
	}
	for _, w := range writes {
		if err := s.Write(ctx, w); err != nil {
			t.Fatalf("seed write %+v: %v", w, err)
		}
	}
}

func TestCheck_Matrix(t *testing.T) {
	store := NewMemoryStore()
	seedWorld(t, store)
	e := NewEngine(store)
	ctx := context.Background()

	tests := []struct {
		name     string
		objType  string
		objID    string
		relation string
		subjID   string // subject is always user:<subjID>
		want     bool
	}{
		// --- own-workspace view + edit ---
		{"alice views own-tenant doc", TypeDocument, "d1", RelViewer, "alice", true},
		{"alice edits own-tenant doc", TypeDocument, "d1", RelEditor, "alice", true},
		{"alice is member of own tenant", TypeTenant, "t1", RelMember, "alice", true},

		// --- cross-tenant denied for non-admins ---
		{"alice cannot view other-tenant doc", TypeDocument, "d2", RelViewer, "alice", false},
		{"alice cannot edit other-tenant doc", TypeDocument, "d2", RelEditor, "alice", false},
		{"alice not member of other tenant", TypeTenant, "t2", RelMember, "alice", false},
		{"bob cannot view t1 doc", TypeDocument, "d1", RelViewer, "bob", false},

		// --- global admin spans tenants (admin from system) ---
		{"gadmin admin of t1", TypeTenant, "t1", RelAdmin, "gadmin", true},
		{"gadmin admin of t2", TypeTenant, "t2", RelAdmin, "gadmin", true},
		{"gadmin member of t1", TypeTenant, "t1", RelMember, "gadmin", true},
		{"gadmin member of t2", TypeTenant, "t2", RelMember, "gadmin", true},
		{"gadmin edits d1", TypeDocument, "d1", RelEditor, "gadmin", true},
		{"gadmin edits d2", TypeDocument, "d2", RelEditor, "gadmin", true},
		{"gadmin views d2", TypeDocument, "d2", RelViewer, "gadmin", true},

		// --- tenant admin (direct) ---
		{"t1admin admin of t1", TypeTenant, "t1", RelAdmin, "t1admin", true},
		{"t1admin member of t1 (admin->member)", TypeTenant, "t1", RelMember, "t1admin", true},
		{"t1admin edits d1", TypeDocument, "d1", RelEditor, "t1admin", true},
		{"t1admin not admin of t2", TypeTenant, "t2", RelAdmin, "t1admin", false},

		// --- common pool: world-read, admin-only write ---
		{"alice reads common doc (wildcard)", TypeDocument, "dc", RelViewer, "alice", true},
		{"bob reads common doc (wildcard)", TypeDocument, "dc", RelViewer, "bob", true},
		{"stranger reads common doc (wildcard)", TypeDocument, "dc", RelViewer, "nobody", true},
		{"stranger cannot edit common doc", TypeDocument, "dc", RelEditor, "nobody", false},
		{"alice cannot edit common doc", TypeDocument, "dc", RelEditor, "alice", false},
		{"cadmin edits common doc (admin->member)", TypeDocument, "dc", RelEditor, "cadmin", true},
		{"gadmin edits common doc", TypeDocument, "dc", RelEditor, "gadmin", true},

		// --- wildcard at the tenant relation directly ---
		{"stranger is tenant viewer via wildcard", TypeTenant, "tc", RelViewer, "nobody", true},
		{"stranger not tenant viewer of t1", TypeTenant, "t1", RelViewer, "nobody", false},

		// --- direct grants ---
		{"alice views dshare via direct grant", TypeDocument, "dshare", RelViewer, "alice", true},
		{"alice cannot edit dshare (no direct editor, not member t2)", TypeDocument, "dshare", RelEditor, "alice", false},

		// --- userset subject resolution ---
		{"alice views dgrp via userset t1#member", TypeDocument, "dgrp", RelViewer, "alice", true},
		{"t1admin views dgrp via userset (admin->member)", TypeDocument, "dgrp", RelViewer, "t1admin", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := e.Check(ctx, tc.objType, tc.objID, tc.relation, TypeUser, tc.subjID)
			if err != nil {
				t.Fatalf("Check(%s:%s#%s@user:%s) error: %v", tc.objType, tc.objID, tc.relation, tc.subjID, err)
			}
			if got != tc.want {
				t.Errorf("Check(%s:%s#%s@user:%s) = %v, want %v", tc.objType, tc.objID, tc.relation, tc.subjID, got, tc.want)
			}
		})
	}
}

// TestCheck_DepthLimit confirms Check fails closed with a distinguishable error
// when the evaluation exceeds the configured depth cap. The global-admin grant
// resolves through document.editor -> tenant.member -> tenant.admin ->
// system.admin (depth 3), so a cap of 2 forces ErrDepthExceeded.
func TestCheck_DepthLimit(t *testing.T) {
	store := NewMemoryStore()
	seedWorld(t, store)
	e := NewEngine(store)
	e.MaxDepth = 2
	ctx := context.Background()

	got, err := e.Check(ctx, TypeDocument, "d2", RelEditor, TypeUser, "gadmin")
	if got {
		t.Errorf("with depth cap 2, Check granted; want deny (fail closed)")
	}
	if !errors.Is(err, ErrDepthExceeded) {
		t.Errorf("want ErrDepthExceeded, got %v", err)
	}
}

// TestCheck_CycleSafety confirms a userset cycle terminates and denies rather
// than recursing forever.
func TestCheck_CycleSafety(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	// tenant:a#member @ tenant:b#member  and  tenant:b#member @ tenant:a#member
	_ = store.Write(ctx, tup(TypeTenant, "a", RelMember, TypeTenant, "b", RelMember))
	_ = store.Write(ctx, tup(TypeTenant, "b", RelMember, TypeTenant, "a", RelMember))

	e := NewEngine(store)
	done := make(chan struct{})
	var got bool
	var err error
	go func() {
		got, err = e.Check(ctx, TypeTenant, "a", RelMember, TypeUser, "eve")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Check did not terminate on a cycle")
	}
	if err != nil {
		t.Errorf("cycle Check returned error %v, want nil (pruned)", err)
	}
	if got {
		t.Errorf("cycle Check granted; want deny")
	}
}

// TestCheck_UnknownRelation confirms an undefined relation is a distinguishable
// error rather than a silent allow.
func TestCheck_UnknownRelation(t *testing.T) {
	e := NewEngine(NewMemoryStore())
	got, err := e.Check(context.Background(), TypeDocument, "d1", "owner", TypeUser, "alice")
	if got {
		t.Error("unknown relation granted; want deny")
	}
	if !errors.Is(err, ErrUnknownRelation) {
		t.Errorf("want ErrUnknownRelation, got %v", err)
	}
}
