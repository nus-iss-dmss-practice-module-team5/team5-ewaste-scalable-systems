package middleware

import (
	"github.com/gin-gonic/gin"

	"workflow-api/internal/apierror"
	"workflow-api/internal/ratelimit"
	"workflow-api/internal/response"
)

func RateLimit(limiter ratelimit.Limiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		allowed, err := limiter.Allow(c.Request.Context(), c.ClientIP())
		if err != nil {
			response.Error(c, apierror.ServiceUnavailable, GetCorrelationID(c))
			return
		}

		if !allowed {
			response.Error(c, apierror.RateLimited, GetCorrelationID(c))
			return
		}

		c.Next()
	}
}
