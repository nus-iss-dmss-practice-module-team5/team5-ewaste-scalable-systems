package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

const testAllowedOrigin = "https://aca-ewaste-dev-ui.kindflower-300f4866.malaysiawest.azurecontainerapps.io"

func TestCORSAllowsConfiguredUIOrigin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(CORS([]string{testAllowedOrigin}))
	r.GET("/api/v1/hello", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/api/v1/hello", nil)
	req.Header.Set("Origin", testAllowedOrigin)
	res := httptest.NewRecorder()
	r.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.Code)
	}
	if got := res.Header().Get("Access-Control-Allow-Origin"); got != testAllowedOrigin {
		t.Fatalf("expected allowed origin %q, got %q", testAllowedOrigin, got)
	}
	if got := res.Header().Get("Vary"); got != "Origin" {
		t.Fatalf("expected Vary: Origin, got %q", got)
	}
}

func TestCORSHandlesPreflightForConfiguredUIOrigin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(CORS([]string{testAllowedOrigin}))
	r.POST("/api/v1/auth/login", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodOptions, "/api/v1/auth/login", nil)
	req.Header.Set("Origin", testAllowedOrigin)
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	req.Header.Set("Access-Control-Request-Headers", "authorization, content-type")
	res := httptest.NewRecorder()
	r.ServeHTTP(res, req)

	if res.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", res.Code)
	}
	if got := res.Header().Get("Access-Control-Allow-Methods"); got != "GET, POST, PATCH, OPTIONS" {
		t.Fatalf("expected allowed methods %q, got %q", "GET, POST, PATCH, OPTIONS", got)
	}
	if got := res.Header().Get("Access-Control-Allow-Headers"); got != "Authorization, Content-Type, X-Correlation-ID, Idempotency-Key, If-Match-Version" {
		t.Fatalf("expected allowed headers %q, got %q", "Authorization, Content-Type, X-Correlation-ID, Idempotency-Key, If-Match-Version", got)
	}
}

func TestCORSDoesNotAllowOtherOrigins(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(CORS([]string{testAllowedOrigin}))
	r.GET("/api/v1/hello", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/api/v1/hello", nil)
	req.Header.Set("Origin", "https://example.com")
	res := httptest.NewRecorder()
	r.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.Code)
	}
	if got := res.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("expected no allow-origin header, got %q", got)
	}
}
