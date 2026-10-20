package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"

	"workflow-api/internal/apierror"
	"workflow-api/internal/dto"
	"workflow-api/internal/model"
	"workflow-api/internal/repository"
	"workflow-api/internal/token"
)

var ErrInvalidCredentials = errors.New("auth: invalid credentials")
var ErrInvalidSession = errors.New("auth: invalid session")

const (
	LoginAuditSuccess = "SUCCESS"
	LoginAuditFailure = "FAILURE"
)

type LoginAuditMetadata struct {
	CorrelationID string
	SourceIP      string
}

type AuthService struct {
	repository repository.AuthRepository
	tokens     *token.Service
	logger     *zap.Logger
	clock      func() time.Time
}

func NewAuthService(repo repository.AuthRepository, tokens *token.Service, logger *zap.Logger) *AuthService {
	return &AuthService{repository: repo, tokens: tokens, logger: logger, clock: func() time.Time { return time.Now().UTC() }}
}

func (s *AuthService) Login(ctx context.Context, request dto.LoginRequest, metadata LoginAuditMetadata) (dto.TokenResponse, error) {
	email := strings.ToLower(strings.TrimSpace(request.Email))
	user, err := s.repository.FindActiveUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			s.writeLoginAudit(ctx, email, nil, metadata, LoginAuditFailure, string(apierror.InvalidCredentials))
			return dto.TokenResponse{}, ErrInvalidCredentials
		}
		s.writeLoginAudit(ctx, email, nil, metadata, LoginAuditFailure, string(apierror.ServiceUnavailable))
		return dto.TokenResponse{}, err
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(request.Password)) != nil {
		s.writeLoginAudit(ctx, email, &user.UserID, metadata, LoginAuditFailure, string(apierror.InvalidCredentials))
		return dto.TokenResponse{}, ErrInvalidCredentials
	}

	now := s.clock()
	sessionID := token.NewSessionID()
	access, refresh, accessExpiry, refreshExpiry, err := s.tokens.Issue(user.UserID, sessionID, user.RoleCode, user.OrganisationID, now)
	if err != nil {
		s.writeLoginAudit(ctx, email, &user.UserID, metadata, LoginAuditFailure, string(apierror.ServiceUnavailable))
		return dto.TokenResponse{}, err
	}
	session := &model.Session{
		SessionID: sessionID, UserID: user.UserID, RefreshTokenHash: s.tokens.HashRefresh(refresh), Status: "ACTIVE",
		IssuedAt: now, ExpiresAt: refreshExpiry,
	}
	if err := s.repository.CreateLoginSession(ctx, user, session, now); err != nil {
		s.writeLoginAudit(ctx, email, &user.UserID, metadata, LoginAuditFailure, string(apierror.ServiceUnavailable))
		return dto.TokenResponse{}, err
	}
	s.writeLoginAudit(ctx, email, &user.UserID, metadata, LoginAuditSuccess, "")
	return dto.TokenResponse{
		AccessToken: access, RefreshToken: refresh, TokenType: "Bearer",
		ExpiresIn: int64(accessExpiry.Sub(now).Seconds()), RefreshExpiresIn: int64(refreshExpiry.Sub(now).Seconds()),
	}, nil
}

func (s *AuthService) AuditInvalidLoginRequest(ctx context.Context, email string, metadata LoginAuditMetadata) {
	s.writeLoginAudit(ctx, normalizeAuditEmail(email), nil, metadata, LoginAuditFailure, string(apierror.InvalidRequest))
}

func (s *AuthService) writeLoginAudit(ctx context.Context, email string, userID *string, metadata LoginAuditMetadata, result, reasonCode string) {
	correlationID := strings.TrimSpace(metadata.CorrelationID)
	if correlationID == "" {
		correlationID = uuid.NewString()
	}

	audit := &model.LoginAudit{
		UserID:         userID,
		AttemptedEmail: normalizeAuditEmail(email),
		Result:         result,
		CorrelationID:  correlationID,
		OccurredAt:     s.clock(),
	}
	if reasonCode != "" {
		audit.ReasonCode = &reasonCode
	}
	if sourceIP := strings.TrimSpace(metadata.SourceIP); sourceIP != "" {
		audit.SourceIP = &sourceIP
	}

	if err := s.repository.CreateLoginAudit(ctx, audit); err != nil {
		if s.logger != nil {
			// Keep database details out of logs; the controller follows the same policy.
			s.logger.Warn("login audit write failed", zap.String("operation", "create"))
		}
	}
}

func normalizeAuditEmail(email string) string {
	email = strings.ToLower(strings.TrimSpace(email))
	runes := []rune(email)
	if len(runes) > 254 {
		runes = runes[:254]
	}
	return string(runes)
}

func (s *AuthService) Refresh(ctx context.Context, rawRefresh string) (dto.TokenResponse, error) {
	claims, err := s.tokens.ParseRefresh(rawRefresh)
	if err != nil {
		return dto.TokenResponse{}, ErrInvalidSession
	}
	session, err := s.repository.FindSession(ctx, claims.SessionID)
	if errors.Is(err, repository.ErrNotFound) || (err == nil && (session == nil || session.UserID != claims.UserID || !session.IsActive(s.clock()))) {
		return dto.TokenResponse{}, ErrInvalidSession
	}
	if err != nil {
		return dto.TokenResponse{}, err
	}
	if session.RefreshTokenHash != s.tokens.HashRefresh(rawRefresh) {
		return dto.TokenResponse{}, ErrInvalidSession
	}
	user, err := s.repository.FindActiveUserByID(ctx, claims.UserID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return dto.TokenResponse{}, ErrInvalidSession
		}
		return dto.TokenResponse{}, err
	}

	now := s.clock()
	access, refresh, accessExpiry, refreshExpiry, err := s.tokens.Issue(user.UserID, claims.SessionID, user.RoleCode, user.OrganisationID, now)
	if err != nil {
		return dto.TokenResponse{}, err
	}
	if err := s.repository.RotateSession(ctx, claims.SessionID, claims.UserID, session.RefreshTokenHash, s.tokens.HashRefresh(refresh), refreshExpiry, now); err != nil {
		return dto.TokenResponse{}, ErrInvalidSession
	}
	return dto.TokenResponse{
		AccessToken: access, RefreshToken: refresh, TokenType: "Bearer",
		ExpiresIn: int64(accessExpiry.Sub(now).Seconds()), RefreshExpiresIn: int64(refreshExpiry.Sub(now).Seconds()),
	}, nil
}

func (s *AuthService) Logout(ctx context.Context, userID, sessionID string) error {
	err := s.repository.RevokeSession(ctx, sessionID, userID, "logout", s.clock())
	if errors.Is(err, repository.ErrNotFound) {
		return ErrInvalidSession
	}
	return err
}
