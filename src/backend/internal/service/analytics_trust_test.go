package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"workflow-api/internal/dto"
	"workflow-api/internal/model"
)

const analyticsTrustBatchID = "batch-receipt-001"

type analyticsTamper struct {
	name   string
	change func(batchID *string, request *dto.AnalyticsAcknowledgement, metadata *BatchCommandMetadata)
	want   error
}

// assertNoAnalyticsWrites checks that a rejected completion left the fixture
// exactly as RecordTreatment created it: one command, audit and outbox row from
// the treatment, and no result, anomaly, completion event or state change.
func assertNoAnalyticsWrites(t *testing.T, repo *fakeBatchRepository) {
	t.Helper()
	batch := repo.state.batches[analyticsTrustBatchID]
	if batch.Status != model.BatchStatusRecycled || batch.Version != 7 {
		t.Errorf("rejected completion changed the batch: status=%s version=%d", batch.Status, batch.Version)
	}
	if len(repo.state.analytics) != 0 || len(repo.state.anomalies) != 0 {
		t.Errorf("rejected completion wrote results: analytics=%d anomalies=%d", len(repo.state.analytics), len(repo.state.anomalies))
	}
	if len(repo.state.commands) != 1 || len(repo.state.audits) != 1 || len(repo.state.outbox) != 1 {
		t.Errorf("rejected completion wrote command, audit or event rows: commands=%d audits=%d outbox=%d", len(repo.state.commands), len(repo.state.audits), len(repo.state.outbox))
	}
}

func runAnalyticsTamperCases(t *testing.T, cases []analyticsTamper) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service, repo := analyticsFixture(t)
			request, metadata := analyticsRequest(repo, "analytics-run-trust", "analytics-command-trust", 7)
			batchID := analyticsTrustBatchID
			tc.change(&batchID, &request, &metadata)

			result, err := service.AcknowledgeAnalytics(context.Background(), batchID, request, metadata)

			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if result.Data.Status != "" || result.EventID != "" {
				t.Errorf("rejected completion returned a result: %+v", result)
			}
			assertNoAnalyticsWrites(t, repo)
		})
	}
}

func TestAnalyticsTrustRejectsCallersWithoutServiceIdentity(t *testing.T) {
	runAnalyticsTamperCases(t, []analyticsTamper{
		{"user scope instead of service", func(_ *string, _ *dto.AnalyticsAcknowledgement, m *BatchCommandMetadata) {
			m.ActorScope = "user:admin-001"
		}, ErrBatchForbidden},
		{"user actor without service scope", func(_ *string, _ *dto.AnalyticsAcknowledgement, m *BatchCommandMetadata) {
			m.ActorScope = ""
			m.Actor = BatchActor{UserID: "admin-001", OrganisationID: "platform-001", RoleCode: "SYSTEM_ADMIN"}
		}, ErrBatchForbidden},
		{"blank service principal", func(_ *string, _ *dto.AnalyticsAcknowledgement, m *BatchCommandMetadata) {
			m.ActorScope = "service: "
		}, ErrBatchForbidden},
		{"missing correlation id", func(_ *string, _ *dto.AnalyticsAcknowledgement, m *BatchCommandMetadata) {
			m.CorrelationID = " "
		}, ErrBatchValidation},
		{"oversized idempotency key", func(_ *string, _ *dto.AnalyticsAcknowledgement, m *BatchCommandMetadata) {
			m.IdempotencyKey = strings.Repeat("k", 65)
		}, ErrBatchValidation},
	})
}

