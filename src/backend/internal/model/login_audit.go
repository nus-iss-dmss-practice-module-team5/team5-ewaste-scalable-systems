package model

import "time"

type LoginAudit struct {
	LoginAuditID   uint64    `gorm:"column:login_audit_id;primaryKey;autoIncrement"`
	UserID         *string   `gorm:"column:user_id;size:32"`
	AttemptedEmail string    `gorm:"column:attempted_email;size:254"`
	Result         string    `gorm:"column:result;size:16"`
	ReasonCode     *string   `gorm:"column:reason_code;size:40"`
	CorrelationID  string    `gorm:"column:correlation_id;size:36"`
	SourceIP       *string   `gorm:"column:source_ip;size:45"`
	OccurredAt     time.Time `gorm:"column:occurred_at"`
}

func (LoginAudit) TableName() string { return "login_audit" }
