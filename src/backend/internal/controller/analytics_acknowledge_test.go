package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"workflow-api/internal/dto"
	"workflow-api/internal/middleware"
	"workflow-api/internal/service"
)

const (
	analyticsTestToken = "analytics-service-token-0123456789abcdef"
	analyticsTestBody  = `{"source_event_id":"event-001","source_event_version":1,"analytics_run_id":"run-001","input_hash":"hash-001","rule_version":"d3-v1","data_quality":"COMPLETE","metrics":{},"anomaly_codes":[]}`
)

type acknowledgeStub struct {
	calls    int
	err      error
	batchID  string
	request  dto.AnalyticsAcknowledgement
	metadata service.BatchCommandMetadata
}

func (*acknowledgeStub) PrepareAnalytics(context.Context, string, string, string) (dto.AnalyticsPreparation, error) {
	panic("unexpected preparation call")
}

func (s *acknowledgeStub) AcknowledgeAnalytics(_ context.Context, batchID string, request dto.AnalyticsAcknowledgement, metadata service.BatchCommandMetadata) (dto.CompletionMutationResult, error) {
	s.calls++
	s.batchID, s.request, s.metadata = batchID, request, metadata
	return dto.CompletionMutationResult{Data: dto.CompletionView{BatchID: batchID, Status: "COMPLETED"}, CorrelationID: metadata.CorrelationID}, s.err
}

type acknowledgeRequest struct {
	authorization  string
	idempotencyKey string
	version        string
	body           string
}

func validAcknowledgeRequest() acknowledgeRequest {
	return acknowledgeRequest{
		authorization: "Bearer " + analyticsTestToken, idempotencyKey: "analytics-v1:event-001", version: "7", body: analyticsTestBody,
	}
}

func serveAcknowledge(configuredToken string, stub *acknowledgeStub, logger *zap.Logger, in acknowledgeRequest) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.CorrelationID())
	r.POST("/api/v1/batches/:batch_id/analytics-results", middleware.RequireAnalyticsService(configuredToken), NewAnalyticsController(stub, logger).Acknowledge)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/batches/batch-001/analytics-results", strings.NewReader(in.body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Correlation-ID", "corr-analytics-001")
	if in.authorization != "" {
		req.Header.Set("Authorization", in.authorization)
	}
	if in.idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", in.idempotencyKey)
	}
	if in.version != "" {
		req.Header.Set("If-Match-Version", in.version)
	}
	recorder := httptest.NewRecorder()
	r.ServeHTTP(recorder, req)
	return recorder
}

func TestAnalyticsAcknowledgeRejectsUnauthorisedCallers(t *testing.T) {
	for _, tc := range []struct {
		name, configured, authorization string
		status                          int
	}{
		{"missing header", analyticsTestToken, "", http.StatusUnauthorized},
		{"wrong token", analyticsTestToken, "Bearer wrong-token", http.StatusUnauthorized},
		{"token with extra characters", analyticsTestToken, "Bearer " + analyticsTestToken + "x", http.StatusUnauthorized},
		{"truncated token", analyticsTestToken, "Bearer " + analyticsTestToken[:10], http.StatusUnauthorized},
		{"basic scheme", analyticsTestToken, "Basic " + analyticsTestToken, http.StatusUnauthorized},
		{"token without scheme", analyticsTestToken, analyticsTestToken, http.StatusUnauthorized},
		{"scheme without token", analyticsTestToken, "Bearer", http.StatusUnauthorized},
		{"extra header field", analyticsTestToken, "Bearer " + analyticsTestToken + " extra", http.StatusUnauthorized},
		{"user session token", analyticsTestToken, "Bearer eyJhbGciOiJIUzI1NiJ9.eyJyb2xlIjoiU1lTVEVNX0FETUlOIn0.signature", http.StatusUnauthorized},
		{"token not configured", "", "Bearer " + analyticsTestToken, http.StatusServiceUnavailable},
		{"blank configured token", "   ", "Bearer    ", http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &acknowledgeStub{}
			in := validAcknowledgeRequest()
			in.authorization = tc.authorization

			recorder := serveAcknowledge(tc.configured, stub, zap.NewNop(), in)

			if recorder.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, tc.status, recorder.Body.String())
			}
			if stub.calls != 0 {
				t.Fatal("unauthorised caller reached the completion workflow")
			}
		})
	}
}

func TestAnalyticsAcknowledgeRejectsMalformedRequestsBeforeWorkflow(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*acknowledgeRequest)
	}{
		{"missing idempotency key", func(in *acknowledgeRequest) { in.idempotencyKey = "" }},
		{"missing version", func(in *acknowledgeRequest) { in.version = "" }},
		{"zero version", func(in *acknowledgeRequest) { in.version = "0" }},
		{"negative version", func(in *acknowledgeRequest) { in.version = "-1" }},
		{"non-numeric version", func(in *acknowledgeRequest) { in.version = "latest" }},
		{"empty body", func(in *acknowledgeRequest) { in.body = "" }},
		{"truncated json", func(in *acknowledgeRequest) { in.body = analyticsTestBody[:40] }},
		{"json array", func(in *acknowledgeRequest) { in.body = "[]" }},
		{"wrong field type", func(in *acknowledgeRequest) { in.body = `{"source_event_version":"one"}` }},
		{"unknown field", func(in *acknowledgeRequest) {
			in.body = `{"source_event_id":"event-001","batch_status":"COMPLETED"}`
		}},
		{"trailing second document", func(in *acknowledgeRequest) { in.body = analyticsTestBody + analyticsTestBody }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &acknowledgeStub{}
			in := validAcknowledgeRequest()
			tc.change(&in)

			recorder := serveAcknowledge(analyticsTestToken, stub, zap.NewNop(), in)

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body.String())
			}
			if stub.calls != 0 {
				t.Fatal("malformed request reached the completion workflow")
			}
		})
	}
}

