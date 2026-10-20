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
	"workflow-api/internal/repository"
	"workflow-api/internal/response"
	"workflow-api/internal/service"
	"workflow-api/internal/token"
)

type BatchController struct {
	service *service.BatchService
	logger  *zap.Logger
}

func NewBatchController(
	batchService *service.BatchService,
	logger *zap.Logger,
) *BatchController {
	return &BatchController{
		service: batchService,
		logger:  logger,
	}
}

func (h *BatchController) CreateDraft(c *gin.Context) {
	claims, ok := middleware.ClaimsFromContext(c)
	if !ok {
		h.writeError(c, apierror.InvalidSession, nil)
		return
	}

	var request dto.BatchDraftRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.writeError(c, apierror.InvalidRequest, err)
		return
	}

	metadata, err := batchMetadata(c, claims, false)
	if err != nil {
		h.writeError(c, apierror.InvalidRequest, err)
		return
	}

	result, err := h.service.CreateDraft(c.Request.Context(), request, metadata)
	if err != nil {
		h.writeError(c, mapBatchError(err), err)
		return
	}

	writeBatchMutation(c, http.StatusCreated, result)
}

func (h *BatchController) EditDraft(c *gin.Context) {
	claims, ok := middleware.ClaimsFromContext(c)
	if !ok {
		h.writeError(c, apierror.InvalidSession, nil)
		return
	}

	var params dto.BatchIDParams
	if err := c.ShouldBindUri(&params); err != nil {
		h.writeError(c, apierror.InvalidRequest, err)
		return
	}

	var request dto.BatchDraftRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.writeError(c, apierror.InvalidRequest, err)
		return
	}

	metadata, err := batchMetadata(c, claims, true)
	if err != nil {
		h.writeError(c, apierror.InvalidRequest, err)
		return
	}

	result, err := h.service.EditDraft(
		c.Request.Context(),
		params.BatchID,
		request,
		metadata,
	)
	if err != nil {
		h.writeError(c, mapBatchError(err), err)
		return
	}

	writeBatchMutation(c, http.StatusOK, result)
}

func (h *BatchController) Submit(c *gin.Context) {
	claims, ok := middleware.ClaimsFromContext(c)
	if !ok {
		h.writeError(c, apierror.InvalidSession, nil)
		return
	}

	var params dto.BatchIDParams
	if err := c.ShouldBindUri(&params); err != nil {
		h.writeError(c, apierror.InvalidRequest, err)
		return
	}

	metadata, err := batchMetadata(c, claims, true)
	if err != nil {
		h.writeError(c, apierror.InvalidRequest, err)
		return
	}

	result, err := h.service.Submit(
		c.Request.Context(),
		params.BatchID,
		metadata,
	)
	if err != nil {
		h.writeError(c, mapBatchError(err), err)
		return
	}

	writeBatchMutation(c, http.StatusOK, result)
}

func (h *BatchController) VerifyReceipt(c *gin.Context) {
	claims, ok := middleware.ClaimsFromContext(c)
	if !ok {
		h.writeError(c, apierror.InvalidSession, nil)
		return
	}

	var params dto.BatchIDParams
	if err := c.ShouldBindUri(&params); err != nil {
		h.writeError(c, apierror.InvalidRequest, err)
		return
	}

	var request dto.ReceiptRequest
	if !decodeBatchJSON(c, &request) {
		return
	}

	metadata, err := batchMetadata(c, claims, true)
	if err != nil {
		h.writeError(c, apierror.InvalidRequest, err)
		return
	}

	result, err := h.service.VerifyReceipt(c.Request.Context(), params.BatchID, request, metadata)
	if err != nil {
		h.writeError(c, mapBatchError(err), err)
		return
	}

	response.JSON(c, http.StatusOK, result)
}

func (h *BatchController) RecordTreatment(c *gin.Context) {
	claims, ok := middleware.ClaimsFromContext(c)
	if !ok {
		h.writeError(c, apierror.InvalidSession, nil)
		return
	}

	var params dto.BatchIDParams
	if err := c.ShouldBindUri(&params); err != nil {
		h.writeError(c, apierror.InvalidRequest, err)
		return
	}

	var request dto.TreatmentRequest
	if !decodeBatchJSON(c, &request) {
		return
	}

	metadata, err := batchMetadata(c, claims, true)
	if err != nil {
		h.writeError(c, apierror.InvalidRequest, err)
		return
	}

	result, err := h.service.RecordTreatment(c.Request.Context(), params.BatchID, request, metadata)
	if err != nil {
		h.writeError(c, mapBatchError(err), err)
		return
	}

	response.JSON(c, http.StatusOK, result)
}

func batchMetadata(
	c *gin.Context,
	claims *token.Claims,
	requireVersion bool,
) (service.BatchCommandMetadata, error) {
	idempotencyKey := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if idempotencyKey == "" {
		return service.BatchCommandMetadata{}, service.NewBatchValidationError(map[string]string{
			"Idempotency-Key": "header is required",
		})
	}

	metadata := service.BatchCommandMetadata{
		Actor: service.BatchActor{
			UserID:         claims.UserID,
			OrganisationID: claims.OrganisationID,
			RoleCode:       claims.RoleCode,
		},
		CorrelationID:  middleware.GetCorrelationID(c),
		IdempotencyKey: idempotencyKey,
	}

	if requireVersion {
		rawVersion := strings.TrimSpace(c.GetHeader("If-Match-Version"))
		if rawVersion == "" {
			return service.BatchCommandMetadata{}, service.NewBatchValidationError(map[string]string{
				"If-Match-Version": "header is required",
			})
		}

		version, err := strconv.ParseInt(rawVersion, 10, 64)
		if err != nil || version < 1 {
			return service.BatchCommandMetadata{}, service.NewBatchValidationError(map[string]string{
				"If-Match-Version": "must be a positive integer",
			})
		}

		metadata.ExpectedVersion = version
	}

	return metadata, nil
}

func writeBatchMutation(
	c *gin.Context,
	status int,
	result service.BatchMutationResult,
) {
	response.JSON(c, status, response.Mutation[dto.BatchView]{
		Data:          result.Batch,
		CorrelationID: result.CorrelationID,
		EventID:       result.EventID,
		EventState:    result.EventState,
	})
}

func decodeBatchJSON(c *gin.Context, target any) bool {
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

func (h *BatchController) writeError(
	c *gin.Context,
	code apierror.Code,
	err error,
) {
	if err != nil && h.logger != nil {
		h.logger.Warn(
			"batch request failed",
			zap.String("error_code", string(code)),
			zap.String("correlation_id", middleware.GetCorrelationID(c)),
		)
	}

	response.Error(c, code, middleware.GetCorrelationID(c))
}

func mapBatchError(err error) apierror.Code {
	switch {
	case errors.Is(err, service.ErrBatchForbidden):
		return apierror.Forbidden
	case errors.Is(err, service.ErrBatchNotFound),
		errors.Is(err, repository.ErrBatchNotFound),
		errors.Is(err, service.ErrBatchEvidenceNotFound):
		return apierror.NotFound
	case errors.Is(err, service.ErrBatchValidation):
		return apierror.ValidationError
	case errors.Is(err, service.ErrBatchStaleVersion),
		errors.Is(err, repository.ErrBatchConcurrency):
		return apierror.StaleVersion
	case errors.Is(err, service.ErrBatchIdempotencyConflict):
		return apierror.IdempotencyConflict
	case errors.Is(err, service.ErrBatchInvalidState),
		errors.Is(err, service.ErrBatchInProgress):
		return apierror.Conflict
	default:
		return apierror.ServiceUnavailable
	}
}
