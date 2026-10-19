package model

import "time"

type RegistrationReview struct {
	ReviewID       string    `gorm:"column:review_id;primaryKey;size:36"`
	UserID         string    `gorm:"column:user_id;size:32"`
	OrganisationID string    `gorm:"column:organisation_id;size:32"`
	Decision       string    `gorm:"column:decision;size:16"`
	ReviewedBy     string    `gorm:"column:reviewed_by;size:32"`
	Reason         *string   `gorm:"column:reason;size:255"`
	CorrelationID  string    `gorm:"column:correlation_id;size:128"`
	ReviewedAt     time.Time `gorm:"column:reviewed_at"`
}

func (RegistrationReview) TableName() string { return "registration_reviews" }

// PendingRegistration is the read model an Administrator reviews: one pending
// user joined with the organisation created at registration.
type PendingRegistration struct {
	UserID           string    `gorm:"column:user_id"`
	Email            string    `gorm:"column:email"`
	DisplayName      string    `gorm:"column:display_name"`
	RoleCode         string    `gorm:"column:role_code"`
	OrganisationID   string    `gorm:"column:organisation_id"`
	OrganisationName string    `gorm:"column:organisation_name"`
	OrganisationType string    `gorm:"column:organisation_type"`
	CreatedAt        time.Time `gorm:"column:created_at"`
}