func TestAnalyticsAcknowledgePassesServiceIdentityNotClientClaims(t *testing.T) {
	stub := &acknowledgeStub{}

	recorder := serveAcknowledge(analyticsTestToken, stub, zap.NewNop(), validAcknowledgeRequest())

	if recorder.Code != http.StatusOK || stub.calls != 1 {
		t.Fatalf("status = %d calls = %d: %s", recorder.Code, stub.calls, recorder.Body.String())
	}
	if stub.batchID != "batch-001" || stub.request.SourceEventID != "event-001" || stub.request.RuleVersion != "d3-v1" {
		t.Fatalf("request was not passed through unchanged: batch=%q request=%+v", stub.batchID, stub.request)
	}
	want := service.BatchCommandMetadata{
		ActorScope: "service:analytics-worker", CommandName: service.AcknowledgeAnalyticsCommand,
		CorrelationID: "corr-analytics-001", IdempotencyKey: "analytics-v1:event-001", ExpectedVersion: 7,
	}
	if stub.metadata != want {
		t.Fatalf("metadata = %+v, want %+v", stub.metadata, want)
	}
}

func TestAnalyticsAcknowledgeErrorsAreSafeAndRedacted(t *testing.T) {
	const internalDetail = "dial tcp 10.0.3.7:3306: access denied for user 'ewaste_app' using password 'db-secret'"

	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"validation", service.NewBatchValidationError(map[string]string{"input_hash": "does not match the frozen RecyclingCompleted input"}), http.StatusUnprocessableEntity, "VALIDATION_ERROR"},
		{"forbidden", service.ErrBatchForbidden, http.StatusForbidden, ""},
		{"not found", service.ErrBatchNotFound, http.StatusNotFound, ""},
		{"stale version", service.ErrBatchStaleVersion, 0, ""},
		{"idempotency conflict", service.ErrBatchIdempotencyConflict, http.StatusConflict, ""},
		{"invalid state", service.ErrBatchInvalidState, http.StatusConflict, ""},
		{"internal failure", fmt.Errorf("analytics: save result: %w", errors.New(internalDetail)), http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core, logs := observer.New(zapcore.DebugLevel)
			stub := &acknowledgeStub{err: tc.err}

			recorder := serveAcknowledge(analyticsTestToken, stub, zap.New(core), validAcknowledgeRequest())

			if recorder.Code < 400 || (tc.status != 0 && recorder.Code != tc.status) {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, tc.status, recorder.Body.String())
			}

			var body map[string]any
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatalf("error response is not JSON: %v", err)
			}
			for field := range body {
				if field != "code" && field != "message" && field != "correlation_id" {
					t.Errorf("error response exposes unexpected field %q: %s", field, recorder.Body.String())
				}
			}
			if tc.code != "" && body["code"] != tc.code {
				t.Errorf("code = %v, want %s", body["code"], tc.code)
			}
			if body["correlation_id"] != "corr-analytics-001" {
				t.Errorf("correlation_id = %v, want the request correlation ID", body["correlation_id"])
			}

			// The response must not echo the credential, the submitted result,
			// or the text of the underlying error.
			for _, secret := range []string{analyticsTestToken, "db-secret", "10.0.3.7", "hash-001", "run-001", "frozen RecyclingCompleted"} {
				if strings.Contains(recorder.Body.String(), secret) {
					t.Errorf("error response leaks %q: %s", secret, recorder.Body.String())
				}
			}

			// Logs may describe the failure but never the credential or payload.
			for _, entry := range logs.All() {
				line := entry.Message + fmt.Sprint(entry.ContextMap())
				for _, secret := range []string{analyticsTestToken, "Bearer", analyticsTestBody, "hash-001", "run-001"} {
					if strings.Contains(line, secret) {
						t.Errorf("log entry leaks %q: %s", secret, line)
					}
				}
			}
			if logs.Len() != 1 {
				t.Errorf("expected one log entry for the rejected completion, got %d", logs.Len())
			}
		})
	}
}

func TestAnalyticsAcknowledgeRejectedAuthIsNotLoggedWithCredential(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	in := validAcknowledgeRequest()
	in.authorization = "Bearer attacker-supplied-token"

	recorder := serveAcknowledge(analyticsTestToken, &acknowledgeStub{}, zap.New(core), in)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "attacker-supplied-token") || strings.Contains(recorder.Body.String(), analyticsTestToken) {
		t.Fatalf("401 response echoes a credential: %s", recorder.Body.String())
	}
	for _, entry := range logs.All() {
		line := entry.Message + fmt.Sprint(entry.ContextMap())
		if strings.Contains(line, "attacker-supplied-token") || strings.Contains(line, analyticsTestToken) {
			t.Fatalf("log entry contains a credential: %s", line)
		}
	}
}
