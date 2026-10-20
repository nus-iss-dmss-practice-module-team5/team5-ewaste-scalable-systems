package middleware

import (
	"crypto/subtle"
	"strings"

	"github.com/gin-gonic/gin"

	"workflow-api/internal/apierror"
	"workflow-api/internal/response"
)

const ServicePrincipalKey = "service_principal"

func RequireAnalyticsService(expectedToken string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if strings.TrimSpace(expectedToken) == "" {
			response.Error(c, apierror.ServiceUnavailable, GetCorrelationID(c))
			return
		}
		parts := strings.Fields(c.GetHeader("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || subtle.ConstantTimeCompare([]byte(parts[1]), []byte(expectedToken)) != 1 {
			response.Error(c, apierror.InvalidSession, GetCorrelationID(c))
			return
		}
		c.Set(ServicePrincipalKey, "analytics-worker")
		c.Next()
	}
}

func ServicePrincipalFromContext(c *gin.Context) (string, bool) {
	value, exists := c.Get(ServicePrincipalKey)
	if !exists {
		return "", false
	}
	principal, ok := value.(string)
	return principal, ok && strings.TrimSpace(principal) != ""
}
