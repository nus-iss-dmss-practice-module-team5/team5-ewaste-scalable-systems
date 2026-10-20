package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"workflow-api/internal/dto"
	"workflow-api/internal/model"
	"workflow-api/internal/repository"
	"workflow-api/internal/storage"
)

const (
	UploadEvidenceCommand                   = "UploadEvidence"
	DownloadEvidenceCommand                 = "DownloadEvidence"
	DefaultEvidenceMaxUploadSizeBytes int64 = 5 * 1024 * 1024
	evidenceCleanupTimeout                  = 10 * time.Second
)

var (
	ErrEvidenceStorage       = errors.New("evidence: storage operation failed")
	ErrEvidenceIntegrity     = errors.New("evidence: stored object does not match its metadata")
	allowedEvidenceMIMETypes = map[string]struct{}{
		"application/pdf": {},
		"image/jpeg":      {},
		"image/png":       {},
	}
)

type EvidenceUploadRequest struct {
	Filename       string
	LifecycleStage model.EvidenceLifecycleStage
	Content        []byte
}

type EvidenceDownloadActor struct {
	BatchActor
	AdminAccessReason string
}

type EvidenceDownloadResult struct {
	Filename  string
	MIMEType  string
	Content   []byte
	SizeBytes int
}

type EvidenceService struct {
	repository repository.BatchRepository
	storage    storage.EvidenceStorage
	clock      func() time.Time
	newID      func() string
	retainFor  time.Duration
	maxBytes   int64
}

func NewEvidenceService(repo repository.BatchRepository, evidenceStorage storage.EvidenceStorage) *EvidenceService {
	return &EvidenceService{
		repository: repo,
		storage:    evidenceStorage,
		clock:      func() time.Time { return time.Now().UTC() },
		newID:      uuid.NewString,
		retainFor:  24 * time.Hour,
		maxBytes:   DefaultEvidenceMaxUploadSizeBytes,
	}
}

func (s *EvidenceService) SetMaxUploadSizeBytes(maxBytes int64) {
	if maxBytes > 0 {
		s.maxBytes = maxBytes
	}
}

func (s *EvidenceService) MaxUploadSizeBytes() int64 { return s.maxBytes }

