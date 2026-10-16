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

type ClaimController struct {
	service *service.ClaimWorkflowService
	logger  *zap.Logger
}

func NewClaimController(
	claimService *service.ClaimWorkflowService,
	logger *zap.Logger,
) *ClaimController {
	return &ClaimController{service: claimService, logger: logger}
}

func (h *ClaimController) Claim(c *gin.Context) {
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

	var request dto.ClaimRequest
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		h.writeError(c, apierror.InvalidRequest)
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		h.writeError(c, apierror.InvalidRequest)
		return
	}

	metadata, err := claimMetadata(c, claims)
	if err != nil {
		h.writeError(c, apierror.ValidationError)
		return
	}

	result, err := h.service.Claim(
		c.Request.Context(),
		params.BatchID,
		request,
		metadata,
	)
	if err != nil {
		h.writeError(c, mapClaimError(err))
		return
	}

	response.JSON(c, http.StatusOK, response.Resource[dto.ClaimResult]{
		Data:          result,
		CorrelationID: result.CorrelationID,
	})
}

func claimMetadata(
	c *gin.Context,
	claims *token.Claims,
) (service.BatchCommandMetadata, error) {
	idempotencyKey := c.GetHeader("Idempotency-Key")
	if strings.TrimSpace(idempotencyKey) == "" {
		return service.BatchCommandMetadata{}, errors.New("idempotency key is required")
	}

	version, err := strconv.ParseInt(strings.TrimSpace(c.GetHeader("If-Match-Version")), 10, 64)
	if err != nil || version < 1 {
		return service.BatchCommandMetadata{}, errors.New("version is invalid")
	}

	return service.BatchCommandMetadata{
		Actor: service.BatchActor{
			UserID:         claims.UserID,
			OrganisationID: claims.OrganisationID,
			RoleCode:       claims.RoleCode,
		},
		CorrelationID:   middleware.GetCorrelationID(c),
		IdempotencyKey:  idempotencyKey,
		ExpectedVersion: version,
	}, nil
}

func (h *ClaimController) writeError(c *gin.Context, code apierror.Code) {
	if h.logger != nil {
		h.logger.Warn(
			"claim request failed",
			zap.String("error_code", string(code)),
			zap.String("correlation_id", middleware.GetCorrelationID(c)),
		)
	}
	response.Error(c, code, middleware.GetCorrelationID(c))
}

func mapClaimError(err error) apierror.Code {
	switch {
	case errors.Is(err, service.ErrClaimForbidden):
		return apierror.Forbidden
	case errors.Is(err, repository.ErrClaimActorNotEligible):
		return apierror.Forbidden
	case errors.Is(err, service.ErrClaimOpportunityNotFound),
		errors.Is(err, repository.ErrClaimBatchNotFound),
		errors.Is(err, repository.ErrClaimOpportunityNotFound):
		return apierror.NotFound
	case errors.Is(err, service.ErrClaimValidation):
		return apierror.ValidationError
	case errors.Is(err, service.ErrClaimStaleVersion):
		return apierror.StaleVersion
	case errors.Is(err, service.ErrClaimIdempotencyConflict):
		return apierror.IdempotencyConflict
	case errors.Is(err, service.ErrClaimConcurrent),
		errors.Is(err, service.ErrClaimInvalidState),
		errors.Is(err, service.ErrClaimInProgress),
		errors.Is(err, service.ErrClaimCapacity),
		errors.Is(err, service.ErrClaimNoActiveCollector):
		return apierror.Conflict
	case errors.Is(err, service.ErrClaimLeaseUnavailable):
		return apierror.ServiceUnavailable
	default:
		return apierror.ServiceUnavailable
	}
}
