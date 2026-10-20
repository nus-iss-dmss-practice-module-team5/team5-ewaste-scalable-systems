package controller

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"workflow-api/internal/apierror"
	"workflow-api/internal/dto"
	"workflow-api/internal/middleware"
	"workflow-api/internal/repository"
	"workflow-api/internal/response"
	"workflow-api/internal/service"
)

type AnalyticsController struct {
	service analyticsWorkflow
	logger  *zap.Logger
}

type analyticsWorkflow interface {
	PrepareAnalytics(context.Context, string, string, string) (dto.AnalyticsPreparation, error)
	AcknowledgeAnalytics(context.Context, string, dto.AnalyticsAcknowledgement, service.BatchCommandMetadata) (dto.CompletionMutationResult, error)
}

func NewAnalyticsController(analyticsService analyticsWorkflow, logger *zap.Logger) *AnalyticsController {
	return &AnalyticsController{service: analyticsService, logger: logger}
}

// Prepare returns a stable snapshot for a specific committed treatment event.
func (h *AnalyticsController) Prepare(c *gin.Context) {
	principal, ok := middleware.ServicePrincipalFromContext(c)
	if !ok {
		response.Error(c, apierror.InvalidSession, middleware.GetCorrelationID(c))
		return
	}
	c.Header("X-Analytics-Contract", "processing-v1")
	c.Header("Cache-Control", "no-store")
	sourceEventID := strings.TrimSpace(c.Query("source_event_id"))
	if sourceEventID == "" {
		response.Error(c, apierror.InvalidRequest, middleware.GetCorrelationID(c))
		return
	}
	result, err := h.service.PrepareAnalytics(c.Request.Context(), c.Param("batch_id"), sourceEventID, principal)
	if err != nil {
		response.Error(c, mapAnalyticsError(err), middleware.GetCorrelationID(c))
		return
	}
	response.JSON(c, http.StatusOK, result)
}

func (h *AnalyticsController) Acknowledge(c *gin.Context) {
	principal, ok := middleware.ServicePrincipalFromContext(c)
	if !ok {
		response.Error(c, apierror.InvalidSession, middleware.GetCorrelationID(c))
		return
	}
	var params struct {
		BatchID string `uri:"batch_id" binding:"required"`
	}
	if err := c.ShouldBindUri(&params); err != nil {
		response.Error(c, apierror.InvalidRequest, middleware.GetCorrelationID(c))
		return
	}
	var request dto.AnalyticsAcknowledgement
	if !decodeBatchJSON(c, &request) {
		return
	}
	idempotencyKey := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if idempotencyKey == "" {
		response.Error(c, apierror.InvalidRequest, middleware.GetCorrelationID(c))
		return
	}
	version, err := strconv.ParseInt(strings.TrimSpace(c.GetHeader("If-Match-Version")), 10, 64)
	if err != nil || version < 1 {
		response.Error(c, apierror.InvalidRequest, middleware.GetCorrelationID(c))
		return
	}
	metadata := service.BatchCommandMetadata{
		ActorScope: "service:" + principal, CommandName: service.AcknowledgeAnalyticsCommand,
		CorrelationID: middleware.GetCorrelationID(c), IdempotencyKey: idempotencyKey, ExpectedVersion: version,
	}
	result, err := h.service.AcknowledgeAnalytics(c.Request.Context(), params.BatchID, request, metadata)
	if err != nil {
		if h.logger != nil {
			h.logger.Warn("analytics acknowledgement failed", zap.Error(err), zap.String("correlation_id", middleware.GetCorrelationID(c)))
		}
		response.Error(c, mapAnalyticsError(err), middleware.GetCorrelationID(c))
		return
	}
	response.JSON(c, http.StatusOK, result)
}

func mapAnalyticsError(err error) apierror.Code {
	switch {
	case errors.Is(err, service.ErrBatchForbidden):
		return apierror.Forbidden
	case errors.Is(err, service.ErrBatchNotFound), errors.Is(err, repository.ErrBatchNotFound):
		return apierror.NotFound
	case errors.Is(err, service.ErrBatchValidation):
		return apierror.ValidationError
	case errors.Is(err, service.ErrBatchStaleVersion):
		return apierror.StaleVersion
	case errors.Is(err, service.ErrBatchIdempotencyConflict):
		return apierror.IdempotencyConflict
	case errors.Is(err, service.ErrBatchInvalidState), errors.Is(err, service.ErrBatchInProgress):
		return apierror.Conflict
	default:
		return apierror.ServiceUnavailable
	}
}
