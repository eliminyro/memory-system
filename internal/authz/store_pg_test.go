//go:build integration

package authz

import (
	"context"
	"os"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// openTestPG returns a Postgres-backed *gorm.DB with the relation_tuples table
// migrated fresh. It reads TEST_DATABASE_URL and skips when unset, e.g.:
//
//	docker run -d --name authz-pg \
//	  -e POSTGRES_USER=memory -e POSTGRES_PASSWORD=memory -e POSTGRES_DB=memory \
//	  -p 5434:5432 pgvector/pgvector:pg17
//	TEST_DATABASE_URL='postgres://memory:memory@localhost:5434/memory?sslmode=disable' \
//	  go test -tags=integration ./internal/authz/...
//
// The table is dropped and recreated per test so runs are isolated (Go runs a
// package's tests sequentially unless t.Parallel is called).
func openTestPG(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; Postgres integration test skipped")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	if err := db.Migrator().DropTable(&RelationTuple{}); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Migrator().DropTable(&RelationTuple{})
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func TestPostgresStore_RoundTrip(t *testing.T) {
	ctx := context.Background()
	db := openTestPG(t)
	s := NewPostgresStore(db)

	tp := tup(TypeTenant, "t1", RelMember, TypeUser, "alice", "")

	// Write is idempotent.
	if err := s.Write(ctx, tp); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := s.Write(ctx, tp); err != nil {
		t.Fatalf("idempotent write: %v", err)
	}

	byObj, err := s.ReadByObjectRelation(ctx, TypeTenant, "t1", RelMember)
	if err != nil {
		t.Fatalf("read by object: %v", err)
	}
	if len(byObj) != 1 || byObj[0] != tp {
		t.Fatalf("read by object = %+v, want exactly [%+v]", byObj, tp)
	}

	bySubj, err := s.ReadBySubject(ctx, TypeUser, "alice")
	if err != nil {
		t.Fatalf("read by subject: %v", err)
	}
	if len(bySubj) != 1 || bySubj[0] != tp {
		t.Fatalf("read by subject = %+v, want exactly [%+v]", bySubj, tp)
	}

	// Delete removes it; deleting again is a no-op.
	if err := s.Delete(ctx, tp); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := s.Delete(ctx, tp); err != nil {
		t.Fatalf("delete absent: %v", err)
	}
	after, _ := s.ReadByObjectRelation(ctx, TypeTenant, "t1", RelMember)
	if len(after) != 0 {
		t.Fatalf("after delete got %d tuples, want 0", len(after))
	}
}

func TestPostgresStore_UsersetSubjectRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := openTestPG(t)
	s := NewPostgresStore(db)

	// Userset subject (non-empty subject_relation) must round-trip.
	tp := tup(TypeDocument, "dgrp", RelViewer, TypeTenant, "t1", RelMember)
	if err := s.Write(ctx, tp); err != nil {
		t.Fatalf("write userset: %v", err)
	}
	got, err := s.ReadByObjectRelation(ctx, TypeDocument, "dgrp", RelViewer)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 1 || got[0] != tp {
		t.Fatalf("userset round-trip = %+v, want [%+v]", got, tp)
	}
}

// TestPostgresStore_EndToEndCheck runs the full recursive Check through the
// Postgres store: a global admin must reach editor on a document in an
// arbitrary tenant via admin-from-system -> member -> member-from-tenant.
func TestPostgresStore_EndToEndCheck(t *testing.T) {
	ctx := context.Background()
	db := openTestPG(t)
	s := NewPostgresStore(db)
	seedWorld(t, s)

	e := NewEngine(s)

	cases := []struct {
		objType, objID, relation, subjID string
		want                             bool
	}{
		{TypeDocument, "d1", RelEditor, "alice", true},   // own workspace
		{TypeDocument, "d2", RelEditor, "alice", false},  // cross-tenant denied
		{TypeDocument, "d2", RelEditor, "gadmin", true},  // global admin spans tenants
		{TypeDocument, "dc", RelViewer, "nobody", true},  // common-pool world read
		{TypeDocument, "dc", RelEditor, "nobody", false}, // common-pool admin-only write
		{TypeDocument, "dgrp", RelViewer, "alice", true}, // userset subject
	}
	for _, c := range cases {
		got, err := e.Check(ctx, c.objType, c.objID, c.relation, TypeUser, c.subjID)
		if err != nil {
			t.Fatalf("Check(%s:%s#%s@user:%s): %v", c.objType, c.objID, c.relation, c.subjID, err)
		}
		if got != c.want {
			t.Errorf("Check(%s:%s#%s@user:%s) = %v, want %v", c.objType, c.objID, c.relation, c.subjID, got, c.want)
		}
	}
}
