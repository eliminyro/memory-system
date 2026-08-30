// Package panicguard provides a deferred recover for long-lived background
// goroutines. A request-path panic is isolated by net/http, but a panic in a
// background goroutine crashes the whole process — this keeps the loop (and the
// process) alive while surfacing the failure in logs.
package panicguard

import (
	"log/slog"
	"runtime/debug"
)

// Recover is a deferred recover for background goroutines. Placed via
//
//	defer panicguard.Recover(logger, "cleanup scan")
//
// around a unit of work, it logs any panic (with stack) at Error and swallows
// it so the surrounding loop survives. Log-only — it never hides a real bug, it
// just keeps the process up. Do NOT use on request handlers; net/http already
// isolates those.
func Recover(logger *slog.Logger, what string) {
	if r := recover(); r != nil {
		if logger == nil {
			logger = slog.Default()
		}
		logger.Error(what+" panicked", "panic", r, "stack", string(debug.Stack()))
	}
}
