// Command memory-admin is a privileged, offline administration CLI for the
// memory system. It talks directly to the database (holding DATABASE_URL is
// already full control) and reuses the exact service methods the network paths
// use, so tenant/user/key lifecycle — including authz tuple seeding — stays in
// one place. The context is marked local-admin (auth.WithLocalAdmin) so the
// service's admin gate honors it without an authenticated Subject.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/database"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/service"
)

const defaultDatabaseURL = "postgres://memory:memory@localhost:5432/memory?sslmode=disable"

// buildService wires a MemoryService against the database and returns a
// local-admin context. Only the repositories the admin operations touch are
// constructed; the rest are nil. It intentionally does NOT call config.Load —
// that validates PUBLIC_BASE_URL for the authlet path, which the CLI must not
// require.
func buildService() (*service.MemoryService, context.Context, error) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = defaultDatabaseURL
	}

	db, err := database.Connect(dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("connect to database: %w", err)
	}

	tenantRepo := repository.NewTenantRepository(db)
	keyRepo := repository.NewAPIKeyRepository(db)
	authzStore := authz.NewPostgresStore(db)

	svc := service.NewMemoryService(db, nil, nil, nil, tenantRepo, keyRepo, nil, nil, nil, nil, authzStore)
	ctx := auth.WithLocalAdmin(context.Background())
	return svc, ctx, nil
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "memory-admin",
		Short: "Offline admin CLI for the memory system (tenants, users, API keys)",
		Long: "memory-admin operates directly against the database and is inherently\n" +
			"privileged. It reuses the same service methods as the network paths, so all\n" +
			"authorization tuples are seeded/unseeded consistently.\n\n" +
			"Database connection is read from DATABASE_URL (default: " + defaultDatabaseURL + ").",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newTenantCmd(), newUserCmd(), newKeyCmd(), newSetupCmd())
	return root
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
