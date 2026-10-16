package controller

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"workflow-api/internal/apierror"
	"workflow-api/internal/dto"
	"workflow-api/internal/middleware"
	"workflow-api/internal/response"
	"workflow-api/internal/service"
	"workflow-api/internal/token"
)

type AssignmentController struct {
	service *service.AssignmentWorkflowService
	logger  *zap.Logger
}

func NewAssignmentController(workflow *service.AssignmentWorkflowService, logger *zap.Logger) *AssignmentController {
	return &AssignmentController{service: workflow, logger: logger}
}

type assignmentIDParams struct {
	AssignmentID string
}

func (h *AssignmentController) Select(c *gin.Context) {
	claims, ok := middleware.ClaimsFromContext(c)
	if !ok {
		h.writeError(c, apierror.InvalidSession)
		return
	}
	var params dto.BatchIDParams
	if err := c.ShouldBindUri(&params); err != nil {
		h.writeError(c, apierror.InvalidRequest)
		return
	}
	var request dto.AssignmentSelectionRequest
	if !decodeAssignmentBody(c, &request) {
		return
	}
	metadata, err := assignmentMetadata(c, claims)
	if err != nil {
		h.writeError(c, apierror.ValidationError)
		return
	}
	result, err := h.service.Select(c.Request.Context(), params.BatchID, request, metadata)
	if err != nil {
		h.writeError(c, mapAssignmentError(err))
		return
	}
	response.JSON(c, http.StatusCreated, result)
}

func (h *AssignmentController) Accept(c *gin.Context) {
	_, params, metadata, ok := h.prepareMutation(c)
	if !ok {
		return
	}
	result, err := h.service.Accept(c.Request.Context(), params.AssignmentID, metadata)
	if err != nil {
		h.writeMutationError(c, err)
		return
	}
	response.JSON(c, http.StatusOK, result)
}

func (h *AssignmentController) Reject(c *gin.Context) {
	_, params, metadata, ok := h.prepareMutation(c)
	if !ok {
		return
	}
	var request dto.RejectAssignmentRequest
	if !decodeAssignmentBody(c, &request) {
		return
	}
	result, err := h.service.Reject(c.Request.Context(), params.AssignmentID, request, metadata)
	if err != nil {
		h.writeMutationError(c, err)
		return
	}
	response.JSON(c, http.StatusOK, result)
}

func (h *AssignmentController) Handoff(c *gin.Context) {
	_, params, metadata, ok := h.prepareMutation(c)
	if !ok {
		return
	}
	var request dto.HandoffRequest
	if !decodeAssignmentBody(c, &request) {
		return
	}
	result, err := h.service.Handoff(c.Request.Context(), params.AssignmentID, request, metadata)
	if err != nil {
		h.writeMutationError(c, err)
		return
	}
	response.JSON(c, http.StatusOK, result)
}

func (h *AssignmentController) Fail(c *gin.Context) {
	_, params, metadata, ok := h.prepareMutation(c)
	if !ok {
		return
	}
	var request dto.FailedPickupRequest
	if !decodeAssignmentBody(c, &request) {
		return
	}
	result, err := h.service.Fail(c.Request.Context(), params.AssignmentID, request, metadata)
	if err != nil {
		h.writeMutationError(c, err)
		return
	}
	response.JSON(c, http.StatusOK, result)
}

func (h *AssignmentController) prepareMutation(c *gin.Context) (*token.Claims, assignmentIDParams, service.BatchCommandMetadata, bool) {
	claims, ok := middleware.ClaimsFromContext(c)
	if !ok {
		h.writeError(c, apierror.InvalidSession)
		return nil, assignmentIDParams{}, service.BatchCommandMetadata{}, false
	}
	assignmentID := strings.TrimSpace(c.Param("assignment_id"))
	if assignmentID == "" {
		h.writeError(c, apierror.InvalidRequest)
		return nil, assignmentIDParams{}, service.BatchCommandMetadata{}, false
	}
	metadata, err := assignmentMetadata(c, claims)
	if err != nil {
		h.writeError(c, apierror.ValidationError)
		return nil, assignmentIDParams{}, service.BatchCommandMetadata{}, false
	}
	return claims, assignmentIDParams{AssignmentID: assignmentID}, metadata, true
}

func assignmentMetadata(c *gin.Context, claims *token.Claims) (service.BatchCommandMetadata, error) {
	idempotencyKey := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if idempotencyKey == "" {
		return service.BatchCommandMetadata{}, errors.New("idempotency key is required")
	}
	version, err := strconv.ParseInt(strings.TrimSpace(c.GetHeader("If-Match-Version")), 10, 64)
	if err != nil || version < 1 {
		return service.BatchCommandMetadata{}, errors.New("version is invalid")
	}
	return service.BatchCommandMetadata{
		Actor:         service.BatchActor{UserID: claims.UserID, OrganisationID: claims.OrganisationID, RoleCode: claims.RoleCode},
		CorrelationID: middleware.GetCorrelationID(c), IdempotencyKey: idempotencyKey, ExpectedVersion: version,
	}, nil
}

func decodeAssignmentBody(c *gin.Context, target any) bool {
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		response.Error(c, apierror.InvalidRequest, middleware.GetCorrelationID(c))
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		response.Error(c, apierror.InvalidRequest, middleware.GetCorrelationID(c))
		return false
	}
	return true
}

func (h *AssignmentController) writeError(c *gin.Context, code apierror.Code) {
	if h.logger != nil {
		h.logger.Warn("assignment request failed", zap.String("error_code", string(code)), zap.String("correlation_id", middleware.GetCorrelationID(c)))
	}
	response.Error(c, code, middleware.GetCorrelationID(c))
}

func (h *AssignmentController) writeMutationError(c *gin.Context, err error) {
	code := mapAssignmentError(err)
	if h.logger != nil {
		h.logger.Warn("assignment request failed", zap.String("error_code", string(code)), zap.String("correlation_id", middleware.GetCorrelationID(c)))
	}
	if code == apierror.StaleVersion {
		if current, ok := service.AssignmentCurrentStateFromError(err); ok {
			response.ErrorWithDetails(c, code, middleware.GetCorrelationID(c), current)
			return
		}
	}
	response.Error(c, code, middleware.GetCorrelationID(c))
}

func mapAssignmentError(err error) apierror.Code {
	switch {
	case errors.Is(err, service.ErrAssignmentForbidden):
		return apierror.Forbidden
	case errors.Is(err, service.ErrAssignmentNotFound):
		return apierror.NotFound
	case errors.Is(err, service.ErrAssignmentValidation):
		return apierror.ValidationError
	case errors.Is(err, service.ErrAssignmentStaleVersion):
		return apierror.StaleVersion
	case errors.Is(err, service.ErrAssignmentIdempotencyConflict):
		return apierror.IdempotencyConflict
	case errors.Is(err, service.ErrAssignmentInvalidState), errors.Is(err, service.ErrAssignmentInProgress), errors.Is(err, service.ErrAssignmentConcurrent):
		return apierror.Conflict
	default:
		return apierror.ServiceUnavailable
	}
}
