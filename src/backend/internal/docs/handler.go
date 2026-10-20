package docs

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/swaggest/swgui/v5emb"

	"workflow-api/api"
)

// Register exposes the OpenAPI document and an embedded Swagger UI.
// This should only be called for development or test environments.
func Register(r *gin.Engine) {
	r.GET("/openapi.yaml", func(c *gin.Context) {
		c.Data(http.StatusOK, "application/yaml; charset=utf-8", api.OpenAPISpec)
	})
	r.GET("/docs", func(c *gin.Context) {
		c.Redirect(http.StatusPermanentRedirect, "/docs/")
	})

	ui := v5emb.New("E-Waste Workflow Authentication API", "/openapi.yaml", "/docs/")
	r.Any("/docs/*path", gin.WrapH(ui))
}
