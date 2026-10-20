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
	ErrClaimBatchNotFound       = errors.New("repository: claim batch not found")
	ErrClaimCommandNotFound     = errors.New("repository: claim command not found")
	ErrClaimOpportunityNotFound = errors.New("repository: claim opportunity not found")
	ErrClaimPoolNotFound        = errors.New("repository: claim capacity pool not found")
	ErrClaimConcurrency         = errors.New("repository: claim concurrency conflict")
	ErrClaimActorNotEligible    = errors.New("repository: claim actor is not eligible")
)

type ClaimRepository interface {
	Transaction(context.Context, func(ClaimTransaction) error) error
}

type ClaimTransaction interface {
	ValidateRecyclerActor(context.Context, string, string) error
	FindCommand(context.Context, string, string, string) (*model.CommandIdempotency, error)
	CreateCommand(context.Context, *model.CommandIdempotency) error
	ClaimKeyExists(context.Context, string) (bool, error)
	FindBatchForUpdate(context.Context, string) (*model.Batch, error)
	FindEligibleMatch(context.Context, *model.Batch, string) (*model.ClaimMatch, error)
	LockCapacityPoolForUpdate(context.Context, string, string) (*model.CapacityPool, error)
	ReserveCapacity(context.Context, string, string, string, time.Time) error
	CreateClaim(context.Context, *model.BatchClaim) error
	CreateReservation(context.Context, *model.CapacityReservation) error
	ApproveBatch(context.Context, string, uint64, uint32, string, time.Time) (*model.Batch, error)
	FindMostRecentCollector(context.Context, string, string) (*CollectorCandidate, error)
	FindNextActiveCollector(context.Context, string, string, *CollectorCandidate, string, time.Time) (*CollectorCandidate, error)
	CreateAssignment(context.Context, *model.BatchAssignment) error
	LinkCommandAssignment(context.Context, string, string) error
	UpdateBatchAssignment(context.Context, string, uint32, model.BatchStatus, model.BatchStatus, *string, time.Time) (*model.Batch, error)
	CreateAction(context.Context, *model.AssignmentAction) error
	CompleteCommand(context.Context, string, int, []byte, time.Time) error
	AppendAudit(context.Context, *model.BatchAuditEvent) error
	EnqueueOutbox(context.Context, *model.EventOutbox) error
}

type GormClaimRepository struct {
	db *gorm.DB
}

func NewGormClaimRepository(db *gorm.DB) *GormClaimRepository {
	return &GormClaimRepository{db: db}
}

func (r *GormClaimRepository) Transaction(
	ctx context.Context,
	fn func(ClaimTransaction) error,
) error {
	if fn == nil {
		return errors.New("repository: claim transaction callback is nil")
	}

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(&gormClaimTransaction{db: tx})
	})
}

type gormClaimTransaction struct {
	db *gorm.DB
}

func (t *gormClaimTransaction) ValidateRecyclerActor(
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
		return ErrClaimActorNotEligible
	}
	return nil
}

func (t *gormClaimTransaction) FindCommand(
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
		return nil, ErrClaimCommandNotFound
	}
	if err != nil {
		return nil, err
	}

	return &command, nil
}

func (t *gormClaimTransaction) CreateCommand(
	ctx context.Context,
	command *model.CommandIdempotency,
) error {
	if command == nil {
		return errors.New("repository: claim command is nil")
	}

	return t.db.WithContext(ctx).Create(command).Error
}

func (t *gormClaimTransaction) ClaimKeyExists(
	ctx context.Context,
	idempotencyKey string,
) (bool, error) {
	var count int64
	err := t.db.WithContext(ctx).
		Model(&model.BatchClaim{}).
		Where("idempotency_key = ?", idempotencyKey).
		Count(&count).
		Error
	return count > 0, err
}

func (t *gormClaimTransaction) FindBatchForUpdate(
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
		return nil, ErrClaimBatchNotFound
	}
	if err != nil {
		return nil, err
	}

	return &batch, nil
}

