//go:build integration

package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/service"
	"github.com/eliminyro/memory-system/internal/staleness"
)

// TestMCPUpdateSectionHeadingOnly proves the MCP update_section handler accepts a
// heading-only edit (Content nil): the heading changes, the content is left
// untouched, and no content is required (M3 — matching HTTP patchSection). It
// also proves the guard rejects a call with neither content nor heading.
func TestMCPUpdateSectionHeadingOnly(t *testing.T) {
	db := openBoundaryPG(t)
	store := authz.NewPostgresStore(db)
	svc := service.NewMemoryService(
		db,
		repository.NewDocumentRepository(db),
		repository.NewSectionRepository(db),
		service.NewFakeEmbedder(boundaryDim),
		repository.NewTenantRepository(db),
		repository.NewAPIKeyRepository(db),
		repository.NewLintRepository(db),
		staleness.NewPolicyStore(db),
		repository.NewOverrideLogRepository(db),
		repository.NewCleanupQueueRepository(db),
		nil, nil,
		store,
	)
	engine := authz.NewEngine(store)
	srv := NewServer(svc, engine)

	tenant := boundaryTenant(t, db, store)
	ctx := boundaryCtx(tenant, "user-"+uuid.NewString())

	slug := "doc-" + uuid.NewString()
	res, err := svc.StoreDocument(ctx, "learnings", nil, slug, "# Title\n\n## Old Heading\nbody text", true, "seed", nil, nil)
	if err != nil {
		t.Fatalf("seed doc: %v", err)
	}
	if res.Document == nil || len(res.Document.Sections) == 0 {
		t.Fatal("seed doc produced no sections")
	}
	sectionID := res.Document.Sections[0].ID
	origContent := res.Document.Sections[0].Content

	t.Run("heading-only edit succeeds and changes heading, content untouched", func(t *testing.T) {
		newHeading := "New Heading"
		out, _, terr := srv.UpdateSection(ctx, nil, UpdateSectionInput{
			SectionID: sectionID.String(),
			Heading:   &newHeading,
			// Content nil -> heading-only, no re-embed
		})
		if terr != nil {
			t.Fatalf("transport error: %v", terr)
		}
		if out == nil || out.IsError {
			t.Fatalf("out = %+v, want a non-error tool result", out)
		}
		var sec struct {
			Heading *string `json:"heading"`
			Content string  `json:"content"`
		}
		if err := json.Unmarshal([]byte(resultText(t, out)), &sec); err != nil {
			t.Fatalf("result not a section JSON: %v", err)
		}
		if sec.Heading == nil || *sec.Heading != newHeading {
			t.Fatalf("heading = %v, want %q", sec.Heading, newHeading)
		}
		if sec.Content != origContent {
			t.Fatalf("content = %q, want it unchanged (%q)", sec.Content, origContent)
		}
	})

	t.Run("neither content nor heading -> errorResult", func(t *testing.T) {
		out, _, terr := srv.UpdateSection(ctx, nil, UpdateSectionInput{
			SectionID: sectionID.String(),
		})
		if terr != nil {
			t.Fatalf("transport error: %v", terr)
		}
		if out == nil || !out.IsError {
			t.Fatalf("out = %+v, want an isError tool result", out)
		}
		if got := resultText(t, out); !strings.Contains(got, "at least one of content or heading") {
			t.Fatalf("text = %q, want the 'at least one of content or heading' guard message", got)
		}
	})
}
