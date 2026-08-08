package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/eliminyro/memory-system/internal/service"
)

func newTenantCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tenant",
		Short: "Manage tenants",
	}
	cmd.AddCommand(
		newTenantCreateCmd(),
		newTenantListCmd(),
		newTenantUpdateCmd(),
		newTenantDeleteCmd(),
	)
	return cmd
}

func newTenantCreateCmd() *cobra.Command {
	var name, email, tenantType string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a tenant",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, ctx, err := buildService()
			if err != nil {
				return err
			}
			// An empty --type is omitted so the service default (shared)
			// applies; a supplied value is validated in-service.
			var opts []string
			if tenantType != "" {
				opts = append(opts, tenantType)
			}
			t, err := svc.CreateTenant(ctx, name, email, opts...)
			if err != nil {
				return err
			}
			fmt.Printf("Created tenant\n  id:   %s\n  name: %s\n  type: %s\n", t.ID, t.Name, t.Type)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Tenant name (required)")
	cmd.Flags().StringVar(&email, "email", "", "Tenant contact email")
	cmd.Flags().StringVar(&tenantType, "type", "", "Tenant type: personal|shared (default shared)")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func newTenantListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List tenants",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, ctx, err := buildService()
			if err != nil {
				return err
			}
			tenants, err := svc.ListTenants(ctx)
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "ID\tNAME\tEMAIL")
			for _, t := range tenants {
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", t.ID, t.Name, t.Email)
			}
			return w.Flush()
		},
	}
}

func newTenantUpdateCmd() *cobra.Command {
	var (
		idStr, name, email, tenantType, staleness string
		dupGuard, cleanupScan                     bool
	)
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update a tenant (only the flags you pass are changed)",
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := uuid.Parse(idStr)
			if err != nil {
				return fmt.Errorf("invalid --id: %w", err)
			}
			svc, ctx, err := buildService()
			if err != nil {
				return err
			}
			var fields service.UpdateTenantFields
			if cmd.Flags().Changed("name") {
				fields.Name = &name
			}
			if cmd.Flags().Changed("email") {
				fields.Email = &email
			}
			if cmd.Flags().Changed("type") {
				fields.Type = &tenantType
			}
			if cmd.Flags().Changed("staleness") {
				fields.StalenessMode = &staleness
			}
			if cmd.Flags().Changed("duplicate-guard") {
				fields.DuplicateGuard = &dupGuard
			}
			if cmd.Flags().Changed("cleanup-scan") {
				fields.CleanupScanEnabled = &cleanupScan
			}
			t, err := svc.UpdateTenant(ctx, id, fields)
			if err != nil {
				return err
			}
			fmt.Printf("Updated tenant\n  id:              %s\n  name:            %s\n  email:           %s\n  type:            %s\n  staleness:       %s\n  duplicate_guard: %t\n  cleanup_scan:    %t\n",
				t.ID, t.Name, t.Email, t.Type, t.StalenessMode, t.DuplicateGuard, t.CleanupScanEnabled)
			return nil
		},
	}
	cmd.Flags().StringVar(&idStr, "id", "", "Tenant UUID (required)")
	cmd.Flags().StringVar(&name, "name", "", "New tenant name")
	cmd.Flags().StringVar(&email, "email", "", "New contact email")
	cmd.Flags().StringVar(&tenantType, "type", "", "Tenant type: personal|shared")
	cmd.Flags().StringVar(&staleness, "staleness", "", "Staleness mode: off|advisory|hard")
	cmd.Flags().BoolVar(&dupGuard, "duplicate-guard", false, "Enable/disable near-duplicate write guard")
	cmd.Flags().BoolVar(&cleanupScan, "cleanup-scan", false, "Enable/disable nightly cleanup scan")
	_ = cmd.MarkFlagRequired("id")
	return cmd
}

func newTenantDeleteCmd() *cobra.Command {
	var idStr string
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete a tenant (the bootstrap tenant cannot be deleted)",
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := uuid.Parse(idStr)
			if err != nil {
				return fmt.Errorf("invalid --id: %w", err)
			}
			svc, ctx, err := buildService()
			if err != nil {
				return err
			}
			if err := svc.DeleteTenant(ctx, id); err != nil {
				return err
			}
			fmt.Printf("Deleted tenant %s\n", id)
			return nil
		},
	}
	cmd.Flags().StringVar(&idStr, "id", "", "Tenant UUID (required)")
	_ = cmd.MarkFlagRequired("id")
	return cmd
}
