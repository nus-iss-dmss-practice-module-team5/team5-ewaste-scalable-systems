package token

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const (
	AccessType  = "access"
	RefreshType = "refresh"
)

var ErrInvalidToken = errors.New("token: invalid token")

type Claims struct {
	UserID         string `json:"-"`
	SessionID      string `json:"sid"`
	RoleCode       string `json:"role"`
	OrganisationID string `json:"org"`
	TokenType      string `json:"typ"`
	jwt.RegisteredClaims
}

type Service struct {
	issuer         string
	accessSecret   []byte
	refreshSecret  []byte
	accessTTL      time.Duration
	refreshTTL     time.Duration
	refreshHashKey []byte
}

func NewService(issuer, accessSecret, refreshSecret, refreshHashSecret string, accessTTL, refreshTTL time.Duration) *Service {
	return &Service{
		issuer:         issuer,
		accessSecret:   []byte(accessSecret),
		refreshSecret:  []byte(refreshSecret),
		accessTTL:      accessTTL,
		refreshTTL:     refreshTTL,
		refreshHashKey: []byte(refreshHashSecret),
	}
}

func NewSessionID() string { return uuid.NewString() }

func (s *Service) Issue(userID, sessionID, roleCode, organisationID string, now time.Time) (access, refresh string, accessExpiry, refreshExpiry time.Time, err error) {
	if len(s.accessSecret) == 0 || len(s.refreshSecret) == 0 || len(s.refreshHashKey) == 0 {
		return "", "", time.Time{}, time.Time{}, errors.New("token secrets are not configured")
	}
	accessExpiry = now.Add(s.accessTTL)
	refreshExpiry = now.Add(s.refreshTTL)
	access, err = s.sign(Claims{
		UserID: userID, SessionID: sessionID, RoleCode: roleCode, OrganisationID: organisationID, TokenType: AccessType,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: issuerOrDefault(s.issuer), Subject: userID, ID: uuid.NewString(), IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(accessExpiry),
		},
	}, s.accessSecret)
	if err != nil {
		return "", "", time.Time{}, time.Time{}, err
	}
	refresh, err = s.sign(Claims{
		UserID: userID, SessionID: sessionID, RoleCode: roleCode, OrganisationID: organisationID, TokenType: RefreshType,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: issuerOrDefault(s.issuer), Subject: userID, ID: uuid.NewString(), IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(refreshExpiry),
		},
	}, s.refreshSecret)
	return access, refresh, accessExpiry, refreshExpiry, err
}

func (s *Service) ParseAccess(raw string) (*Claims, error) {
	return s.parse(raw, s.accessSecret, AccessType)
}

func (s *Service) ParseRefresh(raw string) (*Claims, error) {
	return s.parse(raw, s.refreshSecret, RefreshType)
}

func (s *Service) HashRefresh(raw string) string {
	h := hmac.New(sha256.New, s.refreshHashKey)
	_, _ = h.Write([]byte(raw))
	return hex.EncodeToString(h.Sum(nil))
}

func (s *Service) sign(claims Claims, secret []byte) (string, error) {
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
}

func (s *Service) parse(raw string, secret []byte, expectedType string) (*Claims, error) {
	if raw == "" || len(secret) == 0 {
		return nil, ErrInvalidToken
	}
	claims := &Claims{}
	tok, err := jwt.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method: %s", t.Method.Alg())
		}
		return secret, nil
	}, jwt.WithIssuer(issuerOrDefault(s.issuer)))
	if err != nil || !tok.Valid || claims.TokenType != expectedType {
		return nil, ErrInvalidToken
	}
	claims.UserID = claims.Subject
	if claims.UserID == "" || claims.SessionID == "" {
		return nil, ErrInvalidToken
	}
	return claims, nil
}

func issuerOrDefault(issuer string) string {
	if issuer == "" {
		return "ewaste-workflow-api"
	}
	return issuer
}
