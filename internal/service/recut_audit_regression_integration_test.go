//go:build integration

package service_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/repository"
)

// embeddingIsNull reports whether a section's stored vector is SQL NULL — the
// invariant that keeps embed=false doc_types (prompts) out of search/get_related.
func embeddingIsNull(t *testing.T, db *gorm.DB, secID uuid.UUID) bool {
	t.Helper()
	var isNull bool
	require.NoError(t, db.Raw("SELECT embedding IS NULL FROM sections WHERE id = ?", secID).Scan(&isNull).Error)
	return isNull
}

// TestUpdateSection_PromptKeepsNullEmbedding guards the cross-tenant leak fix: an
// edit of a prompt section must NOT re-embed it. A non-NULL embedding would surface
// the prompt via get_related to any tenant holding a grant on the owner.
func TestUpdateSection_PromptKeepsNullEmbedding(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)

	res, err := f.svc.StoreDocument(ctx, "prompts", strp("derpy"), "persona", promptContent("orig"), false, "", nil, nil)
	require.NoError(t, err)
	require.NotEmpty(t, res.Document.Sections)
	secID := res.Document.Sections[0].ID
	require.True(t, embeddingIsNull(t, f.db, secID), "a freshly stored prompt section starts NULL-embedded")

	body := "edited prompt body"
	_, err = f.svc.UpdateSection(ctx, secID, &body, nil, nil)
	require.NoError(t, err, "editing a prompt section must succeed")
	require.True(t, embeddingIsNull(t, f.db, secID),
		"a prompt section MUST stay NULL-embedded after update_section (else it leaks via get_related)")
}

// TestUpdateSection_NonPromptStillEmbeds is the non-vacuous control: a normal
// (embed=true) doc's section keeps a real embedding after an edit, so the prompt
// gate above isn't just disabling embedding for everything.
func TestUpdateSection_NonPromptStillEmbeds(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)

	body := "updated learnings body"
	_, err := f.svc.UpdateSection(ctx, f.secA, &body, nil, nil)
	require.NoError(t, err)
	require.False(t, embeddingIsNull(t, f.db, f.secA),
		"a normal doc's section keeps a non-NULL embedding after update_section")
}

// TestPutSection_RestoreExistingPromptDoc guards the cascade fix: put_section
// against an EXISTING embed=false doc used to Save the preloaded NULL-embedding
// sections, serializing a zero-value '[]' vector and aborting the transaction.
func TestPutSection_RestoreExistingPromptDoc(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)

	_, err := f.svc.StoreDocument(ctx, "prompts", strp("derpy"), "persona", promptContent("orig"), false, "", nil, nil)
	require.NoError(t, err)

	res, err := f.svc.PutSection(ctx, "prompts", strp("derpy"), "persona", "H", "new heading body", nil)
	require.NoError(t, err, "put_section on an existing prompt doc must not fail on a NULL-embedding cascade")
	require.Equal(t, "ok", res.Status)

	secs, err := repository.NewSectionRepository(f.db).ListByDocumentID(context.Background(), res.Document.ID)
	require.NoError(t, err)
	require.NotEmpty(t, secs)
	for _, s := range secs {
		require.True(t, embeddingIsNull(t, f.db, s.ID), "prompt sections stay NULL-embedded after put_section")
	}
}

// TestMergeSections_DuplicateHeadingsNoDataLoss guards the merge fix: two sections
// sharing a heading must each be re-stored positionally instead of both collapsing
// onto the last row (which dropped one's content and left the other stale).
func TestMergeSections_DuplicateHeadingsNoDataLoss(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)

	// journal uses write_mode=merge_sections; both "Log" sections share a heading.
	dup := "# Journal\n\n## Log\nfirst A\n\n## Log\nfirst B\n"
	res, err := f.svc.StoreDocument(ctx, "journal", nil, "2026-09-03", dup, false, "", nil, nil)
	require.NoError(t, err)
	docID := res.Document.ID

	upd := "# Journal\n\n## Log\nsecond A\n\n## Log\nsecond B\n"
	_, err = f.svc.StoreDocument(ctx, "journal", nil, "2026-09-03", upd, false, "", nil, nil)
	require.NoError(t, err)

	secs, err := repository.NewSectionRepository(f.db).ListByDocumentID(context.Background(), docID)
	require.NoError(t, err)
	var logs []string
	for _, s := range secs {
		if s.Heading != nil && *s.Heading == "Log" {
			logs = append(logs, s.Content)
		}
	}
	require.Equal(t, []string{"second A", "second B"}, logs,
		"duplicate-heading merge updates each row positionally — no collapse or loss")
}
