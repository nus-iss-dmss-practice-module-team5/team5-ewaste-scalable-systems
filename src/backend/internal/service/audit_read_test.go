package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"workflow-api/internal/model"
	"workflow-api/internal/repository"
)

type auditReadRepositoryStub struct {
	timeline     []*model.BatchAuditEvent
	anomalies    []*model.BatchAnomaly
	impact       []*model.ImpactReadResult
	totals       *model.ImpactTotals
	listFilter   repository.ImpactFilter
	totalsFilter repository.ImpactFilter
}

func (s *auditReadRepositoryStub) FindAuditTimeline(context.Context, string, repository.WorkflowReadScope, repository.WorkflowReadPage) ([]*model.BatchAuditEvent, int64, error) {
	return s.timeline, int64(len(s.timeline)), nil
}

func (s *auditReadRepositoryStub) FindAuditAnomalies(context.Context, string, repository.WorkflowReadScope, repository.WorkflowReadPage) ([]*model.BatchAnomaly, int64, error) {
	return s.anomalies, int64(len(s.anomalies)), nil
}

func (s *auditReadRepositoryStub) ListImpactResults(_ context.Context, _ repository.WorkflowReadScope, filter repository.ImpactFilter) ([]*model.ImpactReadResult, error) {
	s.listFilter = filter
	return s.impact, nil
}

func (s *auditReadRepositoryStub) SummariseImpact(_ context.Context, _ repository.WorkflowReadScope, filter repository.ImpactFilter) (*model.ImpactTotals, error) {
	s.totalsFilter = filter
	return s.totals, nil
}

func TestAuditorReadServiceExposesTimelineAnomaliesAndImpact(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	actor := WorkflowReadActor{UserID: "auditor-1", RoleCode: "AUDITOR"}
	service := NewAuditorReadService(&auditReadRepositoryStub{
		timeline: []*model.BatchAuditEvent{{
			ID: "audit-1", BatchID: "batch-1", CommandID: "command-1", EventType: model.BatchAuditEventAnalyticsCompleted,
			FromStatus: model.BatchStatusRecycled, ToStatus: model.BatchStatusCompleted, BatchVersion: 9,
			SequenceInCommand: 1, OccurredAt: now, CorrelationID: "corr-1", DetailsJSON: []byte(`{"rule_version":"d3-v1"}`),
		}},
		anomalies: []*model.BatchAnomaly{{AnomalyID: "anomaly-1", BatchID: "batch-1", ResultID: "result-1", Code: model.AnomalyMissingOutcome, DetectedAt: now}},
		impact: []*model.ImpactReadResult{{
			ResultID: "result-1", BatchID: "batch-1", SourceEventID: "event-1", SourceEventVersion: 8,
			ReceiptID: "receipt-1", ReceiptVersion: 1, TreatmentID: "treatment-1", TreatmentVersion: 1,
			RuleVersion: "d3-v1", InputHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			DataQuality: model.AnalyticsDataQualityMissing, MetricsJSON: []byte(`{"declared_weight_kg":"11.00"}`),
			AnomalyCodes: []string{string(model.AnomalyMissingOutcome)}, AcknowledgedAt: now,
		}},
	})

	timeline, err := service.Timeline(context.Background(), "batch-1", actor, WorkflowReadPage{Page: 1, PageSize: 100})
	if err != nil || timeline.TotalCount != 1 || len(timeline.Data) != 1 || timeline.Data[0].ToStatus != string(model.BatchStatusCompleted) {
		t.Fatalf("unexpected timeline: %v %+v", err, timeline)
	}
	anomalies, err := service.Anomalies(context.Background(), "batch-1", actor, WorkflowReadPage{Page: 1, PageSize: 100})
	if err != nil || anomalies.TotalCount != 1 || len(anomalies.Data) != 1 || anomalies.Data[0].Code != string(model.AnomalyMissingOutcome) {
		t.Fatalf("unexpected anomalies: %v %+v", err, anomalies)
	}
	impact, err := service.Impact(context.Background(), actor, ImpactQuery{})
	if err != nil || impact.TotalCount != 1 || len(impact.Items) != 1 || impact.Items[0].DataQuality != string(model.AnalyticsDataQualityMissing) {
		t.Fatalf("unexpected impact: %v %+v", err, impact)
	}
}

