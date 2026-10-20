package model

import "time"

const ClaimStatusAccepted = "ACCEPTED"

type BatchClaim struct {
	ID             string     `gorm:"column:id;primaryKey;size:36"`
	BatchID        string     `gorm:"column:batch_id;size:36;index"`
	ClaimEpoch     uint64     `gorm:"column:claim_epoch"`
	RecyclerOrgID  string     `gorm:"column:recycler_org_id;size:32;index"`
	ClaimedBy      string     `gorm:"column:claimed_by;size:32;index"`
	ClaimStatus    string     `gorm:"column:claim_status;size:32"`
	IdempotencyKey string     `gorm:"column:idempotency_key;size:64"`
	ClaimedAt      time.Time  `gorm:"column:claimed_at"`
	SupersededAt   *time.Time `gorm:"column:superseded_at"`
	Notes          *string    `gorm:"column:notes;size:255"`
	CreatedAt      time.Time  `gorm:"column:created_at"`
}

func (BatchClaim) TableName() string {
	return "batch_claims"
}
