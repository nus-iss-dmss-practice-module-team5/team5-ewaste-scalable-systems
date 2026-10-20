package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"workflow-api/internal/dto"
	"workflow-api/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrBatchNotFound           = errors.New("repository: batch not found")
	ErrCommandNotFound         = errors.New("repository: command not found")
	ErrBatchConcurrency        = errors.New("repository: batch concurrency conflict")
	ErrCommandConcurrency      = errors.New("repository: command concurrency conflict")
	ErrBatchActorNotEligible   = errors.New("repository: receipt actor is not eligible")
	ErrReceiptNotFound         = errors.New("repository: receipt not found")
	ErrEvidenceNotFound        = errors.New("repository: evidence not found")
	ErrEventOutboxNotFound     = errors.New("repository: outbox event not found")
	ErrAnalyticsNotFound       = errors.New("repository: analytics result not found")
	ErrAnalyticsSourceConflict = errors.New("repository: analytics source event already acknowledged by another run")
)

type BatchRepository interface {
	Transaction(ctx context.Context, fn func(BatchTransaction) error) error
}

type BatchTransaction interface {
	FindBatchForUpdate(ctx context.Context, batchID string) (*model.Batch, error)
	ValidateRecyclerActor(ctx context.Context, userID string, organisationID string) error
	ValidateAuditorActor(ctx context.Context, userID string) error
	ValidateAdminActor(ctx context.Context, userID string) error
	ValidateReceiptScope(ctx context.Context, batchID string, claimID string, assignmentID string, claimEpoch uint64, organisationID string) error

	FindCommand(
		ctx context.Context,
		actorScope string,
		commandName string,
		idempotencyKey string,
	) (*model.CommandIdempotency, error)

	CreateBatch(ctx context.Context, batch *model.Batch) error
	CreateCommand(ctx context.Context, command *model.CommandIdempotency) error

	UpdateDraft(
		ctx context.Context,
		batchID string,
		organizationID string,
		createdBy string,
		expectedVersion uint32,
		changes map[string]any,
	) (*model.Batch, error)

	SubmitDraft(
		ctx context.Context,
		batchID string,
		organizationID string,
		createdBy string,
		expectedVersion uint32,
		submittedAt time.Time,
	) (*model.Batch, error)

	CreateReceipt(ctx context.Context, receipt *model.BatchReceipt) error
	UpdateBatchReceipt(ctx context.Context, batchID string, expectedVersion uint32, now time.Time) (*model.Batch, error)
	FindReceipt(ctx context.Context, batchID string) (*model.BatchReceipt, error)
	FindEvidence(ctx context.Context, batchID string, evidenceID string) (*model.BatchEvidence, error)
	FindOutboxEvent(ctx context.Context, batchID string, eventID string, eventType string) (*model.EventOutbox, error)
	FindAnalyticsResultBySourceRun(ctx context.Context, sourceEventID string, analyticsRunID string) (*model.AnalyticsResult, error)
	FindBatchAnomalies(ctx context.Context, resultID string) ([]*model.BatchAnomaly, error)
	FindRequestCompletedEvent(ctx context.Context, batchID string, resultID string) (*model.EventOutbox, error)
	CreateEvidence(ctx context.Context, evidence *model.BatchEvidence) error
	ValidateTreatmentEvidence(ctx context.Context, batchID string, evidenceID string, organisationID string) error
	CreateTreatment(ctx context.Context, treatment *model.BatchTreatment) error
	UpdateBatchTreatment(ctx context.Context, batchID string, expectedVersion uint32, now time.Time) (*model.Batch, error)
	CreateAnalyticsResult(ctx context.Context, result *model.AnalyticsResult) error
	CreateBatchAnomaly(ctx context.Context, anomaly *model.BatchAnomaly) error
	UpdateBatchAnalytics(ctx context.Context, batchID string, expectedVersion uint32, now time.Time) (*model.Batch, error)

	CompleteCommand(
		ctx context.Context,
		commandID string,
		responseStatus int,
		responseJSON []byte,
		completedAt time.Time,
	) error

	AppendAudit(ctx context.Context, event *model.BatchAuditEvent) error
	EnqueueOutbox(ctx context.Context, event *model.EventOutbox) error
}

