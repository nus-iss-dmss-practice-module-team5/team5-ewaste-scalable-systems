package repository

import (
	"context"
	"errors"

	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"workflow-api/internal/model"
)

var ErrDuplicateRegistration = errors.New("repository: email or organisation already registered")
var ErrRegistrationNotPending = errors.New("repository: registration is not pending")

const mysqlDuplicateEntry = 1062

// MaxPendingRegistrations bounds the Administrator review queue read.
const MaxPendingRegistrations = 200

type RegistrationRepository interface {
	CreateRegistration(ctx context.Context, organisation *model.Organisation, user *model.User) error
	ListPendingRegistrations(ctx context.Context) ([]model.PendingRegistration, error)
	// DecideRegistration moves a PENDING user and its PENDING organisation to the
	// given statuses and appends the review, all in one transaction.
	DecideRegistration(ctx context.Context, review *model.RegistrationReview, userStatus, organisationStatus string) error
}

type GormRegistrationRepository struct {
	db *gorm.DB
}

func NewGormRegistrationRepository(db *gorm.DB) *GormRegistrationRepository {
	return &GormRegistrationRepository{db: db}
}

func (r *GormRegistrationRepository) CreateRegistration(ctx context.Context, organisation *model.Organisation, user *model.User) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(organisation).Error; err != nil {
			return err
		}
		return tx.Create(user).Error
	})
	if isDuplicateEntry(err) {
		return ErrDuplicateRegistration
	}
	return err
}

func (r *GormRegistrationRepository) ListPendingRegistrations(ctx context.Context) ([]model.PendingRegistration, error) {
	var rows []model.PendingRegistration
	err := r.db.WithContext(ctx).
		Table("users AS u").
		Select("u.user_id, u.email, u.display_name, u.role_code, u.organisation_id, o.organisation_name, o.organisation_type, u.created_at").
		Joins("JOIN organisations AS o ON o.organisation_id = u.organisation_id").
		Where("u.status = ? AND o.status = ?", "PENDING", "PENDING").
		Order("u.created_at ASC, u.user_id ASC").
		Limit(MaxPendingRegistrations).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *GormRegistrationRepository) DecideRegistration(ctx context.Context, review *model.RegistrationReview, userStatus, organisationStatus string) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var user model.User
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ?", review.UserID).
			First(&user).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if user.Status != "PENDING" {
			return ErrRegistrationNotPending
		}

		if err := tx.Model(&model.User{}).
			Where("user_id = ? AND status = ?", user.UserID, "PENDING").
			Update("status", userStatus).Error; err != nil {
			return err
		}
		result := tx.Model(&model.Organisation{}).
			Where("organisation_id = ? AND status = ?", user.OrganisationID, "PENDING").
			Update("status", organisationStatus)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrRegistrationNotPending
		}

		review.OrganisationID = user.OrganisationID
		return tx.Create(review).Error
	})
	if isDuplicateEntry(err) {
		return ErrRegistrationNotPending
	}
	return err
}

func isDuplicateEntry(err error) bool {
	var mysqlErr *mysqldriver.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == mysqlDuplicateEntry
}
