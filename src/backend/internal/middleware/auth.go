package middleware

import (
	"errors"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"workflow-api/internal/apierror"
	"workflow-api/internal/repository"
	"workflow-api/internal/response"
	"workflow-api/internal/token"
)

const ClaimsKey = "claims"

func RequireAccessTokens(tokens *token.Service, sessions repository.AuthRepository) gin.HandlerFunc {
	return func(c *gin.Context) {
		parts := strings.Fields(c.GetHeader("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			abortAuth(c)
			return
		}

		claims, err := tokens.ParseAccess(parts[1])
		if err != nil {
			abortAuth(c)
			return
		}

		session, err := sessions.FindSession(c.Request.Context(), claims.SessionID)
		if errors.Is(err, repository.ErrNotFound) ||
			(err == nil && (session == nil ||
				session.UserID != claims.UserID ||
				!session.IsActive(now()))) {
			abortAuth(c)
			return
		}
		if err != nil {
			abortDependency(c)
			return
		}

		user, err := sessions.FindActiveUserByID(c.Request.Context(), claims.UserID)
		if errors.Is(err, repository.ErrNotFound) ||
			(err == nil && (user == nil || user.UserID != claims.UserID)) {
			abortAuth(c)
			return
		}
		if err != nil {
			abortDependency(c)
			return
		}

		c.Set(ClaimsKey, claims)
		c.Next()
	}
}

func ClaimsFromContext(c *gin.Context) (*token.Claims, bool) {
	value, exists := c.Get(ClaimsKey)
	if !exists {
		return nil, false
	}

	claims, ok := value.(*token.Claims)
	return claims, ok
}

func abortAuth(c *gin.Context) {
	response.Error(c, apierror.InvalidSession, GetCorrelationID(c))
}

func abortDependency(c *gin.Context) {
	response.Error(c, apierror.ServiceUnavailable, GetCorrelationID(c))
}

var now = func() time.Time {
	return time.Now().UTC()
}

// RequireRole must run after RequireAccessTokens. It admits only the listed
// role codes and answers FORBIDDEN otherwise.
func RequireRole(roleCodes ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		claims, ok := ClaimsFromContext(c)
		if !ok {
			abortAuth(c)
			return
		}
		for _, roleCode := range roleCodes {
			if strings.EqualFold(claims.RoleCode, roleCode) {
				c.Next()
				return
			}
		}
		response.Error(c, apierror.Forbidden, GetCorrelationID(c))
	}
}
