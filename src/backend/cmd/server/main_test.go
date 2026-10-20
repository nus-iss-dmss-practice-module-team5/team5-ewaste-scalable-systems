package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"workflow-api/internal/router"
)

func Test_RunTestServer(t *testing.T) {
	r := router.NewTestRouter()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/hello", nil)
	res := httptest.NewRecorder()

	r.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, res.Code)
	}

	expected := `{"msg":"hello world"}`
	if res.Body.String() != expected {
		t.Fatalf("expected body %s, got %s", expected, res.Body.String())
	}
}
