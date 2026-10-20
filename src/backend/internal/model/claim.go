package model

import "time"

const CapacityReservationStatusReserved = "RESERVED"

type CapacityReservation struct {
	ID               string     `gorm:"column:id;primaryKey;size:36"`
	BatchID          string     `gorm:"column:batch_id;size:36;index"`
	ClaimID          string     `gorm:"column:claim_id;size:36;uniqueIndex"`
	CapacityPoolID   string     `gorm:"column:capacity_pool_id;size:36;index"`
	ReservedKg       string     `gorm:"column:reserved_kg;type:decimal(8,2)"`
	Status           string     `gorm:"column:status;size:16"`
	ReservedAt       time.Time  `gorm:"column:reserved_at"`
	ReleasedAt       *time.Time `gorm:"column:released_at"`
	ReleaseReason    *string    `gorm:"column:release_reason;size:255"`
	ReleaseCommandID *string    `gorm:"column:release_command_id;size:36"`
	Version          int64      `gorm:"column:version"`
}

func (CapacityReservation) TableName() string {
	return "capacity_reservations"
}
