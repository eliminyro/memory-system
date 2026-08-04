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
	files := http.StripPrefix("/ui/", http.FileServer(http.FS(sub)))
	// The UI assets are not content-hashed, so a CDN/browser caching them serves
	// a stale app after an upgrade (an upstream edge cache pinned a stale script
	// for hours in testing). Mark them no-store so every load fetches the current
	// build; the files are small and this is an admin-only surface.
	fileServer = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		files.ServeHTTP(w, r)
	})

	config = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"client_id":    uiClientID,
			"issuer":       baseURL,
			"redirect_uri": baseURL + "/ui",
			"resource":     baseURL + "/mcp",
		})
	}
	return
}
