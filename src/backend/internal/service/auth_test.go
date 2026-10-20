package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"

	"workflow-api/internal/dto"
	"workflow-api/internal/model"
	"workflow-api/internal/repository"
	"workflow-api/internal/token"
)

type fakeAuthRepository struct {
	user     *model.User
	session  *model.Session
	audits   []*model.LoginAudit
	auditErr error
}

func (f *fakeAuthRepository) FindActiveUserByEmail(context.Context, string) (*model.User, error) {
	if f.user == nil {
		return nil, repository.ErrNotFound
	}
	return f.user, nil
}

func (f *fakeAuthRepository) FindActiveUserByID(context.Context, string) (*model.User, error) {
	if f.user == nil || f.user.Status != "ACTIVE" {
		return nil, repository.ErrNotFound
	}
	return f.user, nil
}

func (f *fakeAuthRepository) CreateLoginSession(_ context.Context, _ *model.User, session *model.Session, _ time.Time) error {
	f.session = session
	return nil
}

func (f *fakeAuthRepository) CreateLoginAudit(_ context.Context, audit *model.LoginAudit) error {
	if f.auditErr != nil {
		return f.auditErr
	}
	f.audits = append(f.audits, audit)
	return nil
}

func (f *fakeAuthRepository) FindSession(context.Context, string) (*model.Session, error) {
	if f.session == nil {
		return nil, repository.ErrNotFound
	}
	return f.session, nil
}

func (f *fakeAuthRepository) RotateSession(_ context.Context, _, _, oldHash, newHash string, expiresAt, now time.Time) error {
	if f.session == nil || f.session.RefreshTokenHash != oldHash || f.session.Status != "ACTIVE" || !f.session.ExpiresAt.After(now) {
		return repository.ErrRotationRejected
	}
	f.session.RefreshTokenHash = newHash
	f.session.ExpiresAt = expiresAt
	f.session.LastSeenAt = &now
	return nil
}

func (f *fakeAuthRepository) RevokeSession(_ context.Context, _, _, _ string, _ time.Time) error {
	if f.session == nil {
		return repository.ErrNotFound
	}
	f.session.Status = "REVOKED"
	return nil
}

func TestLoginAndRefreshRotateTheSession(t *testing.T) {
	passwordHash, err := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	repo := &fakeAuthRepository{user: &model.User{
		UserID: "USR-001", Email: "donor@example.com", PasswordHash: string(passwordHash),
		RoleCode: "DONOR", OrganisationID: "DON-001", Status: "ACTIVE",
	}}
	service := NewAuthService(repo, token.NewService("test", "access", "refresh", "hash", 15*time.Minute, 24*time.Hour), zap.NewNop())
	now := time.Now().UTC().Truncate(time.Second)
	service.clock = func() time.Time { return now }

	response, err := service.Login(context.Background(), dto.LoginRequest{Email: " DONOR@EXAMPLE.COM ", Password: "correct-password"}, LoginAuditMetadata{
		CorrelationID: "corr-login-001",
		SourceIP:      "192.0.2.10",
	})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if response.TokenType != "Bearer" || response.AccessToken == "" || response.RefreshToken == "" {
		t.Fatalf("unexpected login response: %+v", response)
	}
	if len(repo.audits) != 1 || repo.audits[0].Result != LoginAuditSuccess || repo.audits[0].AttemptedEmail != "donor@example.com" {
		t.Fatalf("unexpected successful login audit: %+v", repo.audits)
	}
	if repo.audits[0].UserID == nil || *repo.audits[0].UserID != "USR-001" || repo.audits[0].CorrelationID != "corr-login-001" {
		t.Fatalf("successful login audit missing identity: %+v", repo.audits[0])
	}
	oldHash := repo.session.RefreshTokenHash
	rotated, err := service.Refresh(context.Background(), response.RefreshToken)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if rotated.RefreshToken == response.RefreshToken || repo.session.RefreshTokenHash == oldHash {
		t.Fatal("refresh token was not rotated")
	}
	if _, err := service.Refresh(context.Background(), response.RefreshToken); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("expected old refresh token to be rejected, got %v", err)
	}
}

func TestLoginUsesGenericInvalidCredentialsError(t *testing.T) {
	repo := &fakeAuthRepository{user: &model.User{UserID: "USR-001", Email: "user@example.com", PasswordHash: "not-a-valid-bcrypt-hash", Status: "ACTIVE"}}
	service := NewAuthService(repo, token.NewService("test", "access", "refresh", "hash", time.Minute, time.Hour), zap.NewNop())

	_, err := service.Login(context.Background(), dto.LoginRequest{Email: "user@example.com", Password: "wrong"}, LoginAuditMetadata{CorrelationID: "corr-failure-001"})
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected generic invalid credentials error, got %v", err)
	}
	if len(repo.audits) != 1 || repo.audits[0].Result != LoginAuditFailure || repo.audits[0].ReasonCode == nil || *repo.audits[0].ReasonCode != "AUTH_INVALID_CREDENTIALS" {
		t.Fatalf("unexpected invalid-credentials audit: %+v", repo.audits)
	}
}
