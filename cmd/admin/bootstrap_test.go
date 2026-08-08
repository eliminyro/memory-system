package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/service"
)

// newTestCmd builds a bare cobra.Command with output captured, standing in for
// what newBootstrapCmd's RunE receives — enough for runBootstrap, which only
// calls cmd.OutOrStdout().
func newTestCmd() (*cobra.Command, *bytes.Buffer) {
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	return cmd, &out
}

// TestRunBootstrap_PrintsKeyOnSuccess proves the CLI prints the plaintext key
// (and identifying metadata) to stdout exactly once on success. Uses a stub
// bootstrapFunc — no database needed; internal/service/bootstrap_integration_test.go
// already covers the real service.Bootstrap success path against Postgres.
func TestRunBootstrap_PrintsKeyOnSuccess(t *testing.T) {
	cmd, out := newTestCmd()
	wantKey := &models.APIKey{Label: "admin"}
	stub := func(_ context.Context, token string, _ service.BootstrapSpec) (string, *models.APIKey, error) {
		if token != "tok" {
			t.Fatalf("bootstrap called with token %q, want %q", token, "tok")
		}
		return "mmcp_plaintext_once", wantKey, nil
	}

	if err := runBootstrap(cmd, stub, context.Background(), "tok", service.BootstrapSpec{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out.String(), "mmcp_plaintext_once") {
		t.Errorf("expected plaintext key in stdout, got %q", out.String())
	}
	if !strings.Contains(out.String(), "admin") {
		t.Errorf("expected key label in stdout, got %q", out.String())
	}
}

// TestRunBootstrap_AlreadyBootstrappedErrorsNonNil proves the already-bootstrapped
// rejection propagates as a non-nil error (main.go maps any RunE error to a
// non-zero exit code) and nothing is printed to stdout.
func TestRunBootstrap_AlreadyBootstrappedErrorsNonNil(t *testing.T) {
	cmd, out := newTestCmd()
	stub := func(context.Context, string, service.BootstrapSpec) (string, *models.APIKey, error) {
		return "", nil, service.ErrAlreadyBootstrapped
	}

	err := runBootstrap(cmd, stub, context.Background(), "tok", service.BootstrapSpec{})
	if !errors.Is(err, service.ErrAlreadyBootstrapped) {
		t.Fatalf("err = %v, want ErrAlreadyBootstrapped", err)
	}
	if out.Len() != 0 {
		t.Errorf("expected no stdout output on rejection, got %q", out.String())
	}
}

// TestRunBootstrap_ForbiddenErrorsNonNil covers the bad/missing-token rejection.
func TestRunBootstrap_ForbiddenErrorsNonNil(t *testing.T) {
	cmd, _ := newTestCmd()
	stub := func(context.Context, string, service.BootstrapSpec) (string, *models.APIKey, error) {
		return "", nil, service.ErrBootstrapForbidden
	}

	err := runBootstrap(cmd, stub, context.Background(), "", service.BootstrapSpec{})
	if !errors.Is(err, service.ErrBootstrapForbidden) {
		t.Fatalf("err = %v, want ErrBootstrapForbidden", err)
	}
}
