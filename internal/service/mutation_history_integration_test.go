//go:build integration

package service_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/authzseed"
	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/service"
	"github.com/eliminyro/memory-system/internal/staleness"
)

// historyFixture wires a MemoryService WITH the instance-config + mutation-history
// repos (the production path) so the toggle-gated audit is exercised end to end.
type historyFixture struct {
	db       *gorm.DB
	store    authz.Store
	svc      *service.MemoryService
	cfgRepo  *repository.InstanceConfigRepository
	histRepo *repository.MutationHistoryRepository
}

func newHistoryFixture(t *testing.T, wireHistory bool) *historyFixture {
	t.Helper()
	db := openServicePG(t)
	store := authz.NewPostgresStore(db)
	cfgRepo := repository.NewInstanceConfigRepository(db)
	histRepo := repository.NewMutationHistoryRepository(db)

	var histArg *repository.MutationHistoryRepository
	if wireHistory {
		histArg = histRepo
	}
	svc := service.NewMemoryService(
		db,
		repository.NewDocumentRepository(db),
		repository.NewSectionRepository(db),
		service.NewFakeEmbedder(fakeDim),
		repository.NewTenantRepository(db),
		repository.NewAPIKeyRepository(db),
		repository.NewLintRepository(db),
		staleness.NewThresholdStore(db),
		repository.NewOverrideLogRepository(db),
		repository.NewCleanupQueueRepository(db),
		cfgRepo,
		histArg,
		store,
	)
	return &historyFixture{db: db, store: store, svc: svc, cfgRepo: cfgRepo, histRepo: histRepo}
}

// setToggle flips the global history toggle and resets it to off when the test ends.
func (f *historyFixture) setToggle(t *testing.T, on bool) {
	t.Helper()
	require.NoError(t, f.cfgRepo.SetHistoryEnabled(context.Background(), on))
	t.Cleanup(func() { _ = f.cfgRepo.SetHistoryEnabled(context.Background(), false) })
}

// mkTenant creates a tenant of the given type and a member subject with an email,
// returning the tenant id, subject id, and an authenticated context for it.
func (f *historyFixture) mkTenant(t *testing.T, typ string) (uuid.UUID, string, context.Context) {
	t.Helper()
	ten := models.Tenant{ID: uuid.New(), Name: "t-" + uuid.NewString(), Type: typ}
	require.NoError(t, f.db.Create(&ten).Error)
	require.NoError(t, f.store.Write(context.Background(), authzseed.TenantSystemEdge(ten.ID)))
	subj := "user-" + uuid.NewString()
	require.NoError(t, f.store.Write(context.Background(), authzseed.TenantMember(ten.ID, subj)))
	ctx := auth.WithEmail(ctxFor(ten.ID, subj), subj+"@example.test")
	return ten.ID, subj, ctx
}

func (f *historyFixture) storeDoc(t *testing.T, ctx context.Context) (uuid.UUID, uuid.UUID) {
	t.Helper()
	slug := "d" + uuid.NewString()
	res, err := f.svc.StoreDocument(ctx, "learnings", nil, slug, "# Title\n\n## Heading\nsome body text", true, "seed", nil, nil)
	require.NoError(t, err)
	require.NotEmpty(t, res.Document.Sections)
	return res.Document.ID, res.Document.Sections[0].ID
}

// historyRows returns a doc's history rows newest-first (id tiebreak for determinism).
func (f *historyFixture) historyRows(t *testing.T, docID uuid.UUID) []models.MutationHistory {
	t.Helper()
	var rows []models.MutationHistory
	require.NoError(t, f.db.Where("document_id = ?", docID).
		Order("created_at DESC, id DESC").Find(&rows).Error)
	return rows
}

// TestHistoryToggleOffRecordsNothing: default off => no row on create/update/delete.
func TestHistoryToggleOffRecordsNothing(t *testing.T) {
	f := newHistoryFixture(t, true)
	f.setToggle(t, false)
	_, _, ctx := f.mkTenant(t, models.TenantTypeShared)

	docID, secID := f.storeDoc(t, ctx)
	body := "changed"
	_, err := f.svc.UpdateSection(ctx, secID, &body, nil, nil)
	require.NoError(t, err)
	require.NoError(t, f.svc.DeleteDocumentByID(ctx, docID, nil))

	require.Empty(t, f.historyRows(t, docID), "toggle off must record nothing")
}

