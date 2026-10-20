package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"workflow-api/internal/model"
	"workflow-api/internal/repository"
	"workflow-api/internal/storage"
)

type fakeEvidenceStorage struct {
	objects      map[string][]byte
	putCalls     int
	deleteCalls  int
	getCalls     int
	putErr       error
	getErr       error
	deleteErr    error
	deleteCtxErr error
}

func newFakeEvidenceStorage() *fakeEvidenceStorage {
	return &fakeEvidenceStorage{objects: make(map[string][]byte)}
}

func (s *fakeEvidenceStorage) Put(_ context.Context, key string, content []byte, _ string) error {
	s.putCalls++
	if s.putErr != nil {
		return s.putErr
	}
	s.objects[key] = append([]byte(nil), content...)
	return nil
}

func (s *fakeEvidenceStorage) Get(_ context.Context, key string) ([]byte, error) {
	s.getCalls++
	if s.getErr != nil {
		return nil, s.getErr
	}
	content, ok := s.objects[key]
	if !ok {
		return nil, storage.ErrEvidenceObjectNotFound
	}
	return append([]byte(nil), content...), nil
}

func (s *fakeEvidenceStorage) Delete(ctx context.Context, key string) error {
	s.deleteCalls++
	s.deleteCtxErr = ctx.Err()
	if s.deleteErr != nil {
		return s.deleteErr
	}
	delete(s.objects, key)
	return nil
}

func TestEvidenceServiceUploadValidatesAndPersistsValidatedEvidence(t *testing.T) {
	repo := newFakeBatchRepository()
	repo.state.batches["batch-receipt-001"] = collectedReceiptBatch()
	objectStorage := newFakeEvidenceStorage()
	svc := NewEvidenceService(repo, objectStorage)
	svc.newID = func() string { return "evidence-001" }

	result, err := svc.Upload(context.Background(), "batch-receipt-001", EvidenceUploadRequest{
		Filename:       "receipt.pdf",
		LifecycleStage: model.EvidenceLifecycleReceipt,
		Content:        []byte("%PDF-1.7\nvalidated evidence"),
	}, recyclerMetadata("evidence-upload-001", "corr-evidence-001", 0))
	if err != nil {
		t.Fatalf("upload returned error: %v", err)
	}
	if result.Data.ValidationStatus != string(model.EvidenceValidationValidated) || result.Data.MIMEType != "application/pdf" {
		t.Fatalf("unexpected evidence response: %+v", result.Data)
	}
	if len(repo.state.evidence) != 1 || len(repo.state.audits) != 1 || len(repo.state.commands) != 1 {
		t.Fatalf("expected metadata, command, and audit: evidence=%d commands=%d audits=%d", len(repo.state.evidence), len(repo.state.commands), len(repo.state.audits))
	}
	if repo.state.audits[0].EventType != model.BatchAuditEventEvidenceUploaded {
		t.Fatalf("unexpected audit type: %s", repo.state.audits[0].EventType)
	}
	if objectStorage.putCalls != 1 || len(objectStorage.objects) != 1 {
		t.Fatalf("expected one stored object: puts=%d objects=%d", objectStorage.putCalls, len(objectStorage.objects))
	}
}

func TestEvidenceServiceUploadRejectsInvalidFileWithoutDurableEffects(t *testing.T) {
	repo := newFakeBatchRepository()
	repo.state.batches["batch-receipt-001"] = collectedReceiptBatch()
	objectStorage := newFakeEvidenceStorage()
	svc := NewEvidenceService(repo, objectStorage)

	_, err := svc.Upload(context.Background(), "batch-receipt-001", EvidenceUploadRequest{
		Filename:       "notes.txt",
		LifecycleStage: model.EvidenceLifecycleReceipt,
		Content:        []byte("not an accepted evidence file"),
	}, recyclerMetadata("evidence-upload-invalid", "corr-evidence-invalid", 0))
	if !errors.Is(err, ErrBatchValidation) {
		t.Fatalf("expected validation error, got %v", err)
	}
	if len(repo.state.evidence) != 0 || len(repo.state.commands) != 0 || len(repo.state.audits) != 0 || objectStorage.putCalls != 0 {
		t.Fatalf("invalid upload created durable effects: evidence=%d commands=%d audits=%d puts=%d", len(repo.state.evidence), len(repo.state.commands), len(repo.state.audits), objectStorage.putCalls)
	}
}