func (s *EvidenceService) Upload(
	ctx context.Context,
	batchID string,
	request EvidenceUploadRequest,
	metadata BatchCommandMetadata,
) (dto.EvidenceMutationResult, error) {
	if err := requireReceiptRecycler(metadata); err != nil {
		return dto.EvidenceMutationResult{}, err
	}
	if strings.TrimSpace(batchID) == "" {
		return dto.EvidenceMutationResult{}, NewBatchValidationError(map[string]string{"batch_id": "is required"})
	}

	normalized, err := normalizeEvidenceUpload(request, s.maxBytes)
	if err != nil {
		return dto.EvidenceMutationResult{}, err
	}
	metadata, err = prepareMetadata(metadata, UploadEvidenceCommand, batchID, struct {
		Filename       string                       `json:"filename"`
		LifecycleStage model.EvidenceLifecycleStage `json:"lifecycle_stage"`
		MIMEType       string                       `json:"mime_type"`
		FileSizeBytes  int                          `json:"file_size_bytes"`
		SHA256Hash     string                       `json:"sha256_hash"`
	}{
		Filename: normalized.Filename, LifecycleStage: normalized.LifecycleStage,
		MIMEType: normalized.MIMEType, FileSizeBytes: len(normalized.Content), SHA256Hash: normalized.SHA256Hash,
	})
	if err != nil {
		return dto.EvidenceMutationResult{}, err
	}

	var result dto.EvidenceMutationResult
	var uploadedObjectKey string
	err = s.repository.Transaction(ctx, func(tx repository.BatchTransaction) error {
		command, replayErr := tx.FindCommand(ctx, metadata.ActorScope, metadata.CommandName, metadata.IdempotencyKey)
		if replayErr == nil {
			if command.RequestHash != metadata.RequestHash {
				return ErrBatchIdempotencyConflict
			}
			if command.State != model.CommandStateCompleted {
				return ErrBatchInProgress
			}
			if err := json.Unmarshal(command.ResponseJSON, &result); err != nil {
				return fmt.Errorf("evidence: decode replay response: %w", err)
			}
			return nil
		}
		if !errors.Is(replayErr, repository.ErrCommandNotFound) {
			return replayErr
		}

		if err := tx.ValidateRecyclerActor(ctx, metadata.Actor.UserID, metadata.Actor.OrganisationID); err != nil {
			return mapEvidenceRepositoryError(err)
		}
		batch, err := tx.FindBatchForUpdate(ctx, batchID)
		if err != nil {
			return err
		}
		if !isEvidenceProcessingStatus(batch.Status) {
			return ErrBatchInvalidState
		}
		if batch.CurrentClaimID == nil || batch.CurrentAssignmentID == nil {
			return ErrBatchForbidden
		}
		if err := tx.ValidateReceiptScope(ctx, batch.ID, *batch.CurrentClaimID, *batch.CurrentAssignmentID, batch.ClaimEpoch, metadata.Actor.OrganisationID); err != nil {
			return mapEvidenceRepositoryError(err)
		}

		now := s.clock().UTC()
		command = newCommand(metadata, batch.ID, now, s.retainFor)
		if err := tx.CreateCommand(ctx, command); err != nil {
			return err
		}

		evidenceID := s.newID()
		uploadedObjectKey = evidenceObjectKey(batch.ID, evidenceID)
		if s.storage == nil {
			return ErrEvidenceStorage
		}
		if err := s.storage.Put(ctx, uploadedObjectKey, normalized.Content, normalized.MIMEType); err != nil {
			return fmt.Errorf("%w: upload object: %v", ErrEvidenceStorage, err)
		}

		evidence := &model.BatchEvidence{
			EvidenceID: evidenceID, BatchID: batch.ID, OrganisationID: metadata.Actor.OrganisationID,
			UploadedBy: metadata.Actor.UserID, LifecycleStage: normalized.LifecycleStage,
			OriginalFileName: normalized.Filename, StoredObjectKey: uploadedObjectKey,
			MIMEType: normalized.MIMEType, FileSizeBytes: uint64(len(normalized.Content)),
			SHA256Hash: normalized.SHA256Hash, ValidationStatus: model.EvidenceValidationValidated,
			CreatedAt: now,
		}
		if err := tx.CreateEvidence(ctx, evidence); err != nil {
			return err
		}

		audit := newAuditEvent(metadata, command.ID, batch, model.BatchAuditEventEvidenceUploaded, batch.Status, batch.Status, map[string]string{
			"evidence_id": evidence.EvidenceID, "lifecycle_stage": string(evidence.LifecycleStage),
			"mime_type": evidence.MIMEType, "file_size_bytes": fmt.Sprintf("%d", evidence.FileSizeBytes),
			"sha256_hash": evidence.SHA256Hash,
		}, now)
		if err := tx.AppendAudit(ctx, audit); err != nil {
			return err
		}

		result = dto.EvidenceMutationResult{Data: evidenceView(evidence), CorrelationID: metadata.CorrelationID}
		responseJSON, err := json.Marshal(result)
		if err != nil {
			return err
		}
		return tx.CompleteCommand(ctx, command.ID, http.StatusCreated, responseJSON, now)
	})
	if err != nil && uploadedObjectKey != "" {
		// Database rollback after a successful blob write must not leave an
		// unreferenced object behind. Storage cleanup is best effort, while the
		// original error remains the result returned to the caller. The request
		// context may already be cancelled, so cleanup runs on its own deadline.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), evidenceCleanupTimeout)
		_ = s.storage.Delete(cleanupCtx, uploadedObjectKey)
		cancel()
	}
	return result, err
}

