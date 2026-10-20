package dto

import (
	"encoding/json"
	"testing"
)

func TestTokenResponseUsesAPIFieldNames(t *testing.T) {
	payload, err := json.Marshal(TokenResponse{
		AccessToken: "access", RefreshToken: "refresh", TokenType: "Bearer", ExpiresIn: 900, RefreshExpiresIn: 86400,
	})
	if err != nil {
		t.Fatalf("marshal token response: %v", err)
	}
	expected := `{"access_token":"access","refresh_token":"refresh","token_type":"Bearer","expires_in":900,"refresh_expires_in":86400}`
	if string(payload) != expected {
		t.Fatalf("expected %s, got %s", expected, payload)
	}
}

func TestErrorResponseUsesCorrelationIDField(t *testing.T) {
	payload, err := json.Marshal(ErrorResponse{Code: "INVALID_REQUEST", Message: "invalid request", CorrelationID: "corr-001"})
	if err != nil {
		t.Fatalf("marshal error response: %v", err)
	}
	if string(payload) != `{"code":"INVALID_REQUEST","message":"invalid request","correlation_id":"corr-001"}` {
		t.Fatalf("unexpected error response: %s", payload)
	}
}
