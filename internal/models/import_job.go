package models

import (
	"time"

	"github.com/google/uuid"
)

// Import job status values — the lifecycle a worker drives a job through:
// queued -> running -> (succeeded | failed).
const (
	ImportJobStatusQueued    = "queued"
	ImportJobStatusRunning   = "running"
	ImportJobStatusSucceeded = "succeeded"
	ImportJobStatusFailed    = "failed"
)

// ImportJob tracks an async document-import request: the uploaded archive
// (stored as bytea, bounded by config.ImportMaxUploadBytes) plus progress
// counters a worker updates as it extracts and ingests each document.
type ImportJob struct {
	ID       uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	TenantID uuid.UUID `gorm:"type:uuid;not null;index" json:"tenant_id"`

	// Status is one of the ImportJobStatus* constants.
	Status  string `gorm:"size:20;not null;default:'queued'" json:"status"`
	Archive []byte `gorm:"type:bytea" json:"-"`

	// Progress counters, updated by the worker as it processes the archive.
	Total    int `gorm:"not null;default:0" json:"total"`
	Imported int `gorm:"not null;default:0" json:"imported"`
	Skipped  int `gorm:"not null;default:0" json:"skipped"`
	Failed   int `gorm:"not null;default:0" json:"failed"`

	// Error carries the terminal failure reason when Status == failed.
	Error string `json:"error,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (ImportJob) TableName() string {
	return "import_jobs"
}
