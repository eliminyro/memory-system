package server

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
)

//go:embed ui
var uiFS embed.FS

// uiHandlers returns the /ui file server plus the runtime config endpoint. The
// config carries non-secret OAuth values; issuer/redirect_uri/resource are derived
// from baseURL (never hardcoded) so login works on any deployment host.
func uiHandlers(uiClientID, baseURL string) (fileServer http.Handler, config http.HandlerFunc) {
	sub, _ := fs.Sub(uiFS, "ui")
	fileServer = http.StripPrefix("/ui/", http.FileServer(http.FS(sub)))

	config = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"client_id":    uiClientID,
			"issuer":       baseURL,
			"redirect_uri": baseURL + "/ui",
			"resource":     baseURL + "/mcp",
		})
	}
	return
}
