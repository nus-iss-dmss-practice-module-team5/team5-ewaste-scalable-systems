package middleware

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestProcessingRequestTelemetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, status := range []int{200, 409, 422, 503} {
		core, logs := observer.New(zap.InfoLevel)
		router := gin.New()
		router.Use(CorrelationID(), RequestLogger(zap.New(core)))
		router.POST("/api/v1/batches/:batch_id/receipt", func(c *gin.Context) { c.Status(status) })
		req := httptest.NewRequest("POST", "/api/v1/batches/b-1/receipt", strings.NewReader(`{"private":"not-logged"}`))
		req.Header.Set("Authorization", "Bearer not-logged")
		req.Header.Set("X-Correlation-ID", "trace-1")
		router.ServeHTTP(httptest.NewRecorder(), req)
		if logs.Len() != 1 {
			t.Fatal("expected one observation")
		}
		entry := logs.All()[0].ContextMap()
		if entry["batch_id"] != "b-1" || entry["correlation_id"] != "trace-1" || entry["operation"] != "/api/v1/batches/:batch_id/receipt" {
			t.Fatal(entry)
		}
		if entry["component"] != "workflow-api" {
			t.Fatal("component missing", entry)
		}
		if _, ok := entry["latency_ms"]; !ok {
			t.Fatal("legacy latency missing")
		}
		if _, ok := entry["duration_ms"]; !ok {
			t.Fatal("duration missing")
		}
		for _, field := range []string{"authorization", "body", "password"} {
			if _, ok := entry[field]; ok {
				t.Fatal("sensitive field logged")
			}
		}
	}
}

func TestReadinessFailureTelemetry(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	r := gin.New()
	r.Use(RequestLogger(zap.New(core)))
	r.GET("/readyz", func(c *gin.Context) { c.Status(503) })
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/readyz", nil))
	fields := logs.All()[0].ContextMap()
	if fields["observation"] != "readiness" || fields["outcome"] != "FAILED" || fields["code"] != "HTTP_503" {
		t.Fatal(fields)
	}
}
