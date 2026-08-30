package panicguard

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// TestRecover_RecoversAndLogs proves a panic inside the guarded scope is
// recovered (control returns normally, no re-panic) and logged with its value
// and a stack at Error.
func TestRecover_RecoversAndLogs(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError}))

	func() {
		defer Recover(logger, "unit work")
		panic("boom")
	}()
	// Reaching here means the panic did not propagate.

	out := buf.String()
	if !strings.Contains(out, "unit work panicked") {
		t.Fatalf("log missing panic message: %q", out)
	}
	if !strings.Contains(out, "boom") {
		t.Fatalf("log missing panic value: %q", out)
	}
	if !strings.Contains(out, "stack") {
		t.Fatalf("log missing stack: %q", out)
	}
}

// TestRecover_NoPanicNoLog: the guard is silent when nothing panics.
func TestRecover_NoPanicNoLog(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	func() {
		defer Recover(logger, "quiet")
	}()
	if buf.Len() != 0 {
		t.Fatalf("expected no log when no panic, got %q", buf.String())
	}
}

// TestRecover_NilLoggerFallsBack: a nil logger must not make the guard itself
// panic (it falls back to slog.Default()).
func TestRecover_NilLoggerFallsBack(t *testing.T) {
	func() {
		defer Recover(nil, "nil logger")
		panic("x")
	}()
}
