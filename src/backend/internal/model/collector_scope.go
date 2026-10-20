package model

import "time"

// RecyclerCollectorScope is the persisted authorization boundary for a collector.
type RecyclerCollectorScope struct {
	ID             string     `gorm:"column:id;primaryKey;size:36"`
	RecyclerOrgID  string     `gorm:"column:recycler_org_id;size:32;index"`
	CollectorOrgID string     `gorm:"column:collector_org_id;size:32;index"`
	Zone           string     `gorm:"column:zone;size:16"`
	IsActive       bool       `gorm:"column:is_active;not null"`
	Version        uint64     `gorm:"column:version;not null"`
	ValidFrom      time.Time  `gorm:"column:valid_from"`
	ValidUntil     *time.Time `gorm:"column:valid_until"`
	CreatedAt      time.Time  `gorm:"column:created_at"`
	UpdatedAt      time.Time  `gorm:"column:updated_at"`
}

func (RecyclerCollectorScope) TableName() string { return "recycler_collector_scopes" }
