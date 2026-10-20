package docs

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRegisterServesOpenAPISpec(t *testing.T) {
	r := gin.New()
	Register(r)

	recorder := httptest.NewRecorder()
	r.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/openapi.yaml", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "openapi: 3.0.3") {
		t.Fatal("expected embedded OpenAPI specification in response")
	}
}

func TestRegisterServesEmbeddedSwaggerUI(t *testing.T) {
	r := gin.New()
	Register(r)

	recorder := httptest.NewRecorder()
	r.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/docs/", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}
	if !strings.Contains(strings.ToLower(recorder.Body.String()), "swagger") {
		t.Fatal("expected Swagger UI HTML response")
	}
}