// TestHistorySharedTenantRecords: create (nil Before), update_section (prior
// content/heading), and delete_document (removed content) are all recorded.
func TestHistorySharedTenantRecords(t *testing.T) {
	f := newHistoryFixture(t, true)
	f.setToggle(t, true)
	_, subj, ctx := f.mkTenant(t, models.TenantTypeShared)

	docID, secID := f.storeDoc(t, ctx)

	// create: one row, no prior content, actor recorded.
	rows := f.historyRows(t, docID)
	require.Len(t, rows, 1)
	require.Equal(t, models.MutationOpCreate, rows[0].OpType)
	require.Nil(t, rows[0].Before)
	require.Equal(t, subj, rows[0].ActorSubject)
	require.NotNil(t, rows[0].ActorEmail)

	// update_section: prior content + heading captured.
	body := "new body"
	_, err := f.svc.UpdateSection(ctx, secID, &body, nil, nil)
	require.NoError(t, err)
	rows = f.historyRows(t, docID)
	require.Equal(t, models.MutationOpUpdateSection, rows[0].OpType)
	require.NotNil(t, rows[0].Before)
	var us struct {
		Content string  `json:"content"`
		Heading *string `json:"heading"`
	}
	require.NoError(t, json.Unmarshal([]byte(*rows[0].Before), &us))
	require.Equal(t, "some body text", us.Content)
	require.NotNil(t, us.Heading)
	require.Equal(t, "Heading", *us.Heading)

	// delete_document: whole-doc snapshot recorded even though the doc is now gone.
	require.NoError(t, f.svc.DeleteDocumentByID(ctx, docID, nil))
	rows = f.historyRows(t, docID)
	require.Equal(t, models.MutationOpDeleteDocument, rows[0].OpType)
	require.NotNil(t, rows[0].Before)
	var dd struct {
		Title    string `json:"title"`
		Sections []struct {
			Content string `json:"content"`
		} `json:"sections"`
	}
	require.NoError(t, json.Unmarshal([]byte(*rows[0].Before), &dd))
	require.Equal(t, "Title", dd.Title)
	require.NotEmpty(t, dd.Sections)
	require.Equal(t, "new body", dd.Sections[0].Content)
}

// TestHistoryOverwriteRecordsPriorContent: a full-content overwrite (re-store at
// the same path) records an `overwrite` row carrying the prior doc+sections.
func TestHistoryOverwriteRecordsPriorContent(t *testing.T) {
	f := newHistoryFixture(t, true)
	f.setToggle(t, true)
	_, _, ctx := f.mkTenant(t, models.TenantTypeShared)

	slug := "d" + uuid.NewString()
	_, err := f.svc.StoreDocument(ctx, "learnings", nil, slug,
		"# Title\n\n## Heading\noriginal body", true, "seed", nil, nil)
	require.NoError(t, err)

	res, err := f.svc.StoreDocument(ctx, "learnings", nil, slug,
		"# Title2\n\n## Heading2\nreplacement body", true, "overwrite", nil, nil)
	require.NoError(t, err)
	docID := res.Document.ID

	rows := f.historyRows(t, docID)
	require.Len(t, rows, 2, "create + overwrite")
	require.Equal(t, models.MutationOpOverwrite, rows[0].OpType, "newest is the overwrite")
	require.NotNil(t, rows[0].Before, "overwrite carries the prior snapshot")

	var dd struct {
		Title    string `json:"title"`
		Sections []struct {
			Content string `json:"content"`
		} `json:"sections"`
	}
	require.NoError(t, json.Unmarshal([]byte(*rows[0].Before), &dd))
	require.Equal(t, "Title", dd.Title, "snapshot holds the prior title")
	require.NotEmpty(t, dd.Sections)
	require.Equal(t, "original body", dd.Sections[0].Content, "snapshot holds the prior body")

	require.Equal(t, models.MutationOpCreate, rows[1].OpType)
}

// TestHistoryPersonalTenantNeverRecords: toggle on but personal tenant => no rows.
func TestHistoryPersonalTenantNeverRecords(t *testing.T) {
	f := newHistoryFixture(t, true)
	f.setToggle(t, true)
	_, _, ctx := f.mkTenant(t, models.TenantTypePersonal)

	docID, secID := f.storeDoc(t, ctx)
	body := "changed"
	_, err := f.svc.UpdateSection(ctx, secID, &body, nil, nil)
	require.NoError(t, err)
	require.NoError(t, f.svc.DeleteDocumentByID(ctx, docID, nil))

	require.Empty(t, f.historyRows(t, docID), "personal tenant must never record")
}

// TestHistoryBestEffortNilRepo: toggle on + shared tenant but no history sink =>
// mutations still succeed (best-effort; a missing audit never fails the write).
func TestHistoryBestEffortNilRepo(t *testing.T) {
	f := newHistoryFixture(t, false) // history repo not wired
	f.setToggle(t, true)
	_, _, ctx := f.mkTenant(t, models.TenantTypeShared)

	docID, secID := f.storeDoc(t, ctx)
	body := "changed"
	_, err := f.svc.UpdateSection(ctx, secID, &body, nil, nil)
	require.NoError(t, err, "update must not fail when history sink is absent")
	require.NoError(t, f.svc.DeleteDocumentByID(ctx, docID, nil), "delete must not fail when history sink is absent")

	require.Empty(t, f.historyRows(t, docID))
}

