package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"

	"workflow-api/internal/dto"
	"workflow-api/internal/middleware"
	"workflow-api/internal/model"
	"workflow-api/internal/repository"
	"workflow-api/internal/service"
	"workflow-api/internal/token"
)

type authRepositoryStub struct {
	user      *model.User
	findErr   error
	session   *model.Session
	audits    []*model.LoginAudit
	createErr error
	rotateErr error
	revokeErr error
}

func (s *authRepositoryStub) FindActiveUserByEmail(context.Context, string) (*model.User, error) {
	if s.findErr != nil {
		return nil, s.findErr
	}
	if s.user == nil {
		return nil, repository.ErrNotFound
	}
	return s.user, nil
}

func (s *authRepositoryStub) FindActiveUserByID(context.Context, string) (*model.User, error) {
	if s.user == nil || s.user.Status != "ACTIVE" {
		return nil, repository.ErrNotFound
	}
	return s.user, nil
}

func (s *authRepositoryStub) CreateLoginSession(_ context.Context, _ *model.User, session *model.Session, _ time.Time) error {
	if s.createErr != nil {
		return s.createErr
	}
	s.session = session
	return nil
}

func (s *authRepositoryStub) CreateLoginAudit(_ context.Context, audit *model.LoginAudit) error {
	s.audits = append(s.audits, audit)
	return nil
}

func (s *authRepositoryStub) FindSession(context.Context, string) (*model.Session, error) {
	if s.session == nil {
		return nil, repository.ErrNotFound
	}
	return s.session, nil
}

func (s *authRepositoryStub) RotateSession(context.Context, string, string, string, string, time.Time, time.Time) error {
	return s.rotateErr
}

func (s *authRepositoryStub) RevokeSession(context.Context, string, string, string, time.Time) error {
	return s.revokeErr
}

func newLoginTestRouter(repo repository.AuthRepository) *gin.Engine {
	gin.SetMode(gin.TestMode)
	logger := zap.NewNop()
	tokens := token.NewService("test", "access-secret", "refresh-secret", "hash-secret", 15*time.Minute, 24*time.Hour)
	authService := service.NewAuthService(repo, tokens, logger)
	authController := NewAuthController(authService, logger)

	r := gin.New()
	r.Use(middleware.CorrelationID())
	r.POST("/api/v1/auth/login", authController.Login)
	return r
}

func activeUserForTest(t *testing.T) *model.User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	return &model.User{
		UserID: "USR-001", Email: "user@example.com", PasswordHash: string(hash),
		RoleCode: "DONOR", OrganisationID: "DON-001", Status: "ACTIVE",
	}
}

func TestLoginReturnsFlatTokenResponseAndCorrelationID(t *testing.T) {
	r := newLoginTestRouter(&authRepositoryStub{user: activeUserForTest(t)})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"email":"USER@EXAMPLE.COM","password":"correct-password"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Correlation-ID", "corr-login-001")
	res := httptest.NewRecorder()

	r.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", res.Code, res.Body.String())
	}
	if res.Header().Get("X-Correlation-ID") != "corr-login-001" {
		t.Fatalf("expected correlation ID to be echoed, got %q", res.Header().Get("X-Correlation-ID"))
	}
	var response dto.TokenResponse
	if err := json.Unmarshal(res.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.AccessToken == "" || response.RefreshToken == "" || response.TokenType != "Bearer" {
		t.Fatalf("unexpected token response: %+v", response)
	}
	if response.ExpiresIn != 900 || response.RefreshExpiresIn != 86400 {
		t.Fatalf("unexpected token lifetimes: %+v", response)
	}
}

func TestLoginReturnsSafeBadRequest(t *testing.T) {
	repo := &authRepositoryStub{user: activeUserForTest(t)}
	r := newLoginTestRouter(repo)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"email":"user@example.com"}`))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()

	r.ServeHTTP(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", res.Code)
	}
	var response dto.ErrorResponse
	if err := json.Unmarshal(res.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if response.Code != "INVALID_REQUEST" || response.Message != "invalid request" || response.CorrelationID == "" {
		t.Fatalf("unexpected safe error: %+v", response)
	}
	if len(repo.audits) != 1 || repo.audits[0].Result != service.LoginAuditFailure || repo.audits[0].ReasonCode == nil || *repo.audits[0].ReasonCode != "INVALID_REQUEST" {
		t.Fatalf("unexpected invalid-request audit: %+v", repo.audits)
	}
}

func TestLoginReturnsGenericUnauthorizedError(t *testing.T) {
	r := newLoginTestRouter(&authRepositoryStub{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"email":"unknown@example.com","password":"wrong"}`))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()

	r.ServeHTTP(res, req)

	if res.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", res.Code)
	}
	if strings.Contains(res.Body.String(), "unknown@example.com") || strings.Contains(res.Body.String(), "wrong") {
		t.Fatalf("response exposed sensitive login input: %s", res.Body.String())
	}
	var response dto.ErrorResponse
	if err := json.Unmarshal(res.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if response.Code != "AUTH_INVALID_CREDENTIALS" || response.Message != "invalid credentials" {
		t.Fatalf("unexpected credentials error: %+v", response)
	}
}

func TestLoginReturns503ForRepositoryFailure(t *testing.T) {
	r := newLoginTestRouter(&authRepositoryStub{findErr: errors.New("database unavailable")})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"email":"user@example.com","password":"password"}`))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()

	r.ServeHTTP(res, req)

	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", res.Code)
	}
	if strings.Contains(res.Body.String(), "database unavailable") {
		t.Fatalf("response exposed dependency error: %s", res.Body.String())
	}
}
