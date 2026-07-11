package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

func newUserCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "user",
		Short: "Manage tenant users",
	}
	cmd.AddCommand(
		newUserGrantCmd(),
		newUserListCmd(),
		newUserSetRoleCmd(),
		newUserRevokeCmd(),
	)
	return cmd
}

func newUserGrantCmd() *cobra.Command {
	var email, tenantStr, role string
	cmd := &cobra.Command{
		Use:   "grant",
		Short: "Grant a user access to a tenant",
		RunE: func(cmd *cobra.Command, args []string) error {
			tenantID, err := uuid.Parse(tenantStr)
			if err != nil {
				return fmt.Errorf("invalid --tenant: %w", err)
			}
			svc, ctx, err := buildService()
			if err != nil {
				return err
			}
			tu, err := svc.GrantTenantUser(ctx, email, tenantID, role)
			if err != nil {
				return err
			}
			fmt.Printf("Granted user\n  id:     %s\n  email:  %s\n  tenant: %s\n  role:   %s\n",
				tu.ID, tu.Email, tu.TenantID, tu.Role)
			return nil
		},
	}
	cmd.Flags().StringVar(&email, "email", "", "User email (required)")
	cmd.Flags().StringVar(&tenantStr, "tenant", "", "Tenant UUID (required)")
	cmd.Flags().StringVar(&role, "role", "member", "Role: member|admin")
	_ = cmd.MarkFlagRequired("email")
	_ = cmd.MarkFlagRequired("tenant")
	return cmd
}

func newUserListCmd() *cobra.Command {
	var tenantStr string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List users of a tenant",
		RunE: func(cmd *cobra.Command, args []string) error {
			tenantID, err := uuid.Parse(tenantStr)
			if err != nil {
				return fmt.Errorf("invalid --tenant: %w", err)
			}
			svc, ctx, err := buildService()
			if err != nil {
				return err
			}
			users, err := svc.ListTenantUsers(ctx, tenantID)
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "EMAIL\tROLE\tID")
			for _, u := range users {
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", u.Email, u.Role, u.ID)
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVar(&tenantStr, "tenant", "", "Tenant UUID (required)")
	_ = cmd.MarkFlagRequired("tenant")
	return cmd
}

func newUserSetRoleCmd() *cobra.Command {
	var email, role string
	cmd := &cobra.Command{
		Use:   "set-role",
		Short: "Change a user's role",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, ctx, err := buildService()
			if err != nil {
				return err
			}
			tu, err := svc.UpdateTenantUserRole(ctx, email, role)
			if err != nil {
				return err
			}
			fmt.Printf("Updated role\n  email: %s\n  role:  %s\n", tu.Email, tu.Role)
			return nil
		},
	}
	cmd.Flags().StringVar(&email, "email", "", "User email (required)")
	cmd.Flags().StringVar(&role, "role", "", "Role: member|admin (required)")
	_ = cmd.MarkFlagRequired("email")
	_ = cmd.MarkFlagRequired("role")
	return cmd
}

func newUserRevokeCmd() *cobra.Command {
	var email string
	cmd := &cobra.Command{
		Use:   "revoke",
		Short: "Revoke a user's tenant access",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, ctx, err := buildService()
			if err != nil {
				return err
			}
			if err := svc.RevokeTenantUser(ctx, email); err != nil {
				return err
			}
			fmt.Printf("Revoked user %s\n", email)
			return nil
		},
	}
	cmd.Flags().StringVar(&email, "email", "", "User email (required)")
	_ = cmd.MarkFlagRequired("email")
	return cmd
}
