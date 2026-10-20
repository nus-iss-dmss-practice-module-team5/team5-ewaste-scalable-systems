package controller

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"workflow-api/internal/apierror"
	"workflow-api/internal/dto"
	"workflow-api/internal/middleware"
	"workflow-api/internal/response"
	"workflow-api/internal/service"
	"workflow-api/internal/token"
)

const (
	defaultReadPage      = 1
	defaultReadPageSize  = 20
	maxReadPageSize      = 100
	defaultAuditPageSize = 100
)

type WorkflowReadController struct {
	service *service.WorkflowReadService
	auditor *service.AuditorReadService
	logger  *zap.Logger
}

func NewWorkflowReadController(workflow *service.WorkflowReadService, logger *zap.Logger, auditor ...*service.AuditorReadService) *WorkflowReadController {
	var auditorService *service.AuditorReadService
	if len(auditor) > 0 {
		auditorService = auditor[0]
	}
	return &WorkflowReadController{service: workflow, auditor: auditorService, logger: logger}
}

func (h *WorkflowReadController) ListBatches(c *gin.Context) {
	claims, ok := middleware.ClaimsFromContext(c)
	if !ok {
		h.writeError(c, apierror.InvalidSession)
		return
	}
	page, err := readPage(c)
	if err != nil {
		h.writeError(c, apierror.InvalidRequest)
		return
	}
	result, err := h.service.ListBatches(c.Request.Context(), readActor(claims), c.Query("status"), page)
	if err != nil {
		h.writeError(c, mapWorkflowReadError(err))
		return
	}
	response.JSON(c, http.StatusOK, response.Page[dto.BatchView]{
		Data: result.Data, Page: result.Page, PageSize: result.PageSize, TotalCount: result.TotalCount,
		CorrelationID: middleware.GetCorrelationID(c),
	})
}

func (h *WorkflowReadController) GetBatch(c *gin.Context) {
	claims, ok := middleware.ClaimsFromContext(c)
	if !ok {
		h.writeError(c, apierror.InvalidSession)
		return
	}
	batchID := strings.TrimSpace(c.Param("batch_id"))
	result, err := h.service.GetBatch(c.Request.Context(), batchID, readActor(claims))
	if err != nil {
		h.writeError(c, mapWorkflowReadError(err))
		return
	}
	response.JSON(c, http.StatusOK, response.Mutation[dto.BatchView]{Data: result, CorrelationID: middleware.GetCorrelationID(c)})
}

func (h *WorkflowReadController) ListProcessingBatches(c *gin.Context) {
	claims, ok := middleware.ClaimsFromContext(c)
	if !ok {
		h.writeError(c, apierror.InvalidSession)
		return
	}
	page, err := readPage(c)
	if err != nil {
		h.writeError(c, apierror.InvalidRequest)
		return
	}
	result, err := h.service.ListProcessingBatches(c.Request.Context(), readActor(claims), c.Query("status"), page)
	if err != nil {
		h.writeError(c, mapWorkflowReadError(err))
		return
	}
	response.JSON(c, http.StatusOK, response.Page[dto.ProcessingSummaryView]{
		Data: result.Data, Page: result.Page, PageSize: result.PageSize,
		TotalCount: result.TotalCount, CorrelationID: middleware.GetCorrelationID(c),
	})
}

func (h *WorkflowReadController) GetProcessingBatch(c *gin.Context) {
	claims, ok := middleware.ClaimsFromContext(c)
	if !ok {
		h.writeError(c, apierror.InvalidSession)
		return
	}
	result, err := h.service.GetProcessingBatch(c.Request.Context(), strings.TrimSpace(c.Param("batch_id")), readActor(claims))
	if err != nil {
		h.writeError(c, mapWorkflowReadError(err))
		return
	}
	response.JSON(c, http.StatusOK, response.Resource[dto.ProcessingDetailView]{
		Data: result, CorrelationID: middleware.GetCorrelationID(c),
	})
}

func (h *WorkflowReadController) GetAuditTimeline(c *gin.Context) {
	claims, ok := middleware.ClaimsFromContext(c)
	if !ok || h.auditor == nil {
		h.writeError(c, apierror.InvalidSession)
		return
	}
	page, err := readPageWithDefault(c, defaultAuditPageSize)
	if err != nil {
		h.writeError(c, apierror.InvalidRequest)
		return
	}
	result, err := h.auditor.Timeline(c.Request.Context(), strings.TrimSpace(c.Param("batch_id")), readActor(claims), page)
	if err != nil {
		h.writeError(c, mapWorkflowReadError(err))
		return
	}
	response.JSON(c, http.StatusOK, response.Page[dto.AuditTimelineView]{
		Data: result.Data, Page: result.Page, PageSize: result.PageSize, TotalCount: result.TotalCount,
		CorrelationID: middleware.GetCorrelationID(c),
	})
}

func (h *WorkflowReadController) GetAuditAnomalies(c *gin.Context) {
	claims, ok := middleware.ClaimsFromContext(c)
	if !ok || h.auditor == nil {
		h.writeError(c, apierror.InvalidSession)
		return
	}
	page, err := readPageWithDefault(c, defaultAuditPageSize)
	if err != nil {
		h.writeError(c, apierror.InvalidRequest)
		return
	}
	result, err := h.auditor.Anomalies(c.Request.Context(), strings.TrimSpace(c.Param("batch_id")), readActor(claims), page)
	if err != nil {
		h.writeError(c, mapWorkflowReadError(err))
		return
	}
	response.JSON(c, http.StatusOK, response.Page[dto.AnomalyView]{
		Data: result.Data, Page: result.Page, PageSize: result.PageSize, TotalCount: result.TotalCount,
		CorrelationID: middleware.GetCorrelationID(c),
	})
}

