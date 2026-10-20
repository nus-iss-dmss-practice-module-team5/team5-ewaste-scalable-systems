package service

import (
	"errors"
	"time"

	"workflow-api/internal/dto"
)

var (
	ErrAssignmentForbidden           = errors.New("assignment: forbidden")
	ErrAssignmentNotFound            = errors.New("assignment: not found")
	ErrAssignmentInvalidState        = errors.New("assignment: invalid state")
	ErrAssignmentStaleVersion        = errors.New("assignment: stale version")
	ErrAssignmentValidation          = errors.New("assignment: validation failed")
	ErrAssignmentIdempotencyConflict = errors.New("assignment: idempotency conflict")
	ErrAssignmentInProgress          = errors.New("assignment: command already in progress")
	ErrAssignmentConcurrent          = errors.New("assignment: concurrent update")
)

// AssignmentStaleVersionError preserves the server snapshot captured while
// the aggregate lock is held. It unwraps to ErrAssignmentStaleVersion so the
// existing error/status mapping remains stable.
type AssignmentStaleVersionError struct {
	CurrentState dto.AssignmentCurrentState
}

func (e *AssignmentStaleVersionError) Error() string { return ErrAssignmentStaleVersion.Error() }

func (e *AssignmentStaleVersionError) Unwrap() error { return ErrAssignmentStaleVersion }

func AssignmentCurrentStateFromError(err error) (dto.AssignmentCurrentState, bool) {
	var stale *AssignmentStaleVersionError
	if !errors.As(err, &stale) || stale == nil {
		return dto.AssignmentCurrentState{}, false
	}
	return stale.CurrentState, true
}

// Reasons a handoff's pickup_occurred_at is refused.
const (
	PickupTimeInFuture         = "PICKUP_TIME_IN_FUTURE"
	PickupTimeBeforeAssignment = "PICKUP_TIME_BEFORE_ASSIGNMENT"
)

// AssignmentValidationError carries field-level details for a refused value.
// It unwraps to ErrAssignmentValidation so the status mapping is unchanged.
type AssignmentValidationError struct {
	Details dto.ValidationDetails
}

func (e *AssignmentValidationError) Error() string { return ErrAssignmentValidation.Error() }

func (e *AssignmentValidationError) Unwrap() error { return ErrAssignmentValidation }

// pickupTimeError reports the accepted pickup interval: from the assignment
// time (when known) up to the server time that refused the value.
func pickupTimeError(reason string, earliest *time.Time, latest time.Time) error {
	details := dto.ValidationDetails{Field: "pickup_occurred_at", Reason: reason, LatestAllowed: &latest}
	if earliest != nil {
		utc := earliest.UTC()
		details.EarliestAllowed = &utc
	}
	return &AssignmentValidationError{Details: details}
}

func AssignmentValidationDetailsFromError(err error) (dto.ValidationDetails, bool) {
	var invalid *AssignmentValidationError
	if !errors.As(err, &invalid) || invalid == nil {
		return dto.ValidationDetails{}, false
	}
	return invalid.Details, true
}