func TestEvidenceServiceUploadReplaysWithoutDuplicateObjectOrAudit(t *testing.T) {
	repo := newFakeBatchRepository()
	repo.state.batches["batch-receipt-001"] = collectedReceiptBatch()
	objectStorage := newFakeEvidenceStorage()
	svc := NewEvidenceService(repo, objectStorage)
	nextID := 0
	svc.newID = func() string {
		nextID++
		return "evidence-" + string(rune('0'+nextID))
	}
	request := EvidenceUploadRequest{Filename: "receipt.png", LifecycleStage: model.EvidenceLifecycleReceipt, Content: pngFixture()}
	metadata := recyclerMetadata("evidence-upload-replay", "corr-evidence-replay", 0)

	first, err := svc.Upload(context.Background(), "batch-receipt-001", request, metadata)
	if err != nil {
		t.Fatalf("first upload returned error: %v", err)
	}
	second, err := svc.Upload(context.Background(), "batch-receipt-001", request, metadata)
	if err != nil {
		t.Fatalf("replay returned error: %v", err)
	}
	if first.Data.EvidenceID != second.Data.EvidenceID || len(repo.state.evidence) != 1 || len(repo.state.audits) != 1 || objectStorage.putCalls != 1 {
		t.Fatalf("replay duplicated durable effects: first=%+v second=%+v evidence=%d audits=%d puts=%d", first.Data, second.Data, len(repo.state.evidence), len(repo.state.audits), objectStorage.putCalls)
	}
}

func TestEvidenceServiceUploadCleansObjectWhenMetadataTransactionRollsBack(t *testing.T) {
	repo := newFakeBatchRepository()
	repo.state.batches["batch-receipt-001"] = collectedReceiptBatch()
	repo.failAudit = true
	objectStorage := newFakeEvidenceStorage()
	svc := NewEvidenceService(repo, objectStorage)

	_, err := svc.Upload(context.Background(), "batch-receipt-001", EvidenceUploadRequest{
		Filename: "receipt.pdf", LifecycleStage: model.EvidenceLifecycleReceipt, Content: []byte("%PDF-1.7\ncontent"),
	}, recyclerMetadata("evidence-upload-storage-error", "corr-evidence-storage-error", 0))
	if err == nil {
		t.Fatal("expected audit failure")
	}
	if len(repo.state.evidence) != 0 || len(repo.state.commands) != 0 || objectStorage.deleteCalls != 1 || len(objectStorage.objects) != 0 {
		t.Fatalf("storage failure left effects: evidence=%d commands=%d deletes=%d", len(repo.state.evidence), len(repo.state.commands), objectStorage.deleteCalls)
	}
}

func TestEvidenceServiceDownloadAuthorizesOwnerAndAuditsAdmin(t *testing.T) {
	repo := newFakeBatchRepository()
	repo.state.batches["batch-receipt-001"] = collectedReceiptBatch()
	objectStorage := seedStoredEvidence(repo, pdfFixture)
	svc := NewEvidenceService(repo, objectStorage)

	owner, err := svc.Download(context.Background(), "batch-receipt-001", "evidence-001", EvidenceDownloadActor{BatchActor: recyclerMetadata("unused", "corr-download-owner", 0).Actor}, "corr-download-owner")
	if err != nil || !bytes.Equal(owner.Content, pdfFixture) {
		t.Fatalf("owner download failed: result=%+v err=%v", owner, err)
	}

	admin, err := svc.Download(context.Background(), "batch-receipt-001", "evidence-001", EvidenceDownloadActor{
		BatchActor:        BatchActor{UserID: "admin-001", OrganisationID: "platform-001", RoleCode: "SYSTEM_ADMIN"},
		AdminAccessReason: "incident review",
	}, "corr-download-admin")
	if err != nil || len(admin.Content) == 0 {
		t.Fatalf("admin download failed: result=%+v err=%v", admin, err)
	}
	if len(repo.state.audits) != 1 || repo.state.audits[0].EventType != model.BatchAuditEventEvidenceDownloaded {
		t.Fatalf("expected one admin download audit, got %+v", repo.state.audits)
	}
}

