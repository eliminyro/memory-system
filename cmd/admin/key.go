package main

import (
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/eliminyro/memory-system/internal/models"
)

func newKeyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "key",
		Short: "Manage API keys",
	}
	cmd.AddCommand(
		newKeyIssueCmd(),
		newKeyListCmd(),
		newKeyRotateCmd(),
		newKeyRevokeCmd(),
	)
	return cmd
}

// printPlaintextKey emits a freshly minted key exactly once, clearly marked.
func printPlaintextKey(plaintext string, key *models.APIKey) {
	fmt.Println("API key (shown only once — store it now, it cannot be retrieved again):")
	fmt.Println()
	fmt.Println("  " + plaintext)
	fmt.Println()
	fmt.Printf("  id:     %s\n  label:  %s\n  prefix: %s\n", key.ID, key.Label, key.Prefix)
	if key.ExpiresAt != nil {
		fmt.Printf("  expires: %s\n", key.ExpiresAt.Format(time.RFC3339))
	}
}

func newKeyIssueCmd() *cobra.Command {
	var (
		tenantStr, label, subject string
		ttlDays                   int
	)
	cmd := &cobra.Command{
		Use:   "issue",
		Short: "Issue a new API key for a tenant",
		RunE: func(cmd *cobra.Command, args []string) error {
			tenantID, err := uuid.Parse(tenantStr)
			if err != nil {
				return fmt.Errorf("invalid --tenant: %w", err)
			}
			svc, ctx, err := buildService()
			if err != nil {
				return err
			}
			var subjectPtr *string
			if cmd.Flags().Changed("subject") {
				subjectPtr = &subject
			}
			var expiresAt *time.Time
			if ttlDays > 0 {
				e := time.Now().Add(time.Duration(ttlDays) * 24 * time.Hour)
				expiresAt = &e
			}
			plaintext, key, err := svc.CreateAPIKey(ctx, tenantID, label, subjectPtr, expiresAt)
			if err != nil {
				return err
			}
			printPlaintextKey(plaintext, key)
			return nil
		},
	}
	cmd.Flags().StringVar(&tenantStr, "tenant", "", "Tenant UUID (required)")
	cmd.Flags().StringVar(&label, "label", "", "Human-readable key label (required)")
	cmd.Flags().StringVar(&subject, "subject", "", "Pin the key to a specific authorization subject id")
	cmd.Flags().IntVar(&ttlDays, "ttl-days", 0, "Days until the key expires (0 = never)")
	_ = cmd.MarkFlagRequired("tenant")
	_ = cmd.MarkFlagRequired("label")
	return cmd
}

func newKeyListCmd() *cobra.Command {
	var tenantStr string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List API key metadata for a tenant (never prints secrets)",
		RunE: func(cmd *cobra.Command, args []string) error {
			tenantID, err := uuid.Parse(tenantStr)
			if err != nil {
				return fmt.Errorf("invalid --tenant: %w", err)
			}
			svc, ctx, err := buildService()
			if err != nil {
				return err
			}
			keys, err := svc.ListAPIKeys(ctx, tenantID)
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "ID\tLABEL\tPREFIX\tCREATED_AT\tLAST_USED_AT\tEXPIRES_AT\tREVOKED_AT")
			for _, k := range keys {
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					k.ID, k.Label, k.Prefix,
					fmtTime(&k.CreatedAt), fmtTime(k.LastUsedAt), fmtTime(k.ExpiresAt), fmtTime(k.RevokedAt))
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVar(&tenantStr, "tenant", "", "Tenant UUID (required)")
	_ = cmd.MarkFlagRequired("tenant")
	return cmd
}

func newKeyRotateCmd() *cobra.Command {
	var (
		idStr      string
		graceHours int
	)
	cmd := &cobra.Command{
		Use:   "rotate",
		Short: "Rotate an API key, optionally leaving the old one valid for a grace window",
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := uuid.Parse(idStr)
			if err != nil {
				return fmt.Errorf("invalid --id: %w", err)
			}
			svc, ctx, err := buildService()
			if err != nil {
				return err
			}
			grace := time.Duration(graceHours) * time.Hour
			plaintext, key, err := svc.RotateAPIKey(ctx, id, grace)
			if err != nil {
				return err
			}
			printPlaintextKey(plaintext, key)
			return nil
		},
	}
	cmd.Flags().StringVar(&idStr, "id", "", "API key UUID to rotate (required)")
	cmd.Flags().IntVar(&graceHours, "grace-hours", 0, "Hours the old key stays valid (0 = revoke immediately)")
	_ = cmd.MarkFlagRequired("id")
	return cmd
}

func newKeyRevokeCmd() *cobra.Command {
	var idStr string
	cmd := &cobra.Command{
		Use:   "revoke",
		Short: "Revoke an API key",
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := uuid.Parse(idStr)
			if err != nil {
				return fmt.Errorf("invalid --id: %w", err)
			}
			svc, ctx, err := buildService()
			if err != nil {
				return err
			}
			if err := svc.RevokeAPIKey(ctx, id); err != nil {
				return err
			}
			fmt.Printf("Revoked API key %s\n", id)
			return nil
		},
	}
	cmd.Flags().StringVar(&idStr, "id", "", "API key UUID (required)")
	_ = cmd.MarkFlagRequired("id")
	return cmd
}

// fmtTime renders a nullable timestamp for tabular output. Nil -> "-".
func fmtTime(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.Format(time.RFC3339)
}
