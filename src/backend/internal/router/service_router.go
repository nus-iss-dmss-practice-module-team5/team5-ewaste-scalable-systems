package router

import (
	"fmt"
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

// Service identifies one independently deployable workflow API bounded context.
type Service string

const (
	IdentityAccessService      Service = "identity-access"
	BatchWorkflowService       Service = "batch-workflow"
	ClaimLogisticsService      Service = "claim-logistics"
	CustodyVerificationService Service = "custody-verification"
)

// ServiceDependencies are the platform dependencies made available to a single
// service router. A service uses only the fields needed for its own routes.
// AuthRepository is intentionally read-only from the workflow route handlers:
// it preserves local JWT validation plus the existing session/user-status check
// without adding an Identity service call on the request path.
type ServiceDependencies struct {
	AuthController         *controller.AuthController
	BatchController        *controller.BatchController
	EvidenceController     *controller.EvidenceController
	ClaimController        *controller.ClaimController
	AssignmentController   *controller.AssignmentController
	AnalyticsController    *controller.AnalyticsController
	WorkflowReadController *controller.WorkflowReadController
	Tokens                 *token.Service
	AuthRepository         repository.AuthRepository
	Limiter                ratelimit.Limiter
	Checker                *health.Checker
	AllowedOrigins         []string
	Logger                 *zap.Logger
	AnalyticsServiceToken  string
}

// NewServiceRouter creates the route surface for exactly one bounded context.
// The Next.js gateway maps existing /api/v1 paths to these canonical local
// prefixes. No domain route is shared by two service routers.
func NewServiceRouter(service Service, deps ServiceDependencies) (*gin.Engine, error) {
	r := newServiceEngine(deps)

	switch service {
	case IdentityAccessService:
		registerIdentityRoutes(r, deps)
	case BatchWorkflowService:
		registerBatchWorkflowRoutes(r, deps)
	case ClaimLogisticsService:
		registerClaimLogisticsRoutes(r, deps)
	case CustodyVerificationService:
		registerCustodyVerificationRoutes(r, deps)
	default:
		return nil, fmt.Errorf("unknown workflow service %q", service)
	}

	r.NoRoute(func(c *gin.Context) {
		response.Error(c, apierror.NotFound, middleware.GetCorrelationID(c))
	})
	return r, nil
}

func newServiceEngine(deps ServiceDependencies) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	logger := deps.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	r.Use(
		gin.Recovery(),
		middleware.CorrelationID(),
		middleware.RequestLogger(logger),
		middleware.CORS(deps.AllowedOrigins),
	)

	r.GET("/healthz", func(c *gin.Context) {
		response.JSON(c, http.StatusOK, gin.H{"status": "ok"})
	})
	r.GET("/readyz", func(c *gin.Context) {
		if deps.Checker == nil {
			response.JSON(c, http.StatusServiceUnavailable, health.Report{Status: "not_ready", MySQL: "unavailable", Redis: "unavailable"})
			return
		}
		report, err := deps.Checker.CheckDetailed(c.Request.Context())
		if err != nil {
			response.JSON(c, http.StatusServiceUnavailable, report)
			return
		}
		response.JSON(c, http.StatusOK, report)
	})
	return r
}

func userRoutes(r *gin.Engine, deps ServiceDependencies, prefix string) *gin.RouterGroup {
	group := r.Group(prefix)
	group.Use(middleware.RequireAccessTokens(deps.Tokens, deps.AuthRepository))
	return group
}

func registerIdentityRoutes(r *gin.Engine, deps ServiceDependencies) {
	if deps.AuthController == nil {
		return
	}
	auth := r.Group("/v1/auth")
	auth.POST("/login", middleware.RateLimit(deps.Limiter), deps.AuthController.Login)
	auth.POST("/refresh", middleware.RateLimit(deps.Limiter), deps.AuthController.Refresh)
	auth.POST("/logout", middleware.RequireAccessTokens(deps.Tokens, deps.AuthRepository), deps.AuthController.Logout)
}