func (s *EvidenceService) Download(
	ctx context.Context,
	batchID string,
	evidenceID string,
	actor EvidenceDownloadActor,
	correlationID string,
) (EvidenceDownloadResult, error) {
	if strings.TrimSpace(batchID) == "" || strings.TrimSpace(evidenceID) == "" {
		return EvidenceDownloadResult{}, NewBatchValidationError(map[string]string{"path": "batch_id and evidence_id are required"})
	}
	role := strings.ToUpper(strings.TrimSpace(actor.RoleCode))
	if role == "SYSTEM_ADMIN" && strings.TrimSpace(actor.AdminAccessReason) == "" {
		return EvidenceDownloadResult{}, NewBatchValidationError(map[string]string{"X-Admin-Access-Reason": "is required for system administrator downloads"})
	}
	if correlationID == "" {
		return EvidenceDownloadResult{}, NewBatchValidationError(map[string]string{"X-Correlation-ID": "is required"})
	}

	var evidence *model.BatchEvidence
	err := s.repository.Transaction(ctx, func(tx repository.BatchTransaction) error {
		batch, err := tx.FindBatchForUpdate(ctx, batchID)
		if err != nil {
			return err
		}
		if !isEvidenceProcessingStatus(batch.Status) {
			return ErrBatchEvidenceNotFound
		}
		evidence, err = tx.FindEvidence(ctx, batchID, evidenceID)
		if err != nil {
			return err
		}
		// Successful uploads are synchronously D2-validated before the row is
		// created. The deployed 026 schema has no separate status column, so an
		// empty read value represents that implicit VALIDATED state. If a future
		// schema stores the status explicitly, non-VALIDATED rows still fail
		// closed.
		if evidence.ValidationStatus != "" && evidence.ValidationStatus != model.EvidenceValidationValidated {
			return ErrBatchEvidenceNotFound
		}

		switch role {
		case "RECYCLER":
			if err := tx.ValidateRecyclerActor(ctx, actor.UserID, actor.OrganisationID); err != nil {
				return mapEvidenceRepositoryError(err)
			}
			if evidence.OrganisationID != actor.OrganisationID || batch.CurrentClaimID == nil || batch.CurrentAssignmentID == nil {
				return ErrBatchEvidenceNotFound
			}
			if err := tx.ValidateReceiptScope(ctx, batch.ID, *batch.CurrentClaimID, *batch.CurrentAssignmentID, batch.ClaimEpoch, actor.OrganisationID); err != nil {
				return mapEvidenceRepositoryError(err)
			}
		case "AUDITOR":
			if err := tx.ValidateAuditorActor(ctx, actor.UserID); err != nil {
				return mapEvidenceRepositoryError(err)
			}
		case "SYSTEM_ADMIN":
			if err := tx.ValidateAdminActor(ctx, actor.UserID); err != nil {
				return mapEvidenceRepositoryError(err)
			}
		default:
			return ErrBatchForbidden
		}
		return nil
	})
	if err != nil {
		return EvidenceDownloadResult{}, err
	}
	if s.storage == nil {
		return EvidenceDownloadResult{}, ErrEvidenceStorage
	}
	content, err := s.storage.Get(ctx, evidence.StoredObjectKey)
	if err != nil {
		return EvidenceDownloadResult{}, fmt.Errorf("%w: download object: %v", ErrEvidenceStorage, err)
	}
	if err := verifyEvidenceContent(evidence, content); err != nil {
		return EvidenceDownloadResult{}, err
	}

	if role == "SYSTEM_ADMIN" {
		if err := s.recordAdminDownload(ctx, batchID, evidenceID, actor, correlationID); err != nil {
			return EvidenceDownloadResult{}, err
		}
	}

	return EvidenceDownloadResult{Filename: evidence.OriginalFileName, MIMEType: evidence.MIMEType, Content: content, SizeBytes: len(content)}, nil
}

func (s *EvidenceService) recordAdminDownload(ctx context.Context, batchID string, evidenceID string, actor EvidenceDownloadActor, correlationID string) error {
	metadata, err := prepareMetadata(BatchCommandMetadata{
		Actor: actor.BatchActor, CorrelationID: correlationID, IdempotencyKey: "download-" + s.newID(),
	}, DownloadEvidenceCommand, batchID, struct {
		EvidenceID string `json:"evidence_id"`
		Reason     string `json:"reason"`
	}{evidenceID, strings.TrimSpace(actor.AdminAccessReason)})
	if err != nil {
		return err
	}
	return s.repository.Transaction(ctx, func(tx repository.BatchTransaction) error {
		batch, err := tx.FindBatchForUpdate(ctx, batchID)
		if err != nil {
			return err
		}
		now := s.clock().UTC()
		command := newCommand(metadata, batch.ID, now, s.retainFor)
		if err := tx.CreateCommand(ctx, command); err != nil {
			return err
		}
		audit := newAuditEvent(metadata, command.ID, batch, model.BatchAuditEventEvidenceDownloaded, batch.Status, batch.Status, map[string]string{
			"evidence_id": evidenceID, "reason": strings.TrimSpace(actor.AdminAccessReason),
		}, now)
		if err := tx.AppendAudit(ctx, audit); err != nil {
			return err
		}
		return tx.CompleteCommand(ctx, command.ID, http.StatusOK, []byte(`{"downloaded":true}`), now)
	})
}

