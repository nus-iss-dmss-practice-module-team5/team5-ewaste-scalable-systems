package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"workflow-api/internal/middleware"
	"workflow-api/internal/model"
	"workflow-api/internal/repository"
	"workflow-api/internal/service"
	"workflow-api/internal/token"
)

type impactRepositoryStub struct {
	repository.AuditorReadRepository
	filter repository.ImpactFilter
}

func (s *impactRepositoryStub) SummariseImpact(_ context.Context, _ repository.WorkflowReadScope, filter repository.ImpactFilter) (*model.ImpactTotals, error) {
	s.filter = filter
	return &model.ImpactTotals{
		CompletedBatchCount: 2, CompleteBatchCount: 1, MissingBatchCount: 1,
		ReceivedKg: new("24.00"), ReusedKg: new("2.00"), RecycledKg: new("9.00"), DisposedKg: new("1.00"),
		DivertedKg: new("11.00"), UnknownKg: new("12.00"), RuleVersions: []string{"analytics-impact-v1"},
	}, nil
}

func (s *impactRepositoryStub) ListImpactResults(context.Context, repository.WorkflowReadScope, repository.ImpactFilter) ([]*model.ImpactReadResult, error) {
	return nil, nil
}

func impactRequest(repo *impactRepositoryStub, role, query string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.CorrelationID(), func(c *gin.Context) {
		c.Set(middleware.ClaimsKey, &token.Claims{UserID: "USR-002", RoleCode: role, OrganisationID: "PLATFORM"})
	})
	reads := NewWorkflowReadController(nil, nil, service.NewAuditorReadService(repo))
	r.GET("/api/v1/audit/impact", reads.GetAuditImpact)
	recorder := httptest.NewRecorder()
	r.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/audit/impact"+query, nil))
	return recorder
}

func TestGetAuditImpactReturnsTheFilterTotalsAndItems(t *testing.T) {
	repo := &impactRepositoryStub{}

	recorder := impactRequest(repo, "AUDITOR", "?completed_from=2026-10-01&completed_to=2026-10-07&category=BATTERIES&processing_org_id=PROC-001")

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if repo.filter.Category != "BATTERIES" || repo.filter.ProcessingOrgID != "PROC-001" || repo.filter.CompletedFrom == nil || repo.filter.CompletedBefore == nil {
		t.Fatalf("query parameters did not reach the repository: %+v", repo.filter)
	}
	var body struct {
		Data struct {
			Filter     map[string]any `json:"filter"`
			Totals     map[string]any `json:"totals"`
			Items      []any          `json:"items"`
			TotalCount int            `json:"total_count"`
		} `json:"data"`
		CorrelationID string `json:"correlation_id"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Data.Filter["completed_from"] != "2026-10-01" || body.Data.Filter["completed_to"] != "2026-10-07" || body.Data.Filter["category"] != "BATTERIES" || body.Data.Filter["processing_org_id"] != "PROC-001" {
		t.Fatalf("filter echo = %v", body.Data.Filter)
	}
	want := map[string]any{
		"completed_batch_count": float64(2), "complete_batch_count": float64(1), "partial_batch_count": float64(0),
		"missing_outcome_batch_count": float64(1), "received_kg": "24.00", "reused_kg": "2.00", "recycled_kg": "9.00",
		"disposed_kg": "1.00", "diverted_kg": "11.00", "unknown_kg": "12.00",
	}
	for key, value := range want {
		if body.Data.Totals[key] != value {
			t.Fatalf("totals.%s = %v, want %v", key, body.Data.Totals[key], value)
		}
	}
	if body.Data.Items == nil || body.Data.TotalCount != 0 || body.CorrelationID == "" {
		t.Fatalf("unexpected envelope: %s", recorder.Body.String())
	}
}

func TestGetAuditImpactRejectsBadFiltersAndOtherRoles(t *testing.T) {
	cases := []struct {
		name, role, query string
		status            int
		code              string
	}{
		{"unknown category", "AUDITOR", "?category=LAPTOPS", http.StatusBadRequest, "INVALID_REQUEST"},
		{"unparseable date", "AUDITOR", "?completed_from=yesterday", http.StatusBadRequest, "INVALID_REQUEST"},
		{"window ends before it starts", "AUDITOR", "?completed_from=2026-10-08&completed_to=2026-10-07", http.StatusBadRequest, "INVALID_REQUEST"},
		{"recycler", "RECYCLER", "", http.StatusForbidden, "FORBIDDEN"},
		{"system admin", "SYSTEM_ADMIN", "", http.StatusForbidden, "FORBIDDEN"},
	}
	for _, tc := range cases {
		recorder := impactRequest(&impactRepositoryStub{}, tc.role, tc.query)
		var body struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(recorder.Body.Bytes(), &body)
		if recorder.Code != tc.status || body.Code != tc.code {
			t.Fatalf("%s: status = %d code = %q, want %d %q (body %s)", tc.name, recorder.Code, body.Code, tc.status, tc.code, recorder.Body.String())
		}
	}
}
