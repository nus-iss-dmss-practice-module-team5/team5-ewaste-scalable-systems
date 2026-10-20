package router

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"workflow-api/internal/apierror"
	"workflow-api/internal/controller"
	"workflow-api/internal/health"
	"workflow-api/internal/middleware"
	"workflow-api/internal/ratelimit"
	"workflow-api/internal/repository"
	"workflow-api/internal/response"
	"workflow-api/internal/token"
)

func NewAuthRouter(
	authController *controller.AuthController,
	batchController *controller.BatchController,
	evidenceController *controller.EvidenceController,
	claimController *controller.ClaimController,
	assignmentController *controller.AssignmentController,
	analyticsController *controller.AnalyticsController,
	tokens *token.Service,
	repo repository.AuthRepository,
	limiter ratelimit.Limiter,
	checker *health.Checker,
	allowedOrigins []string,
	logger *zap.Logger,
	analyticsServiceToken string,
	readControllers ...*controller.WorkflowReadController,
) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)

	r := gin.New()
	r.Use(
		gin.Recovery(),
		middleware.CorrelationID(),
		middleware.RequestLogger(logger),
		middleware.CORS(allowedOrigins),
	)

	r.GET("/healthz", func(c *gin.Context) {
		response.JSON(c, http.StatusOK, gin.H{"status": "ok"})
	})

	r.GET("/readyz", func(c *gin.Context) {
		if checker == nil {
			response.JSON(c, http.StatusServiceUnavailable, health.Report{
				Status: "not_ready",
				MySQL:  "unavailable",
				Redis:  "unavailable",
			})
			return
		}

		report, err := checker.CheckDetailed(c.Request.Context())
		if err != nil {
			response.JSON(c, http.StatusServiceUnavailable, report)
			return
		}

		response.JSON(c, http.StatusOK, report)
	})

	r.GET("/api/v1/hello", func(c *gin.Context) {
		response.JSON(c, http.StatusOK, gin.H{"msg": "hello world"})
	})

	auth := r.Group("/api/v1/auth")
	auth.POST("/login", middleware.RateLimit(limiter), authController.Login)
	auth.POST("/refresh", middleware.RateLimit(limiter), authController.Refresh)
	auth.POST("/logout", middleware.RequireAccessTokens(tokens, repo), authController.Logout)

	batches := r.Group("/api/v1/batches")
	batches.Use(middleware.RequireAccessTokens(tokens, repo))

	if len(readControllers) > 0 && readControllers[0] != nil {
		batches.GET("", readControllers[0].ListBatches)
		batches.GET("/:batch_id", readControllers[0].GetBatch)

		processingBatches := r.Group("/api/v1/processing/batches")
		processingBatches.Use(middleware.RequireAccessTokens(tokens, repo))
		processingBatches.GET("", readControllers[0].ListProcessingBatches)
		processingBatches.GET("/:batch_id", readControllers[0].GetProcessingBatch)
	}
	if batchController != nil {
		batches.POST("", batchController.CreateDraft)
		batches.PATCH("/:batch_id", batchController.EditDraft)
		batches.POST("/:batch_id/submit", batchController.Submit)
		batches.POST("/:batch_id/receipt", batchController.VerifyReceipt)
		batches.POST("/:batch_id/treatment", batchController.RecordTreatment)
	}
	if evidenceController != nil {
		batches.POST("/:batch_id/evidence", evidenceController.Upload)
		batches.GET("/:batch_id/evidence/:evidence_id", evidenceController.Download)
	}
	if claimController != nil {
		batches.POST("/:batch_id/claim", claimController.Claim)
	}
	if assignmentController != nil {
		batches.POST("/:batch_id/assignments", assignmentController.Select)
	}
	analyticsBatches := r.Group("/api/v1/batches")
	analyticsBatches.Use(middleware.RequireAnalyticsService(analyticsServiceToken))
	if analyticsController != nil {
		analyticsBatches.GET("/:batch_id/analytics-input", analyticsController.Prepare)
		analyticsBatches.POST("/:batch_id/analytics-results", analyticsController.Acknowledge)
	}

	assignments := r.Group("/api/v1/assignments")
	assignments.Use(middleware.RequireAccessTokens(tokens, repo))
	if len(readControllers) > 0 && readControllers[0] != nil {
		assignments.GET("", readControllers[0].ListAssignments)
		assignments.GET("/:assignment_id", readControllers[0].GetAssignment)
	}
	if assignmentController != nil {
		assignments.POST("/:assignment_id/accept", assignmentController.Accept)
		assignments.POST("/:assignment_id/reject", assignmentController.Reject)
		assignments.POST("/:assignment_id/handoff", assignmentController.Handoff)
		assignments.POST("/:assignment_id/fail", assignmentController.Fail)
	}

	if len(readControllers) > 0 && readControllers[0] != nil {
		opportunities := r.Group("/api/v1/opportunities")
		opportunities.Use(middleware.RequireAccessTokens(tokens, repo))
		opportunities.GET("", readControllers[0].ListOpportunities)
		opportunities.GET("/:batch_id", readControllers[0].GetOpportunity)

		audit := r.Group("/api/v1/audit")
		audit.Use(middleware.RequireAccessTokens(tokens, repo))
		audit.GET("/batches/:batch_id/timeline", readControllers[0].GetAuditTimeline)
		audit.GET("/batches/:batch_id/anomalies", readControllers[0].GetAuditAnomalies)
		audit.GET("/impact", readControllers[0].GetAuditImpact)
	}

	r.NoRoute(func(c *gin.Context) {
		response.Error(c, apierror.NotFound, middleware.GetCorrelationID(c))
	})

	return r
}

func NewTestRouter(allowedOrigins ...string) *gin.Engine {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(
		middleware.CorrelationID(),
		middleware.CORS(allowedOrigins),
	)

	r.GET("/api/v1/hello", func(c *gin.Context) {
		response.JSON(c, http.StatusOK, gin.H{"msg": "hello world"})
	})

	r.NoRoute(func(c *gin.Context) {
		response.Error(c, apierror.NotFound, middleware.GetCorrelationID(c))
	})

	return r
}
