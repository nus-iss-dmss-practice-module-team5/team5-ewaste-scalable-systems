package apierror

import "net/http"

func Status(code Code) int {
	switch code {
	case InvalidRequest:
		return http.StatusBadRequest
	case InvalidCredentials, InvalidSession:
		return http.StatusUnauthorized
	case Forbidden:
		return http.StatusForbidden
	case NotFound:
		return http.StatusNotFound
	case StaleVersion:
		return http.StatusConflict
	case ValidationError:
		return http.StatusUnprocessableEntity
	case RateLimited:
		return http.StatusTooManyRequests
	case Conflict, IdempotencyConflict, RegistrationConflict:
		return http.StatusConflict
	default:
		return http.StatusServiceUnavailable
	}
}
