package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"workflow-api/internal/dto"
	"workflow-api/internal/model"
)

func TestBatchServiceAcknowledgeAnalyticsCompletesRecycledBatchAtomically(t *testing.T) {
	service, repo := analyticsFixture(t)
	request, metadata := analyticsRequest(repo, "analytics-run-001", "analytics-command-001", 7)

	result, err := service.AcknowledgeAnalytics(context.Background(), "batch-receipt-001", request, metadata)
	if err != nil {
		t.Fatalf("acknowledge analytics returned error: %v", err)
	}
	if result.Data.Status != string(model.BatchStatusCompleted) || result.Data.Version != 8 || result.EventState != string(model.OutboxPublishStatePending) {
		t.Fatalf("unexpected completion result: %+v", result)
	}
	if len(repo.state.analytics) != 1 || len(repo.state.anomalies) != 3 || len(repo.state.audits) != 2 || len(repo.state.outbox) != 2 {
		t.Fatalf("expected result, anomaly, audit and RequestCompleted writes: analytics=%d anomalies=%d audits=%d outbox=%d", len(repo.state.analytics), len(repo.state.anomalies), len(repo.state.audits), len(repo.state.outbox))
	}
	if repo.state.batches["batch-receipt-001"].Status != model.BatchStatusCompleted {
		t.Fatal("batch did not transition to COMPLETED")
	}
	completedEvent := repo.state.outbox[1]
	if completedEvent.EventType != model.RequestCompletedEventType || completedEvent.AggregateVersion != 8 || completedEvent.Topic != "ewaste.batch.events" {
		t.Fatalf("unexpected RequestCompleted outbox row: %+v", completedEvent)
	}
	var payload map[string]any
	if err := json.Unmarshal(completedEvent.PayloadJSON, &payload); err != nil {
		t.Fatalf("decode RequestCompleted payload: %v", err)
	}
	if payload["event_type"] != model.RequestCompletedEventType || payload["occurred_at"] == nil {
		t.Fatalf("unexpected RequestCompleted payload: %+v", payload)
	}
	if repo.state.audits[1].ServicePrincipal == nil || *repo.state.audits[1].ServicePrincipal != "analytics-worker" || repo.state.audits[1].ActorUserID != nil {
		t.Fatalf("analytics audit actor was not recorded as service identity: %+v", repo.state.audits[1])
	}
}

func TestBatchServiceAcknowledgeAnalyticsReplaysWithoutDuplicateDurableEffects(t *testing.T) {
	service, repo := analyticsFixture(t)
	request, metadata := analyticsRequest(repo, "analytics-run-replay", "analytics-command-replay", 7)
	first, err := service.AcknowledgeAnalytics(context.Background(), "batch-receipt-001", request, metadata)
	if err != nil {
		t.Fatalf("first acknowledgement returned error: %v", err)
	}
	second, err := service.AcknowledgeAnalytics(context.Background(), "batch-receipt-001", request, metadata)
	if err != nil {
		t.Fatalf("replay returned error: %v", err)
	}
	if second.EventState != "REPLAYED" || second.EventID != first.EventID || second.Data.AnalyticsResultID != first.Data.AnalyticsResultID {
		t.Fatalf("replay did not return the saved result: first=%+v second=%+v", first, second)
	}
	if len(repo.state.analytics) != 1 || len(repo.state.anomalies) != 3 || len(repo.state.audits) != 2 || len(repo.state.outbox) != 2 {
		t.Fatalf("replay created duplicate durable effects")
	}
}

func TestBatchServiceAcknowledgeAnalyticsRejectsAnomalyCodesThatContradictFrozenInput(t *testing.T) {
	cases := []struct {
		name  string
		codes []string
	}{
		{name: "missing required weight and count flags", codes: []string{string(model.AnomalyMissingOutcome)}},
		{name: "invented category flag", codes: []string{string(model.AnomalyCategoryMismatch), string(model.AnomalyCountMismatch), string(model.AnomalyWeightMismatch), string(model.AnomalyMissingOutcome)}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service, repo := analyticsFixture(t)
			request, metadata := analyticsRequest(repo, "analytics-run-anomaly-"+tc.name, "analytics-command-anomaly-"+tc.name, 7)
			request.AnomalyCodes = tc.codes

			if _, err := service.AcknowledgeAnalytics(context.Background(), "batch-receipt-001", request, metadata); !errors.Is(err, ErrBatchValidation) {
				t.Fatalf("expected anomaly-code validation error, got %v", err)
			}
			batch := repo.state.batches["batch-receipt-001"]
			if batch.Status != model.BatchStatusRecycled || len(repo.state.analytics) != 0 || len(repo.state.anomalies) != 0 || len(repo.state.audits) != 1 || len(repo.state.outbox) != 1 {
				t.Fatalf("invalid anomaly codes changed durable state: batch=%+v analytics=%d anomalies=%d audits=%d outbox=%d", batch, len(repo.state.analytics), len(repo.state.anomalies), len(repo.state.audits), len(repo.state.outbox))
			}
		})
	}
}

