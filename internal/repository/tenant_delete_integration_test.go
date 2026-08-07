//go:build integration

package repository_test

import (
	"context"
	"math/rand"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/authzseed"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
)

// TestTenantDelete_PurgesAllScopedRows guards B4: deleting a tenant must purge all
// per-tenant rows (import_jobs, cleanup_queue, override_log, deletion_events) and
// prune its authz relation tuples (tenant grants, document edges, and the service
// principal's out-of-tenant grants) — not just sections/documents/api_keys.
func TestTenantDelete_PurgesAllScopedRows(t *testing.T) {
	db := openLintPG(t)
	rng := rand.New(rand.NewSource(4))
	tenantID := seedTenant(t, db)
	t.Cleanup(func() { cleanupTenant(db, tenantID) }) // defensive; Delete already removes it

	ctx := context.Background()
	docID := seedDoc(t, db, tenantID, "b4-doc-"+uuid.NewString(), randUnit(rng))

	require.NoError(t, db.Create(&models.APIKey{TenantID: tenantID, KeyHash: uuid.NewString(), Label: "k", Prefix: "pfx"}).Error)
	require.NoError(t, db.Create(&models.ImportJob{TenantID: tenantID, Status: models.ImportJobStatusQueued}).Error)
	require.NoError(t, db.Create(&models.CleanupQueue{TenantID: tenantID, DocAID: uuid.New(), DocBID: uuid.New(), Similarity: 0.9}).Error)
	require.NoError(t, db.Create(&models.OverrideLog{TenantID: tenantID, Tool: models.OverrideToolStoreMemory, OverrideType: models.OverrideTypeForceCreate, Reason: "test"}).Error)
	require.NoError(t, db.Create(&models.DeletionEvent{TenantID: tenantID, DocumentPath: "learnings/x", Reason: models.DeletionReasonRetention}).Error)

	// Relation tuples: a tenant grant, a document->tenant edge, and the service
	// principal's system:memory#admin (a grant OUTSIDE the tenant object).
	store := authz.NewPostgresStore(db)
	svcID := authz.ServicePrincipalID(tenantID.String())
	require.NoError(t, store.Write(ctx, authzseed.TenantOwner(tenantID, "b4-user")))
	require.NoError(t, store.Write(ctx, authzseed.DocumentTenantEdge(docID, tenantID)))
	require.NoError(t, store.Write(ctx, authzseed.SystemAdmin(svcID)))

	require.NoError(t, repository.NewTenantRepository(db).Delete(ctx, tenantID))

	assertZero := func(msg, sql string, args ...any) {
		var n int64
		require.NoError(t, db.Raw(sql, args...).Scan(&n).Error)
		require.Equalf(t, int64(0), n, "%s: expected 0 rows", msg)
	}
	assertZero("documents", "SELECT count(*) FROM documents WHERE tenant_id = ?", tenantID)
	assertZero("sections", "SELECT count(*) FROM sections WHERE document_id = ?", docID)
	assertZero("api_keys", "SELECT count(*) FROM api_keys WHERE tenant_id = ?", tenantID)
	assertZero("import_jobs", "SELECT count(*) FROM import_jobs WHERE tenant_id = ?", tenantID)
	assertZero("cleanup_queue", "SELECT count(*) FROM cleanup_queue WHERE tenant_id = ?", tenantID)
	assertZero("override_log", "SELECT count(*) FROM override_log WHERE tenant_id = ?", tenantID)
	assertZero("deletion_events", "SELECT count(*) FROM deletion_events WHERE tenant_id = ?", tenantID)
	assertZero("tenants", "SELECT count(*) FROM tenants WHERE id = ?", tenantID)

	// Tuples: tenant-as-object grants, the document edge, and the svc principal.
	assertZero("tenant tuples", "SELECT count(*) FROM relation_tuples WHERE object_type = ? AND object_id = ?", authz.TypeTenant, tenantID.String())
	assertZero("document tuples", "SELECT count(*) FROM relation_tuples WHERE object_type = ? AND object_id = ?", authz.TypeDocument, docID.String())
	assertZero("svc principal tuples", "SELECT count(*) FROM relation_tuples WHERE subject_id = ?", svcID)
}