func TestEvidenceServiceDownloadRequiresAdminReasonAndRejectsOtherRoles(t *testing.T) {
	repo := newFakeBatchRepository()
	repo.state.batches["batch-receipt-001"] = collectedReceiptBatch()
	objectStorage := seedStoredEvidence(repo, pdfFixture)
	svc := NewEvidenceService(repo, objectStorage)

	_, err := svc.Download(context.Background(), "batch-receipt-001", "evidence-001", EvidenceDownloadActor{
		BatchActor: BatchActor{UserID: "admin-001", OrganisationID: "platform-001", RoleCode: "SYSTEM_ADMIN"},
	}, "corr-admin-no-reason")
	if !errors.Is(err, ErrBatchValidation) {
		t.Fatalf("expected missing admin reason validation error, got %v", err)
	}

	_, err = svc.Download(context.Background(), "batch-receipt-001", "evidence-001", EvidenceDownloadActor{
		BatchActor: BatchActor{UserID: "donor-001", OrganisationID: "org-001", RoleCode: "DONOR"},
	}, "corr-donor-download")
	if !errors.Is(err, ErrBatchForbidden) {
		t.Fatalf("expected donor forbidden error, got %v", err)
	}
}

func pngFixture() []byte {
	return []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00}
}

var pdfFixture = []byte("%PDF-1.7\ncontent")

const storedEvidenceKey = "batches/batch-receipt-001/evidence/evidence-001"

// seedStoredEvidence registers facility-001's evidence-001 with metadata that
// matches content, as a completed upload would have left it.
func seedStoredEvidence(repo *fakeBatchRepository, content []byte) *fakeEvidenceStorage {
	hash := sha256.Sum256(content)
	repo.state.evidence["evidence-001"] = &model.BatchEvidence{
		EvidenceID: "evidence-001", BatchID: "batch-receipt-001", OrganisationID: "facility-001", UploadedBy: "recycler-user-001",
		LifecycleStage: model.EvidenceLifecycleReceipt, OriginalFileName: "receipt.pdf", StoredObjectKey: storedEvidenceKey,
		MIMEType: "application/pdf", FileSizeBytes: uint64(len(content)), SHA256Hash: hex.EncodeToString(hash[:]),
		ValidationStatus: model.EvidenceValidationValidated,
	}
	objectStorage := newFakeEvidenceStorage()
	objectStorage.objects[storedEvidenceKey] = append([]byte(nil), content...)
	return objectStorage
}

func downloadAsRecycler(svc *EvidenceService, organisationID string) (EvidenceDownloadResult, error) {
	return svc.Download(context.Background(), "batch-receipt-001", "evidence-001", EvidenceDownloadActor{
		BatchActor: BatchActor{UserID: "recycler-user-001", OrganisationID: organisationID, RoleCode: "RECYCLER"},
	}, "corr-download")
}

