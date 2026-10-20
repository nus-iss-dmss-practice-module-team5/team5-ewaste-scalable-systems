package model

import "time"

type BatchStatus string

const (
	BatchStatusDraft            BatchStatus = "DRAFT"
	BatchStatusSubmitted        BatchStatus = "SUBMITTED"
	BatchStatusMatched          BatchStatus = "MATCHED"
	BatchStatusApproved         BatchStatus = "APPROVED"
	BatchStatusAssigned         BatchStatus = "ASSIGNED"
	BatchStatusCollected        BatchStatus = "COLLECTED"
	BatchStatusVerified         BatchStatus = "VERIFIED"
	BatchStatusRecycled         BatchStatus = "RECYCLED"
	BatchStatusCompleted        BatchStatus = "COMPLETED"
	BatchStatusFailedCollection BatchStatus = "FAILED_COLLECTION"
)

type Batch struct {
	ID                  string      `gorm:"column:id;primaryKey;size:36"`
	OrganizationID      string      `gorm:"column:organization_id;size:32;index"`
	CreatedBy           string      `gorm:"column:created_by;size:32;index"`
	Status              BatchStatus `gorm:"column:status;size:32;index"`
	Category            *string     `gorm:"column:category;size:32"`
	Quantity            *int        `gorm:"column:quantity"`
	EstimatedWeightKg   *string     `gorm:"column:estimated_weight_kg;type:decimal(8,2)"`
	ConditionRating     *string     `gorm:"column:condition_rating;size:32"`
	IsDataBearing       bool        `gorm:"column:is_data_bearing;not null;default:false"`
	Zone                *string     `gorm:"column:zone;size:16"`
	CollectionDeadline  *time.Time  `gorm:"column:collection_deadline"`
	Notes               *string     `gorm:"column:notes;size:500"`
	ClaimEpoch          uint64      `gorm:"column:claim_epoch;not null;default:1"`
	CurrentClaimID      *string     `gorm:"column:current_claim_id;size:36"`
	CurrentAssignmentID *string     `gorm:"column:current_assignment_id;size:36"`
	// CollectorScopeID is populated only by the collector-scoped read query.
	// It is derived from the accepted claim and the active recycler/collector
	// scope, so it must never be persisted with the batch itself.
	CollectorScopeID *string    `gorm:"column:collector_scope_id;->"`
	Version          uint32     `gorm:"column:version;not null;default:1"`
	SubmittedAt      *time.Time `gorm:"column:submitted_at"`
	CreatedAt        time.Time  `gorm:"column:created_at"`
	UpdatedAt        time.Time  `gorm:"column:updated_at"`
}

func (Batch) TableName() string {
	return "ewaste_batches"
}

func (b Batch) CanEdit() bool {
	return b.Status == BatchStatusDraft
}

func (b Batch) CanSubmit() bool {
	return b.Status == BatchStatusDraft
}
