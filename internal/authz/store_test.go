package authz

import (
	"context"
	"testing"
)

func tup(objType, objID, rel, subjType, subjID, subjRel string) Tuple {
	return Tuple{
		ObjectType: objType, ObjectID: objID, Relation: rel,
		SubjectType: subjType, SubjectID: subjID, SubjectRelation: subjRel,
	}
}

func TestMemoryStore_WriteIdempotent(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	tp := tup(TypeSystem, SystemObjectID, RelAdmin, TypeUser, "alice", "")

	if err := s.Write(ctx, tp); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := s.Write(ctx, tp); err != nil {
		t.Fatalf("second write (idempotent): %v", err)
	}

	got, err := s.ReadByObjectRelation(ctx, TypeSystem, SystemObjectID, RelAdmin)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("after duplicate writes got %d tuples, want 1: %+v", len(got), got)
	}
}

func TestMemoryStore_Delete(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	tp := tup(TypeTenant, "t1", RelMember, TypeUser, "alice", "")

	// Deleting an absent tuple is a no-op.
	if err := s.Delete(ctx, tp); err != nil {
		t.Fatalf("delete absent: %v", err)
	}
	if err := s.Write(ctx, tp); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := s.Delete(ctx, tp); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got, _ := s.ReadByObjectRelation(ctx, TypeTenant, "t1", RelMember)
	if len(got) != 0 {
		t.Fatalf("after delete got %d tuples, want 0", len(got))
	}
}

func TestMemoryStore_ReadByObjectRelation(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	_ = s.Write(ctx, tup(TypeTenant, "t1", RelMember, TypeUser, "alice", ""))
	_ = s.Write(ctx, tup(TypeTenant, "t1", RelMember, TypeUser, "bob", ""))
	_ = s.Write(ctx, tup(TypeTenant, "t1", RelAdmin, TypeUser, "carol", ""))
	_ = s.Write(ctx, tup(TypeTenant, "t2", RelMember, TypeUser, "dave", ""))

	got, err := s.ReadByObjectRelation(ctx, TypeTenant, "t1", RelMember)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d tuples, want 2: %+v", len(got), got)
	}
	for _, g := range got {
		if g.ObjectID != "t1" || g.Relation != RelMember {
			t.Errorf("unexpected tuple in result: %+v", g)
		}
	}
}

func TestMemoryStore_ReadBySubject(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	_ = s.Write(ctx, tup(TypeTenant, "t1", RelMember, TypeUser, "alice", ""))
	_ = s.Write(ctx, tup(TypeDocument, "d9", RelViewer, TypeUser, "alice", ""))
	_ = s.Write(ctx, tup(TypeTenant, "t2", RelMember, TypeUser, "bob", ""))

	got, err := s.ReadBySubject(ctx, TypeUser, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d tuples for alice, want 2: %+v", len(got), got)
	}
}

func TestMemoryStore_ReadBySubject_Userset(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	// A userset subject: document:d1#viewer @ tenant:t1#member.
	_ = s.Write(ctx, tup(TypeDocument, "d1", RelViewer, TypeTenant, "t1", RelMember))

	got, err := s.ReadBySubject(ctx, TypeTenant, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SubjectRelation != RelMember {
		t.Fatalf("userset subject not returned correctly: %+v", got)
	}
}
