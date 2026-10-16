package repository

import (
	"context"
	"errors"
	"time"

	"workflow-api/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrAssignmentBatchNotFound     = errors.New("repository: assignment batch not found")
	ErrAssignmentNotFound          = errors.New("repository: assignment not found")
	ErrAssignmentCommandNotFound   = errors.New("repository: assignment command not found")
	ErrAssignmentScopeNotFound     = errors.New("repository: collector scope not found")
	ErrAssignmentClaimNotFound     = errors.New("repository: accepted claim not found")
	ErrAssignmentConcurrency       = errors.New("repository: assignment concurrency conflict")
	ErrAssignmentActorNotEligible  = errors.New("repository: assignment actor is not eligible")
	ErrAssignmentNoActiveCollector = errors.New("repository: no active collector")
)

type AssignmentRepository interface {
	Transaction(context.Context, func(AssignmentTransaction) error) error
}

type AssignmentTransaction interface {
	ValidateCollectorActor(context.Context, string, string) error
	FindCommand(context.Context, string, string, string) (*model.CommandIdempotency, error)
	CreateCommand(context.Context, *model.CommandIdempotency) error
	LinkCommandAssignment(context.Context, string, string) error
	CompleteCommand(context.Context, string, int, []byte, time.Time) error
	FindBatchForUpdate(context.Context, string) (*model.Batch, error)
	FindAcceptedClaimReservation(context.Context, *model.Batch) (*model.BatchClaim, *model.CapacityReservation, error)
	FindCollectorScope(context.Context, string, string, string, string, time.Time) (*model.RecyclerCollectorScope, error)
	FindMostRecentCollector(context.Context, string, string) (*CollectorCandidate, error)
	FindNextActiveCollector(context.Context, string, string, *CollectorCandidate, string, time.Time) (*CollectorCandidate, error)
	FindLatestAssignment(context.Context, string) (*model.BatchAssignment, error)
	FindAssignmentBatchID(context.Context, string) (string, error)
	FindAssignmentForUpdate(context.Context, string) (*model.BatchAssignment, error)
	CreateAssignment(context.Context, *model.BatchAssignment) error
	UpdateAssignment(context.Context, *model.BatchAssignment) error
	UpdateBatchAssignment(context.Context, string, uint32, model.BatchStatus, model.BatchStatus, *string, time.Time) (*model.Batch, error)
	CreateHandoff(context.Context, *model.BatchHandoff) error
	CreateAction(context.Context, *model.AssignmentAction) error
	AppendAudit(context.Context, *model.BatchAuditEvent) error
	EnqueueOutbox(context.Context, *model.EventOutbox) error
}

type GormAssignmentRepository struct{ db *gorm.DB }

func NewGormAssignmentRepository(db *gorm.DB) *GormAssignmentRepository {
	return &GormAssignmentRepository{db: db}
}

func (r *GormAssignmentRepository) Transaction(ctx context.Context, fn func(AssignmentTransaction) error) error {
	if fn == nil {
		return errors.New("repository: assignment transaction callback is nil")
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(&gormAssignmentTransaction{db: tx})
	})
}

type gormAssignmentTransaction struct{ db *gorm.DB }

func (t *gormAssignmentTransaction) ValidateCollectorActor(ctx context.Context, userID, organisationID string) error {
	var count int64
	err := t.db.WithContext(ctx).Table("users AS u").
		Joins("INNER JOIN roles AS role ON role.role_code = u.role_code").
		Joins("INNER JOIN organisations AS org ON org.organisation_id = u.organisation_id").
		Where(`u.user_id = ? AND u.organisation_id = ? AND u.status = 'ACTIVE' AND u.role_code = 'COLLECTOR' AND role.is_active = TRUE AND role.allowed_organisation_type = 'COLLECTION_OPERATOR' AND org.organisation_id = ? AND org.organisation_type = 'COLLECTION_OPERATOR' AND org.status = 'ACTIVE'`, userID, organisationID, organisationID).
		Count(&count).Error
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrAssignmentActorNotEligible
	}
	return nil
}

