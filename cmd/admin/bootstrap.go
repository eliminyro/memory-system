package main

import (
	"context"
	"fmt"
	"os"

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

// newBootstrapCmd provisions the initial tenant and admin API key: one-shot,
// token-gated first-run provisioning — the same shared core POST /api/bootstrap
// runs. Named "bootstrap", not "setup" (setup.go already owns that name for the
// mcpServers-config emitter).
//
// The token comes from BOOTSTRAP_TOKEN. service.Bootstrap compares the caller's
// token against the *service's own* configured BootstrapToken field, and this
// CLI process shares no memory with the running server — buildService() has no
// way to inherit cfg.BootstrapToken. So this command also assigns the service's
// BootstrapToken from that same env var before calling Bootstrap. In practice
// that means: any non-empty BOOTSTRAP_TOKEN bootstraps an empty instance from
// the CLI (already privileged — it holds DATABASE_URL); leaving it unset still
// fails closed (design D4), matching the HTTP front-end's behavior.
func newBootstrapCmd() *cobra.Command {
	var tenantName, tenantEmail, keyLabel string
	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Provision the initial tenant and admin API key (one-shot, token-gated)",
		Long: "bootstrap runs the same one-shot, token-gated first-run provisioning\n" +
			"core as POST /api/bootstrap. The token is read from BOOTSTRAP_TOKEN and is\n" +
			"also used to configure the service's compare target for this process, so any\n" +
			"non-empty value bootstraps an empty instance; unset fails closed. Errors\n" +
			"non-zero if an admin already exists.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			svc, ctx, err := buildService()
			if err != nil {
				return err
			}
			token := os.Getenv("BOOTSTRAP_TOKEN")
			svc.BootstrapToken = token
			spec := service.BootstrapSpec{TenantName: tenantName, TenantEmail: tenantEmail, KeyLabel: keyLabel}
			return runBootstrap(cmd, svc.Bootstrap, ctx, token, spec)
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
