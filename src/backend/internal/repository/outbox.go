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
	ErrOutboxEventNotFound = errors.New("repository: outbox event not found")
	ErrOutboxLeaseLost     = errors.New("repository: outbox lease lost")
)

type OutboxRepository interface {
	ClaimPending(
		ctx context.Context,
		now time.Time,
		limit int,
		leaseDuration time.Duration,
	) ([]model.EventOutbox, error)

	MarkPublished(
		ctx context.Context,
		eventID string,
		publishedAt time.Time,
	) error

	MarkRetry(
		ctx context.Context,
		eventID string,
		nextAttemptAt time.Time,
		errorCode string,
	) error

	MarkQuarantined(
		ctx context.Context,
		eventID string,
		errorCode string,
	) error
}

type GormOutboxRepository struct {
	db *gorm.DB
}

func NewGormOutboxRepository(db *gorm.DB) *GormOutboxRepository {
	return &GormOutboxRepository{db: db}
}

func (r *GormOutboxRepository) ClaimPending(
	ctx context.Context,
	now time.Time,
	limit int,
	leaseDuration time.Duration,
) ([]model.EventOutbox, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository: outbox database is nil")
	}
	if limit <= 0 {
		return nil, errors.New("repository: outbox batch size must be positive")
	}
	if leaseDuration <= 0 {
		return nil, errors.New("repository: outbox lease duration must be positive")
	}

	now = now.UTC()
	leaseUntil := now.Add(leaseDuration)

	var events []model.EventOutbox

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Table("event_outbox AS candidate").Select("candidate.*").
			Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("candidate.publish_state = ? AND candidate.next_attempt_at <= ?", model.OutboxPublishStatePending, now).
			Where(`NOT EXISTS (SELECT 1 FROM event_outbox AS blocked
                WHERE blocked.batch_id = candidate.batch_id AND blocked.publish_state = 'QUARANTINED')`).
			Where(`NOT EXISTS (SELECT 1 FROM event_outbox AS predecessor
                WHERE predecessor.batch_id = candidate.batch_id AND predecessor.publish_state = 'PENDING'
                AND (predecessor.aggregate_version, predecessor.created_at, predecessor.event_id)
                  < (candidate.aggregate_version, candidate.created_at, candidate.event_id))`).
			Order("candidate.aggregate_version ASC, candidate.created_at ASC, candidate.event_id ASC").
			Limit(limit).Find(&events).Error
		if err != nil {
			return err
		}

		if len(events) == 0 {
			return nil
		}

		eventIDs := make([]string, 0, len(events))
		for _, event := range events {
			eventIDs = append(eventIDs, event.EventID)
		}

		result := tx.
			Model(&model.EventOutbox{}).
			Where(
				"event_id IN ? AND publish_state = ?",
				eventIDs,
				model.OutboxPublishStatePending,
			).
			Updates(map[string]any{
				"attempt_count":   gorm.Expr("attempt_count + 1"),
				"next_attempt_at": leaseUntil,
			})

		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != int64(len(events)) {
			return ErrOutboxLeaseLost
		}

		return nil
	})

	return events, err
}

func (r *GormOutboxRepository) MarkPublished(
	ctx context.Context,
	eventID string,
	publishedAt time.Time,
) error {
	publishedAt = publishedAt.UTC()

	result := r.db.WithContext(ctx).
		Model(&model.EventOutbox{}).
		Where(
			"event_id = ? AND publish_state = ?",
			eventID,
			model.OutboxPublishStatePending,
		).
		Updates(map[string]any{
			"publish_state":   model.OutboxPublishStatePublished,
			"published_at":    publishedAt,
			"next_attempt_at": nil,
			"last_error_code": nil,
		})

	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		return nil
	}

	var state model.EventOutbox
	err := r.db.WithContext(ctx).
		Select("publish_state").
		Where("event_id = ?", eventID).
		First(&state).
		Error

	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrOutboxEventNotFound
	}
	if err != nil {
		return err
	}
	if state.PublishState == model.OutboxPublishStatePublished {
		return nil
	}

	return ErrOutboxLeaseLost
}

func (r *GormOutboxRepository) MarkRetry(
	ctx context.Context,
	eventID string,
	nextAttemptAt time.Time,
	errorCode string,
) error {
	nextAttemptAt = nextAttemptAt.UTC()

	result := r.db.WithContext(ctx).
		Model(&model.EventOutbox{}).
		Where(
			"event_id = ? AND publish_state = ?",
			eventID,
			model.OutboxPublishStatePending,
		).
		Updates(map[string]any{
			"next_attempt_at": nextAttemptAt,
			"last_error_code": errorCode,
		})

	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		return nil
	}

	var state model.EventOutbox
	err := r.db.WithContext(ctx).
		Select("publish_state").
		Where("event_id = ?", eventID).
		First(&state).
		Error

	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrOutboxEventNotFound
	}
	if err != nil {
		return err
	}
	if state.PublishState == model.OutboxPublishStatePublished {
		return nil
	}

	return ErrOutboxLeaseLost
}

// MarkQuarantined preserves the immutable payload while preventing endless
// retries for malformed or unsupported persisted events.
func (r *GormOutboxRepository) MarkQuarantined(
	ctx context.Context,
	eventID string,
	errorCode string,
) error {
	result := r.db.WithContext(ctx).
		Model(&model.EventOutbox{}).
		Where(
			"event_id = ? AND publish_state = ?",
			eventID,
			model.OutboxPublishStatePending,
		).
		Updates(map[string]any{
			"publish_state":   model.OutboxPublishStateQuarantined,
			"next_attempt_at": nil,
			"last_error_code": errorCode,
		})

	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		return nil
	}

	var state model.EventOutbox
	err := r.db.WithContext(ctx).
		Select("publish_state").
		Where("event_id = ?", eventID).
		First(&state).
		Error

	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrOutboxEventNotFound
	}
	if err != nil {
		return err
	}
	if state.PublishState == model.OutboxPublishStateQuarantined {
		return nil
	}

	return ErrOutboxLeaseLost
}
