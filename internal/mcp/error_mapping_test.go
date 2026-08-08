package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/models"
)

// resultText extracts the first TextContent string from a tool result, failing
// the test if the shape isn't the expected single TextContent.
func resultText(t *testing.T, res *mcpsdk.CallToolResult) string {
	t.Helper()
	if res == nil || len(res.Content) == 0 {
		t.Fatalf("res = %+v, want a tool result with content", res)
	}
	tc, ok := res.Content[0].(*mcpsdk.TextContent)
	if !ok {
		t.Fatalf("unexpected content type %T", res.Content[0])
	}
	return tc.Text
}

// TestToolErr proves the shared MCP error mapper every memory-tool and
// admin/ACL handler routes through (B16/B18, R10/R17, RG1): the safe sentinels
// (ErrNotFound, ErrInvalidInput) become a clean errorResult carrying their
// message (isError tool result, no Go error — mirroring the HTTP surface's
// 404/400), while anything else is logged server-side and returned as a generic
// "<prefix>: internal error" isError result — never a Go error carrying the raw
// internal message, which the go-sdk would marshal onto the wire and leak.
func TestToolErr(t *testing.T) {
	t.Run("ErrInvalidInput -> errorResult carrying the sentinel message, no Go error", func(t *testing.T) {
		res, _, err := toolErr("x", fmt.Errorf("%w: bad", apperr.ErrInvalidInput))
		if err != nil {
			t.Fatalf("Go error = %v, want nil (ErrInvalidInput must be a tool result)", err)
		}
		if res == nil || !res.IsError {
			t.Fatalf("res = %+v, want an isError tool result", res)
		}
		if got := resultText(t, res); !strings.Contains(got, "bad") {
			t.Fatalf("text = %q, want it to carry the sentinel message %q", got, "bad")
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

	t.Run("generic error -> generic errorResult, no leak, no Go error", func(t *testing.T) {
		res, _, err := toolErr("search", errors.New("internal boom: host=db:5432"))
		if err != nil {
			t.Fatalf("Go error = %v, want nil (a generic error must not be returned to the SDK)", err)
		}
		if res == nil || !res.IsError {
			t.Fatalf("res = %+v, want an isError tool result", res)
		}
		got := resultText(t, res)
		if got != "search: internal error" {
			t.Fatalf("text = %q, want %q (generic, prefix parameterized)", got, "search: internal error")
		}
		// The raw internal message must never reach the client.
		if strings.Contains(got, "boom") || strings.Contains(got, "db:5432") {
			t.Fatalf("text = %q leaks the raw internal error", got)
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
