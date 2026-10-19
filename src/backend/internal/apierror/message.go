package apierror

var Messages = map[Code]string{
	InvalidRequest:       "invalid request",
	InvalidCredentials:   "invalid credentials",
	InvalidSession:       "invalid or expired session",
	Forbidden:            "access denied",
	NotFound:             "resource not found",
	StaleVersion:         "The resource has changed. Refresh and retry.",
	ValidationError:      "one or more fields are invalid",
	RateLimited:          "too many requests",
	ServiceUnavailable:   "service temporarily unavailable",
	Conflict:             "the requested batch operation conflicts with the current state",
	IdempotencyConflict:  "the idempotency key was reused with different request data",
	RegistrationConflict: "the account, organisation or registration decision already exists",
}
