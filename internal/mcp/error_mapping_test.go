package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/models"
)

// TestToolErr proves the shared MCP error mapper every memory-tool and
// admin/ACL handler now routes through (B16/B18, R10/R17): the safe sentinels
// (ErrNotFound, ErrInvalidInput) become a clean errorResult (isError tool
// result, no Go error — mirroring the HTTP surface's 404/400), while anything
// else is wrapped as a Go error under the parameterized prefix.
func TestToolErr(t *testing.T) {
	t.Run("ErrInvalidInput -> errorResult, no Go error", func(t *testing.T) {
		res, _, err := toolErr("store", fmt.Errorf("bad path: %w", apperr.ErrInvalidInput))
		if err != nil {
			t.Fatalf("Go error = %v, want nil (ErrInvalidInput must be a tool result)", err)
		}
		if res == nil || !res.IsError {
			t.Fatalf("res = %+v, want an isError tool result", res)
		}
	})

	t.Run("ErrNotFound -> errorResult, no Go error", func(t *testing.T) {
		res, _, err := toolErr("get document", fmt.Errorf("missing: %w", apperr.ErrNotFound))
		if err != nil {
			t.Fatalf("Go error = %v, want nil (ErrNotFound must be a tool result)", err)
		}
		if res == nil || !res.IsError {
			t.Fatalf("res = %+v, want an isError tool result", res)
		}
	})

	t.Run("generic error -> wrapped Go error, no tool result", func(t *testing.T) {
		sentinel := errors.New("boom")
		res, _, err := toolErr("lint memory", sentinel)
		if res != nil {
			t.Fatalf("res = %+v, want nil for a generic error", res)
		}
		if err == nil {
			t.Fatal("Go error = nil, want a wrapped internal error")
		}
		if !errors.Is(err, sentinel) {
			t.Fatalf("err = %v, want it to wrap the underlying error", err)
		}
		if got := err.Error(); got != "lint memory: boom" {
			t.Fatalf("err = %q, want %q (prefix parameterized)", got, "lint memory: boom")
		}
	})
}

// TestKeyIssueResult_IncludesExpiresAt proves the shared one-time key-issue
// projection used by BOTH create_api_key and rotate_api_key emits expires_at
// (R19: rotate previously omitted it, so a rotated key's expiry was present
// over HTTP but absent over MCP).
func TestKeyIssueResult_IncludesExpiresAt(t *testing.T) {
	exp := time.Now().Add(24 * time.Hour).UTC()
	key := &models.APIKey{
		ID:        uuid.New(),
		TenantID:  uuid.New(),
		Label:     "ci",
		Prefix:    "mk_abcd",
		ExpiresAt: &exp,
	}

	res := keyIssueResult("mk_abcd_secret", key)
	if res == nil || res.IsError {
		t.Fatalf("res = %+v, want a non-error tool result", res)
	}
	tc, ok := res.Content[0].(*mcpsdk.TextContent)
	if !ok {
		t.Fatalf("unexpected content type %T", res.Content[0])
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(tc.Text), &payload); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if _, present := payload["expires_at"]; !present {
		t.Fatalf("key-issue payload missing expires_at: %v", payload)
	}
	// The plaintext and the non-secret metadata round-trip too.
	if payload["key"] != "mk_abcd_secret" {
		t.Errorf("key = %v, want the plaintext", payload["key"])
	}
	if payload["prefix"] != "mk_abcd" {
		t.Errorf("prefix = %v, want mk_abcd", payload["prefix"])
	}
}
