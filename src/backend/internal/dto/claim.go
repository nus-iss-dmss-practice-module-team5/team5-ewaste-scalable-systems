package dto

type ClaimRequest struct {
	ExpectedVersion int64   `json:"expected_version"`
	ClaimEpoch      string  `json:"claim_epoch"`
	Notes           *string `json:"notes"`
}

type ClaimResult struct {
	BatchID           string `json:"batch_id"`
	Status            string `json:"status"`
	Version           int64  `json:"version"`
	ClaimEpoch        string `json:"claim_epoch"`
	ClaimID           string `json:"claim_id"`
	ReservationID     string `json:"reservation_id"`
	AssignmentID      string `json:"assignment_id"`
	AssignmentEventID string `json:"assignment_event_id"`
	EventID           string `json:"event_id"`
	EventState        string `json:"event_state"`
	CorrelationID     string `json:"correlation_id"`
}