// FindEligibleMatch consumes matching evidence written by the Python matcher.
// It does not calculate a new match or call the matcher service.
func (t *gormClaimTransaction) FindEligibleMatch(
	ctx context.Context,
	batch *model.Batch,
	recyclerOrgID string,
) (*model.ClaimMatch, error) {
	var match model.ClaimMatch

	err := t.db.WithContext(ctx).
		Table("matched_results AS mr").
		Select(`
			mr.decision_id,
			mr.id AS matched_result_id,
			mr.batch_id,
			mr.recycler_org_id,
			mr.capacity_pool_id,
			md.batch_version AS decision_batch_version,
			md.claim_epoch
		`).
		Joins(`
			INNER JOIN matching_decisions AS md
				ON md.id = mr.decision_id
				AND md.batch_id = mr.batch_id
		`).
		Joins(`
			INNER JOIN organisations AS org
				ON org.organisation_id = mr.recycler_org_id
		`).
		Joins(`
			INNER JOIN recycler_matching_profiles AS profile
				ON profile.recycler_org_id = mr.recycler_org_id
		`).
		Joins(`
			INNER JOIN recycler_category_capabilities AS capability
				ON capability.recycler_org_id = mr.recycler_org_id
				AND capability.capacity_pool_id = mr.capacity_pool_id
				AND capability.category = ?
		`, batch.Category).
		Joins(`
			INNER JOIN recycler_service_zones AS zone
				ON zone.recycler_org_id = mr.recycler_org_id
				AND zone.zone = ?
		`, batch.Zone).
		Joins(`
			INNER JOIN recycler_capacity_pools AS pool
				ON pool.id = mr.capacity_pool_id
				AND pool.recycler_org_id = mr.recycler_org_id
		`).
		Where(`
			mr.batch_id = ?
			AND mr.recycler_org_id = ?
			AND mr.is_matched = TRUE
			AND mr.category_match = TRUE
			AND mr.capability_match = TRUE
			AND mr.capacity_available = TRUE
			AND mr.zone_match = TRUE
			AND mr.deadline_viable = TRUE
			AND md.outcome = 'MATCHED'
			AND md.claim_epoch = ?
			AND md.batch_version = ?
			AND org.status = 'ACTIVE'
			AND org.organisation_type = 'PROCESSING_FACILITY'
			AND profile.is_active = TRUE
			AND capability.is_active = TRUE
			AND (? = FALSE OR capability.supports_data_bearing = TRUE)
			AND JSON_CONTAINS(
				capability.accepted_conditions_json,
				JSON_QUOTE(?)
			) = 1
			AND zone.is_active = TRUE
			AND pool.is_active = TRUE
			AND pool.total_kg - pool.reserved_kg >= ?
			AND ? >= DATE_ADD(
				UTC_TIMESTAMP(6),
				INTERVAL zone.minimum_lead_minutes MINUTE
			)
		`,
			batch.ID,
			recyclerOrgID,
			batch.ClaimEpoch,
			batch.Version-1,
			batch.IsDataBearing,
			batch.ConditionRating,
			batch.EstimatedWeightKg,
			batch.CollectionDeadline,
		).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Limit(1).
		Scan(&match).
		Error

	if err != nil {
		return nil, err
	}
	if match.MatchedResultID == "" {
		return nil, ErrClaimOpportunityNotFound
	}

	return &match, nil
}

func (t *gormClaimTransaction) LockCapacityPoolForUpdate(
	ctx context.Context,
	poolID string,
	recyclerOrgID string,
) (*model.CapacityPool, error) {
	var pool model.CapacityPool
	err := t.db.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND recycler_org_id = ?", poolID, recyclerOrgID).
		First(&pool).
		Error

	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrClaimPoolNotFound
	}
	if err != nil {
		return nil, err
	}

	return &pool, nil
}

func (t *gormClaimTransaction) ReserveCapacity(
	ctx context.Context,
	poolID string,
	recyclerOrgID string,
	weightKg string,
	updatedAt time.Time,
) error {
	result := t.db.WithContext(ctx).
		Model(&model.CapacityPool{}).
		Where(`
			id = ?
			AND recycler_org_id = ?
			AND is_active = TRUE
			AND total_kg - reserved_kg >= ?
		`, poolID, recyclerOrgID, weightKg).
		Updates(map[string]any{
			"reserved_kg": gorm.Expr("reserved_kg + ?", weightKg),
			"version":     gorm.Expr("version + 1"),
			"updated_at":  updatedAt,
		})

	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrClaimConcurrency
	}

	return nil
}

func (t *gormClaimTransaction) CreateClaim(
	ctx context.Context,
	claim *model.BatchClaim,
) error {
	if claim == nil {
		return errors.New("repository: claim is nil")
	}
	return t.db.WithContext(ctx).Create(claim).Error
}

