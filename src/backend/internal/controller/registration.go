package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"workflow-api/internal/apierror"
	"workflow-api/internal/dto"
	"workflow-api/internal/middleware"
	"workflow-api/internal/response"
	"workflow-api/internal/service"
)

type RegistrationController struct {
	service *service.RegistrationService
	logger  *zap.Logger
}

func NewRegistrationController(registrationService *service.RegistrationService, logger *zap.Logger) *RegistrationController {
	return &RegistrationController{service: registrationService, logger: logger}
}

func (h *RegistrationController) Register(c *gin.Context) {
	var request dto.RegisterRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.writeError(c, apierror.InvalidRequest, err)
		return
	}

	result, err := h.service.Register(c.Request.Context(), request)
	if err != nil {
		h.writeError(c, mapRegistrationError(err), err)
		return
	}

	response.JSON(c, http.StatusCreated, response.Resource[dto.RegistrationResponse]{
		Data: result, CorrelationID: middleware.GetCorrelationID(c),
	})
}

func (h *RegistrationController) ListPending(c *gin.Context) {
	result, err := h.service.ListPending(c.Request.Context())
	if err != nil {
		h.writeError(c, apierror.ServiceUnavailable, err)
		return
	}

	response.JSON(c, http.StatusOK, response.Resource[[]dto.PendingRegistrationResponse]{
		Data: result, CorrelationID: middleware.GetCorrelationID(c),
	})
}

func (h *RegistrationController) Approve(c *gin.Context) {
	h.decide(c, h.service.Approve)
}

func (h *RegistrationController) Reject(c *gin.Context) {
	h.decide(c, h.service.Reject)
}

func (h *RegistrationController) decide(c *gin.Context, apply func(context.Context, service.RegistrationDecision) (dto.RegistrationDecisionResponse, error)) {
	claims, ok := middleware.ClaimsFromContext(c)
	if !ok {
		h.writeError(c, apierror.InvalidSession, nil)
		return
	}

	// The reason is optional, so an empty body is a valid decision.
	var request dto.RegistrationDecisionRequest
	if err := c.ShouldBindJSON(&request); err != nil && !errors.Is(err, io.EOF) {
		h.writeError(c, apierror.InvalidRequest, err)
		return
	}

	result, err := apply(c.Request.Context(), service.RegistrationDecision{
		UserID:        c.Param("user_id"),
		ReviewerID:    claims.UserID,
		Reason:        request.Reason,
		CorrelationID: middleware.GetCorrelationID(c),
	})
	if err != nil {
		h.writeError(c, mapRegistrationError(err), err)
		return
	}

	response.JSON(c, http.StatusOK, response.Resource[dto.RegistrationDecisionResponse]{
		Data: result, CorrelationID: middleware.GetCorrelationID(c),
	})
}

func (h *RegistrationController) writeError(c *gin.Context, code apierror.Code, err error) {
	if err != nil && h.logger != nil {
		h.logger.Warn(
			"registration request failed",
			zap.String("error_code", string(code)),
			zap.String("correlation_id", middleware.GetCorrelationID(c)),
			zap.String("error_type", fmt.Sprintf("%T", err)),
		)
	}

	response.Error(c, code, middleware.GetCorrelationID(c))
}

func mapRegistrationError(err error) apierror.Code {
	switch {
	case errors.Is(err, service.ErrRegistrationInvalid):
		return apierror.ValidationError
	case errors.Is(err, service.ErrRegistrationConflict), errors.Is(err, service.ErrRegistrationNotPending):
		return apierror.RegistrationConflict
	case errors.Is(err, service.ErrRegistrationNotFound):
		return apierror.NotFound
	default:
		return apierror.ServiceUnavailable
	}
}
