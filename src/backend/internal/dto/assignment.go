package dto

import "time"

type AssignmentSelectionRequest struct {
	ExpectedVersion  int64  `json:"expected_version"`
	ClaimEpoch       string `json:"claim_epoch"`
	CollectorScopeID string `json:"collector_scope_id"`
}

type RejectAssignmentRequest struct {
	RejectionReason string `json:"rejection_reason"`
}

type HandoffRequest struct {
	PickupOccurredAt        time.Time `json:"pickup_occurred_at"`
	DonorRepresentativeName string    `json:"donor_representative_name"`
	ActualItemCount         int       `json:"actual_item_count"`
	VerificationHash        string    `json:"verification_hash"`
	Notes                   *string   `json:"notes"`
}

type FailedPickupRequest struct {
	FailureReason   string  `json:"failure_reason"`
	ObservedDetails *string `json:"observed_details"`
}

type AssignmentView struct {
	AssignmentID       string    `json:"assignment_id"`
	BatchID            string    `json:"batch_id"`
	ClaimID            string    `json:"claim_id"`
	CollectorUserID    string    `json:"collector_user_id"`
	CollectorScopeID   string    `json:"collector_scope_id"`
	AssignmentStatus   string    `json:"assignment_status"`
	AssignmentSequence int64     `json:"assignment_sequence"`
	Version            int64     `json:"version"`
	BatchVersion       int64     `json:"batch_version,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type AssignmentMutationResult struct {
	Data          AssignmentView `json:"data"`
	CorrelationID string         `json:"correlation_id"`
	EventID       string         `json:"event_id,omitempty"`
	EventState    string         `json:"event_state,omitempty"`
}

// AssignmentCurrentState is returned with STALE_VERSION so an offline client
// can show the server state and stop retrying its obsolete draft.
type AssignmentCurrentState struct {
	AssignmentID        string  `json:"assignment_id"`
	AssignmentStatus    string  `json:"assignment_status"`
	AssignmentVersion   int64   `json:"assignment_version"`
	BatchID             string  `json:"batch_id"`
	BatchStatus         string  `json:"batch_status"`
	BatchVersion        int64   `json:"batch_version"`
	CurrentAssignmentID *string `json:"current_assignment_id,omitempty"`
}
