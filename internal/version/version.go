// Package version is the single source of truth for the server version, used by
// the MCP server identity and the /~/version endpoint.
package version

// Version is the server version. A var (not const) so release builds can stamp
// the git tag via -ldflags "-X .../version.Version=<tag>"; dev builds report this default.
var Version = "1.0.0"

// Commit is the git SHA, stamped at build time via -ldflags; "unknown" for dev builds.
var Commit = "unknown"

// Date is the build timestamp, stamped at release build time; empty for dev builds.
var Date = ""
