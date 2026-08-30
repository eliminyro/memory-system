package main

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/service"
)

// IngestInstance renders one Instance's haystack sessions (BuildDocSource)
// and drives them through the shared import core. A DocSource-level error —
// a haystack length mismatch or a session-id collision (see corpus.go) —
// aborts this instance's import and is propagated here rather than swallowed;
// per-document StoreDocument failures are tallied in the returned
// ImportResult instead, matching ImportDocuments' existing contract.
func IngestInstance(ctx context.Context, svc *service.MemoryService, tenantID uuid.UUID, inst Instance) (service.ImportResult, error) {
	src, _ := BuildDocSource(inst)
	ctx = auth.WithTenantID(ctx, tenantID)

	result, err := svc.ImportDocuments(ctx, tenantID, src)
	if err != nil {
		return result, fmt.Errorf("ingest %s: %w", inst.QuestionID, err)
	}
	return result, nil
}
