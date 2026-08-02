package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/eliminyro/memory-system/internal/auth"
	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/models"
)

// fakeImportJobs stubs the import_jobs repository for handler unit tests.
type fakeImportJobs struct {
	created   *models.ImportJob
	createErr error
	getJob    *models.ImportJob
	getErr    error
}

func (f *fakeImportJobs) Create(ctx context.Context, job *models.ImportJob) error {
	if f.createErr != nil {
		return f.createErr
	}
	job.ID = uuid.New()
	f.created = job
	return nil
}

func (f *fakeImportJobs) GetByID(ctx context.Context, id, tenantID uuid.UUID) (*models.ImportJob, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.getJob, nil
}

// multipartArchive builds a multipart/form-data body carrying `body` under the
// "archive" file field. Returns the body and its Content-Type header.
func multipartArchive(t *testing.T, body []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("archive", "import.zip")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := fw.Write(body); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return &buf, mw.FormDataContentType()
}

func TestImportEnqueue_Success(t *testing.T) {
	fake := &fakeImportJobs{}
	h := &adminAPIHandler{importJobs: fake, maxUploadBytes: 1 << 20}
	tid := uuid.New()

	archive := []byte("PK\x03\x04 pretend zip payload")
	body, ct := multipartArchive(t, archive)
	req := httptest.NewRequest(http.MethodPost, "/import", body)
	req.Header.Set("Content-Type", ct)
	req = req.WithContext(auth.WithTenantID(req.Context(), tid))
	rec := httptest.NewRecorder()

	h.enqueueImport(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}
	if fake.created == nil {
		t.Fatal("expected a job to be created")
	}
	if fake.created.TenantID != tid {
		t.Errorf("job tenant = %s, want %s (from context)", fake.created.TenantID, tid)
	}
	if fake.created.Status != models.ImportJobStatusQueued {
		t.Errorf("job status = %q, want queued", fake.created.Status)
	}
	if !bytes.Equal(fake.created.Archive, archive) {
		t.Errorf("stored archive bytes do not match upload")
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	if resp["id"] == nil || resp["id"] == "" {
		t.Errorf("response missing job id: %v", resp)
	}
}

func TestImportEnqueue_Oversized(t *testing.T) {
	fake := &fakeImportJobs{}
	h := &adminAPIHandler{importJobs: fake, maxUploadBytes: 16} // tiny cap
	tid := uuid.New()

	body, ct := multipartArchive(t, bytes.Repeat([]byte("A"), 4096))
	req := httptest.NewRequest(http.MethodPost, "/import", body)
	req.Header.Set("Content-Type", ct)
	req = req.WithContext(auth.WithTenantID(req.Context(), tid))
	rec := httptest.NewRecorder()

	h.enqueueImport(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body=%s", rec.Code, rec.Body.String())
	}
	if fake.created != nil {
		t.Error("no job should be created for an oversized upload")
	}
}

func TestImportEnqueue_NoTenant(t *testing.T) {
	fake := &fakeImportJobs{}
	h := &adminAPIHandler{importJobs: fake, maxUploadBytes: 1 << 20}

	body, ct := multipartArchive(t, []byte("payload"))
	req := httptest.NewRequest(http.MethodPost, "/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()

	h.enqueueImport(rec, req) // no tenant on context

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (no tenant)", rec.Code)
	}
	if fake.created != nil {
		t.Error("no job should be created without a tenant")
	}
}

func TestImportStatus_Success(t *testing.T) {
	id := uuid.New()
	tid := uuid.New()
	fake := &fakeImportJobs{getJob: &models.ImportJob{
		ID: id, TenantID: tid, Status: models.ImportJobStatusRunning,
		Total: 10, Imported: 4, Skipped: 1, Failed: 0,
	}}
	h := &adminAPIHandler{importJobs: fake}

	req := httptest.NewRequest(http.MethodGet, "/import/"+id.String(), nil)
	req.SetPathValue("id", id.String())
	req = req.WithContext(auth.WithTenantID(req.Context(), tid))
	rec := httptest.NewRecorder()

	h.importStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	if resp["status"] != models.ImportJobStatusRunning {
		t.Errorf("status field = %v, want running", resp["status"])
	}
	if resp["total"] != float64(10) || resp["imported"] != float64(4) {
		t.Errorf("counts wrong: total=%v imported=%v", resp["total"], resp["imported"])
	}
	// The archive bytes must never be serialized.
	if _, ok := resp["archive"]; ok {
		t.Error("archive bytes leaked into status response")
	}
}

func TestImportStatus_NotFound(t *testing.T) {
	id := uuid.New()
	fake := &fakeImportJobs{getErr: fmt.Errorf("%w: import job %s", apperr.ErrNotFound, id)}
	h := &adminAPIHandler{importJobs: fake}

	req := httptest.NewRequest(http.MethodGet, "/import/"+id.String(), nil)
	req.SetPathValue("id", id.String())
	req = req.WithContext(auth.WithTenantID(req.Context(), uuid.New()))
	rec := httptest.NewRecorder()

	h.importStatus(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for unknown/cross-tenant job", rec.Code)
	}
}

func TestImportStatus_InvalidID(t *testing.T) {
	h := &adminAPIHandler{importJobs: &fakeImportJobs{}}
	req := httptest.NewRequest(http.MethodGet, "/import/not-a-uuid", nil)
	req.SetPathValue("id", "not-a-uuid")
	rec := httptest.NewRecorder()

	h.importStatus(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for malformed id", rec.Code)
	}
}
