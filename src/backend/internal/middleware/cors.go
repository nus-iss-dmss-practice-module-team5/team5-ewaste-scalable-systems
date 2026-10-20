package middleware

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
)

func CORS(allowedOrigins []string) gin.HandlerFunc {
	allowed := make(map[string]struct{}, len(allowedOrigins))

	for _, origin := range allowedOrigins {
		normalized, ok := normalizeOrigin(origin)
		if ok {
			allowed[normalized] = struct{}{}
		}
	}

	return func(c *gin.Context) {
		origin, ok := normalizeOrigin(c.GetHeader("Origin"))
		if !ok {
			c.Next()
			return
		}

		if _, ok := allowed[origin]; !ok {
			c.Next()
			return
		}

		c.Header("Access-Control-Allow-Origin", origin)
		c.Header("Access-Control-Allow-Methods", "GET, POST, PATCH, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Correlation-ID, Idempotency-Key, If-Match-Version")
		c.Header("Access-Control-Max-Age", "600")
		c.Header("Vary", "Origin")

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}

func normalizeOrigin(raw string) (string, bool) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return "", false
	}

	parsed, err := url.Parse(raw)
	if err != nil ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" ||
		parsed.Path != "" ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" {
		return "", false
	}

	return raw, true
}