type GormBatchRepository struct {
	db *gorm.DB
}

func NewGormBatchRepository(db *gorm.DB) *GormBatchRepository {
	return &GormBatchRepository{db: db}
}

func (r *GormBatchRepository) Transaction(
	ctx context.Context,
	fn func(BatchTransaction) error,
) error {
	if fn == nil {
		return errors.New("repository: transaction callback is nil")
	}

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(&gormBatchTransaction{db: tx})
	})
}

type gormBatchTransaction struct {
	db *gorm.DB
}

func (t *gormBatchTransaction) ValidateRecyclerActor(
	ctx context.Context,
	userID string,
	organisationID string,
) error {
	var count int64
	err := t.db.WithContext(ctx).
		Table("users AS u").
		Joins("INNER JOIN roles AS role ON role.role_code = u.role_code").
		Joins("INNER JOIN organisations AS org ON org.organisation_id = u.organisation_id").
		Where(`
			u.user_id = ?
			AND u.organisation_id = ?
			AND u.status = 'ACTIVE'
			AND u.role_code = 'RECYCLER'
			AND role.is_active = TRUE
			AND role.allowed_organisation_type = 'PROCESSING_FACILITY'
			AND org.organisation_id = ?
			AND org.organisation_type = 'PROCESSING_FACILITY'
			AND org.status = 'ACTIVE'
		`, userID, organisationID, organisationID).
		Count(&count).
		Error
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrBatchActorNotEligible
	}
	return nil
}

func (t *gormBatchTransaction) ValidateAuditorActor(ctx context.Context, userID string) error {
	return t.validatePlatformRole(ctx, userID, "AUDITOR")
}

func (t *gormBatchTransaction) ValidateAdminActor(ctx context.Context, userID string) error {
	return t.validatePlatformRole(ctx, userID, "SYSTEM_ADMIN")
}

func (t *gormBatchTransaction) validatePlatformRole(ctx context.Context, userID string, roleCode string) error {
	var count int64
	err := t.db.WithContext(ctx).
		Table("users AS u").
		Joins("INNER JOIN roles AS role ON role.role_code = u.role_code").
		Where("u.user_id = ? AND u.status = 'ACTIVE' AND u.role_code = ? AND role.is_active = TRUE AND role.allowed_organisation_type = 'PLATFORM'", userID, roleCode).
		Count(&count).Error
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrBatchActorNotEligible
	}
	return nil
}

func (t *gormBatchTransaction) ValidateReceiptScope(
	ctx context.Context,
	batchID string,
	claimID string,
	assignmentID string,
	claimEpoch uint64,
	organisationID string,
) error {
	var count int64
	err := t.db.WithContext(ctx).
		Table("ewaste_batches AS b").
		Joins("INNER JOIN batch_claims AS c ON c.id = b.current_claim_id AND c.batch_id = b.id AND c.claim_epoch = b.claim_epoch").
		Joins("INNER JOIN batch_assignments AS a ON a.id = b.current_assignment_id AND a.batch_id = b.id AND a.claim_id = c.id AND a.claim_epoch = b.claim_epoch").
		Where(
			"b.id = ? AND b.current_claim_id = ? AND b.current_assignment_id = ? AND b.claim_epoch = ? AND c.recycler_org_id = ? AND c.claim_status = ? AND c.superseded_at IS NULL AND a.recycler_org_id = ? AND a.assignment_status IN (?, ?)",
			batchID,
			claimID,
			assignmentID,
			claimEpoch,
			organisationID,
			model.ClaimStatusAccepted,
			organisationID,
			model.AssignmentStatusAccepted,
			model.AssignmentStatusCompleted,
		).
		Count(&count).
		Error
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrBatchActorNotEligible
	}
	return nil
}

