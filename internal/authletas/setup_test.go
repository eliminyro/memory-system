package authletas

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestLoadMasterKey_MissingEnvErrors(t *testing.T) {
	t.Setenv("AUTHLET_MASTER_KEY", "")

	if _, err := loadMasterKey(); err == nil {
		t.Fatal("expected error for empty AUTHLET_MASTER_KEY")
	} else if !strings.Contains(err.Error(), "AUTHLET_MASTER_KEY") {
		t.Fatalf("err should reference env var, got: %v", err)
	}
}

func TestLoadMasterKey_BadBase64Errors(t *testing.T) {
	// "!!!" is not valid base64.
	t.Setenv("AUTHLET_MASTER_KEY", "!!!")

	if _, err := loadMasterKey(); err == nil {
		t.Fatal("expected error for malformed base64")
	} else if !strings.Contains(err.Error(), "base64") {
		t.Fatalf("err should mention base64, got: %v", err)
	}
}

func TestLoadMasterKey_WrongSizeErrors(t *testing.T) {
	// base64.StdEncoding.EncodeToString([]byte("AAA")) -> "QUFB" (3 bytes
	// after decode, not 32).
	t.Setenv("AUTHLET_MASTER_KEY", "QUFB")

	if _, err := loadMasterKey(); err == nil {
		t.Fatal("expected error for 3-byte decoded key")
	} else if !strings.Contains(err.Error(), "32 bytes") {
		t.Fatalf("err should mention 32-byte requirement, got: %v", err)
	}
}

func TestLoadMasterKey_AcceptsValidKey(t *testing.T) {
	// 32 bytes of 0x41 ('A') base64-encoded — decodes to exactly 32 bytes.
	t.Setenv("AUTHLET_MASTER_KEY", "QUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUE=")

	k, err := loadMasterKey()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(k) != 32 {
		t.Fatalf("got %d bytes, want 32", len(k))
	}
}

// TestTrySetup_ReturnsNilWiringOnSetupFailure verifies the graceful-boot
// path: when Setup fails (here because googleClientID is empty) TrySetup
// must return a nil wiring and emit a "authlet setup skipped" warn log
// rather than propagate the error.
func TestTrySetup_ReturnsNilWiringOnSetupFailure(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	w := TrySetup(context.Background(), nil, nil, "", "", logger)
	if w != nil {
		t.Fatalf("expected nil wiring when Setup fails, got %#v", w)
	}
	if !strings.Contains(logBuf.String(), "authlet setup skipped") {
		t.Fatalf("expected 'authlet setup skipped' warning, got: %q", logBuf.String())
	}
}

// TestTrySetup_NilLoggerDoesNotPanic guards against a nil deref when
// callers forget to supply a logger.
func TestTrySetup_NilLoggerDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panicked with nil logger: %v", r)
		}
	}()
	if w := TrySetup(context.Background(), nil, nil, "", "", nil); w != nil {
		t.Fatalf("expected nil wiring, got %#v", w)
	}
}
