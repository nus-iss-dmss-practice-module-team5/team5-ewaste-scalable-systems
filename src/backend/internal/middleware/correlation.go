package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"workflow-api/internal/apierror"
	"workflow-api/internal/response"
)

const (
	CorrelationIDKey     = "correlation_id"
	MaxCorrelationIDSize = 128
)

func CorrelationID() gin.HandlerFunc {
	return func(c *gin.Context) {
		correlationID := strings.TrimSpace(c.GetHeader("X-Correlation-ID"))

		if len([]rune(correlationID)) > MaxCorrelationIDSize {
			correlationID = uuid.NewString()
			c.Set(CorrelationIDKey, correlationID)
			c.Header("X-Correlation-ID", correlationID)
			response.Error(c, apierror.InvalidRequest, correlationID)
			return
		}

		if correlationID == "" {
			correlationID = uuid.NewString()
		}

		c.Set(CorrelationIDKey, correlationID)
		c.Header("X-Correlation-ID", correlationID)
		c.Next()
	}
}

func GetCorrelationID(c *gin.Context) string {
	value, exists := c.Get(CorrelationIDKey)
	if !exists {
		return ""
	}

	correlationID, ok := value.(string)
	if !ok {
		return ""
	}

	return correlationID
}