func (t *gormBatchTransaction) FindBatchForUpdate(
	ctx context.Context,
	batchID string,
) (*model.Batch, error) {
	var batch model.Batch

	err := t.db.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", batchID).
		First(&batch).
		Error

	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrBatchNotFound
	}
	if err != nil {
		return nil, err
	}

	return &batch, nil
}

func (t *gormBatchTransaction) FindCommand(
	ctx context.Context,
	actorScope string,
	commandName string,
	idempotencyKey string,
) (*model.CommandIdempotency, error) {
	var command model.CommandIdempotency

	err := t.db.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where(
			"actor_scope = ? AND command_name = ? AND idempotency_key = ?",
			actorScope,
			commandName,
			idempotencyKey,
		).
		First(&command).
		Error

	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrCommandNotFound
	}
	if err != nil {
		return nil, err
	}

	return &command, nil
}

func (t *gormBatchTransaction) CreateBatch(
	ctx context.Context,
	batch *model.Batch,
) error {
	if batch == nil {
		return errors.New("repository: batch is nil")
	}

	return t.db.WithContext(ctx).Create(batch).Error
}

func (t *gormBatchTransaction) CreateCommand(
	ctx context.Context,
	command *model.CommandIdempotency,
) error {
	if command == nil {
		return errors.New("repository: command is nil")
	}

	return t.db.WithContext(ctx).Create(command).Error
}

func (t *gormBatchTransaction) UpdateDraft(
	ctx context.Context,
	batchID string,
	organizationID string,
	createdBy string,
	expectedVersion uint32,
	changes map[string]any,
) (*model.Batch, error) {
	updateValues := make(map[string]any, len(changes)+1)

	for column, value := range changes {
		updateValues[column] = value
	}

	updateValues["version"] = gorm.Expr("version + 1")

	result := t.db.WithContext(ctx).
		Model(&model.Batch{}).
		Where(
			"id = ? AND organization_id = ? AND created_by = ? AND status = ? AND version = ?",
			batchID,
			organizationID,
			createdBy,
			model.BatchStatusDraft,
			expectedVersion,
		).
		Updates(updateValues)

	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, ErrBatchConcurrency
	}

	var batch model.Batch
	if err := t.db.WithContext(ctx).
		Where("id = ?", batchID).
		First(&batch).
		Error; err != nil {
		return nil, err
	}

	return &batch, nil
}

func (t *gormBatchTransaction) SubmitDraft(
	ctx context.Context,
	batchID string,
	organizationID string,
	createdBy string,
	expectedVersion uint32,
	submittedAt time.Time,
) (*model.Batch, error) {
	result := t.db.WithContext(ctx).
		Model(&model.Batch{}).
		Where(
			"id = ? AND organization_id = ? AND created_by = ? AND status = ? AND version = ?",
			batchID,
			organizationID,
			createdBy,
			model.BatchStatusDraft,
			expectedVersion,
		).
		Updates(map[string]any{
			"status":       model.BatchStatusSubmitted,
			"submitted_at": submittedAt,
			"version":      gorm.Expr("version + 1"),
		})

	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, ErrBatchConcurrency
	}

	var batch model.Batch
	if err := t.db.WithContext(ctx).
		Where("id = ?", batchID).
		First(&batch).
		Error; err != nil {
		return nil, err
	}

	return &batch, nil
}

func (t *gormBatchTransaction) CreateReceipt(
	ctx context.Context,
	receipt *model.BatchReceipt,
) error {
	if receipt == nil {
		return errors.New("repository: receipt is nil")
	}
	return t.db.WithContext(ctx).Create(receipt).Error
}

func (t *gormBatchTransaction) UpdateBatchReceipt(
	ctx context.Context,
	batchID string,
	expectedVersion uint32,
	now time.Time,
) (*model.Batch, error) {
	result := t.db.WithContext(ctx).
		Model(&model.Batch{}).
		Where(
			"id = ? AND status = ? AND version = ?",
			batchID,
			model.BatchStatusCollected,
			expectedVersion,
		).
		Updates(map[string]any{
			"status":     model.BatchStatusVerified,
			"version":    gorm.Expr("version + 1"),
			"updated_at": now,
		})
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, ErrBatchConcurrency
	}

	var batch model.Batch
	if err := t.db.WithContext(ctx).Where("id = ?", batchID).First(&batch).Error; err != nil {
		return nil, err
	}
	return &batch, nil
}

