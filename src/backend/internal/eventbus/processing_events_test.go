package eventbus

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"

	"workflow-api/internal/model"
)

func TestValidateEventAcceptsProcessingEventContracts(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 123456000, time.UTC)
	for _, eventType := range []string{model.ReceiptVerifiedEventType, model.RecyclingCompletedEventType, model.RequestCompletedEventType} {
		t.Run(eventType, func(t *testing.T) {
			payload := processingEventPayload(t, eventType, now)
			event := model.EventOutbox{
				EventID: "11111111-1111-4111-8111-111111111111", BatchID: "22222222-2222-4222-8222-222222222222", CommandID: "33333333-3333-4333-8333-333333333333",
				EventType: eventType, Topic: "ewaste.batch.events", SchemaVersion: 1, AggregateVersion: 8, SequenceInCommand: 1,
				PartitionKey: "22222222-2222-4222-8222-222222222222", PayloadJSON: payload, CorrelationID: "processing-contract-test", OccurredAt: now,
			}
			if err := ValidateEvent(event); err != nil {
				t.Fatalf("valid processing event rejected: %v", err)
			}
			var body map[string]any
			if err := json.Unmarshal(payload, &body); err != nil {
				t.Fatal(err)
			}
			data := body["data"].(map[string]any)
			if eventType == model.RequestCompletedEventType {
				data["anomaly_codes"] = "MISSING_OUTCOME"
			} else {
				data["actual_item_count"] = "10"
			}
			event.PayloadJSON, _ = json.Marshal(body)
			if err := ValidateEvent(event); err == nil {
				t.Fatal("wrong data type passed publisher validation")
			}
		})
	}
}

func TestProcessingSchemasMatchPublishedAPIContracts(t *testing.T) {
	for event, apiFile := range map[string]string{
		"ReceiptVerified":    "receipt-verified",
		"RecyclingCompleted": "recycling-completed",
		"RequestCompleted":   "request-completed",
	} {
		published, err := os.ReadFile("../../api/events/" + apiFile + ".v1.schema.json")
		if err != nil {
			t.Fatal(err)
		}
		for _, directory := range []string{"../matchingcontract/schemas/", "../../../../contracts/matching/kafka/"} {
			actual, err := os.ReadFile(directory + event + ".v1.schema.json")
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(published, actual) {
				t.Fatalf("%s contract drift in %s", event, directory)
			}
		}
	}
}

func TestValidateEventRejectsProcessingSequenceMismatch(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 123456000, time.UTC)
	payload := processingEventPayload(t, model.RequestCompletedEventType, now)
	event := model.EventOutbox{
		EventID: "11111111-1111-4111-8111-111111111111", BatchID: "22222222-2222-4222-8222-222222222222", CommandID: "33333333-3333-4333-8333-333333333333",
		EventType: model.RequestCompletedEventType, Topic: "ewaste.batch.events", SchemaVersion: 1, AggregateVersion: 8, SequenceInCommand: 2,
		PartitionKey: "22222222-2222-4222-8222-222222222222", PayloadJSON: payload, CorrelationID: "processing-contract-test", OccurredAt: now,
	}
	if err := ValidateEvent(event); err == nil {
		t.Fatal("expected sequence mismatch to be rejected")
	}
}

func processingEventPayload(t *testing.T, eventType string, now time.Time) []byte {
	t.Helper()
	data := map[string]any{"batch_id": "22222222-2222-4222-8222-222222222222"}
	switch eventType {
	case model.ReceiptVerifiedEventType:
		for key, value := range map[string]any{"receipt_id": "44444444-4444-4444-8444-444444444444", "facility_org_id": "PROC-001", "actor_user_id": "user-1", "actual_category": "ICT_EQUIPMENT", "actual_item_count": 10, "actual_weight_kg": "10.50", "declared_category": "ICT_EQUIPMENT", "declared_quantity": 10, "declared_weight_kg": "10.50"} {
			data[key] = value
		}
	case model.RecyclingCompletedEventType:
		for key, value := range map[string]any{"treatment_id": "55555555-5555-4555-8555-555555555555", "receipt_id": "44444444-4444-4444-8444-444444444444", "receipt_version": 1, "treatment_version": 1, "rule_version": "d3-v1", "facility_org_id": "PROC-001", "actor_user_id": "user-1", "declared_category": "ICT_EQUIPMENT", "declared_quantity": 10, "declared_weight_kg": "10.50", "actual_category": "ICT_EQUIPMENT", "actual_item_count": 10, "actual_weight_kg": "10.50", "reused_kg": nil, "recycled_kg": nil, "disposed_kg": nil, "unknown_kg": "10.50", "diverted_kg": nil, "data_quality": "MISSING", "evidence_id": nil, "evidence_status": "ABSENT", "claim_epoch": "1"} {
			data[key] = value
		}
	case model.RequestCompletedEventType:
		for key, value := range map[string]any{"result_id": "66666666-6666-4666-8666-666666666666", "source_event_id": "55555555-5555-4555-8555-555555555555", "rule_version": "d3-v1", "data_quality": "MISSING", "anomaly_codes": []string{"MISSING_OUTCOME"}} {
			data[key] = value
		}
	}
	payload, err := json.Marshal(map[string]any{
		"event_id": "11111111-1111-4111-8111-111111111111", "event_type": eventType, "schema_version": 1, "producer": "go-workflow-service", "aggregate_type": "EWasteBatch", "aggregate_id": "22222222-2222-4222-8222-222222222222", "aggregate_version": 8, "command_id": "33333333-3333-4333-8333-333333333333", "batch_id": "22222222-2222-4222-8222-222222222222", "claim_epoch": "1", "sequence_in_command": 1, "occurred_at": now.Format("2006-01-02T15:04:05.000000Z07:00"), "correlation_id": "processing-contract-test", "data": data,
	})
	if err != nil {
		t.Fatalf("marshal processing event: %v", err)
	}
	return payload
}