func TestEvidenceServiceUploadRejectsUnacceptableFiles(t *testing.T) {
	oversize := make([]byte, DefaultEvidenceMaxUploadSizeBytes+1)
	copy(oversize, pngFixture())
	atLimit := oversize[:DefaultEvidenceMaxUploadSizeBytes]

	cases := []struct {
		name     string
		filename string
		stage    model.EvidenceLifecycleStage
		content  []byte
		wantErr  bool
	}{
		{name: "exactly at limit", filename: "photo.png", stage: model.EvidenceLifecycleReceipt, content: atLimit},
		{name: "one byte over limit", filename: "photo.png", stage: model.EvidenceLifecycleReceipt, content: oversize, wantErr: true},
		{name: "empty", filename: "photo.png", stage: model.EvidenceLifecycleReceipt, content: nil, wantErr: true},
		{name: "script named as image", filename: "photo.png", stage: model.EvidenceLifecycleReceipt, content: []byte("<script>alert(1)</script>"), wantErr: true},
		{name: "executable named as pdf", filename: "receipt.pdf", stage: model.EvidenceLifecycleReceipt, content: []byte("MZ\x90\x00binary"), wantErr: true},
		{name: "unknown lifecycle stage", filename: "photo.png", stage: "DISPOSAL", content: pngFixture(), wantErr: true},
		{name: "missing filename", filename: " ", stage: model.EvidenceLifecycleReceipt, content: pngFixture(), wantErr: true},
		{name: "filename that is only a directory", filename: "../", stage: model.EvidenceLifecycleReceipt, content: pngFixture(), wantErr: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			repo := newFakeBatchRepository()
			repo.state.batches["batch-receipt-001"] = collectedReceiptBatch()
			objectStorage := newFakeEvidenceStorage()
			svc := NewEvidenceService(repo, objectStorage)

			_, err := svc.Upload(context.Background(), "batch-receipt-001", EvidenceUploadRequest{
				Filename: testCase.filename, LifecycleStage: testCase.stage, Content: testCase.content,
			}, recyclerMetadata("evidence-upload-validation", "corr-evidence-validation", 0))
			if !testCase.wantErr {
				if err != nil || len(repo.state.evidence) != 1 {
					t.Fatalf("expected accepted upload: err=%v evidence=%d", err, len(repo.state.evidence))
				}
				return
			}
			if !errors.Is(err, ErrBatchValidation) {
				t.Fatalf("expected validation error, got %v", err)
			}
			if len(repo.state.evidence) != 0 || len(repo.state.commands) != 0 || objectStorage.putCalls != 0 {
				t.Fatalf("rejected upload created durable effects: evidence=%d commands=%d puts=%d", len(repo.state.evidence), len(repo.state.commands), objectStorage.putCalls)
			}
		})
	}
}

func TestEvidenceServiceUploadGeneratesPrivateKeyAndServerHash(t *testing.T) {
	repo := newFakeBatchRepository()
	repo.state.batches["batch-receipt-001"] = collectedReceiptBatch()
	objectStorage := newFakeEvidenceStorage()
	svc := NewEvidenceService(repo, objectStorage)
	svc.newID = func() string { return "evidence-001" }

	result, err := svc.Upload(context.Background(), "batch-receipt-001", EvidenceUploadRequest{
		Filename: "../../etc/passwd\x00.pdf", LifecycleStage: model.EvidenceLifecycleReceipt, Content: pdfFixture,
	}, recyclerMetadata("evidence-upload-key", "corr-evidence-key", 0))
	if err != nil {
		t.Fatalf("upload returned error: %v", err)
	}

	stored := repo.state.evidence["evidence-001"]
	if stored.StoredObjectKey != storedEvidenceKey || strings.Contains(stored.StoredObjectKey, "passwd") {
		t.Fatalf("object key must be server-generated, got %q", stored.StoredObjectKey)
	}
	if stored.OriginalFileName != "passwd.pdf" {
		t.Fatalf("file name was not reduced to a safe base name: %q", stored.OriginalFileName)
	}
	if got := safeEvidenceFilename(`C:\\Users\\me\\receipt.pdf`); got != "receipt.pdf" {
		t.Fatalf("windows path was not reduced to a base name: %q", got)
	}
	want := sha256.Sum256(pdfFixture)
	if stored.SHA256Hash != hex.EncodeToString(want[:]) || result.Data.SHA256Hash != stored.SHA256Hash || stored.FileSizeBytes != uint64(len(pdfFixture)) {
		t.Fatalf("metadata does not describe the stored bytes: %+v", stored)
	}
	if !bytes.Equal(objectStorage.objects[storedEvidenceKey], pdfFixture) {
		t.Fatal("stored object differs from the uploaded bytes")
	}
}

