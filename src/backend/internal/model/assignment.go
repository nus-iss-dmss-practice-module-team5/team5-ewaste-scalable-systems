package model

import "time"

const (
	AssignmentStatusPending    = "PENDING"
	AssignmentStatusAccepted   = "ACCEPTED"
	AssignmentStatusSuperseded = "SUPERSEDED"
	AssignmentStatusCompleted  = "COMPLETED"
	AssignmentStatusFailed     = "FAILED"
)

type BatchAssignment struct {
	ID                   string     `gorm:"column:id;primaryKey;size:36"`
	BatchID              string     `gorm:"column:batch_id;size:36;index"`
	ClaimID              string     `gorm:"column:claim_id;size:36;index"`
	RecyclerOrgID        string     `gorm:"column:recycler_org_id;size:32;index"`
	CollectorOrgID       string     `gorm:"column:collector_org_id;size:32;index"`
	CollectorUserID      string     `gorm:"column:collector_user_id;size:32;index"`
	CollectorScopeID     string     `gorm:"column:collector_scope_id;size:36"`
	AssignmentSequence   uint64     `gorm:"column:assignment_sequence"`
	ClaimEpoch           uint64     `gorm:"column:claim_epoch"`
	PreviousAssignmentID *string    `gorm:"column:previous_assignment_id;size:36"`
	AssignmentStatus     string     `gorm:"column:assignment_status;size:24;index"`
	RejectionReason      *string    `gorm:"column:rejection_reason;size:255"`
	ReassignmentReason   *string    `gorm:"column:reassignment_reason;size:255"`
	AssignedAt           time.Time  `gorm:"column:assigned_at"`
	RespondedAt          *time.Time `gorm:"column:responded_at"`
	ClosedAt             *time.Time `gorm:"column:closed_at"`
	ClosureReason        *string    `gorm:"column:closure_reason;size:48"`
	Version              uint32     `gorm:"column:version;not null"`
	// Read projection from ewaste_batches, never an assignment-table column.
	BatchVersion uint32    `gorm:"column:batch_version;->;-:migration"`
	CreatedAt    time.Time `gorm:"column:created_at"`
	UpdatedAt    time.Time `gorm:"column:updated_at"`
}

func (BatchAssignment) TableName() string { return "batch_assignments" }