func TestAnalyticsTrustRejectsTamperedIdentifiers(t *testing.T) {
	runAnalyticsTamperCases(t, []analyticsTamper{
		{"result submitted against another batch", func(b *string, _ *dto.AnalyticsAcknowledgement, _ *BatchCommandMetadata) {
			*b = "batch-other-001"
		}, ErrBatchNotFound},
		{"unknown source event", func(_ *string, r *dto.AnalyticsAcknowledgement, _ *BatchCommandMetadata) {
			r.SourceEventID = "event-does-not-exist"
		}, ErrBatchNotFound},
		{"empty source event", func(_ *string, r *dto.AnalyticsAcknowledgement, _ *BatchCommandMetadata) {
			r.SourceEventID = " "
		}, ErrBatchValidation},
		{"later source version", func(_ *string, r *dto.AnalyticsAcknowledgement, _ *BatchCommandMetadata) {
			r.SourceEventVersion = 8
		}, ErrBatchValidation},
		{"earlier source version", func(_ *string, r *dto.AnalyticsAcknowledgement, _ *BatchCommandMetadata) {
			r.SourceEventVersion = 6
		}, ErrBatchValidation},
		{"zero source version", func(_ *string, r *dto.AnalyticsAcknowledgement, _ *BatchCommandMetadata) {
			r.SourceEventVersion = 0
		}, ErrBatchValidation},
		{"empty run id", func(_ *string, r *dto.AnalyticsAcknowledgement, _ *BatchCommandMetadata) {
			r.AnalyticsRunID = ""
		}, ErrBatchValidation},
		{"input hash of other content", func(_ *string, r *dto.AnalyticsAcknowledgement, _ *BatchCommandMetadata) {
			r.InputHash = strings.Repeat("a", 64)
		}, ErrBatchValidation},
		{"upper-case input hash", func(_ *string, r *dto.AnalyticsAcknowledgement, _ *BatchCommandMetadata) {
			r.InputHash = strings.ToUpper(r.InputHash)
		}, ErrBatchValidation},
		{"truncated input hash", func(_ *string, r *dto.AnalyticsAcknowledgement, _ *BatchCommandMetadata) {
			r.InputHash = r.InputHash[:32]
		}, ErrBatchValidation},
		{"expected version zero", func(_ *string, _ *dto.AnalyticsAcknowledgement, m *BatchCommandMetadata) {
			m.ExpectedVersion = 0
		}, ErrBatchStaleVersion},
		{"expected version ahead of batch", func(_ *string, _ *dto.AnalyticsAcknowledgement, m *BatchCommandMetadata) {
			m.ExpectedVersion = 8
		}, ErrBatchStaleVersion},
	})
}

func TestAnalyticsTrustRejectsUnsupportedRuleVersions(t *testing.T) {
	var cases []analyticsTamper
	for name, version := range map[string]string{
		"next version":      "d3-v2",
		"previous version":  "d3-v0",
		"different casing":  "D3-V1",
		"padded":            " d3-v1 ",
		"empty":             "",
		"over length limit": strings.Repeat("v", 65),
	} {
		cases = append(cases, analyticsTamper{name, func(_ *string, r *dto.AnalyticsAcknowledgement, _ *BatchCommandMetadata) {
			r.RuleVersion = version
		}, ErrBatchValidation})
	}
	runAnalyticsTamperCases(t, cases)
}

