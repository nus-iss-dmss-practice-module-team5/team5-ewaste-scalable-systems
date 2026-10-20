package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"workflow-api/internal/dto"
	"workflow-api/internal/middleware"
	"workflow-api/internal/service"
)

type preparationStub struct {
	calls int
	err   error
}

func (s *preparationStub) PrepareAnalytics(_ context.Context, batchID, eventID, _ string) (dto.AnalyticsPreparation, error) {
	s.calls++
	return dto.AnalyticsPreparation{BatchID: batchID, SourceEventID: eventID, RuleVersion: service.ApprovedAnalyticsRuleVersion}, s.err
}
func (*preparationStub) AcknowledgeAnalytics(context.Context, string, dto.AnalyticsAcknowledgement, service.BatchCommandMetadata) (dto.CompletionMutationResult, error) {
	panic("unexpected result call")
}

func TestAnalyticsPreparationHTTPAuthenticationAndErrors(t *testing.T) {
	for _, tc := range []struct {
		name, configured, bearer, query string
		err                             error
		status, calls                   int
	}{
		{"disabled", "", "test", "?source_event_id=event", nil, 503, 0},
		{"missing", "test", "", "?source_event_id=event", nil, 401, 0},
		{"wrong", "test", "wrong", "?source_event_id=event", nil, 401, 0},
		{"missing_source", "test", "test", "", nil, 400, 0},
		{"ready", "test", "test", "?source_event_id=event", nil, 200, 1},
		{"not_found", "test", "test", "?source_event_id=event", service.ErrBatchNotFound, 404, 1},
		{"wrong_state", "test", "test", "?source_event_id=event", service.ErrBatchInvalidState, 409, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &preparationStub{err: tc.err}
			h := NewAnalyticsController(stub, zap.NewNop())
			r := gin.New()
			r.GET("/api/v1/batches/:batch_id/analytics-input", middleware.RequireAnalyticsService(tc.configured), h.Prepare)
			req := httptest.NewRequest(http.MethodGet, "/api/v1/batches/batch/analytics-input"+tc.query, nil)
			if tc.bearer != "" {
				req.Header.Set("Authorization", "Bearer "+tc.bearer)
			}
			response := httptest.NewRecorder()
			r.ServeHTTP(response, req)
			if response.Code != tc.status || stub.calls != tc.calls {
				t.Fatalf("status=%d calls=%d", response.Code, stub.calls)
			}
			if tc.calls > 0 && (response.Header().Get("X-Analytics-Contract") != "processing-v1" || response.Header().Get("Cache-Control") != "no-store") {
				t.Fatal("missing authenticated contract/cache headers")
			}
		})
	}
}
