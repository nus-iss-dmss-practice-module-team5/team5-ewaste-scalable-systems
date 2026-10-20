package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"workflow-api/internal/token"
)

func TestRequireAccessTokensRejectsMissingAuthorization(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(CorrelationID())
	r.GET("/protected", RequireAccessTokens(token.NewService("test", "access", "refresh", "hash", time.Minute, time.Hour), nil), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	res := httptest.NewRecorder()
	r.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/protected", nil))

	if res.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", res.Code)
	}
}
