package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
)

// BenchTenantName is the fixed tenant name for the LongMemEval bench corpus.
const BenchTenantName = "benchmark"

// BenchTenantID is the fixed UUID every benchmark run ingests/queries under —
// uuid.NewSHA1(uuid.NameSpaceDNS, []byte("longmemeval.bench.memory-system")),
// so re-runs always target the same tenant (idempotency, task 5.3) without a
// config knob. Do not change: it would orphan any previously ingested corpus.
var BenchTenantID = uuid.MustParse("a18eb56c-a5e8-56bc-a28f-1bbcd8de9a6a")

// EnsureBenchTenant returns BenchTenantID, creating the tenant row on first
// use. Idempotent — a second run finds the existing row and reuses it, so
// ingestion never fans out across tenants.
func EnsureBenchTenant(ctx context.Context, tenants *repository.TenantRepository) (uuid.UUID, error) {
	if _, err := tenants.GetByID(ctx, BenchTenantID); err == nil {
		return BenchTenantID, nil
	} else if !errors.Is(err, apperr.ErrNotFound) {
		return uuid.Nil, fmt.Errorf("lookup bench tenant: %w", err)
	}

	tenant := &models.Tenant{
		ID:   BenchTenantID,
		Name: BenchTenantName,
		Type: models.TenantTypeShared,
	}
	if err := tenants.Create(ctx, tenant); err != nil {
		return uuid.Nil, fmt.Errorf("create bench tenant: %w", err)
	}
	return BenchTenantID, nil
}
