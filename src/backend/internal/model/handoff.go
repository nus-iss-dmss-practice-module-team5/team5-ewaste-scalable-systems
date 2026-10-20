package model

import "time"

const (
	PickupStatusCollected = "COLLECTED"
	PickupStatusFailed    = "FAILED_COLLECTION"
)

type BatchHandoff struct {
	ID                        string     `gorm:"column:id;primaryKey;size:36"`
	BatchID                   string     `gorm:"column:batch_id;size:36;index"`
	AssignmentID              string     `gorm:"column:assignment_id;size:36;uniqueIndex"`
	CollectorUserID           string     `gorm:"column:collector_user_id;size:32"`
	CollectorOrgID            string     `gorm:"column:collector_org_id;size:32"`
	PickupStatus              string     `gorm:"column:pickup_status;size:24"`
	DonorRepresentativeName   *string    `gorm:"column:donor_representative_name;size:100"`
	ActualItemCount           *int       `gorm:"column:actual_item_count"`
	VerificationHash          *string    `gorm:"column:verification_hash;size:64"`
	FailureReason             *string    `gorm:"column:failure_reason;size:32"`
	QuantityDiscrepancyReason *string    `gorm:"column:quantity_discrepancy_reason;size:255"`
	Notes                     *string    `gorm:"column:notes;size:500"`
	PickupOccurredAt          time.Time  `gorm:"column:pickup_occurred_at"`
	RecordedAt                time.Time  `gorm:"column:recorded_at"`
	CollectedAt               *time.Time `gorm:"column:collected_at"`
	CommandID                 string     `gorm:"column:command_id;size:36;uniqueIndex"`
	CorrelationID             string     `gorm:"column:correlation_id;size:128"`
	CreatedAt                 time.Time  `gorm:"column:created_at"`
}

func (BatchHandoff) TableName() string { return "batch_handoffs" }