func (h *WorkflowReadController) GetAuditImpact(c *gin.Context) {
	claims, ok := middleware.ClaimsFromContext(c)
	if !ok || h.auditor == nil {
		h.writeError(c, apierror.InvalidSession)
		return
	}
	data, err := h.auditor.Impact(c.Request.Context(), readActor(claims), service.ImpactQuery{
		CompletedFrom: c.Query("completed_from"), CompletedTo: c.Query("completed_to"),
		Category: c.Query("category"), ProcessingOrgID: c.Query("processing_org_id"),
	})
	if err != nil {
		h.writeError(c, mapWorkflowReadError(err))
		return
	}
	response.JSON(c, http.StatusOK, response.Resource[dto.ImpactCollectionView]{
		Data: data, CorrelationID: middleware.GetCorrelationID(c),
	})
}

func (h *WorkflowReadController) ListOpportunities(c *gin.Context) {
	claims, ok := middleware.ClaimsFromContext(c)
	if !ok {
		h.writeError(c, apierror.InvalidSession)
		return
	}
	page, err := readPage(c)
	if err != nil {
		h.writeError(c, apierror.InvalidRequest)
		return
	}
	result, err := h.service.ListOpportunities(c.Request.Context(), readActor(claims), page)
	if err != nil {
		h.writeError(c, mapWorkflowReadError(err))
		return
	}
	response.JSON(c, http.StatusOK, response.Page[dto.OpportunityView]{
		Data: result.Data, Page: result.Page, PageSize: result.PageSize, TotalCount: result.TotalCount,
		CorrelationID: middleware.GetCorrelationID(c),
	})
}

func (h *WorkflowReadController) GetOpportunity(c *gin.Context) {
	claims, ok := middleware.ClaimsFromContext(c)
	if !ok {
		h.writeError(c, apierror.InvalidSession)
		return
	}
	result, err := h.service.GetOpportunity(c.Request.Context(), strings.TrimSpace(c.Param("batch_id")), readActor(claims))
	if err != nil {
		h.writeError(c, mapWorkflowReadError(err))
		return
	}
	response.JSON(c, http.StatusOK, response.Resource[dto.OpportunityView]{Data: result, CorrelationID: middleware.GetCorrelationID(c)})
}

func (h *WorkflowReadController) ListAssignments(c *gin.Context) {
	claims, ok := middleware.ClaimsFromContext(c)
	if !ok {
		h.writeError(c, apierror.InvalidSession)
		return
	}
	page, err := readPage(c)
	if err != nil {
		h.writeError(c, apierror.InvalidRequest)
		return
	}
	result, err := h.service.ListAssignments(c.Request.Context(), readActor(claims), c.Query("status"), page)
	if err != nil {
		h.writeError(c, mapWorkflowReadError(err))
		return
	}
	response.JSON(c, http.StatusOK, response.Page[dto.AssignmentView]{
		Data: result.Data, Page: result.Page, PageSize: result.PageSize, TotalCount: result.TotalCount,
		CorrelationID: middleware.GetCorrelationID(c), ServerTime: new(time.Now().UTC()),
	})
}

func (h *WorkflowReadController) GetAssignment(c *gin.Context) {
	claims, ok := middleware.ClaimsFromContext(c)
	if !ok {
		h.writeError(c, apierror.InvalidSession)
		return
	}
	result, err := h.service.GetAssignment(c.Request.Context(), strings.TrimSpace(c.Param("assignment_id")), readActor(claims))
	if err != nil {
		h.writeError(c, mapWorkflowReadError(err))
		return
	}
	response.JSON(c, http.StatusOK, response.Mutation[dto.AssignmentView]{Data: result, CorrelationID: middleware.GetCorrelationID(c)})
}

func readPage(c *gin.Context) (service.WorkflowReadPage, error) {
	return readPageWithDefault(c, defaultReadPageSize)
}

func readPageWithDefault(c *gin.Context, defaultPageSize int) (service.WorkflowReadPage, error) {
	page := defaultReadPage
	pageSize := defaultPageSize
	var err error
	if raw := strings.TrimSpace(c.Query("page")); raw != "" {
		page, err = strconv.Atoi(raw)
		if err != nil || page < 1 {
			return service.WorkflowReadPage{}, service.ErrWorkflowReadInvalid
		}
	}
	if raw := strings.TrimSpace(c.Query("page_size")); raw != "" {
		pageSize, err = strconv.Atoi(raw)
		if err != nil || pageSize < 1 || pageSize > maxReadPageSize {
			return service.WorkflowReadPage{}, service.ErrWorkflowReadInvalid
		}
	}
	return service.WorkflowReadPage{Page: page, PageSize: pageSize}, nil
}

func readActor(claims *token.Claims) service.WorkflowReadActor {
	return service.WorkflowReadActor{UserID: claims.UserID, OrganisationID: claims.OrganisationID, RoleCode: claims.RoleCode}
}

func mapWorkflowReadError(err error) apierror.Code {
	switch {
	case errors.Is(err, service.ErrWorkflowReadForbidden):
		return apierror.Forbidden
	case errors.Is(err, service.ErrWorkflowReadNotFound):
		return apierror.NotFound
	case errors.Is(err, service.ErrWorkflowReadInvalid):
		return apierror.InvalidRequest
	default:
		return apierror.ServiceUnavailable
	}
}

func (h *WorkflowReadController) writeError(c *gin.Context, code apierror.Code) {
	if h.logger != nil {
		h.logger.Warn("workflow read request failed", zap.String("error_code", string(code)), zap.String("correlation_id", middleware.GetCorrelationID(c)))
	}
	response.Error(c, code, middleware.GetCorrelationID(c))
}
