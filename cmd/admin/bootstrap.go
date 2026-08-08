package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/service"
)

// bootstrapFunc matches service.MemoryService.Bootstrap's signature. The
// command's core logic (runBootstrap) depends on this narrow function type
// rather than *service.MemoryService directly, so output formatting and error
// handling are unit-testable with a stub — no database required (buildService
// dials a real Postgres connection, which the RunE below is the only thing
// that touches).
type bootstrapFunc func(ctx context.Context, token string, spec service.BootstrapSpec) (string, *models.APIKey, error)

// newBootstrapCmd provisions the initial tenant and admin API key: one-shot
// first-run provisioning — the same shared core POST /bootstrap runs. Named
// "bootstrap", not "setup" (setup.go already owns that name for the
// mcpServers-config emitter).
//
// buildService returns a local-admin context (auth.WithLocalAdmin), and
// service.Bootstrap skips the token gate for local admins (design D2) — the CLI
// talks directly to the DB and is inherently privileged (holding DATABASE_URL is
// full control). So this command provisions directly with no token; it still
// errors non-zero if an admin already exists.
func newBootstrapCmd() *cobra.Command {
	var tenantName, tenantEmail, keyLabel string
	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Provision the initial tenant and admin API key (one-shot)",
		Long: "bootstrap runs the same one-shot first-run provisioning core as\n" +
			"POST /bootstrap. It runs under a local-admin context and is inherently\n" +
			"privileged (it holds DATABASE_URL), so no bootstrap token is required.\n" +
			"Errors non-zero if an admin already exists.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			svc, ctx, err := buildService()
			if err != nil {
				return err
			}
			// ctx is local-admin, so Bootstrap bypasses the token gate — pass none.
			spec := service.BootstrapSpec{TenantName: tenantName, TenantEmail: tenantEmail, KeyLabel: keyLabel}
			return runBootstrap(cmd, svc.Bootstrap, ctx, "", spec)
		},
	}
	cmd.Flags().StringVar(&tenantName, "tenant-name", "", "Initial tenant name (default \"admin\")")
	cmd.Flags().StringVar(&tenantEmail, "tenant-email", "", "Initial tenant contact email")
	cmd.Flags().StringVar(&keyLabel, "key-label", "", "Admin API key label (default \"admin\")")
	return cmd
}

// runBootstrap is the testable core: given a Bootstrap-shaped function it
// provisions and prints the plaintext admin key to stdout exactly once, or
// returns the error (already-bootstrapped / forbidden) so main.go's Execute
// wrapper reports it and exits non-zero.
func runBootstrap(cmd *cobra.Command, bootstrap bootstrapFunc, ctx context.Context, token string, spec service.BootstrapSpec) error {
	plaintext, key, err := bootstrap(ctx, token, spec)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	_, _ = fmt.Fprintln(out, "Bootstrap succeeded — admin API key (shown only once, store it now):")
	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintln(out, "  "+plaintext)
	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintf(out, "  tenant_id: %s\n  key_id:    %s\n  label:     %s\n", key.TenantID, key.ID, key.Label)
	return nil
}