func (t *gormClaimTransaction) CreateReservation(
	ctx context.Context,
	reservation *model.CapacityReservation,
) error {
	if reservation == nil {
		return errors.New("repository: reservation is nil")
	}
	return t.db.WithContext(ctx).Create(reservation).Error
}

func (t *gormClaimTransaction) ApproveBatch(
	ctx context.Context,
	batchID string,
	claimEpoch uint64,
	expectedVersion uint32,
	claimID string,
	updatedAt time.Time,
) (*model.Batch, error) {
	result := t.db.WithContext(ctx).
		Model(&model.Batch{}).
		Where(`
			id = ?
			AND status = ?
			AND current_claim_id IS NULL
			AND claim_epoch = ?
			AND version = ?
		`, batchID, model.BatchStatusMatched, claimEpoch, expectedVersion).
		Updates(map[string]any{
			"status":           model.BatchStatusApproved,
			"current_claim_id": claimID,
			"version":          gorm.Expr("version + 1"),
			"updated_at":       updatedAt,
		})

	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, ErrClaimConcurrency
	}

	var batch model.Batch
	if err := t.db.WithContext(ctx).Where("id = ?", batchID).First(&batch).Error; err != nil {
		return nil, err
	}
	return &batch, nil
}

func (t *gormClaimTransaction) FindMostRecentCollector(ctx context.Context, recyclerOrgID, zone string) (*CollectorCandidate, error) {
	return findMostRecentCollector(ctx, t.db, recyclerOrgID, zone)
}

func (t *gormClaimTransaction) FindNextActiveCollector(ctx context.Context, recyclerOrgID, zone string, after *CollectorCandidate, excludeUserID string, now time.Time) (*CollectorCandidate, error) {
	return findNextActiveCollector(ctx, t.db, recyclerOrgID, zone, after, excludeUserID, now)
}

func (t *gormClaimTransaction) CreateAssignment(ctx context.Context, assignment *model.BatchAssignment) error {
	if assignment == nil {
		return errors.New("repository: claim assignment is nil")
	}
	return t.db.WithContext(ctx).Create(assignment).Error
}

func (t *gormClaimTransaction) LinkCommandAssignment(ctx context.Context, commandID, assignmentID string) error {
	result := t.db.WithContext(ctx).Model(&model.CommandIdempotency{}).Where("id = ? AND assignment_id IS NULL", commandID).Update("assignment_id", assignmentID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrClaimConcurrency
	}
	return nil
}

func (t *gormClaimTransaction) UpdateBatchAssignment(ctx context.Context, batchID string, expectedVersion uint32, fromStatus, toStatus model.BatchStatus, assignmentID *string, now time.Time) (*model.Batch, error) {
	result := t.db.WithContext(ctx).Model(&model.Batch{}).Where("id = ? AND version = ? AND status = ?", batchID, expectedVersion, fromStatus).Updates(map[string]any{
		"status": toStatus, "current_assignment_id": assignmentID, "version": gorm.Expr("version + 1"), "updated_at": now,
	})
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, ErrClaimConcurrency
	}
	var batch model.Batch
	if err := t.db.WithContext(ctx).Where("id = ?", batchID).First(&batch).Error; err != nil {
		return nil, err
	}
	return &batch, nil
}

func (t *gormClaimTransaction) CreateAction(ctx context.Context, action *model.AssignmentAction) error {
	if action == nil {
		return errors.New("repository: claim assignment action is nil")
	}
	return t.db.WithContext(ctx).Create(action).Error
}

func (t *gormClaimTransaction) CompleteCommand(
	ctx context.Context,
	commandID string,
	responseStatus int,
	responseJSON []byte,
	completedAt time.Time,
) error {
	result := t.db.WithContext(ctx).
		Model(&model.CommandIdempotency{}).
		Where("id = ? AND state = ?", commandID, model.CommandStateInProgress).
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
		return ErrClaimConcurrency
	}
	return nil
}

func (t *gormClaimTransaction) AppendAudit(
	ctx context.Context,
	event *model.BatchAuditEvent,
) error {
	if event == nil {
		return errors.New("repository: claim audit event is nil")
	}
	return t.db.WithContext(ctx).Create(event).Error
}

func (t *gormClaimTransaction) EnqueueOutbox(
	ctx context.Context,
	event *model.EventOutbox,
) error {
	if event == nil {
		return errors.New("repository: claim outbox event is nil")
	}
	return t.db.WithContext(ctx).Create(event).Error
}