func TestEvidenceServiceUploadStorageFailureLeavesNoMetadata(t *testing.T) {
	repo := newFakeBatchRepository()
	repo.state.batches["batch-receipt-001"] = collectedReceiptBatch()
	objectStorage := newFakeEvidenceStorage()
	objectStorage.putErr = storage.ErrEvidenceStorageUnavailable
	svc := NewEvidenceService(repo, objectStorage)
	request := EvidenceUploadRequest{Filename: "receipt.pdf", LifecycleStage: model.EvidenceLifecycleReceipt, Content: pdfFixture}
	metadata := recyclerMetadata("evidence-upload-put-failure", "corr-evidence-put-failure", 0)

	_, err := svc.Upload(context.Background(), "batch-receipt-001", request, metadata)
	if !errors.Is(err, ErrEvidenceStorage) {
		t.Fatalf("expected storage error, got %v", err)
	}
	if len(repo.state.evidence) != 0 || len(repo.state.commands) != 0 || len(repo.state.audits) != 0 {
		t.Fatalf("failed upload left durable effects: evidence=%d commands=%d audits=%d", len(repo.state.evidence), len(repo.state.commands), len(repo.state.audits))
	}

	// The failed attempt must not poison the idempotency key: a retry succeeds.
	objectStorage.putErr = nil
	if _, err := svc.Upload(context.Background(), "batch-receipt-001", request, metadata); err != nil {
		t.Fatalf("retry after storage recovery returned error: %v", err)
	}
	if len(repo.state.evidence) != 1 || len(objectStorage.objects) != 1 {
		t.Fatalf("retry did not persist exactly one evidence object: evidence=%d objects=%d", len(repo.state.evidence), len(objectStorage.objects))
	}
}

func TestEvidenceServiceUploadCleansObjectAfterRequestCancellation(t *testing.T) {
	repo := newFakeBatchRepository()
	repo.state.batches["batch-receipt-001"] = collectedReceiptBatch()
	repo.failAudit = true
	objectStorage := newFakeEvidenceStorage()
	svc := NewEvidenceService(repo, objectStorage)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := svc.Upload(ctx, "batch-receipt-001", EvidenceUploadRequest{
		Filename: "receipt.pdf", LifecycleStage: model.EvidenceLifecycleReceipt, Content: pdfFixture,
	}, recyclerMetadata("evidence-upload-cancelled", "corr-evidence-cancelled", 0))
	if err == nil {
		t.Fatal("expected audit failure")
	}
	if objectStorage.deleteCalls != 1 || objectStorage.deleteCtxErr != nil || len(objectStorage.objects) != 0 {
		t.Fatalf("cleanup must outlive the cancelled request: deletes=%d ctxErr=%v objects=%d", objectStorage.deleteCalls, objectStorage.deleteCtxErr, len(objectStorage.objects))
	}
}

func TestEvidenceServiceDownloadDeniesOtherOrganisation(t *testing.T) {
	repo := newFakeBatchRepository()
	repo.state.batches["batch-receipt-001"] = collectedReceiptBatch()
	objectStorage := seedStoredEvidence(repo, pdfFixture)
	svc := NewEvidenceService(repo, objectStorage)

	result, err := downloadAsRecycler(svc, "facility-002")
	if !errors.Is(err, ErrBatchEvidenceNotFound) {
		t.Fatalf("expected another organisation's evidence to be reported as not found, got %v", err)
	}
	if len(result.Content) != 0 || objectStorage.getCalls != 0 {
		t.Fatalf("cross-organisation request reached object storage: gets=%d", objectStorage.getCalls)
	}

	if _, err := downloadAsRecycler(svc, "facility-001"); err != nil {
		t.Fatalf("owning organisation download failed: %v", err)
	}
}

