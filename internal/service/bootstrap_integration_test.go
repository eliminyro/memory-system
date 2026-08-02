//go:build integration

package service_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/authzseed"
	"github.com/eliminyro/memory-system/internal/service"
)

// clearAdmins removes every system:memory#admin tuple so a test starts from an
// un-bootstrapped instance. system:memory is a global singleton object (not
// tenant-scoped), so it cannot be isolated by a random UUID like other fixtures;
// the integration suite runs with -p 1, so this global reset is safe. Cleanup
// re-runs it so the reset does not leak into later tests.
func clearAdmins(t *testing.T, db *gorm.DB) {
	t.Helper()
	del := func() {
		db.Exec("DELETE FROM relation_tuples WHERE object_type = ? AND object_id = ? AND relation = ?",
			authz.TypeSystem, authz.SystemObjectID, authz.RelAdmin)
	}
	del()
	t.Cleanup(del)
}

func systemAdminTuples(t *testing.T, store authz.Store) []authz.Tuple {
	t.Helper()
	got, err := store.ReadByObjectRelation(context.Background(), authz.TypeSystem, authz.SystemObjectID, authz.RelAdmin)
	require.NoError(t, err)
	return got
}

// TestBootstrapEmptyInstanceValidToken: an empty instance with the correct token
// provisions the first tenant + admin key and seeds system:memory#admin.
func TestBootstrapEmptyInstanceValidToken(t *testing.T) {
	db := openServicePG(t)
	clearAdmins(t, db)
	store := authz.NewPostgresStore(db)
	svc := newAdminTestSvc(db, store)
	svc.BootstrapToken = "s3cr3t"

	spec := service.BootstrapSpec{
		TenantName:  "admin-" + uuid.NewString(),
		TenantEmail: uuid.NewString() + "@example.test",
		KeyLabel:    "admin",
	}
	plaintext, key, err := svc.Bootstrap(context.Background(), "s3cr3t", spec)
	require.NoError(t, err)
	require.NotEmpty(t, plaintext)
	require.NotNil(t, key)

	has, err := svc.HasAnyAdmin(context.Background())
	require.NoError(t, err)
	require.True(t, has)

	// The admin tuple is granted to the new key's subject (its tenant svc principal).
	requireTuple(t, store, authzseed.SystemAdmin(authzseed.APIKeySubjectID(*key)))
}

// TestBootstrapAlreadyAdminRejects: bootstrap is one-shot — a second attempt on an
// instance that already has an admin is rejected and provisions nothing.
func TestBootstrapAlreadyAdminRejects(t *testing.T) {
	db := openServicePG(t)
	clearAdmins(t, db)
	store := authz.NewPostgresStore(db)
	svc := newAdminTestSvc(db, store)
	svc.BootstrapToken = "s3cr3t"

	_, _, err := svc.Bootstrap(context.Background(), "s3cr3t", service.BootstrapSpec{
		TenantName: "admin-" + uuid.NewString(),
	})
	require.NoError(t, err)

	before := len(systemAdminTuples(t, store))

	plaintext, key, err := svc.Bootstrap(context.Background(), "s3cr3t", service.BootstrapSpec{
		TenantName: "admin-" + uuid.NewString(),
	})
	require.ErrorIs(t, err, service.ErrAlreadyBootstrapped)
	require.Empty(t, plaintext)
	require.Nil(t, key)
	require.Equal(t, before, len(systemAdminTuples(t, store)), "rejected bootstrap must not add an admin")
}

// TestBootstrapRace: many concurrent valid-token bootstraps against an empty
// instance yield exactly one admin (advisory-lock serialization, design D2).
func TestBootstrapRace(t *testing.T) {
	db := openServicePG(t)
	clearAdmins(t, db)
	store := authz.NewPostgresStore(db)
	svc := newAdminTestSvc(db, store)
	svc.BootstrapToken = "s3cr3t"

	const n = 8
	errs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			_, _, errs[i] = svc.Bootstrap(context.Background(), "s3cr3t", service.BootstrapSpec{
				TenantName: "admin-" + uuid.NewString(),
			})
		}(i)
	}
	wg.Wait()

	var success, rejected int
	for _, err := range errs {
		switch {
		case err == nil:
			success++
		case errors.Is(err, service.ErrAlreadyBootstrapped):
			rejected++
		default:
			t.Fatalf("unexpected bootstrap error: %v", err)
		}
	}
	require.Equal(t, 1, success, "exactly one bootstrap must win the race")
	require.Equal(t, n-1, rejected, "all other bootstraps must be rejected as already-bootstrapped")
	require.Len(t, systemAdminTuples(t, store), 1, "exactly one system#admin tuple must exist")
}

// TestBootstrapNoLog: the plaintext admin key is never written to the log on a
// successful bootstrap.
func TestBootstrapNoLog(t *testing.T) {
	db := openServicePG(t)
	clearAdmins(t, db)
	store := authz.NewPostgresStore(db)
	svc := newAdminTestSvc(db, store)
	svc.BootstrapToken = "s3cr3t"

	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })

	plaintext, _, err := svc.Bootstrap(context.Background(), "s3cr3t", service.BootstrapSpec{
		TenantName: "admin-" + uuid.NewString(),
	})
	require.NoError(t, err)
	require.NotEmpty(t, plaintext)
	require.NotContains(t, buf.String(), plaintext, "plaintext admin key must never be logged")
}