func TestBatchServiceAcknowledgeAnalyticsReplaysBySourceRunWithoutDuplicateEffects(t *testing.T) {
	service, repo := analyticsFixture(t)
	request, firstMetadata := analyticsRequest(repo, "analytics-run-source-replay", "analytics-command-source-1", 7)
	first, err := service.AcknowledgeAnalytics(context.Background(), "batch-receipt-001", request, firstMetadata)
	if err != nil {
		t.Fatalf("first acknowledgement returned error: %v", err)
	}
	_, secondMetadata := analyticsRequest(repo, "analytics-run-source-replay", "analytics-command-source-2", 7)
	second, err := service.AcknowledgeAnalytics(context.Background(), "batch-receipt-001", request, secondMetadata)
	if err != nil {
		t.Fatalf("source/run replay returned error: %v", err)
	}
	if second.EventState != "REPLAYED" || second.EventID != first.EventID || second.Data.AnalyticsResultID != first.Data.AnalyticsResultID {
		t.Fatalf("source/run replay did not return the saved result: first=%+v second=%+v", first, second)
	}
	if len(repo.state.commands) != 2 || len(repo.state.analytics) != 1 || len(repo.state.audits) != 2 || len(repo.state.outbox) != 2 {
		t.Fatalf("source/run replay created duplicate durable effects")
	}
}

func TestBatchServiceAcknowledgeAnalyticsRejectsReplayForAnotherBatchOrSourceVersion(t *testing.T) {
	service, repo := analyticsFixture(t)
	request, metadata := analyticsRequest(repo, "analytics-run-identity", "analytics-command-identity-1", 7)
	if _, err := service.AcknowledgeAnalytics(context.Background(), "batch-receipt-001", request, metadata); err != nil {
		t.Fatalf("first acknowledgement returned error: %v", err)
	}

	otherBatchMetadata := metadata
	otherBatchMetadata.IdempotencyKey = "analytics-command-identity-2"
	if _, err := service.AcknowledgeAnalytics(context.Background(), "another-batch", request, otherBatchMetadata); !errors.Is(err, ErrBatchIdempotencyConflict) {
		t.Fatalf("expected cross-batch replay conflict, got %v", err)
	}

	otherVersionMetadata := metadata
	otherVersionMetadata.IdempotencyKey = "analytics-command-identity-3"
	otherVersionRequest := request
	otherVersionRequest.SourceEventVersion++
	if _, err := service.AcknowledgeAnalytics(context.Background(), "batch-receipt-001", otherVersionRequest, otherVersionMetadata); !errors.Is(err, ErrBatchIdempotencyConflict) {
		t.Fatalf("expected source-version replay conflict, got %v", err)
	}

	if repo.state.batches["batch-receipt-001"].Status != model.BatchStatusCompleted || len(repo.state.analytics) != 1 || len(repo.state.outbox) != 2 {
		t.Fatalf("identity-conflict replay changed durable state: batch=%+v analytics=%d outbox=%d", repo.state.batches["batch-receipt-001"], len(repo.state.analytics), len(repo.state.outbox))
	}
}

func TestBatchServiceAcknowledgeAnalyticsRejectsUnapprovedRuleVersion(t *testing.T) {
	service, repo := analyticsFixture(t)
	request, metadata := analyticsRequest(repo, "analytics-run-unapproved", "analytics-command-unapproved", 7)
	request.RuleVersion = "not-an-approved-policy"

	if _, err := service.AcknowledgeAnalytics(context.Background(), "batch-receipt-001", request, metadata); !errors.Is(err, ErrBatchValidation) {
		t.Fatalf("expected unapproved rule version validation error, got %v", err)
	}
	if repo.state.batches["batch-receipt-001"].Status != model.BatchStatusRecycled || len(repo.state.analytics) != 0 || len(repo.state.outbox) != 1 {
		t.Fatalf("unapproved rule version changed durable state: batch=%+v analytics=%d outbox=%d", repo.state.batches["batch-receipt-001"], len(repo.state.analytics), len(repo.state.outbox))
	}
}

