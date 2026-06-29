package server

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
)

//go:embed ui
var uiFS embed.FS

// uiHandlers returns the file server for /ui plus the runtime config endpoint.
// The config carries non-secret values the page needs to start the OAuth flow.
func uiHandlers(uiClientID string) (fileServer http.Handler, config http.HandlerFunc) {
	sub, _ := fs.Sub(uiFS, "ui")
	fileServer = http.StripPrefix("/ui/", http.FileServer(http.FS(sub)))

	config = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"client_id":    uiClientID,
			"issuer":       "https://memory-mcp.a11s.dev",
			"redirect_uri": "https://memory-mcp.a11s.dev/ui",
			"resource":     "https://memory-mcp.a11s.dev/mcp",
		})
	}
	return
}
