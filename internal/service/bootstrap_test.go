package service_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/authzseed"
	"github.com/eliminyro/memory-system/internal/service"
)

// newBootstrapUnitSvc builds a service wired only with an authz store and a
// configured bootstrap token. db/repos are nil: the paths under test here
// (HasAnyAdmin, and every Bootstrap rejection) never touch the database.
func newBootstrapUnitSvc(store authz.Store, token string) *service.MemoryService {
	svc := service.NewMemoryService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, store)
	svc.BootstrapToken = token
	return svc
}

// TestHasAnyAdmin proves the derived "is this instance bootstrapped?" signal:
// true iff some subject holds system:memory#admin.
func TestHasAnyAdmin(t *testing.T) {
	cases := []struct {
		name string
		seed []authz.Tuple
		want bool
	}{
		{"empty store", nil, false},
		{"system admin present", []authz.Tuple{authzseed.SystemAdmin("svc:" + uuid.NewString())}, true},
		{"unrelated tuple only", []authz.Tuple{authzseed.TenantMember(uuid.New(), "svc:x")}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := authz.NewMemoryStore()
			for _, tp := range c.seed {
				require.NoError(t, store.Write(context.Background(), tp))
			}
			svc := newBootstrapUnitSvc(store, "tok")
			got, err := svc.HasAnyAdmin(context.Background())
			require.NoError(t, err)
			require.Equal(t, c.want, got)
		})
	}
}

// TestBootstrapTokenGates covers every fail-closed rejection: empty caller token,
// unset configured token, both empty, and a mismatched token. None may provision.
func TestBootstrapTokenGates(t *testing.T) {
	cases := []struct {
		name       string
		configured string
		token      string
	}{
		{"empty caller token", "s3cr3t", ""},
		{"unset configured token fails closed", "", "s3cr3t"},
		{"both empty", "", ""},
		{"mismatched token", "s3cr3t", "wrong"},
		{"length-differing token", "s3cr3t", "s"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := authz.NewMemoryStore()
			svc := newBootstrapUnitSvc(store, c.configured)

			plaintext, key, err := svc.Bootstrap(context.Background(), c.token, service.BootstrapSpec{})
			require.ErrorIs(t, err, service.ErrBootstrapForbidden)
			require.Empty(t, plaintext, "rejection must not return a plaintext key")
			require.Nil(t, key)

			// One-shot invariant untouched: no admin was created.
			has, herr := svc.HasAnyAdmin(context.Background())
			require.NoError(t, herr)
			require.False(t, has)
		})
	}
}
