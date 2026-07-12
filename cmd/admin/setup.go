package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// mcpServerEntry is one "mcpServers" entry for an HTTP-transport MCP server.
type mcpServerEntry struct {
	Type    string            `json:"type"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

type mcpConfig struct {
	MCPServers map[string]mcpServerEntry `json:"mcpServers"`
}

// newSetupCmd prints a client-agnostic MCP config for a running instance. No DB
// or network I/O, never persists the token. Inputs: flags or MEMORY_URL/MEMORY_TOKEN.
func newSetupCmd() *cobra.Command {
	var url, token, name string
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Emit an MCP client config (mcpServers JSON) for a running instance",
		Long: "setup prints the standard mcpServers JSON entry (HTTP transport +\n" +
			"Authorization: Bearer header) pointing at <base-url>/mcp. Paste it wherever\n" +
			"your MCP client reads server config. No database or network access; the\n" +
			"token is never written to disk.\n\n" +
			"Inputs: --url/--token flags, or the MEMORY_URL / MEMORY_TOKEN env vars.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if url == "" {
				url = os.Getenv("MEMORY_URL")
			}
			if token == "" {
				token = os.Getenv("MEMORY_TOKEN")
			}
			if url == "" || token == "" {
				return fmt.Errorf("both a base URL (--url or MEMORY_URL) and a token (--token or MEMORY_TOKEN) are required")
			}
			base := strings.TrimRight(strings.TrimSpace(url), "/")
			cfg := mcpConfig{MCPServers: map[string]mcpServerEntry{
				name: {
					Type:    "http",
					URL:     base + "/mcp",
					Headers: map[string]string{"Authorization": "Bearer " + token},
				},
			}}
			out, err := json.MarshalIndent(cfg, "", "  ")
			if err != nil {
				return fmt.Errorf("marshal config: %w", err)
			}
			// Token is secret: warn on stderr so piping stdout to a file stays clean.
			fmt.Fprintln(os.Stderr, "warning: the output below contains a bearer token — treat it as a secret and do not commit it.")
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(out))
			return nil
		},
	}
	cmd.Flags().StringVar(&url, "url", "", "Public base URL of the instance (or MEMORY_URL), e.g. https://mem.example.org")
	cmd.Flags().StringVar(&token, "token", "", "API token to embed (or MEMORY_TOKEN)")
	cmd.Flags().StringVar(&name, "name", "memory", "Server name key under mcpServers")
	return cmd
}
