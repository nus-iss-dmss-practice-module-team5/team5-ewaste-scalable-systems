package api

import _ "embed"

// OpenAPISpec is the versioned API contract served by the development/test docs endpoint.
//
//go:embed openapi.yaml
var OpenAPISpec []byte