func registerBatchWorkflowRoutes(r *gin.Engine, deps ServiceDependencies) {
	batches := userRoutes(r, deps, "/v1/batches")
	if deps.WorkflowReadController != nil {
		batches.GET("", deps.WorkflowReadController.ListBatches)
		batches.GET("/:batch_id", deps.WorkflowReadController.GetBatch)
	}
	if deps.BatchController != nil {
		batches.POST("", deps.BatchController.CreateDraft)
		batches.PATCH("/:batch_id", deps.BatchController.EditDraft)
		batches.POST("/:batch_id/submit", deps.BatchController.Submit)
	}
	if deps.WorkflowReadController != nil {
		opportunities := userRoutes(r, deps, "/v1/opportunities")
		opportunities.GET("", deps.WorkflowReadController.ListOpportunities)
		opportunities.GET("/:batch_id", deps.WorkflowReadController.GetOpportunity)
	}
	if deps.AnalyticsController != nil {
		analytics := r.Group("/internal/v1/analytics")
		analytics.Use(middleware.RequireAnalyticsService(deps.AnalyticsServiceToken))
		analytics.GET("/batches/:batch_id/input", deps.AnalyticsController.Prepare)
		analytics.POST("/batches/:batch_id/results", deps.AnalyticsController.Acknowledge)
	}
}

func registerClaimLogisticsRoutes(r *gin.Engine, deps ServiceDependencies) {
	if deps.ClaimController != nil {
		claims := userRoutes(r, deps, "/v1/claims")
		claims.POST("/batches/:batch_id", deps.ClaimController.Claim)
	}
	assignments := userRoutes(r, deps, "/v1/logistics/assignments")
	if deps.AssignmentController != nil {
		assignments.POST("/batches/:batch_id", deps.AssignmentController.Select)
		assignments.POST("/:assignment_id/accept", deps.AssignmentController.Accept)
		assignments.POST("/:assignment_id/reject", deps.AssignmentController.Reject)
		assignments.POST("/:assignment_id/fail", deps.AssignmentController.Fail)
	}
	if deps.WorkflowReadController != nil {
		assignments.GET("", deps.WorkflowReadController.ListAssignments)
		assignments.GET("/:assignment_id", deps.WorkflowReadController.GetAssignment)
	}
}

func registerCustodyVerificationRoutes(r *gin.Engine, deps ServiceDependencies) {
	custody := userRoutes(r, deps, "/v1/custody/assignments")
	if deps.AssignmentController != nil {
		custody.POST("/:assignment_id/handoff", deps.AssignmentController.Handoff)
	}

	processing := userRoutes(r, deps, "/v1/processing/batches")
	if deps.WorkflowReadController != nil {
		processing.GET("", deps.WorkflowReadController.ListProcessingBatches)
		processing.GET("/:batch_id", deps.WorkflowReadController.GetProcessingBatch)
	}
	if deps.BatchController != nil {
		processing.POST("/:batch_id/receipt", deps.BatchController.VerifyReceipt)
		processing.POST("/:batch_id/treatment", deps.BatchController.RecordTreatment)
	}

	if deps.EvidenceController != nil {
		evidence := userRoutes(r, deps, "/v1/evidence/batches")
		evidence.POST("/:batch_id", deps.EvidenceController.Upload)
		evidence.GET("/:batch_id/:evidence_id", deps.EvidenceController.Download)
	}
	if deps.WorkflowReadController != nil {
		audit := userRoutes(r, deps, "/v1/audit")
		audit.GET("/batches/:batch_id/timeline", deps.WorkflowReadController.GetAuditTimeline)
		audit.GET("/batches/:batch_id/anomalies", deps.WorkflowReadController.GetAuditAnomalies)
		audit.GET("/impact", deps.WorkflowReadController.GetAuditImpact)
	}
}