func TestAnalyticsTrustRejectsMalformedOrAlteredResults(t *testing.T) {
	metric := func(change func(*dto.AnalyticsMetrics)) func(*string, *dto.AnalyticsAcknowledgement, *BatchCommandMetadata) {
		return func(_ *string, r *dto.AnalyticsAcknowledgement, _ *BatchCommandMetadata) { change(&r.Metrics) }
	}
	runAnalyticsTamperCases(t, []analyticsTamper{
		// Malformed values.
		{"three decimal places", metric(func(m *dto.AnalyticsMetrics) { m.ActualWeightKg = stringPointer("10.500") }), ErrBatchValidation},
		{"negative weight", metric(func(m *dto.AnalyticsMetrics) { m.ActualWeightKg = stringPointer("-10.50") }), ErrBatchValidation},
		{"exponent notation", metric(func(m *dto.AnalyticsMetrics) { m.UnknownKg = stringPointer("1e3") }), ErrBatchValidation},
		{"non-numeric weight", metric(func(m *dto.AnalyticsMetrics) { m.DeclaredWeightKg = stringPointer("11.00; DROP TABLE") }), ErrBatchValidation},
		{"negative item count", metric(func(m *dto.AnalyticsMetrics) { m.ActualItemCount = new(-1) }), ErrBatchValidation},
		{"unknown data quality", func(_ *string, r *dto.AnalyticsAcknowledgement, _ *BatchCommandMetadata) {
			r.DataQuality = "GOOD"
		}, ErrBatchValidation},
		{"unsupported anomaly code", func(_ *string, r *dto.AnalyticsAcknowledgement, _ *BatchCommandMetadata) {
			r.AnomalyCodes = []string{"NOT_A_REAL_CODE"}
		}, ErrBatchValidation},
		{"duplicate anomaly code", func(_ *string, r *dto.AnalyticsAcknowledgement, _ *BatchCommandMetadata) {
			r.AnomalyCodes = []string{string(model.AnomalyMissingOutcome), string(model.AnomalyMissingOutcome)}
		}, ErrBatchValidation},
		{"null anomaly codes", func(_ *string, r *dto.AnalyticsAcknowledgement, _ *BatchCommandMetadata) {
			r.AnomalyCodes = nil
		}, ErrBatchValidation},

		// Well-formed values that disagree with the frozen input.
		{"data quality upgraded to complete", func(_ *string, r *dto.AnalyticsAcknowledgement, _ *BatchCommandMetadata) {
			r.DataQuality = string(model.AnalyticsDataQualityComplete)
		}, ErrBatchValidation},
		{"actual weight altered", metric(func(m *dto.AnalyticsMetrics) { m.ActualWeightKg = stringPointer("99.00") }), ErrBatchValidation},
		{"declared weight altered", metric(func(m *dto.AnalyticsMetrics) { m.DeclaredWeightKg = stringPointer("10.50") }), ErrBatchValidation},
		{"unknown weight hidden", metric(func(m *dto.AnalyticsMetrics) { m.UnknownKg = stringPointer("0.00") }), ErrBatchValidation},
		{"recycled weight invented", metric(func(m *dto.AnalyticsMetrics) { m.RecycledKg = stringPointer("10.50") }), ErrBatchValidation},
		{"diverted weight invented", metric(func(m *dto.AnalyticsMetrics) { m.DivertedKg = stringPointer("10.50") }), ErrBatchValidation},
		{"declared quantity altered", metric(func(m *dto.AnalyticsMetrics) { m.DeclaredQuantity = new(10) }), ErrBatchValidation},
		{"item count altered", metric(func(m *dto.AnalyticsMetrics) { m.ActualItemCount = new(12) }), ErrBatchValidation},
		{"category match flipped", metric(func(m *dto.AnalyticsMetrics) { m.CategoryMatch = new(false) }), ErrBatchValidation},
		{"category match omitted", metric(func(m *dto.AnalyticsMetrics) { m.CategoryMatch = nil }), ErrBatchValidation},
		{"weight delta zeroed", metric(func(m *dto.AnalyticsMetrics) { m.WeightDeltaKg = stringPointer("0.00") }), ErrBatchValidation},
		{"count delta zeroed", metric(func(m *dto.AnalyticsMetrics) { m.CountDelta = new(0) }), ErrBatchValidation},
	})
}

// Anomaly codes must match the D3 flags the service derives from the frozen
// input, so a completion cannot drop a required flag or invent one. This was
// reported under EWCSB-173 and fixed in PR #84.
func TestAnalyticsTrustRejectsAnomalyCodesThatContradictFrozenInput(t *testing.T) {
	runAnalyticsTamperCases(t, []analyticsTamper{
		{"required flag omitted", func(_ *string, r *dto.AnalyticsAcknowledgement, _ *BatchCommandMetadata) {
			r.AnomalyCodes = []string{}
		}, ErrBatchValidation},
		{"flag invented", func(_ *string, r *dto.AnalyticsAcknowledgement, _ *BatchCommandMetadata) {
			r.AnomalyCodes = []string{string(model.AnomalyCategoryMismatch), string(model.AnomalyMissingOutcome)}
		}, ErrBatchValidation},
	})
}

func TestAnalyticsTrustRejectionDoesNotBlockTheValidResult(t *testing.T) {
	service, repo := analyticsFixture(t)
	tampered, metadata := analyticsRequest(repo, "analytics-run-trust", "analytics-command-trust", 7)
	tampered.Metrics.UnknownKg = stringPointer("0.00")
	if _, err := service.AcknowledgeAnalytics(context.Background(), analyticsTrustBatchID, tampered, metadata); !errors.Is(err, ErrBatchValidation) {
		t.Fatalf("expected tampered result to be rejected, got %v", err)
	}
	assertNoAnalyticsWrites(t, repo)

	valid, metadata := analyticsRequest(repo, "analytics-run-trust", "analytics-command-trust", 7)
	result, err := service.AcknowledgeAnalytics(context.Background(), analyticsTrustBatchID, valid, metadata)
	if err != nil {
		t.Fatalf("valid result after a rejected attempt returned error: %v", err)
	}
	if result.Data.Status != string(model.BatchStatusCompleted) || len(repo.state.analytics) != 1 {
		t.Fatalf("valid result did not complete the batch: %+v analytics=%d", result, len(repo.state.analytics))
	}
}