func (t *gormAssignmentTransaction) FindCommand(ctx context.Context, actorScope, commandName, key string) (*model.CommandIdempotency, error) {
	var command model.CommandIdempotency
	err := t.db.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("actor_scope = ? AND command_name = ? AND idempotency_key = ?", actorScope, commandName, key).First(&command).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrAssignmentCommandNotFound
	}
	if err != nil {
		return nil, err
	}
	return &command, nil
}

func (t *gormAssignmentTransaction) CreateCommand(ctx context.Context, command *model.CommandIdempotency) error {
	if command == nil {
		return errors.New("repository: assignment command is nil")
	}
	return t.db.WithContext(ctx).Create(command).Error
}

func (t *gormAssignmentTransaction) LinkCommandAssignment(ctx context.Context, commandID, assignmentID string) error {
	result := t.db.WithContext(ctx).Model(&model.CommandIdempotency{}).Where("id = ? AND assignment_id IS NULL", commandID).Update("assignment_id", assignmentID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrAssignmentConcurrency
	}
	return nil
}

func (t *gormAssignmentTransaction) CompleteCommand(ctx context.Context, commandID string, status int, responseJSON []byte, completedAt time.Time) error {
	result := t.db.WithContext(ctx).Model(&model.CommandIdempotency{}).Where("id = ? AND state = ?", commandID, model.CommandStateInProgress).Updates(map[string]any{
		"state": model.CommandStateCompleted, "response_status": status, "response_json": responseJSON, "completed_at": completedAt,
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrAssignmentConcurrency
	}
	return nil
}

func (t *gormAssignmentTransaction) FindBatchForUpdate(ctx context.Context, batchID string) (*model.Batch, error) {
	var batch model.Batch
	err := t.db.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", batchID).First(&batch).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrAssignmentBatchNotFound
	}
	if err != nil {
		return nil, err
	}
	return &batch, nil
}

func (t *gormAssignmentTransaction) FindAcceptedClaimReservation(ctx context.Context, batch *model.Batch) (*model.BatchClaim, *model.CapacityReservation, error) {
	if batch == nil || batch.CurrentClaimID == nil {
		return nil, nil, ErrAssignmentClaimNotFound
	}
	var claim model.BatchClaim
	err := t.db.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND batch_id = ? AND claim_epoch = ? AND claim_status = ?", *batch.CurrentClaimID, batch.ID, batch.ClaimEpoch, model.ClaimStatusAccepted).First(&claim).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, ErrAssignmentClaimNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	var reservation model.CapacityReservation
	err = t.db.WithContext(ctx).Where("batch_id = ? AND claim_id = ? AND status = ?", batch.ID, claim.ID, model.CapacityReservationStatusReserved).First(&reservation).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, ErrAssignmentClaimNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	return &claim, &reservation, nil
}

func (t *gormAssignmentTransaction) FindCollectorScope(ctx context.Context, scopeID, recyclerOrgID, collectorOrgID, zone string, now time.Time) (*model.RecyclerCollectorScope, error) {
	var scope model.RecyclerCollectorScope
	err := t.db.WithContext(ctx).Where("id = ? AND recycler_org_id = ? AND collector_org_id = ? AND zone = ? AND is_active = TRUE AND valid_from <= ? AND (valid_until IS NULL OR valid_until > ?)", scopeID, recyclerOrgID, collectorOrgID, zone, now, now).First(&scope).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrAssignmentScopeNotFound
	}
	if err != nil {
		return nil, err
	}
	return &scope, nil
}

func (t *gormAssignmentTransaction) FindMostRecentCollector(ctx context.Context, recyclerOrgID, zone string) (*CollectorCandidate, error) {
	return findMostRecentCollector(ctx, t.db, recyclerOrgID, zone)
}

func (t *gormAssignmentTransaction) FindNextActiveCollector(ctx context.Context, recyclerOrgID, zone string, after *CollectorCandidate, excludeUserID string, now time.Time) (*CollectorCandidate, error) {
	return findNextActiveCollector(ctx, t.db, recyclerOrgID, zone, after, excludeUserID, now)
}

