// Package version is the single source of truth for the server version, used by
// the MCP server identity and the /~/version endpoint.
package version

// Version is the memory-system server version.
const Version = "1.0.0"

// Commit is the git SHA, stamped at build time via -ldflags "-X .../version.Commit=<sha>"
// (see Dockerfile); unstamped local/dev builds report "unknown".
var Commit = "unknown"
