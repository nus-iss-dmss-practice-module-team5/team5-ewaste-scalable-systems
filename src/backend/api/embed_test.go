package api

import (
	"bytes"
	"testing"
)

func TestOpenAPISpecIsEmbedded(t *testing.T) {
	if len(OpenAPISpec) == 0 {
		t.Fatal("expected embedded OpenAPI specification")
	}
	if !bytes.HasPrefix(OpenAPISpec, []byte("openapi: 3.0.3")) {
		t.Fatalf("expected OpenAPI 3.0.3 specification, got %q", OpenAPISpec[:min(len(OpenAPISpec), 30)])
	}
}
