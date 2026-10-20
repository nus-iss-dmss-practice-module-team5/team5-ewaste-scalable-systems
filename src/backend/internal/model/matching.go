package model

import "time"

// ClaimMatch contains only the persisted matching evidence needed by C3.
// It deliberately does not duplicate matching decision fields into the claim.
type ClaimMatch struct {
	DecisionID           string
	MatchedResultID      string
	BatchID              string
	RecyclerOrgID        string
	CapacityPoolID       string
	DecisionBatchVersion uint32
	ClaimEpoch           uint64
}

type CapacityPool struct {
	ID            string    `gorm:"column:id;primaryKey;size:36"`
	RecyclerOrgID string    `gorm:"column:recycler_org_id;size:32"`
	TotalKg       string    `gorm:"column:total_kg;type:decimal(12,2)"`
	ReservedKg    string    `gorm:"column:reserved_kg;type:decimal(12,2)"`
	IsActive      bool      `gorm:"column:is_active"`
	Version       int64     `gorm:"column:version"`
	UpdatedAt     time.Time `gorm:"column:updated_at"`
}

func (CapacityPool) TableName() string {
	return "recycler_capacity_pools"
}
