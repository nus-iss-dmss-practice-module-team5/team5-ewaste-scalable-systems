package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

type limiterStub struct {
	allowed bool
	err     error
}

func (s limiterStub) Allow(context.Context, string) (bool, error) {
	return s.allowed, s.err
}

func TestRateLimitReturns429WhenLimitExceeded(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(CorrelationID())
	r.GET("/test", RateLimit(limiterStub{allowed: false}), func(c *gin.Context) { c.Status(http.StatusOK) })

	res := httptest.NewRecorder()
	r.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/test", nil))

	if res.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", res.Code)
	}
}

func TestRateLimitReturns503WhenRedisIsUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(CorrelationID())
	r.GET("/test", RateLimit(limiterStub{err: errors.New("redis unavailable")}), func(c *gin.Context) { c.Status(http.StatusOK) })

	res := httptest.NewRecorder()
	r.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/test", nil))

	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", res.Code)
	}
	if strings.Contains(res.Body.String(), "redis unavailable") {
		t.Fatalf("response exposed dependency error: %s", res.Body.String())
	}
}
