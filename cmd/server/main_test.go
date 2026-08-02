package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
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
