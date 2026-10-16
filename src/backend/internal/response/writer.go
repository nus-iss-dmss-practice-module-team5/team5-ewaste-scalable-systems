package response

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"workflow-api/internal/apierror"
	"workflow-api/internal/dto"
)

func JSON(c *gin.Context, status int, body any) {
	c.JSON(status, body)
}

func Error(c *gin.Context, code apierror.Code, correlationID string) {
	c.Set("telemetry_error_code", string(code))
	c.AbortWithStatusJSON(apierror.Status(code), dto.ErrorResponse{
		Code:          string(code),
		Message:       apierror.Messages[code],
		CorrelationID: correlationID,
	})
}

// ErrorWithDetails adds a safe, typed detail payload to an error envelope.
// It is used for optimistic-concurrency conflicts where a caller needs the
// authoritative state to resolve an offline draft.
func ErrorWithDetails(c *gin.Context, code apierror.Code, correlationID string, details any) {
	c.Set("telemetry_error_code", string(code))
	c.AbortWithStatusJSON(apierror.Status(code), gin.H{
		"code":           string(code),
		"message":        apierror.Messages[code],
		"correlation_id": correlationID,
		"current_state":  details,
	})
}

func NoContent(c *gin.Context) {
	c.Status(http.StatusNoContent)
}
