package service

import (
	"errors"
	"fmt"
)

var (
	ErrBatchNotFound            = errors.New("batch: not found")
	ErrBatchForbidden           = errors.New("batch: forbidden")
	ErrBatchInvalidState        = errors.New("batch: invalid state")
	ErrBatchStaleVersion        = errors.New("batch: stale version")
	ErrBatchValidation          = errors.New("batch: validation failed")
	ErrBatchIdempotencyConflict = errors.New("batch: idempotency conflict")
	ErrBatchInProgress          = errors.New("batch: command already in progress")
	ErrBatchEvidenceNotFound    = errors.New("batch: evidence not found")
)

type BatchValidationError struct {
	Fields map[string]string
}

func (e *BatchValidationError) Error() string {
	return "batch: validation failed"
}

func (e *BatchValidationError) Unwrap() error {
	return ErrBatchValidation
}

func NewBatchValidationError(fields map[string]string) error {
	if len(fields) == 0 {
		return ErrBatchValidation
	}

	return &BatchValidationError{
		Fields: fields,
	}
}

type BatchActor struct {
	UserID         string
	OrganisationID string
	RoleCode       string
}

type BatchCommandMetadata struct {
	Actor           BatchActor
	CorrelationID   string
	ActorScope      string
	CommandName     string
	IdempotencyKey  string
	RequestHash     string
	ExpectedVersion int64
}

func (m BatchCommandMetadata) Validate() error {
	if m.Actor.UserID == "" || m.Actor.OrganisationID == "" {
		return fmt.Errorf("%w: actor scope is missing", ErrBatchForbidden)
	}

	if m.CorrelationID == "" {
		return fmt.Errorf("%w: correlation ID is missing", ErrBatchValidation)
	}

	if m.ActorScope == "" || m.CommandName == "" || m.IdempotencyKey == "" || m.RequestHash == "" {
		return fmt.Errorf("%w: command replay metadata is incomplete", ErrBatchValidation)
	}

	return nil
}