func TestAuditorReadServiceRejectsNonAuditor(t *testing.T) {
	service := NewAuditorReadService(&auditReadRepositoryStub{})
	if _, err := service.Timeline(context.Background(), "batch-1", WorkflowReadActor{UserID: "user-1", RoleCode: "RECYCLER"}, WorkflowReadPage{Page: 1, PageSize: 100}); !errors.Is(err, ErrWorkflowReadForbidden) {
		t.Fatalf("timeline error = %v, want forbidden", err)
	}
	if _, err := service.Impact(context.Background(), WorkflowReadActor{UserID: "user-1", RoleCode: "SYSTEM_ADMIN"}, ImpactQuery{}); !errors.Is(err, ErrWorkflowReadForbidden) {
		t.Fatalf("impact error = %v, want forbidden", err)
	}
}

func TestImpactReturnsTotalsAndEchoesTheFilter(t *testing.T) {
	repo := &auditReadRepositoryStub{totals: &model.ImpactTotals{
		CompletedBatchCount: 3, CompleteBatchCount: 1, PartialBatchCount: 1, MissingBatchCount: 1,
		ReceivedKg: new("36.00"), ReusedKg: new("4.00"), RecycledKg: new("17.00"), DisposedKg: new("2.00"),
		DivertedKg: new("21.00"), UnknownKg: new("13.00"), RuleVersions: []string{"analytics-impact-v1"},
	}}
	actor := WorkflowReadActor{UserID: "auditor-1", RoleCode: "AUDITOR"}

	impact, err := NewAuditorReadService(repo).Impact(context.Background(), actor, ImpactQuery{
		CompletedFrom: " 2026-10-01 ", CompletedTo: "2026-10-07", Category: "batteries", ProcessingOrgID: " PROC-001 ",
	})

	if err != nil {
		t.Fatalf("impact: %v", err)
	}
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	before := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	for name, filter := range map[string]repository.ImpactFilter{"totals": repo.totalsFilter, "list": repo.listFilter} {
		if filter.CompletedFrom == nil || !filter.CompletedFrom.Equal(from) || filter.CompletedBefore == nil || !filter.CompletedBefore.Equal(before) {
			t.Fatalf("%s window = %v .. %v, want the whole of 1 to 7 October", name, filter.CompletedFrom, filter.CompletedBefore)
		}
		if filter.Category != "BATTERIES" || filter.ProcessingOrgID != "PROC-001" {
			t.Fatalf("%s filter = %+v", name, filter)
		}
	}
	if *impact.Filter.CompletedFrom != "2026-10-01" || *impact.Filter.CompletedTo != "2026-10-07" || *impact.Filter.Category != "BATTERIES" || *impact.Filter.ProcessingOrgID != "PROC-001" {
		t.Fatalf("filter echo = %+v", impact.Filter)
	}
	totals := impact.Totals
	if totals.CompletedBatchCount != 3 || totals.CompleteBatchCount != 1 || totals.PartialBatchCount != 1 || totals.MissingOutcomeBatchCount != 1 {
		t.Fatalf("counts = %+v", totals)
	}
	if *totals.ReceivedKg != "36.00" || *totals.DivertedKg != "21.00" || *totals.UnknownKg != "13.00" || *totals.DisposedKg != "2.00" || len(totals.RuleVersions) != 1 {
		t.Fatalf("weights = %+v", totals)
	}
}

