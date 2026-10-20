package model

import "time"

type Session struct {
	SessionID        string     `gorm:"column:session_id;primaryKey;size:36"`
	UserID           string     `gorm:"column:user_id;size:32"`
	RefreshTokenHash string     `gorm:"column:token_hash;size:64"`
	Status           string     `gorm:"-"`
	IssuedAt         time.Time  `gorm:"column:issued_at"`
	ExpiresAt        time.Time  `gorm:"column:expires_at"`
	LastSeenAt       *time.Time `gorm:"column:last_seen_at"`
	RevokedAt        *time.Time `gorm:"column:revoked_at"`
	RevocationReason *string    `gorm:"column:revocation_reason;size:120"`
}

func (Session) TableName() string { return "sessions" }

func (s Session) IsActive(now time.Time) bool {
	return s.ExpiresAt.After(now) &&
		s.RevokedAt == nil &&
		(s.Status == "" || s.Status == "ACTIVE")
}