func TestEvidenceServiceDownloadRejectsEvidenceFromAnotherBatch(t *testing.T) {
	repo := newFakeBatchRepository()
	repo.state.batches["batch-receipt-001"] = collectedReceiptBatch()
	other := collectedReceiptBatch()
	other.ID = "batch-receipt-002"
	repo.state.batches[other.ID] = other
	objectStorage := seedStoredEvidence(repo, pdfFixture)
	svc := NewEvidenceService(repo, objectStorage)

	_, err := svc.Download(context.Background(), "batch-receipt-002", "evidence-001", EvidenceDownloadActor{
		BatchActor: recyclerMetadata("unused", "corr-download", 0).Actor,
	}, "corr-download")
	if !errors.Is(err, repository.ErrEvidenceNotFound) || objectStorage.getCalls != 0 {
		t.Fatalf("expected evidence lookup through another batch to fail before storage: err=%v gets=%d", err, objectStorage.getCalls)
	}
}

func TestEvidenceServiceDownloadFailsClosedOnMetadataMismatch(t *testing.T) {
	cases := []struct {
		name   string
		tamper func(*model.BatchEvidence, *fakeEvidenceStorage)
	}{
		{name: "altered bytes of the same size", tamper: func(_ *model.BatchEvidence, s *fakeEvidenceStorage) {
			s.objects[storedEvidenceKey] = []byte("%PDF-1.7\ncontenT")
		}},
		{name: "truncated object", tamper: func(_ *model.BatchEvidence, s *fakeEvidenceStorage) {
			s.objects[storedEvidenceKey] = pdfFixture[:8]
		}},
		{name: "substituted type", tamper: func(e *model.BatchEvidence, s *fakeEvidenceStorage) {
			replacement := append(pngFixture(), make([]byte, len(pdfFixture)-len(pngFixture()))...)
			hash := sha256.Sum256(replacement)
			e.SHA256Hash = hex.EncodeToString(hash[:])
			s.objects[storedEvidenceKey] = replacement
		}},
		{name: "metadata without hash", tamper: func(e *model.BatchEvidence, _ *fakeEvidenceStorage) {
			e.SHA256Hash = ""
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			repo := newFakeBatchRepository()
			repo.state.batches["batch-receipt-001"] = collectedReceiptBatch()
			objectStorage := seedStoredEvidence(repo, pdfFixture)
			testCase.tamper(repo.state.evidence["evidence-001"], objectStorage)
			svc := NewEvidenceService(repo, objectStorage)

			result, err := svc.Download(context.Background(), "batch-receipt-001", "evidence-001", EvidenceDownloadActor{
				BatchActor:        BatchActor{UserID: "admin-001", OrganisationID: "platform-001", RoleCode: "SYSTEM_ADMIN"},
				AdminAccessReason: "incident review",
			}, "corr-download-integrity")
			if !errors.Is(err, ErrEvidenceIntegrity) {
				t.Fatalf("expected integrity error, got %v", err)
			}
			if len(result.Content) != 0 || len(repo.state.audits) != 0 {
				t.Fatalf("mismatched object was served or audited as downloaded: bytes=%d audits=%d", len(result.Content), len(repo.state.audits))
			}
		})
	}
}

func TestEvidenceServiceDownloadSurfacesStorageFailures(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		break_ func(*fakeEvidenceStorage)
	}{
		{name: "object missing", break_: func(s *fakeEvidenceStorage) { delete(s.objects, storedEvidenceKey) }},
		{name: "storage unavailable", break_: func(s *fakeEvidenceStorage) { s.getErr = storage.ErrEvidenceStorageUnavailable }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			repo := newFakeBatchRepository()
			repo.state.batches["batch-receipt-001"] = collectedReceiptBatch()
			objectStorage := seedStoredEvidence(repo, pdfFixture)
			testCase.break_(objectStorage)
			svc := NewEvidenceService(repo, objectStorage)

			result, err := downloadAsRecycler(svc, "facility-001")
			if !errors.Is(err, ErrEvidenceStorage) || len(result.Content) != 0 {
				t.Fatalf("expected storage error without content, got result=%+v err=%v", result, err)
			}
		})
	}
}
