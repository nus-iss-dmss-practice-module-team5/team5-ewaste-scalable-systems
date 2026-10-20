package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"workflow-api/internal/model"
)

var ErrNotFound = errors.New("repository: record not found")
var ErrRotationRejected = errors.New("repository: refresh rotation rejected")

type AuthRepository interface {
	FindActiveUserByEmail(ctx context.Context, email string) (*model.User, error)
	FindActiveUserByID(ctx context.Context, userID string) (*model.User, error)
	CreateLoginSession(ctx context.Context, user *model.User, session *model.Session, loginAt time.Time) error
	CreateLoginAudit(ctx context.Context, audit *model.LoginAudit) error
	FindSession(ctx context.Context, sessionID string) (*model.Session, error)
	RotateSession(ctx context.Context, sessionID, userID, oldHash, newHash string, expiresAt, now time.Time) error
	RevokeSession(ctx context.Context, sessionID, userID, reason string, revokedAt time.Time) error
}

type GormAuthRepository struct {
	db *gorm.DB
}

func NewGormAuthRepository(db *gorm.DB) *GormAuthRepository {
	return &GormAuthRepository{db: db}
}

func (r *GormAuthRepository) FindActiveUserByEmail(ctx context.Context, email string) (*model.User, error) {
	var user model.User
	err := r.db.WithContext(ctx).
		Where("email = ? AND status = ?", email, "ACTIVE").
		First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *GormAuthRepository) FindActiveUserByID(ctx context.Context, userID string) (*model.User, error) {
	var user model.User
	err := r.db.WithContext(ctx).
		Where("user_id = ? AND status = ?", userID, "ACTIVE").
		First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *GormAuthRepository) CreateLoginSession(ctx context.Context, user *model.User, session *model.Session, loginAt time.Time) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.User{}).
			Where("user_id = ? AND status = ?", user.UserID, "ACTIVE").
			Updates(map[string]any{
				"failed_login_attempts": 0,
				"locked_until":          nil,
				"last_login_at":         loginAt,
			}).Error; err != nil {
			return err
		}
		return tx.Create(session).Error
	})
}

func (r *GormAuthRepository) CreateLoginAudit(ctx context.Context, audit *model.LoginAudit) error {
	return r.db.WithContext(ctx).Create(audit).Error
}

func (r *GormAuthRepository) FindSession(ctx context.Context, sessionID string) (*model.Session, error) {
	var session model.Session
	err := r.db.WithContext(ctx).Where("session_id = ?", sessionID).First(&session).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &session, nil
}

func (r *GormAuthRepository) RotateSession(ctx context.Context, sessionID, userID, oldHash, newHash string, expiresAt, now time.Time) error {
	result := r.db.WithContext(ctx).Model(&model.Session{}).
		Where("session_id = ? AND user_id = ? AND token_hash = ? AND revoked_at IS NULL AND expires_at > ?",
			sessionID, userID, oldHash, now).
		Updates(map[string]any{
			"token_hash":   newHash,
			"expires_at":   expiresAt,
			"last_seen_at": now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrRotationRejected
	}
	return nil
}

func (r *GormAuthRepository) RevokeSession(ctx context.Context, sessionID, userID, reason string, revokedAt time.Time) error {
	result := r.db.WithContext(ctx).Model(&model.Session{}).
		Where("session_id = ? AND user_id = ? AND revoked_at IS NULL", sessionID, userID).
		Updates(map[string]any{
			"revoked_at":        revokedAt,
			"revocation_reason": reason,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