type normalizedEvidenceUpload struct {
	Filename       string
	LifecycleStage model.EvidenceLifecycleStage
	MIMEType       string
	Content        []byte
	SHA256Hash     string
}

func normalizeEvidenceUpload(request EvidenceUploadRequest, maxBytes int64) (normalizedEvidenceUpload, error) {
	if maxBytes < 1 {
		maxBytes = DefaultEvidenceMaxUploadSizeBytes
	}
	if len(request.Content) == 0 {
		return normalizedEvidenceUpload{}, NewBatchValidationError(map[string]string{"file": "must not be empty"})
	}
	if int64(len(request.Content)) > maxBytes {
		return normalizedEvidenceUpload{}, NewBatchValidationError(map[string]string{"file": "exceeds the 5 MiB upload limit"})
	}
	if request.LifecycleStage != model.EvidenceLifecycleReceipt && request.LifecycleStage != model.EvidenceLifecycleTreatment {
		return normalizedEvidenceUpload{}, NewBatchValidationError(map[string]string{"lifecycle_stage": "must be RECEIPT or TREATMENT"})
	}

	mimeType := http.DetectContentType(request.Content)
	if _, ok := allowedEvidenceMIMETypes[mimeType]; !ok {
		return normalizedEvidenceUpload{}, NewBatchValidationError(map[string]string{"file": "must be a PDF, JPEG, or PNG"})
	}
	filename := safeEvidenceFilename(request.Filename)
	if filename == "" {
		return normalizedEvidenceUpload{}, NewBatchValidationError(map[string]string{"file": "filename is required"})
	}
	hash := sha256.Sum256(request.Content)
	return normalizedEvidenceUpload{
		Filename: filename, LifecycleStage: request.LifecycleStage, MIMEType: mimeType,
		Content: append([]byte(nil), request.Content...), SHA256Hash: hex.EncodeToString(hash[:]),
	}, nil
}

// verifyEvidenceContent fails closed when the stored object no longer matches
// the size, hash, or type recorded at upload, so altered or substituted bytes
// are never served as validated evidence.
func verifyEvidenceContent(evidence *model.BatchEvidence, content []byte) error {
	hash := sha256.Sum256(content)
	if uint64(len(content)) != evidence.FileSizeBytes ||
		!strings.EqualFold(hex.EncodeToString(hash[:]), evidence.SHA256Hash) ||
		http.DetectContentType(content) != evidence.MIMEType {
		return ErrEvidenceIntegrity
	}
	return nil
}

func safeEvidenceFilename(raw string) string {
	// Clients on any platform may send a full path; keep only the last element.
	name := filepath.Base(strings.ReplaceAll(strings.TrimSpace(raw), "\\", "/"))
	if name == "." || name == ".." || name == string(filepath.Separator) {
		return ""
	}
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	if len([]rune(name)) > 255 {
		name = string([]rune(name)[:255])
	}
	return strings.TrimSpace(name)
}

func evidenceObjectKey(batchID string, evidenceID string) string {
	return "batches/" + batchID + "/evidence/" + evidenceID
}

func evidenceView(evidence *model.BatchEvidence) dto.EvidenceView {
	validationStatus := evidence.ValidationStatus
	if validationStatus == "" {
		validationStatus = model.EvidenceValidationValidated
	}
	return dto.EvidenceView{
		EvidenceID: evidence.EvidenceID, BatchID: evidence.BatchID, LifecycleStage: string(evidence.LifecycleStage),
		MIMEType: evidence.MIMEType, FileSizeBytes: evidence.FileSizeBytes, SHA256Hash: evidence.SHA256Hash,
		ValidationStatus: string(validationStatus),
	}
}

func isEvidenceProcessingStatus(status model.BatchStatus) bool {
	switch status {
	case model.BatchStatusCollected, model.BatchStatusVerified, model.BatchStatusRecycled, model.BatchStatusCompleted:
		return true
	default:
		return false
	}
}

func mapEvidenceRepositoryError(err error) error {
	switch {
	case errors.Is(err, repository.ErrBatchActorNotEligible):
		return ErrBatchForbidden
	case errors.Is(err, repository.ErrEvidenceNotFound):
		return ErrBatchEvidenceNotFound
	case errors.Is(err, repository.ErrBatchConcurrency):
		return ErrBatchStaleVersion
	default:
		return err
	}
}
