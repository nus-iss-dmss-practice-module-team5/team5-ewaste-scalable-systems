package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"workflow-api/internal/dto"
	"workflow-api/internal/eventbus"
	"workflow-api/internal/model"
)

func TestAnalyticsPreparationIsFrozenAndReadOnly(t *testing.T) {
	svc, repo := analyticsFixture(t)
	event := repo.state.outbox[0]
	first, err := svc.PrepareAnalytics(context.Background(), event.BatchID, event.EventID, "analytics-worker")
	if err != nil {
		t.Fatal(err)
	}
	// Preparation must use committed source data rather than these live values.
	repo.state.batches[event.BatchID].Quantity = new(999)
	again, err := svc.PrepareAnalytics(context.Background(), event.BatchID, event.EventID, "analytics-worker")
	if err != nil || first != again {
		t.Fatalf("preparation drifted: %v", err)
	}
	sum := sha256.Sum256([]byte(first.InputCanonicalJSON))
	if hex.EncodeToString(sum[:]) != first.InputHash || first.RuleVersion != ApprovedAnalyticsRuleVersion || first.SourceEventVersion != event.AggregateVersion {
		t.Fatal("unbound preparation identity")
	}
	var input map[string]any
	if err := json.Unmarshal([]byte(first.InputCanonicalJSON), &input); err != nil {
		t.Fatal(err)
	}
	if input["source_event_id"] != event.EventID || input["rule_version"] != ApprovedAnalyticsRuleVersion || input["receipt_version"] != float64(1) {
		t.Fatal("missing frozen identities")
	}
	if len(repo.state.commands) != 1 || len(repo.state.analytics) != 0 || len(repo.state.outbox) != 1 {
		t.Fatal("preparation wrote business effects")
	}
	if _, err := svc.PrepareAnalytics(context.Background(), event.BatchID, event.EventID, ""); !errors.Is(err, ErrBatchForbidden) {
		t.Fatal("missing service identity accepted")
	}
	if _, err := svc.PrepareAnalytics(context.Background(), "other-batch", event.EventID, "analytics-worker"); !errors.Is(err, ErrBatchNotFound) {
		t.Fatalf("cross-batch source accepted: %v", err)
	}
	repo.state.batches[event.BatchID].Status = model.BatchStatusVerified
	if _, err := svc.PrepareAnalytics(context.Background(), event.BatchID, event.EventID, "analytics-worker"); !errors.Is(err, ErrBatchInvalidState) {
		t.Fatal("invalid preparation state accepted")
	}
}

func TestAnalyticsReplayRejectsChangedBatchAndSourceVersion(t *testing.T) {
	for _, mode := range []string{"batch", "source_version", "header_version"} {
		t.Run(mode, func(t *testing.T) {
			svc, repo := analyticsFixture(t)
			req, meta := analyticsRequest(repo, "run", "first", 7)
			if _, err := svc.AcknowledgeAnalytics(context.Background(), "batch-receipt-001", req, meta); err != nil {
				t.Fatal(err)
			}
			batchID := "batch-receipt-001"
			meta.IdempotencyKey = "second"
			switch mode {
			case "batch":
				other := collectedReceiptBatch()
				other.ID = "other-completed-batch"
				other.Status = model.BatchStatusCompleted
				other.Version = 8
				repo.state.batches[other.ID] = other
				batchID = other.ID
			case "source_version":
				req.SourceEventVersion = 999
			case "header_version":
				meta.ExpectedVersion = 999
			}
			if _, err := svc.AcknowledgeAnalytics(context.Background(), batchID, req, meta); !errors.Is(err, ErrBatchIdempotencyConflict) {
				t.Fatalf("changed identity accepted: %v", err)
			}
			if len(repo.state.analytics) != 1 || len(repo.state.outbox) != 2 {
				t.Fatal("replay changed durable effects")
			}
		})
	}
}

