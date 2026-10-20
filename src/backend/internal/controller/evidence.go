package controller

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"workflow-api/internal/apierror"
	"workflow-api/internal/dto"
	"workflow-api/internal/middleware"
	"workflow-api/internal/model"
	"workflow-api/internal/repository"
	"workflow-api/internal/response"
	"workflow-api/internal/service"
)

type EvidenceController struct {
	service evidenceWorkflow
	logger  *zap.Logger
}

type evidenceWorkflow interface {
	Upload(context.Context, string, service.EvidenceUploadRequest, service.BatchCommandMetadata) (dto.EvidenceMutationResult, error)
	Download(context.Context, string, string, service.EvidenceDownloadActor, string) (service.EvidenceDownloadResult, error)
	MaxUploadSizeBytes() int64
}

func NewEvidenceController(evidenceService evidenceWorkflow, logger *zap.Logger) *EvidenceController {
	return &EvidenceController{service: evidenceService, logger: logger}
}

func (h *EvidenceController) Upload(c *gin.Context) {
	claims, ok := middleware.ClaimsFromContext(c)
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

	maxBytes := h.service.MaxUploadSizeBytes()
	// Multipart framing is small compared with the file itself. The service
	// remains the authoritative validator and hashes only the bounded content.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes+1024*1024)
	fileHeader, err := c.FormFile("file")
	if err != nil {
		h.writeError(c, apierror.ValidationError, err)
		return
	}
	lifecycleStage := strings.TrimSpace(c.PostForm("lifecycle_stage"))
	file, err := fileHeader.Open()
	if err != nil {
		h.writeError(c, apierror.ValidationError, err)
		return
	}
	content, readErr := io.ReadAll(io.LimitReader(file, maxBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		h.writeError(c, apierror.ValidationError, readErr)
		return
	}
	if closeErr != nil {
		h.writeError(c, apierror.ValidationError, closeErr)
		return
	}
	metadata, err := batchMetadata(c, claims, false)
	if err != nil {
		h.writeError(c, apierror.InvalidRequest, err)
		return
	}
	result, err := h.service.Upload(c.Request.Context(), params.BatchID, service.EvidenceUploadRequest{
		Filename: fileHeader.Filename, LifecycleStage: modelEvidenceLifecycleStage(lifecycleStage), Content: content,
	}, metadata)
	if err != nil {
		h.writeError(c, mapEvidenceError(err), err)
		return
	}
	response.JSON(c, http.StatusCreated, result)
}

func (h *EvidenceController) Download(c *gin.Context) {
	claims, ok := middleware.ClaimsFromContext(c)
	if !ok {
		response.Error(c, apierror.InvalidSession, middleware.GetCorrelationID(c))
		return
	}
	var params struct {
		BatchID    string `uri:"batch_id" binding:"required"`
		EvidenceID string `uri:"evidence_id" binding:"required"`
	}
	if err := c.ShouldBindUri(&params); err != nil {
		response.Error(c, apierror.InvalidRequest, middleware.GetCorrelationID(c))
		return
	}

	result, err := h.service.Download(c.Request.Context(), params.BatchID, params.EvidenceID, service.EvidenceDownloadActor{
		BatchActor:        service.BatchActor{UserID: claims.UserID, OrganisationID: claims.OrganisationID, RoleCode: claims.RoleCode},
		AdminAccessReason: c.GetHeader("X-Admin-Access-Reason"),
	}, middleware.GetCorrelationID(c))
	if err != nil {
		h.writeError(c, mapEvidenceError(err), err)
		return
	}

	filename := filepath.Base(result.Filename)
	if filename == "" || filename == "." || filename == string(filepath.Separator) {
		filename = "evidence"
	}
	contentDisposition := mime.FormatMediaType("attachment", map[string]string{"filename": filename})
	c.Header("Content-Type", result.MIMEType)
	c.Header("Content-Disposition", contentDisposition)
	c.Header("Content-Length", strconv.Itoa(result.SizeBytes))
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Security-Policy", "default-src 'none'; sandbox")
	c.Header("Cache-Control", "private, no-store")
	c.Data(http.StatusOK, result.MIMEType, result.Content)
}

func (h *EvidenceController) writeError(c *gin.Context, code apierror.Code, err error) {
	if err != nil && h.logger != nil {
		h.logger.Warn("evidence request failed", zap.String("error_code", string(code)), zap.String("correlation_id", middleware.GetCorrelationID(c)))
	}
	response.Error(c, code, middleware.GetCorrelationID(c))
}

func mapEvidenceError(err error) apierror.Code {
	switch {
	case errors.Is(err, service.ErrBatchForbidden):
		return apierror.Forbidden
	case errors.Is(err, service.ErrBatchNotFound), errors.Is(err, service.ErrBatchEvidenceNotFound), errors.Is(err, repository.ErrBatchNotFound), errors.Is(err, repository.ErrEvidenceNotFound):
		return apierror.NotFound
	case errors.Is(err, service.ErrBatchValidation):
		return apierror.ValidationError
	case errors.Is(err, service.ErrBatchIdempotencyConflict):
		return apierror.IdempotencyConflict
	case errors.Is(err, service.ErrBatchInvalidState):
		return apierror.Conflict
	case errors.Is(err, service.ErrEvidenceStorage), errors.Is(err, service.ErrEvidenceIntegrity):
		return apierror.ServiceUnavailable
	default:
		return apierror.ServiceUnavailable
	}
}

// Keep the controller independent from the model package's string alias while
// accepting only the two OpenAPI values.
func modelEvidenceLifecycleStage(raw string) model.EvidenceLifecycleStage {
	return model.EvidenceLifecycleStage(raw)
}
