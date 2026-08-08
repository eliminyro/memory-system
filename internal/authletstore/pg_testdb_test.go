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

// authletTables lists every model backing the store, in AutoMigrate/teardown order.
func authletTables() []any {
	return []any{
		&OAuthClient{},
		&OAuthCode{},
		&OAuthRefreshToken{},
		&FamilyRevocation{},
		&AuthletSigningKey{},
	}
}

// openTestPG returns a Postgres-backed *gorm.DB with the authlet tables migrated
// fresh. Unlike openTestDB (sqlite :memory:, one connection, serializes every
// txn), Postgres gives real READ COMMITTED concurrency — the only way to exercise
// the check-then-act races in ConsumeOnce / MarkUsed.
//
// Reads TEST_DATABASE_URL and skips if unset, e.g.:
//
//	docker run -d --name memsys-authlet-pg \
//	  -e POSTGRES_USER=memory -e POSTGRES_PASSWORD=memory -e POSTGRES_DB=memory \
//	  -p 5433:5432 pgvector/pgvector:pg17
//	TEST_DATABASE_URL='postgres://memory:memory@localhost:5433/memory?sslmode=disable' \
//	  go test -tags=integration ./internal/authletstore/...
//
// Each test drops and re-creates the tables (public schema) for isolation; the
// package runs tests sequentially, so the shared schema is safe.
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

// warmPool caps the pool at n and pre-opens n idle connections. A cold pool
// staggers goroutine startup (each remote connection costs a round-trip), so the
// first txn commits before others begin and the race hides. Pre-warming lets all
// n workers start their txns together so their unlocked reads overlap.
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
