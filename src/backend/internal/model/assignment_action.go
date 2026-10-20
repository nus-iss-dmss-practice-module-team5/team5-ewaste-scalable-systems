package model

import "time"

const (
	AssignmentActionAssigned        = "ASSIGNED"
	AssignmentActionAccepted        = "ACCEPTED"
	AssignmentActionRejected        = "REJECTED"
	AssignmentActionHandoffRecorded = "HANDOFF_RECORDED"
	AssignmentActionPickupFailed    = "PICKUP_FAILED"
	AssignmentActionReassigned      = "REASSIGNED"
)

type AssignmentAction struct {
	ID                   string      `gorm:"column:id;primaryKey;size:36"`
	BatchID              string      `gorm:"column:batch_id;size:36;index"`
	AssignmentID         string      `gorm:"column:assignment_id;size:36;index"`
	ActionType           string      `gorm:"column:action_type;size:32"`
	ActorUserID          *string     `gorm:"column:actor_user_id;size:32"`
	ServicePrincipal     *string     `gorm:"column:service_principal;size:128"`
	Reason               *string     `gorm:"column:reason;size:255"`
	PreviousAssignmentID *string     `gorm:"column:previous_assignment_id;size:36"`
	FromStatus           BatchStatus `gorm:"column:from_status;size:32"`
	ToStatus             BatchStatus `gorm:"column:to_status;size:32"`
	AssignmentVersion    uint32      `gorm:"column:assignment_version"`
	CommandID            string      `gorm:"column:command_id;size:36"`
	OccurredAt           time.Time   `gorm:"column:occurred_at"`
	CorrelationID        string      `gorm:"column:correlation_id;size:128"`
	DetailsJSON          []byte      `gorm:"column:details_json;type:json"`
}

func (AssignmentAction) TableName() string { return "assignment_actions" }