func (t *gormBatchTransaction) FindReceipt(
	ctx context.Context,
	batchID string,
) (*model.BatchReceipt, error) {
	var receipt model.BatchReceipt
	if err := t.db.WithContext(ctx).Where("batch_id = ?", batchID).First(&receipt).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrReceiptNotFound
		}
		return nil, err
	}
	return &receipt, nil
}

func (t *gormBatchTransaction) FindEvidence(ctx context.Context, batchID string, evidenceID string) (*model.BatchEvidence, error) {
	var evidence model.BatchEvidence
	err := t.db.WithContext(ctx).
		Where("batch_id = ? AND evidence_id = ?", batchID, evidenceID).
		First(&evidence).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrEvidenceNotFound
	}
	if err != nil {
		return nil, err
	}
	return &evidence, nil
}

func (t *gormBatchTransaction) FindOutboxEvent(ctx context.Context, batchID string, eventID string, eventType string) (*model.EventOutbox, error) {
	var event model.EventOutbox
	err := t.db.WithContext(ctx).
		Where("batch_id = ? AND event_id = ? AND event_type = ?", batchID, eventID, eventType).
		First(&event).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrEventOutboxNotFound
	}
	if err != nil {
		return nil, err
	}
	return &event, nil
}

func (t *gormBatchTransaction) FindAnalyticsResultBySourceRun(ctx context.Context, sourceEventID string, analyticsRunID string) (*model.AnalyticsResult, error) {
	var result model.AnalyticsResult
	err := t.db.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("source_event_id = ?", sourceEventID).
		First(&result).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrAnalyticsNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := hydrateAnalyticsSnapshot(&result); err != nil {
		return nil, err
	}
	if result.AnalyticsRunID != analyticsRunID {
		return nil, ErrAnalyticsSourceConflict
	}
	return &result, nil
}

func (t *gormBatchTransaction) FindBatchAnomalies(ctx context.Context, resultID string) ([]*model.BatchAnomaly, error) {
	var anomalies []*model.BatchAnomaly
	if err := t.db.WithContext(ctx).Where("metric_id = ?", resultID).Order("anomaly_id ASC").Find(&anomalies).Error; err != nil {
		return nil, err
	}
	return anomalies, nil
}

func hydrateAnalyticsSnapshot(result *model.AnalyticsResult) error {
	if result == nil || len(result.InputSnapshotJSON) == 0 {
		return nil
	}
	var snapshot struct {
		AnalyticsRunID string               `json:"analytics_run_id"`
		Metrics        dto.AnalyticsMetrics `json:"metrics"`
	}
	if err := json.Unmarshal(result.InputSnapshotJSON, &snapshot); err != nil {
		return fmt.Errorf("repository: decode analytics snapshot: %w", err)
	}
	result.AnalyticsRunID = snapshot.AnalyticsRunID
	result.MetricsJSON, _ = json.Marshal(snapshot.Metrics)
	return nil
}

func (t *gormBatchTransaction) FindRequestCompletedEvent(ctx context.Context, batchID string, resultID string) (*model.EventOutbox, error) {
	var events []model.EventOutbox
	if err := t.db.WithContext(ctx).
		Where("batch_id = ? AND event_type = ? AND JSON_UNQUOTE(JSON_EXTRACT(payload_json, '$.data.result_id')) = ?", batchID, model.RequestCompletedEventType, resultID).
		Order("created_at ASC").Find(&events).Error; err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, ErrEventOutboxNotFound
	}
	return &events[0], nil
}

func (t *gormBatchTransaction) CreateEvidence(ctx context.Context, evidence *model.BatchEvidence) error {
	if evidence == nil {
		return errors.New("repository: evidence is nil")
	}
	return t.db.WithContext(ctx).Create(evidence).Error
}

