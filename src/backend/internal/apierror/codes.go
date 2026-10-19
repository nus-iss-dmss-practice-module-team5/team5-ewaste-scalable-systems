package apierror

type Code string

const (
	InvalidRequest       Code = "INVALID_REQUEST"
	InvalidCredentials   Code = "AUTH_INVALID_CREDENTIALS"
	InvalidSession       Code = "AUTH_INVALID_SESSION"
	Forbidden            Code = "FORBIDDEN"
	NotFound             Code = "NOT_FOUND"
	StaleVersion         Code = "STALE_VERSION"
	ValidationError      Code = "VALIDATION_ERROR"
	RateLimited          Code = "AUTH_RATE_LIMITED"
	ServiceUnavailable   Code = "SERVICE_UNAVAILABLE"
	Conflict             Code = "CONFLICT"
	IdempotencyConflict  Code = "IDEMPOTENCY_CONFLICT"
	RegistrationConflict Code = "REGISTRATION_CONFLICT"
)