// TestHistoryViewAccess: a reader sees entries newest-first; a non-reader is
// refused with the same not-found it gets for the doc itself (reveals nothing).
func TestHistoryViewAccess(t *testing.T) {
	f := newHistoryFixture(t, true)
	f.setToggle(t, true)
	_, _, ownerCtx := f.mkTenant(t, models.TenantTypeShared)
	_, _, otherCtx := f.mkTenant(t, models.TenantTypeShared)

	docID, secID := f.storeDoc(t, ownerCtx)
	body := "new body"
	_, err := f.svc.UpdateSection(ownerCtx, secID, &body, nil, nil)
	require.NoError(t, err)

	entries, err := f.svc.GetDocumentHistory(ownerCtx, docID, nil)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	require.Equal(t, models.MutationOpUpdateSection, entries[0].OpType, "newest first")
	require.Equal(t, models.MutationOpCreate, entries[1].OpType)

	_, err = f.svc.GetDocumentHistory(otherCtx, docID, nil)
	require.ErrorIs(t, err, apperr.ErrNotFound, "non-reader must be refused, revealing nothing")
}

// TestHistoryPruneOlderThan: the sweep's prune removes rows past the window only.
func TestHistoryPruneOlderThan(t *testing.T) {
	f := newHistoryFixture(t, true)
	ctx := context.Background()
	docID := uuid.New()
	old := models.MutationHistory{
		TenantID: uuid.New(), DocumentID: docID, OpType: models.MutationOpCreate,
		CreatedAt: time.Now().AddDate(0, 0, -100),
	}
	fresh := models.MutationHistory{
		TenantID: uuid.New(), DocumentID: docID, OpType: models.MutationOpCreate,
		CreatedAt: time.Now(),
	}
	require.NoError(t, f.db.Create(&old).Error)
	require.NoError(t, f.db.Create(&fresh).Error)

	deleted, err := f.histRepo.PruneOlderThan(ctx, time.Now().AddDate(0, 0, -90))
	require.NoError(t, err)
	require.GreaterOrEqual(t, deleted, int64(1))

	remaining := f.historyRows(t, docID)
	require.Len(t, remaining, 1, "only the fresh row survives the window")
}

// TestHistoryDeletedDocViewableByTenantReader: after a doc is deleted its history
// (incl. the delete_document snapshot) stays visible to a reader of its owning
// tenant, but a non-reader of that tenant still gets ErrNotFound.
func TestHistoryDeletedDocViewableByTenantReader(t *testing.T) {
	f := newHistoryFixture(t, true)
	f.setToggle(t, true)
	_, _, ownerCtx := f.mkTenant(t, models.TenantTypeShared)
	_, _, otherCtx := f.mkTenant(t, models.TenantTypeShared)

	docID, _ := f.storeDoc(t, ownerCtx)
	require.NoError(t, f.svc.DeleteDocumentByID(ownerCtx, docID, nil))

	entries, err := f.svc.GetDocumentHistory(ownerCtx, docID, nil)
	require.NoError(t, err, "deleted-doc history must stay visible to a tenant reader")
	require.NotEmpty(t, entries)
	require.Equal(t, models.MutationOpDeleteDocument, entries[0].OpType, "newest is the delete")

	_, err = f.svc.GetDocumentHistory(otherCtx, docID, nil)
	require.ErrorIs(t, err, apperr.ErrNotFound, "non-reader of the tenant must be refused")
}

// TestHistoryDeleteSurvivesHistoryWriteFailure: a history INSERT that fails inside
// the delete transaction must roll back only its savepoint and let the delete
// commit — the best-effort audit never sacrifices the primary op.
func TestHistoryDeleteSurvivesHistoryWriteFailure(t *testing.T) {
	f := newHistoryFixture(t, true)
	f.setToggle(t, true)
	_, _, ctx := f.mkTenant(t, models.TenantTypeShared)
	docID, _ := f.storeDoc(t, ctx)

	// Hide the table so the in-tx history INSERT fails; restore it after.
	require.NoError(t, f.db.Exec("ALTER TABLE mutation_history RENAME TO mutation_history_bak").Error)
	t.Cleanup(func() { _ = f.db.Exec("ALTER TABLE mutation_history_bak RENAME TO mutation_history").Error })

	require.NoError(t, f.svc.DeleteDocumentByID(ctx, docID, nil),
		"delete must commit even though the in-tx history write failed")

	var n int64
	require.NoError(t, f.db.Model(&models.Document{}).Where("id = ?", docID).Count(&n).Error)
	require.Zero(t, n, "document must be deleted despite the failed audit write")
}