func TestBatchServiceAcknowledgeAnalyticsRejectsFrozenInputMismatchWithoutCompletion(t *testing.T) {
	service, repo := analyticsFixture(t)
	request, metadata := analyticsRequest(repo, "analytics-run-invalid", "analytics-command-invalid", 7)
	request.InputHash = "0000000000000000000000000000000000000000000000000000000000000000"

	_, err := service.AcknowledgeAnalytics(context.Background(), "batch-receipt-001", request, metadata)
	if !errors.Is(err, ErrBatchValidation) {
		t.Fatalf("expected validation error, got %v", err)
	}
	batch := repo.state.batches["batch-receipt-001"]
	if batch.Status != model.BatchStatusRecycled || batch.Version != 7 || len(repo.state.analytics) != 0 || len(repo.state.audits) != 1 || len(repo.state.outbox) != 1 {
		t.Fatalf("invalid acknowledgement changed durable state: batch=%+v analytics=%d audits=%d outbox=%d", batch, len(repo.state.analytics), len(repo.state.audits), len(repo.state.outbox))
	}
}

func TestBatchServiceAcknowledgeAnalyticsRollsBackWhenOutboxFails(t *testing.T) {
	service, repo := analyticsFixture(t)
	repo.failOutbox = true
	request, metadata := analyticsRequest(repo, "analytics-run-rollback", "analytics-command-rollback", 7)

	if _, err := service.AcknowledgeAnalytics(context.Background(), "batch-receipt-001", request, metadata); err == nil {
		t.Fatal("expected outbox failure")
	}
	batch := repo.state.batches["batch-receipt-001"]
	if batch.Status != model.BatchStatusRecycled || batch.Version != 7 || len(repo.state.analytics) != 0 || len(repo.state.anomalies) != 0 || len(repo.state.commands) != 1 || len(repo.state.audits) != 1 || len(repo.state.outbox) != 1 {
		t.Fatalf("outbox failure left partial analytics writes: batch=%+v analytics=%d anomalies=%d commands=%d audits=%d outbox=%d", batch, len(repo.state.analytics), len(repo.state.anomalies), len(repo.state.commands), len(repo.state.audits), len(repo.state.outbox))
	}
}

func TestBatchServiceAcknowledgeAnalyticsRejectsStaleVersion(t *testing.T) {
	service, repo := analyticsFixture(t)
	request, metadata := analyticsRequest(repo, "analytics-run-stale", "analytics-command-stale", 6)

	_, err := service.AcknowledgeAnalytics(context.Background(), "batch-receipt-001", request, metadata)
	if !errors.Is(err, ErrBatchStaleVersion) {
		t.Fatalf("expected stale version, got %v", err)
	}
	if repo.state.batches["batch-receipt-001"].Status != model.BatchStatusRecycled || len(repo.state.analytics) != 0 {
		t.Fatal("stale acknowledgement changed state")
	}
}

func analyticsFixture(t *testing.T) (*BatchService, *fakeBatchRepository) {
	t.Helper()
	service, repo, _ := testBatchService()
	installTreatmentFixture(repo)
	if _, err := service.RecordTreatment(context.Background(), "batch-receipt-001", dto.TreatmentRequest{}, treatmentMetadata("treatment-for-analytics", "corr-treatment-for-analytics", 6)); err != nil {
		t.Fatalf("create RecyclingCompleted fixture: %v", err)
	}
	return service, repo
}

func analyticsRequest(repo *fakeBatchRepository, runID, idempotencyKey string, version int64) (dto.AnalyticsAcknowledgement, BatchCommandMetadata) {
	event := repo.state.outbox[0]
	payload, _ := decodeEventPayload(event.PayloadJSON)
	hash, _ := analyticsInputHash(payload)
	return dto.AnalyticsAcknowledgement{
		SourceEventID: event.EventID, SourceEventVersion: 7, AnalyticsRunID: runID, InputHash: hash,
		RuleVersion: "d3-v1", DataQuality: string(model.AnalyticsDataQualityMissing),
		Metrics: dto.AnalyticsMetrics{
			DeclaredWeightKg: stringPointer("11.00"), ActualWeightKg: stringPointer("10.50"),
			ReusedKg: nil, RecycledKg: nil, DisposedKg: nil, UnknownKg: stringPointer("10.50"),
			DivertedKg: nil, DeclaredQuantity: new(12), ActualItemCount: new(10), CategoryMatch: new(true),
			WeightDeltaKg: stringPointer("-0.50"), CountDelta: new(-2),
		},
		AnomalyCodes: []string{string(model.AnomalyCountMismatch), string(model.AnomalyWeightMismatch), string(model.AnomalyMissingOutcome)},
	}, BatchCommandMetadata{ActorScope: "service:analytics-worker", CorrelationID: "corr-analytics", IdempotencyKey: idempotencyKey, ExpectedVersion: version}
}
