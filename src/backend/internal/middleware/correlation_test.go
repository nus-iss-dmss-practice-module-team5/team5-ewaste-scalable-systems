package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"workflow-api/internal/dto"
)

func TestCorrelationIDGeneratesAndEchoesID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(CorrelationID())
	r.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"correlation_id": GetCorrelationID(c)})
	})

	res := httptest.NewRecorder()
	r.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/test", nil))

	if res.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.Code)
	}
	if res.Header().Get("X-Correlation-ID") == "" {
		t.Fatal("expected generated correlation ID")
	}
}

func TestCorrelationIDRejectsValuesLongerThan128Characters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(CorrelationID())
	r.GET("/test", func(c *gin.Context) { c.Status(http.StatusOK) })

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("X-Correlation-ID", string(make([]byte, 129)))
	r.ServeHTTP(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", res.Code)
	}
	var response dto.ErrorResponse
	if err := json.Unmarshal(res.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Code != "INVALID_REQUEST" {
		t.Fatalf("unexpected error code: %q", response.Code)
	}
}
