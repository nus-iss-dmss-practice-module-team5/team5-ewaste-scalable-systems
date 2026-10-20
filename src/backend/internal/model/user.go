package model

import "time"

type User struct {
	UserID              string     `gorm:"column:user_id;primaryKey;size:32"`
	Email               string     `gorm:"column:email;uniqueIndex;size:254"`
	DisplayName         string     `gorm:"column:display_name;size:120"`
	PasswordHash        string     `gorm:"column:password_hash;size:255"`
	RoleCode            string     `gorm:"column:role_code;size:32"`
	OrganisationID      string     `gorm:"column:organisation_id;size:32"`
	Status              string     `gorm:"column:status;size:24"`
	FailedLoginAttempts uint16     `gorm:"column:failed_login_attempts"`
	LockedUntil         *time.Time `gorm:"column:locked_until"`
	LastLoginAt         *time.Time `gorm:"column:last_login_at"`
	CreatedAt           time.Time  `gorm:"column:created_at"`
	UpdatedAt           time.Time  `gorm:"column:updated_at"`
}

func (User) TableName() string { return "users" }