func TestImpactWithNoFilterEchoesNullsAndKeepsUnknownWeightsNull(t *testing.T) {
	repo := &auditReadRepositoryStub{totals: &model.ImpactTotals{CompletedBatchCount: 1, MissingBatchCount: 1, ReceivedKg: new("12.00"), UnknownKg: new("12.00")}}

	impact, err := NewAuditorReadService(repo).Impact(context.Background(), WorkflowReadActor{UserID: "auditor-1", RoleCode: "AUDITOR"}, ImpactQuery{})

	if err != nil {
		t.Fatalf("impact: %v", err)
	}
	if repo.totalsFilter != (repository.ImpactFilter{}) {
		t.Fatalf("an empty query produced a filter: %+v", repo.totalsFilter)
	}
	encoded, err := json.Marshal(impact)
	if err != nil {
		t.Fatalf("marshal impact: %v", err)
	}
	var raw struct {
		Filter map[string]any `json:"filter"`
		Totals map[string]any `json:"totals"`
	}
	_ = json.Unmarshal(encoded, &raw)
	for _, key := range []string{"completed_from", "completed_to", "category", "processing_org_id"} {
		if value, present := raw.Filter[key]; !present || value != nil {
			t.Fatalf("filter.%s = %v (present %v), want an explicit null", key, value, present)
		}
	}
	for _, key := range []string{"reused_kg", "recycled_kg", "disposed_kg", "diverted_kg"} {
		if value, present := raw.Totals[key]; !present || value != nil {
			t.Fatalf("totals.%s = %v (present %v), want null for an unrecorded outcome, not zero", key, value, present)
		}
	}
	if raw.Totals["unknown_kg"] != "12.00" || raw.Totals["missing_outcome_batch_count"] != float64(1) {
		t.Fatalf("totals = %v", raw.Totals)
	}
	if versions, ok := raw.Totals["rule_versions"].([]any); !ok || len(versions) != 0 {
		t.Fatalf("rule_versions = %v, want an empty list", raw.Totals["rule_versions"])
	}
}

func TestImpactAcceptsAnInstantWindowInclusiveOfItsEnd(t *testing.T) {
	repo := &auditReadRepositoryStub{}
	_, err := NewAuditorReadService(repo).Impact(context.Background(), WorkflowReadActor{UserID: "auditor-1", RoleCode: "AUDITOR"}, ImpactQuery{
		CompletedFrom: "2026-10-07T08:00:00+08:00", CompletedTo: "2026-10-07T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("impact: %v", err)
	}
	instant := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	if !repo.totalsFilter.CompletedFrom.Equal(instant) || !repo.totalsFilter.CompletedBefore.After(instant) || repo.totalsFilter.CompletedBefore.Sub(instant) > time.Microsecond {
		t.Fatalf("window = %v .. %v, want exactly that instant", repo.totalsFilter.CompletedFrom, repo.totalsFilter.CompletedBefore)
	}
}

func TestImpactRejectsInvalidFilters(t *testing.T) {
	service := NewAuditorReadService(&auditReadRepositoryStub{})
	actor := WorkflowReadActor{UserID: "auditor-1", RoleCode: "AUDITOR"}
	for name, query := range map[string]ImpactQuery{
		"unparseable from":  {CompletedFrom: "yesterday"},
		"unparseable to":    {CompletedTo: "07/10/2026"},
		"from after to":     {CompletedFrom: "2026-10-08", CompletedTo: "2026-10-07"},
		"unknown category":  {Category: "LAPTOPS"},
		"overlong facility": {ProcessingOrgID: "PROC-000000000000000000000000000001"},
	} {
		if _, err := service.Impact(context.Background(), actor, query); !errors.Is(err, ErrWorkflowReadInvalid) {
			t.Fatalf("%s: error = %v, want invalid", name, err)
		}
	}
}

func TestImpactChecksTheRoleBeforeTheFilter(t *testing.T) {
	_, err := NewAuditorReadService(&auditReadRepositoryStub{}).Impact(context.Background(), WorkflowReadActor{UserID: "user-1", RoleCode: "RECYCLER"}, ImpactQuery{Category: "LAPTOPS"})
	if !errors.Is(err, ErrWorkflowReadForbidden) {
		t.Fatalf("error = %v, want forbidden", err)
	}
}
