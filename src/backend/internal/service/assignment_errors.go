package service

import (
	"errors"

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
