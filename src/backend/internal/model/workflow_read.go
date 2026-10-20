package model

import "time"

// WorkflowOpportunity is the deliberately small public projection of a
// persisted matching result. Matching evidence, organisation identifiers, and
// claim internals stay behind the repository boundary.
type WorkflowOpportunity struct {
	BatchID            string      `gorm:"column:batch_id"`
	Status             BatchStatus `gorm:"column:status"`
	Version            uint32      `gorm:"column:version"`
	ClaimEpoch         uint64      `gorm:"column:claim_epoch"`
	Category           *string     `gorm:"column:category"`
	Quantity           *int        `gorm:"column:quantity"`
	EstimatedWeightKg  *string     `gorm:"column:estimated_weight_kg"`
	Zone               *string     `gorm:"column:zone"`
	CollectionDeadline *time.Time  `gorm:"column:collection_deadline"`
	EligibilityReason  string      `gorm:"column:eligibility_reason"`
}
