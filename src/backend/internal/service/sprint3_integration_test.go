package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"workflow-api/internal/dto"
	"workflow-api/internal/model"
)

// fakeKafka is deliberately small: it models the two Kafka boundaries needed
// by this integration test without requiring a broker, Redis, or MySQL.
type fakeKafka struct {
	toPython chan model.EventOutbox
	fromGo   chan model.EventOutbox

	mu        sync.Mutex
	published []model.EventOutbox
}

func newFakeKafka() *fakeKafka {
	return &fakeKafka{
		toPython: make(chan model.EventOutbox, 4),
		fromGo:   make(chan model.EventOutbox, 4),
	}
}

func (b *fakeKafka) deliverToPython(event model.EventOutbox) {
	b.toPython <- event
}

func (b *fakeKafka) publishFromGo(event model.EventOutbox) {
	b.mu.Lock()
	b.published = append(b.published, event)
	b.mu.Unlock()
	b.fromGo <- event
}

type fakePythonWorker struct {
	service *BatchService
	kafka   *fakeKafka
	now     time.Time
	result  dto.CompletionMutationResult
	outbox  func() []*model.EventOutbox
}

func (w *fakePythonWorker) consumeOne(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case event := <-w.kafka.toPython:
		if event.EventType != model.RecyclingCompletedEventType {
			return fmt.Errorf("python received unexpected event %q", event.EventType)
		}

		payload, err := decodeEventPayload(event.PayloadJSON)
		if err != nil {
			return fmt.Errorf("python decode RecyclingCompleted: %w", err)
		}
		request, err := pythonAnalyticsAcknowledgement(payload, event.EventID)
		if err != nil {
			return err
		}
		w.result, err = w.service.AcknowledgeAnalytics(ctx, event.BatchID, request, BatchCommandMetadata{
			ActorScope:      "service:analytics-worker",
			CorrelationID:   "corr-fake-integration",
			IdempotencyKey:  "python-analytics-run-001",
			ExpectedVersion: int64(request.SourceEventVersion),
		})
		if err != nil {
			return fmt.Errorf("python acknowledgement: %w", err)
		}

		for index := len(w.outbox()) - 1; index >= 0; index-- {
			completed := w.outbox()[index]
			if completed.EventType == model.RequestCompletedEventType {
				w.kafka.publishFromGo(*completed)
				return nil
			}
		}
		return fmt.Errorf("go did not enqueue RequestCompleted")
	}
}

