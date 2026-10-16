package service

import "errors"

var (
	ErrClaimForbidden           = errors.New("claim: forbidden")
	ErrClaimValidation          = errors.New("claim: validation failed")
	ErrClaimStaleVersion        = errors.New("claim: stale version or epoch")
	ErrClaimInvalidState        = errors.New("claim: invalid lifecycle state")
	ErrClaimOpportunityNotFound = errors.New("claim: opportunity not found")
	ErrClaimIdempotencyConflict = errors.New("claim: idempotency conflict")
	ErrClaimInProgress          = errors.New("claim: command already in progress")
	ErrClaimConcurrent          = errors.New("claim: concurrent claim")
	ErrClaimCapacity            = errors.New("claim: insufficient capacity")
	ErrClaimNoActiveCollector   = errors.New("claim: no active collector")
	ErrClaimLeaseUnavailable    = errors.New("claim: redis lease unavailable")
)
