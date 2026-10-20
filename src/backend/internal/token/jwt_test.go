package token

import (
	"testing"
	"time"
)

func TestIssueAndParseTokens(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	service := NewService("test-issuer", "access-secret", "refresh-secret", "hash-secret", 15*time.Minute, 24*time.Hour)

	access, refresh, accessExpiry, refreshExpiry, err := service.Issue("USR-001", "session-001", "AUDITOR", "PLATFORM", now)
	if err != nil {
		t.Fatalf("issue tokens: %v", err)
	}
	if access == refresh || access == "" || refresh == "" {
		t.Fatal("expected two different non-empty tokens")
	}
	if !accessExpiry.Equal(now.Add(15*time.Minute)) || !refreshExpiry.Equal(now.Add(24*time.Hour)) {
		t.Fatal("unexpected token expiry")
	}

	accessClaims, err := service.ParseAccess(access)
	if err != nil {
		t.Fatalf("parse access token: %v", err)
	}
	if accessClaims.UserID != "USR-001" || accessClaims.SessionID != "session-001" || accessClaims.TokenType != AccessType {
		t.Fatalf("unexpected access claims: %+v", accessClaims)
	}
	if _, err := service.ParseAccess(refresh); err == nil {
		t.Fatal("refresh token must not be accepted as an access token")
	}

	refreshClaims, err := service.ParseRefresh(refresh)
	if err != nil {
		t.Fatalf("parse refresh token: %v", err)
	}
	if refreshClaims.UserID != "USR-001" || refreshClaims.TokenType != RefreshType {
		t.Fatalf("unexpected refresh claims: %+v", refreshClaims)
	}
	if service.HashRefresh(refresh) == refresh {
		t.Fatal("raw refresh token must not equal its stored hash")
	}
}
