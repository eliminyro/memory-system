package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
)

// fakeResetBootstrapper counts ResetBootstrap calls and optionally returns an
// error, so maybeResetBootstrap's decision logic can be tested without a DB.
type fakeResetBootstrapper struct {
	calls int
	err   error
}

func (f *fakeResetBootstrapper) ResetBootstrap(ctx context.Context) error {
	f.calls++
	return f.err
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestMaybeResetBootstrap_ResetFalse_NoCall(t *testing.T) {
	svc := &fakeResetBootstrapper{}
	if err := maybeResetBootstrap(context.Background(), false, svc, discardLogger()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc.calls != 0 {
		t.Fatalf("expected no ResetBootstrap call, got %d", svc.calls)
	}
}

func TestMaybeResetBootstrap_ResetTrue_Calls(t *testing.T) {
	svc := &fakeResetBootstrapper{}
	if err := maybeResetBootstrap(context.Background(), true, svc, discardLogger()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc.calls != 1 {
		t.Fatalf("expected exactly 1 ResetBootstrap call, got %d", svc.calls)
	}
}

func TestMaybeResetBootstrap_ResetTrue_PropagatesError(t *testing.T) {
	wantErr := errors.New("boom")
	svc := &fakeResetBootstrapper{err: wantErr}
	err := maybeResetBootstrap(context.Background(), true, svc, discardLogger())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected wrapped %v, got %v", wantErr, err)
	}
	if svc.calls != 1 {
		t.Fatalf("expected exactly 1 ResetBootstrap call, got %d", svc.calls)
	}
}

// bufLogger returns a WARN+ logger writing to buf so tests can assert what was
// (and was not) logged.
func bufLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// TestArmBootstrapToken_NoAdmin_GeneratesAndLogs: an un-bootstrapped instance
// (hasAdmin == false) gets a hex token that is also logged, so operators can read
// it from `docker logs` (design D1).
func TestArmBootstrapToken_NoAdmin_GeneratesAndLogs(t *testing.T) {
	var buf bytes.Buffer
	token, err := armBootstrapToken(false, bufLogger(&buf))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(token) != 64 {
		t.Fatalf("token length = %d, want 64 hex chars (32 bytes)", len(token))
	}
	if _, err := hex.DecodeString(token); err != nil {
		t.Fatalf("token is not valid hex: %v", err)
	}
	logged := buf.String()
	if !strings.Contains(logged, token) {
		t.Errorf("expected the token to be logged, log did not contain it:\n%s", logged)
	}
	if !strings.Contains(logged, "not bootstrapped") {
		t.Errorf("expected an un-bootstrapped WARN message, got:\n%s", logged)
	}
}

// TestArmBootstrapToken_AdminExists_EmptyAndSilent: when an admin already exists
// (hasAdmin == true) no token is generated and nothing is logged, so a
// bootstrapped instance never re-arms or leaks a token (design D1).
func TestArmBootstrapToken_AdminExists_EmptyAndSilent(t *testing.T) {
	var buf bytes.Buffer
	token, err := armBootstrapToken(true, bufLogger(&buf))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "" {
		t.Errorf("token = %q, want empty when an admin already exists", token)
	}
	if buf.Len() != 0 {
		t.Errorf("expected no log output when an admin exists, got:\n%s", buf.String())
	}
}

// TestGenerateBootstrapToken_Unique: two calls yield distinct tokens (crypto/rand,
// not a constant).
func TestGenerateBootstrapToken_Unique(t *testing.T) {
	a, err := generateBootstrapToken()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, err := generateBootstrapToken()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a == b {
		t.Errorf("expected distinct tokens, both were %q", a)
	}
}
