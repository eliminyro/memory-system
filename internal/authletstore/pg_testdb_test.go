//go:build integration

package authletstore

import (
	"context"
	"os"
	"sync"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// authletTables lists every model backing the store, in the order used by
// both AutoMigrate and teardown.
func authletTables() []any {
	return []any{
		&OAuthClient{},
		&OAuthCode{},
		&OAuthRefreshToken{},
		&FamilyRevocation{},
		&AuthletSigningKey{},
	}
}

// openTestPG returns a Postgres-backed *gorm.DB with the authlet tables
// migrated fresh. Unlike openTestDB (sqlite :memory: capped at one
// connection, which serializes every transaction), Postgres runs the
// goroutines under real READ COMMITTED concurrency — the only way to
// exercise the check-then-act races in ConsumeOnce / MarkUsed.
//
// It reads TEST_DATABASE_URL and skips the test if it is unset, e.g.:
//
//	docker run -d --name memsys-authlet-pg \
//	  -e POSTGRES_USER=memory -e POSTGRES_PASSWORD=memory -e POSTGRES_DB=memory \
//	  -p 5433:5432 pgvector/pgvector:pg17
//	TEST_DATABASE_URL='postgres://memory:memory@localhost:5433/memory?sslmode=disable' \
//	  go test -tags=integration ./internal/authletstore/...
//
// Tables live in the default (public) schema; each test drops and re-creates
// them so runs are isolated. Go runs tests in a package sequentially unless
// t.Parallel is called, so the shared schema is safe here.
func openTestPG(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; a Postgres DB is required to exercise the READ COMMITTED race (sqlite serializes transactions)")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	tables := authletTables()
	// Clean slate, tolerating leftovers from a previously aborted run.
	if err := db.Migrator().DropTable(tables...); err != nil {
		t.Fatalf("drop tables: %v", err)
	}
	if err := db.AutoMigrate(tables...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Migrator().DropTable(tables...)
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// warmPool caps the pool at n and pre-opens n idle connections. Establishing
// a connection to a remote Postgres costs a full round-trip; a cold pool
// staggers goroutine startup so much that the first redemption's transaction
// commits before the others even begin, hiding the race. Pre-warming lets all
// n workers grab a live connection instantly and begin their transactions
// together, so their (unlocked) reads overlap and the check-then-act window is
// actually exercised.
func warmPool(t *testing.T, db *gorm.DB, n int) {
	t.Helper()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(n)
	sqlDB.SetMaxIdleConns(n)

	ctx := context.Background()
	var ready, wg sync.WaitGroup
	release := make(chan struct{})
	ready.Add(n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := sqlDB.Conn(ctx)
			if err != nil {
				ready.Done()
				return
			}
			_ = conn.PingContext(ctx)
			ready.Done()
			<-release        // hold the physical connection open
			_ = conn.Close() // return it to the idle pool
		}()
	}
	ready.Wait()   // n physical connections now established and held
	close(release) // release them back to the pool as idle
	wg.Wait()
}