func TestAnalyticsPolicyRemainsPinnedAfterConfigurationChange(t *testing.T) {
	svc, repo := analyticsFixture(t)
	event := repo.state.outbox[0]
	before, err := svc.PrepareAnalytics(context.Background(), event.BatchID, event.EventID, "analytics-worker")
	if err != nil {
		t.Fatal(err)
	}
	// A deployment may select another policy for new treatments while old work
	// is still pending. Both preparation and acknowledgement retain the source policy.
	svc.approvedAnalyticsRuleVersion = "d3-v2"
	after, err := svc.PrepareAnalytics(context.Background(), event.BatchID, event.EventID, "analytics-worker")
	if err != nil || before != after {
		t.Fatalf("configuration changed frozen preparation: %v", err)
	}
	req, meta := analyticsRequest(repo, "run", "first", 7)
	wrongPolicy := req
	wrongPolicy.RuleVersion = "d3-v2"
	if _, err := svc.AcknowledgeAnalytics(context.Background(), event.BatchID, wrongPolicy, meta); !errors.Is(err, ErrBatchValidation) {
		t.Fatalf("new policy reinterpreted old source: %v", err)
	}
	if _, err := svc.AcknowledgeAnalytics(context.Background(), event.BatchID, req, meta); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"first", "source-run-replay"} {
		meta.IdempotencyKey = key
		result, err := svc.AcknowledgeAnalytics(context.Background(), event.BatchID, req, meta)
		if err != nil || result.EventState != "REPLAYED" {
			t.Fatalf("policy change broke replay: %v", err)
		}
	}
	if len(repo.state.analytics) != 1 || len(repo.state.outbox) != 2 {
		t.Fatal("replay duplicated durable effects")
	}
}

func TestAnalyticsRejectsUnsupportedOrUnpinnedPolicy(t *testing.T) {
	for _, mode := range []string{"unknown", "source", "missing_array"} {
		t.Run(mode, func(t *testing.T) {
			svc, repo := analyticsFixture(t)
			req, meta := analyticsRequest(repo, "run", "first", 7)
			switch mode {
			case "unknown":
				req.RuleVersion = "not-an-approved-policy"
			case "source":
				payload, _ := decodeEventPayload(repo.state.outbox[0].PayloadJSON)
				payload["data"].(map[string]any)["rule_version"] = "unapproved-source-policy"
				repo.state.outbox[0].PayloadJSON, _ = json.Marshal(payload)
				req.InputHash, _ = analyticsInputHash(payload)
			case "missing_array":
				req.AnomalyCodes = nil
			}
			if _, err := svc.AcknowledgeAnalytics(context.Background(), "batch-receipt-001", req, meta); !errors.Is(err, ErrBatchValidation) {
				t.Fatalf("invalid policy/schema accepted: %v", err)
			}
			if repo.state.batches["batch-receipt-001"].Status != model.BatchStatusRecycled || len(repo.state.analytics) != 0 {
				t.Fatal("invalid result completed batch")
			}
		})
	}
}

func TestProcessingOutboxEventsPassPublisherValidation(t *testing.T) {
	svc, repo, _ := testBatchService()
	batch := collectedReceiptBatch()
	batch.ID = "12345678-1234-4123-8123-123456789abc"
	repo.state.batches[batch.ID] = batch
	if _, err := svc.VerifyReceipt(context.Background(), batch.ID, dto.ReceiptRequest{ActualCategory: "ICT_EQUIPMENT", ActualItemCount: 12, ActualWeightKg: "11.00"}, recyclerMetadata("receipt", "correlation", 5)); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RecordTreatment(context.Background(), batch.ID, dto.TreatmentRequest{}, treatmentMetadata("treatment", "correlation", 6)); err != nil {
		t.Fatal(err)
	}
	payload, _ := decodeEventPayload(repo.state.outbox[1].PayloadJSON)
	req, err := pythonAnalyticsAcknowledgement(payload, repo.state.outbox[1].EventID)
	if err != nil {
		t.Fatal(err)
	}
	meta := BatchCommandMetadata{ActorScope: "service:analytics-worker", CorrelationID: "correlation", IdempotencyKey: "analytics", ExpectedVersion: 7}
	if _, err := svc.AcknowledgeAnalytics(context.Background(), batch.ID, req, meta); err != nil {
		t.Fatal(err)
	}
	for _, event := range repo.state.outbox {
		if err := eventbus.ValidateEvent(*event); err != nil {
			t.Fatalf("%s cannot publish: %v", event.EventType, err)
		}
		for _, field := range []string{"aggregate_id", "aggregate_version", "sequence_in_command", "batch_id"} {
			bad := *event
			body, _ := decodeEventPayload(bad.PayloadJSON)
			delete(body, field)
			bad.PayloadJSON, _ = json.Marshal(body)
			if err := eventbus.ValidateEvent(bad); err == nil {
				t.Fatalf("%s accepted missing %s", event.EventType, field)
			}
		}
	}
	before, err := svc.PrepareAnalytics(context.Background(), batch.ID, repo.state.outbox[1].EventID, "analytics-worker")
	if err != nil {
		t.Fatal(err)
	}
	meta.IdempotencyKey = "new-replay-key"
	if _, err := svc.AcknowledgeAnalytics(context.Background(), batch.ID, req, meta); err != nil {
		t.Fatal(err)
	}
	after, err := svc.PrepareAnalytics(context.Background(), batch.ID, repo.state.outbox[1].EventID, "analytics-worker")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("completed replay changed preparation: %v", err)
	}
}