func TestFakeSprint3ReceiptTreatmentAnalyticsCompletionFlow(t *testing.T) {
	service, repo, _ := testBatchService()
	repo.state.batches["batch-receipt-001"] = collectedReceiptBatch()
	if _, err := service.VerifyReceipt(context.Background(), "batch-receipt-001", dto.ReceiptRequest{
		ActualCategory: "ICT_EQUIPMENT", ActualItemCount: 10, ActualWeightKg: "10.50",
	}, recyclerMetadata("receipt-fake-integration", "corr-fake-receipt", 5)); err != nil {
		t.Fatalf("receipt verification failed in fake integration flow: %v", err)
	}
	if _, err := service.RecordTreatment(context.Background(), "batch-receipt-001", dto.TreatmentRequest{}, treatmentMetadata("treatment-fake-integration", "corr-fake-treatment", 6)); err != nil {
		t.Fatalf("treatment recording failed in fake integration flow: %v", err)
	}
	kafka := newFakeKafka()

	// The treatment command is the real Go implementation. Its durable
	// RecyclingCompleted row is handed to the fake Kafka broker exactly as the
	// outbox relay would hand it to the analytics consumer.
	if len(repo.state.outbox) != 2 || repo.state.outbox[0].EventType != model.ReceiptVerifiedEventType {
		t.Fatalf("receipt and treatment events were not both persisted: %+v", repo.state.outbox)
	}
	recyclingCompleted := repo.state.outbox[1]
	assertFakeKafkaEnvelope(t, *recyclingCompleted, model.RecyclingCompletedEventType, "ewaste.batch.events")

	worker := &fakePythonWorker{service: service, kafka: kafka, outbox: func() []*model.EventOutbox { return repo.state.outbox }}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	workerDone := make(chan error, 1)
	go func() { workerDone <- worker.consumeOne(ctx) }()
	kafka.deliverToPython(*recyclingCompleted)

	select {
	case err := <-workerDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for mock Python acknowledgement")
	}

	select {
	case completed := <-kafka.fromGo:
		assertFakeKafkaEnvelope(t, completed, model.RequestCompletedEventType, "ewaste.batch.events")
		if completed.PartitionKey != "batch-receipt-001" || completed.AggregateVersion != 8 {
			t.Fatalf("unexpected completion routing metadata: %+v", completed)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for RequestCompleted")
	}

	if worker.result.Data.Status != string(model.BatchStatusCompleted) || worker.result.Data.Version != 8 {
		t.Fatalf("mock Python did not observe completed result: %+v", worker.result)
	}
	if repo.state.batches["batch-receipt-001"].Status != model.BatchStatusCompleted {
		t.Fatalf("batch status was not completed: %+v", repo.state.batches["batch-receipt-001"])
	}
	if len(repo.state.analytics) != 1 || len(repo.state.audits) != 3 || len(repo.state.outbox) != 3 {
		t.Fatalf("unexpected durable integration state: analytics=%d audits=%d outbox=%d", len(repo.state.analytics), len(repo.state.audits), len(repo.state.outbox))
	}

	t.Logf("fake integration report: treatment event=%s, analytics result=%s, completion event=%s, final state=%s, final version=%d", recyclingCompleted.EventID, worker.result.Data.AnalyticsResultID, worker.result.EventID, worker.result.Data.Status, worker.result.Data.Version)
}

func pythonAnalyticsAcknowledgement(payload map[string]any, sourceEventID string) (dto.AnalyticsAcknowledgement, error) {
	data, ok := payload["data"].(map[string]any)
	if !ok {
		return dto.AnalyticsAcknowledgement{}, fmt.Errorf("python received event without data")
	}

	// This independently reconstructs the canonical object specified by the
	// Sprint 3 Python boundary. It intentionally does not call Go's hash helper.
	canonicalKeys := []string{"actor_user_id", "actual_category", "actual_item_count", "actual_weight_kg", "aggregate_version", "batch_id", "claim_epoch", "data_quality", "declared_category", "declared_quantity", "declared_weight_kg", "disposed_kg", "diverted_kg", "evidence_id", "evidence_status", "facility_org_id", "receipt_id", "receipt_version", "recycled_kg", "reused_kg", "treatment_id", "treatment_version", "unknown_kg", "rule_version", "source_event_id", "correlation_id"}
	canonical := make(map[string]any, len(canonicalKeys))
	for _, key := range canonicalKeys {
		if key == "source_event_id" {
			canonical[key] = payload["event_id"]
		} else if key == "aggregate_version" || key == "correlation_id" {
			canonical[key] = payload[key]
		} else {
			canonical[key] = data[key]
		}
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return dto.AnalyticsAcknowledgement{}, fmt.Errorf("python canonical input: %w", err)
	}
	sum := sha256.Sum256(raw)
	inputHash := hex.EncodeToString(sum[:])

	dataQuality := stringValue(data["data_quality"])
	anomalyCodes := make([]string, 0, 5)
	if stringValue(data["declared_category"]) != stringValue(data["actual_category"]) {
		anomalyCodes = append(anomalyCodes, string(model.AnomalyCategoryMismatch))
	}
	declaredCount := int(numberValue(data["declared_quantity"]))
	actualCount := int(numberValue(data["actual_item_count"]))
	if declaredCount != actualCount {
		anomalyCodes = append(anomalyCodes, string(model.AnomalyCountMismatch))
	}
	weightDelta, _ := signedDecimalDifference(stringValue(data["actual_weight_kg"]), stringValue(data["declared_weight_kg"]))
	if weightDelta != "0.00" {
		anomalyCodes = append(anomalyCodes, string(model.AnomalyWeightMismatch))
	}
	if dataQuality == string(model.TreatmentDataQualityMissing) {
		anomalyCodes = append(anomalyCodes, string(model.AnomalyMissingOutcome))
	}
	return dto.AnalyticsAcknowledgement{
		SourceEventID:      sourceEventID,
		SourceEventVersion: uint32(numberValue(payload["aggregate_version"])),
		AnalyticsRunID:     "python-run-001",
		InputHash:          inputHash,
		RuleVersion:        stringValue(data["rule_version"]),
		DataQuality:        dataQuality,
		Metrics: dto.AnalyticsMetrics{
			DeclaredWeightKg: stringPointer(stringValue(data["declared_weight_kg"])),
			ActualWeightKg:   stringPointer(stringValue(data["actual_weight_kg"])),
			UnknownKg:        stringPointer(stringValue(data["unknown_kg"])),
			DeclaredQuantity: intPointer(numberValue(data["declared_quantity"])),
			ActualItemCount:  intPointer(numberValue(data["actual_item_count"])),
			CategoryMatch:    boolPointer(stringValue(data["declared_category"]) == stringValue(data["actual_category"])),
			WeightDeltaKg:    stringPointer(weightDelta),
			CountDelta:       new(actualCount - declaredCount),
		},
		AnomalyCodes: anomalyCodes,
	}, nil
}

func intPointer(value uint64) *int {
	return new(int(value))
}

func boolPointer(value bool) *bool {
	return new(value)
}

func assertFakeKafkaEnvelope(t *testing.T, event model.EventOutbox, eventType, topic string) {
	t.Helper()
	if event.EventType != eventType || event.Topic != topic || event.PartitionKey != event.BatchID || event.SchemaVersion != 1 || len(event.PayloadJSON) == 0 {
		t.Fatalf("invalid fake Kafka envelope: %+v", event)
	}
	var payload map[string]any
	if err := json.Unmarshal(event.PayloadJSON, &payload); err != nil {
		t.Fatalf("invalid fake Kafka JSON payload: %v", err)
	}
	if stringValue(payload["event_type"]) != eventType || stringValue(payload["batch_id"]) != event.BatchID {
		t.Fatalf("payload/envelope mismatch: event=%+v payload=%+v", event, payload)
	}
}