func (t *gormBatchTransaction) ValidateTreatmentEvidence(
	ctx context.Context,
	batchID string,
	evidenceID string,
	organisationID string,
) error {
	var count int64
	err := t.db.WithContext(ctx).
		Table("batch_evidence").
		Where(
			"evidence_id = ? AND batch_id = ? AND organisation_id = ? AND lifecycle_stage = ?",
			evidenceID,
			batchID,
			organisationID,
			model.EvidenceLifecycleTreatment,
		).
		Count(&count).
		Error
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrEvidenceNotFound
	}
	return nil
}

func (t *gormBatchTransaction) CreateTreatment(
	ctx context.Context,
	treatment *model.BatchTreatment,
) error {
	if treatment == nil {
		return errors.New("repository: treatment is nil")
	}
	return t.db.WithContext(ctx).Create(treatment).Error
}

func (t *gormBatchTransaction) UpdateBatchTreatment(
	ctx context.Context,
	batchID string,
	expectedVersion uint32,
	now time.Time,
) (*model.Batch, error) {
	result := t.db.WithContext(ctx).
		Model(&model.Batch{}).
		Where(
			"id = ? AND status = ? AND version = ?",
			batchID,
			model.BatchStatusVerified,
			expectedVersion,
		).
		Updates(map[string]any{
			"status":     model.BatchStatusRecycled,
			"version":    gorm.Expr("version + 1"),
			"updated_at": now,
		})
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, ErrBatchConcurrency
	}

	var batch model.Batch
	if err := t.db.WithContext(ctx).Where("id = ?", batchID).First(&batch).Error; err != nil {
		return nil, err
	}
	return &batch, nil
}

func (t *gormBatchTransaction) CreateAnalyticsResult(ctx context.Context, result *model.AnalyticsResult) error {
	if result == nil {
		return errors.New("repository: analytics result is nil")
	}
	return t.db.WithContext(ctx).Create(result).Error
}

func (t *gormBatchTransaction) CreateBatchAnomaly(ctx context.Context, anomaly *model.BatchAnomaly) error {
	if anomaly == nil {
		return errors.New("repository: batch anomaly is nil")
	}
	return t.db.WithContext(ctx).Create(anomaly).Error
}

func (t *gormBatchTransaction) UpdateBatchAnalytics(
	ctx context.Context,
	batchID string,
	expectedVersion uint32,
	now time.Time,
) (*model.Batch, error) {
	result := t.db.WithContext(ctx).
		Model(&model.Batch{}).
		Where("id = ? AND status = ? AND version = ?", batchID, model.BatchStatusRecycled, expectedVersion).
		Updates(map[string]any{
			"status":     model.BatchStatusCompleted,
			"version":    gorm.Expr("version + 1"),
			"updated_at": now,
		})
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, ErrBatchConcurrency
	}

	var batch model.Batch
	if err := t.db.WithContext(ctx).Where("id = ?", batchID).First(&batch).Error; err != nil {
		return nil, err
	}
	return &batch, nil
}

func (t *gormBatchTransaction) CompleteCommand(
	ctx context.Context,
	commandID string,
	responseStatus int,
	responseJSON []byte,
	completedAt time.Time,
) error {
	result := t.db.WithContext(ctx).
		Model(&model.CommandIdempotency{}).
		Where(
			"id = ? AND state = ?",
			commandID,
			model.CommandStateInProgress,
		).
		Updates(map[string]any{
			"state":           model.CommandStateCompleted,
			"response_status": responseStatus,
			"response_json":   responseJSON,
			"completed_at":    completedAt,
		})

	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrCommandConcurrency
	}

	return nil
}

func (t *gormBatchTransaction) AppendAudit(
	ctx context.Context,
	event *model.BatchAuditEvent,
) error {
	if event == nil {
		return errors.New("repository: audit event is nil")
	}

	return t.db.WithContext(ctx).Create(event).Error
}

func (t *gormBatchTransaction) EnqueueOutbox(
	ctx context.Context,
	event *model.EventOutbox,
) error {
	if event == nil {
		return errors.New("repository: outbox event is nil")
	}

	return t.db.WithContext(ctx).Create(event).Error
}
