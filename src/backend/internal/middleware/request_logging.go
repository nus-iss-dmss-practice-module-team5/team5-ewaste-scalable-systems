package middleware

import (
	"fmt"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func RequestLogger(log *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		startedAt := time.Now()

		c.Next()

		if log == nil {
			return
		}

		duration := time.Since(startedAt)
		outcome := "SUCCEEDED"
		code := c.GetString("telemetry_error_code")
		if c.Writer.Status() >= 400 {
			outcome = "REJECTED"
			if c.Writer.Status() >= 500 {
				outcome = "FAILED"
			}
			if code == "" {
				code = fmt.Sprintf("HTTP_%d", c.Writer.Status())
			}
		}
		observation := "http_request"
		if c.FullPath() == "/readyz" {
			observation = "readiness"
		}
		log.Info(
			"http request",
			zap.String("component", "workflow-api"),
			zap.Int("telemetry_version", 1),
			zap.String("service", "api"),
			zap.String("observation", observation),
			zap.String("operation", c.FullPath()),
			zap.String("batch_id", c.Param("batch_id")),
			zap.String("outcome", outcome),
			zap.String("code", code),
			zap.Float64("duration_ms", float64(duration.Microseconds())/1000),
			zap.String("method", c.Request.Method),
			zap.String("path", c.Request.URL.Path),
			zap.Int("status", c.Writer.Status()),
			zap.Duration("duration", duration),
			zap.Int64("latency_ms", duration.Milliseconds()),
			zap.String("correlation_id", GetCorrelationID(c)),
		)
	}
}