func (t *gormAssignmentTransaction) FindLatestAssignment(ctx context.Context, batchID string) (*model.BatchAssignment, error) {
	var assignment model.BatchAssignment
	err := t.db.WithContext(ctx).Where("batch_id = ?", batchID).Order("assignment_sequence DESC").First(&assignment).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrAssignmentNotFound
	}
	if err != nil {
		return nil, err
	}
	return &assignment, nil
}

// FindAssignmentBatchID reads only the immutable parent identity, without a
// row lock, so callers can lock the batch before any assignment row.
func (t *gormAssignmentTransaction) FindAssignmentBatchID(ctx context.Context, assignmentID string) (string, error) {
	var assignment model.BatchAssignment
	err := t.db.WithContext(ctx).Select("batch_id").Where("id = ?", assignmentID).Take(&assignment).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", ErrAssignmentNotFound
	}
	return assignment.BatchID, err
}

func (t *gormAssignmentTransaction) FindAssignmentForUpdate(ctx context.Context, assignmentID string) (*model.BatchAssignment, error) {
	var assignment model.BatchAssignment
	err := t.db.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", assignmentID).First(&assignment).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrAssignmentNotFound
	}
	if err != nil {
		return nil, err
	}
	return &assignment, nil
}

func (t *gormAssignmentTransaction) CreateAssignment(ctx context.Context, assignment *model.BatchAssignment) error {
	if assignment == nil {
		return errors.New("repository: assignment is nil")
	}
	return t.db.WithContext(ctx).Create(assignment).Error
}

func (t *gormAssignmentTransaction) UpdateAssignment(ctx context.Context, assignment *model.BatchAssignment) error {
	if assignment == nil {
		return errors.New("repository: assignment is nil")
	}
	result := t.db.WithContext(ctx).Model(&model.BatchAssignment{}).Where("id = ? AND version = ?", assignment.ID, assignment.Version-1).Updates(map[string]any{
		"assignment_status": assignment.AssignmentStatus, "rejection_reason": assignment.RejectionReason, "responded_at": assignment.RespondedAt, "closed_at": assignment.ClosedAt, "closure_reason": assignment.ClosureReason, "version": assignment.Version, "updated_at": assignment.UpdatedAt,
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrAssignmentConcurrency
	}
	return nil
}

func (t *gormAssignmentTransaction) UpdateBatchAssignment(ctx context.Context, batchID string, expectedVersion uint32, fromStatus, toStatus model.BatchStatus, assignmentID *string, now time.Time) (*model.Batch, error) {
	updates := map[string]any{"status": toStatus, "current_assignment_id": assignmentID, "version": gorm.Expr("version + 1"), "updated_at": now}
	result := t.db.WithContext(ctx).Model(&model.Batch{}).Where("id = ? AND version = ? AND status = ?", batchID, expectedVersion, fromStatus).Updates(updates)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, ErrAssignmentConcurrency
	}
	var batch model.Batch
	if err := t.db.WithContext(ctx).Where("id = ?", batchID).First(&batch).Error; err != nil {
		return nil, err
	}
	return &batch, nil
}

func (t *gormAssignmentTransaction) CreateHandoff(ctx context.Context, handoff *model.BatchHandoff) error {
	if handoff == nil {
		return errors.New("repository: handoff is nil")
	}
	return t.db.WithContext(ctx).Create(handoff).Error
}

func (t *gormAssignmentTransaction) CreateAction(ctx context.Context, action *model.AssignmentAction) error {
	if action == nil {
		return errors.New("repository: assignment action is nil")
	}
	return t.db.WithContext(ctx).Create(action).Error
}

func (t *gormAssignmentTransaction) AppendAudit(ctx context.Context, event *model.BatchAuditEvent) error {
	if event == nil {
		return errors.New("repository: assignment audit event is nil")
	}
	return t.db.WithContext(ctx).Create(event).Error
}

func (t *gormAssignmentTransaction) EnqueueOutbox(ctx context.Context, event *model.EventOutbox) error {
	if event == nil {
		return errors.New("repository: assignment outbox event is nil")
	}
	return t.db.WithContext(ctx).Create(event).Error
}
