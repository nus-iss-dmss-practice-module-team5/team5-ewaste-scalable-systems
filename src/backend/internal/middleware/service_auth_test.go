package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRequireAnalyticsServiceAcceptsConfiguredBearerToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(CorrelationID(), RequireAnalyticsService("analytics-secret"))
	r.GET("/analytics", func(c *gin.Context) {
		principal, ok := ServicePrincipalFromContext(c)
		if !ok || principal != "analytics-worker" {
			t.Fatalf("unexpected service principal: %q %v", principal, ok)
		}
		c.Status(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodGet, "/analytics", nil)
	request.Header.Set("Authorization", "Bearer analytics-secret")
	recorder := httptest.NewRecorder()
	r.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", recorder.Code)
	}
}

func TestRequireAnalyticsServiceFailsClosedWhenTokenIsMissingOrWrong(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for name, token := range map[string]string{"missing": "", "wrong": "wrong-secret"} {
		t.Run(name, func(t *testing.T) {
			r := gin.New()
			r.Use(CorrelationID(), RequireAnalyticsService("analytics-secret"))
			r.GET("/analytics", func(c *gin.Context) { c.Status(http.StatusNoContent) })
			request := httptest.NewRequest(http.MethodGet, "/analytics", nil)
			if token != "" {
				request.Header.Set("Authorization", "Bearer "+token)
			}
			recorder := httptest.NewRecorder()
			r.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d", recorder.Code)
			}
		})
	}
}

func TestRequireAnalyticsServiceReturnsUnavailableWhenNotConfigured(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(CorrelationID(), RequireAnalyticsService(""))
	r.GET("/analytics", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	recorder := httptest.NewRecorder()
	r.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/analytics", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", recorder.Code)
	}
}
