package controller

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"workflow-api/internal/apierror"
	"workflow-api/internal/dto"
	"workflow-api/internal/middleware"
	"workflow-api/internal/response"
	"workflow-api/internal/service"
)

type AuthController struct {
	service *service.AuthService
	logger  *zap.Logger
}

func NewAuthController(authService *service.AuthService, logger *zap.Logger) *AuthController {
	return &AuthController{service: authService, logger: logger}
}

func (h *AuthController) Login(c *gin.Context) {
	var request dto.LoginRequest
	metadata := service.LoginAuditMetadata{
		CorrelationID: middleware.GetCorrelationID(c),
		SourceIP:      c.ClientIP(),
	}

	if err := c.ShouldBindJSON(&request); err != nil {
		h.service.AuditInvalidLoginRequest(c.Request.Context(), request.Email, metadata)
		h.writeError(c, apierror.InvalidRequest, err)
		return
	}

	result, err := h.service.Login(c.Request.Context(), request, metadata)
	if err != nil {
		h.writeError(c, mapAuthError(err), err)
		return
	}

	response.JSON(c, http.StatusOK, result)
}

func (h *AuthController) Refresh(c *gin.Context) {
	var request dto.RefreshRequest

	if err := c.ShouldBindJSON(&request); err != nil {
		h.writeError(c, apierror.InvalidRequest, err)
		return
	}

	result, err := h.service.Refresh(c.Request.Context(), request.RefreshToken)
	if err != nil {
		h.writeError(c, mapAuthError(err), err)
		return
	}

	response.JSON(c, http.StatusOK, result)
}

func (h *AuthController) Logout(c *gin.Context) {
	claims, ok := middleware.ClaimsFromContext(c)
	if !ok {
		h.writeError(c, apierror.InvalidSession, nil)
		return
	}

	if err := h.service.Logout(c.Request.Context(), claims.UserID, claims.SessionID); err != nil {
		h.writeError(c, mapAuthError(err), err)
		return
	}

	response.NoContent(c)
}

func (h *AuthController) writeError(c *gin.Context, code apierror.Code, err error) {
	if err != nil && h.logger != nil {
		h.logger.Warn(
			"authentication request failed",
			zap.String("error_code", string(code)),
			zap.String("correlation_id", middleware.GetCorrelationID(c)),
			zap.String("error_type", fmt.Sprintf("%T", err)),
		)
	}

	response.Error(c, code, middleware.GetCorrelationID(c))
}

func mapAuthError(err error) apierror.Code {
	switch {
	case errors.Is(err, service.ErrInvalidCredentials):
		return apierror.InvalidCredentials
	case errors.Is(err, service.ErrInvalidSession):
		return apierror.InvalidSession
	default:
		return apierror.ServiceUnavailable
	}
}
