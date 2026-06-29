// Package version is the single source of truth for the memory-system server
// version, shared by the MCP server identity and the /~/version HTTP endpoint.
package version

// Version is the memory-system server version.
const Version = "1.0.0"

// Commit is the git SHA the binary was built from. It is stamped at build time
// via -ldflags "-X .../version.Commit=<sha>" (see Dockerfile); local and dev
// builds without that flag report "unknown".
var Commit = "unknown"
