// Command memory-admin is a privileged offline admin CLI. It talks directly to
// the DB and reuses the network paths' service methods, so tenant/user/key
// lifecycle (incl. authz tuple seeding) stays in one place. Context is local-admin
// (auth.WithLocalAdmin) — admin gate honored without an authenticated Subject.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/database"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/service"
)

const defaultDatabaseURL = "postgres://memory:memory@localhost:5432/memory?sslmode=disable"

// buildService wires a MemoryService and returns a local-admin context. Only the
// repos admin ops touch are constructed; rest nil. It deliberately skips
// config.Load (which requires PUBLIC_BASE_URL for authlet) — the CLI must not need it.
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

	svc := service.NewMemoryService(db, nil, nil, nil, tenantRepo, keyRepo, nil, nil, nil, nil, nil, authzStore)
	// No config.Load here, so stamp the built-in safe bundle onto CLI-created tenants.
	svc.TenantDefaults = models.BaselineTenantDefaults()
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
	root.AddCommand(newTenantCmd(), newUserCmd(), newKeyCmd(), newSetupCmd(), newBootstrapCmd())
	return root
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
