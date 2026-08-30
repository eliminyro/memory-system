package authz

import (
	"context"
	"testing"
)

// TestCheck_WildcardSubjectTyping locks that the Check engine honors a user:*
// wildcard tuple ONLY on a relation whose model permits a wildcard subject of
// that type. tenant#viewer lists "user:*" in its DirectSubjects (public read),
// so a wildcard grants there; member/editor do not, so a wildcard tuple written
// on them must NOT grant. This proves the engine enforces the namespace's own
// subject typing rather than honoring any wildcard unconditionally.
func TestCheck_WildcardSubjectTyping(t *testing.T) {
	ctx := context.Background()

	t.Run("wildcard NOT honored on tenant#member", func(t *testing.T) {
		store := NewMemoryStore()
		if err := store.Write(ctx, tup(TypeTenant, "tW", RelMember, TypeUser, Wildcard, "")); err != nil {
			t.Fatalf("seed: %v", err)
		}
		e := NewEngine(store)
		got, err := e.Check(ctx, TypeTenant, "tW", RelMember, TypeUser, "unlisted-user")
		if err != nil {
			t.Fatalf("Check: %v", err)
		}
		if got {
			t.Fatalf("user:* granted member; want deny (member does not permit a wildcard subject)")
		}
	})

	t.Run("wildcard honored on tenant#viewer (public read)", func(t *testing.T) {
		store := NewMemoryStore()
		if err := store.Write(ctx, tup(TypeTenant, "tW", RelViewer, TypeUser, Wildcard, "")); err != nil {
			t.Fatalf("seed: %v", err)
		}
		e := NewEngine(store)
		got, err := e.Check(ctx, TypeTenant, "tW", RelViewer, TypeUser, "anyone")
		if err != nil {
			t.Fatalf("Check: %v", err)
		}
		if !got {
			t.Fatalf("user:* denied viewer; want grant (public read is the one wildcard-typed relation)")
		}
	})

	t.Run("wildcard NOT honored on document#editor", func(t *testing.T) {
		store := NewMemoryStore()
		if err := store.Write(ctx, tup(TypeDocument, "dW", RelEditor, TypeUser, Wildcard, "")); err != nil {
			t.Fatalf("seed: %v", err)
		}
		e := NewEngine(store)
		got, err := e.Check(ctx, TypeDocument, "dW", RelEditor, TypeUser, "unlisted-user")
		if err != nil {
			t.Fatalf("Check: %v", err)
		}
		if got {
			t.Fatalf("user:* granted editor; want deny (editor does not permit a wildcard subject)")
		}
	})
}
