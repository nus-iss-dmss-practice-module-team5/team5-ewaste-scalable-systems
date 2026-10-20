package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"workflow-api/internal/dto"
	"workflow-api/internal/model"
)

func TestBatchServiceRecordTreatmentAcceptsMissingOutcomesAndPublishesVersionedEvent(t *testing.T) {
	service, repo, _ := testBatchService()
	installTreatmentFixture(repo)

	result, err := service.RecordTreatment(
		context.Background(),
		"batch-receipt-001",
		dto.TreatmentRequest{},
		treatmentMetadata("treatment-missing-001", "corr-treatment-missing", 6),
	)
	if err != nil {
		t.Fatalf("record treatment returned error: %v", err)
	}

	if result.Data.Status != string(model.BatchStatusRecycled) || result.Data.Version != 7 {
		t.Fatalf("unexpected treatment transition: %+v", result.Data)
	}
	if result.Data.DataQuality != string(model.TreatmentDataQualityMissing) ||
		result.Data.UnknownKg == nil || *result.Data.UnknownKg != "10.50" ||
		result.Data.ReusedKg != nil || result.Data.RecycledKg != nil || result.Data.DisposedKg != nil ||
		result.Data.DivertedKg != nil || result.Data.EvidenceStatus != "ABSENT" {
		t.Fatalf("unexpected missing treatment result: %+v", result.Data)
	}

	assertRecyclingCompletedEvent(t, repo, map[string]any{
		"aggregate_version": float64(7),
		"claim_epoch":       "3",
		"data_quality":      string(model.TreatmentDataQualityMissing),
		"evidence_status":   "ABSENT",
		"unknown_kg":        "10.50",
		"receipt_version":   float64(1),
		"treatment_version": float64(1),
	})
	if len(repo.state.treatments) != 1 || len(repo.state.audits) != 1 {
		t.Fatalf("expected treatment and audit to be persisted: treatments=%d audits=%d", len(repo.state.treatments), len(repo.state.audits))
	}
}

func TestBatchServiceRecordTreatmentPersistsEvidenceLinkAndPartialValues(t *testing.T) {
	service, repo, _ := testBatchService()
	installTreatmentFixture(repo)
	repo.state.evidence["evidence-treatment-001"] = &model.BatchEvidence{
		EvidenceID:       "evidence-treatment-001",
		BatchID:          "batch-receipt-001",
		OrganisationID:   "facility-001",
		LifecycleStage:   model.EvidenceLifecycleTreatment,
		ValidationStatus: model.EvidenceValidationValidated,
	}

	result, err := service.RecordTreatment(
		context.Background(),
		"batch-receipt-001",
		dto.TreatmentRequest{
			ReusedKg:   stringPointer("2"),
			RecycledKg: stringPointer("3.5"),
			DisposedKg: stringPointer("1.00"),
			EvidenceID: stringPointer("evidence-treatment-001"),
		},
		treatmentMetadata("treatment-partial-001", "corr-treatment-partial", 6),
	)
	if err != nil {
		t.Fatalf("record treatment returned error: %v", err)
	}

	if result.Data.DataQuality != string(model.TreatmentDataQualityPartial) ||
		result.Data.UnknownKg == nil || *result.Data.UnknownKg != "4.00" ||
		result.Data.DivertedKg == nil || *result.Data.DivertedKg != "5.50" ||
		result.Data.EvidenceStatus != "PRESENT" {
		t.Fatalf("unexpected partial treatment result: %+v", result.Data)
	}
	treatment := repo.state.treatments["batch-receipt-001"]
	if treatment == nil || treatment.EvidenceID == nil || *treatment.EvidenceID != "evidence-treatment-001" {
		t.Fatalf("evidence link was not persisted: %+v", treatment)
	}

	assertRecyclingCompletedEvent(t, repo, map[string]any{
		"data_quality":    string(model.TreatmentDataQualityPartial),
		"evidence_id":     "evidence-treatment-001",
		"evidence_status": "PRESENT",
		"reused_kg":       "2.00",
		"recycled_kg":     "3.50",
		"disposed_kg":     "1.00",
		"unknown_kg":      "4.00",
		"diverted_kg":     "5.50",
	})
}

func TestBatchServiceRecordTreatmentRejectsInvalidAllocationWithoutStateChange(t *testing.T) {
	tests := []struct {
		name    string
		request dto.TreatmentRequest
	}{
		{
			name:    "only one amount",
			request: dto.TreatmentRequest{ReusedKg: stringPointer("1.00")},
		},
		{
			name: "excess precision",
			request: dto.TreatmentRequest{
				ReusedKg:   stringPointer("1.001"),
				RecycledKg: stringPointer("1.00"),
				DisposedKg: stringPointer("1.00"),
			},
		},
		{
			name: "over allocation",
			request: dto.TreatmentRequest{
				ReusedKg:   stringPointer("10.00"),
				RecycledKg: stringPointer("1.00"),
				DisposedKg: stringPointer("0.00"),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, repo, _ := testBatchService()
			installTreatmentFixture(repo)

			_, err := service.RecordTreatment(
				context.Background(),
				"batch-receipt-001",
				test.request,
				treatmentMetadata("treatment-invalid-001", "corr-treatment-invalid", 6),
			)
			if !errors.Is(err, ErrBatchValidation) {
				t.Fatalf("expected validation error, got %v", err)
			}
			batch := repo.state.batches["batch-receipt-001"]
			if batch.Status != model.BatchStatusVerified || batch.Version != 6 ||
				len(repo.state.treatments) != 0 || len(repo.state.commands) != 0 || len(repo.state.audits) != 0 || len(repo.state.outbox) != 0 {
				t.Fatalf("invalid treatment changed durable state: batch=%+v treatments=%d commands=%d audits=%d outbox=%d", batch, len(repo.state.treatments), len(repo.state.commands), len(repo.state.audits), len(repo.state.outbox))
			}
		})
	}
}

func TestBatchServiceRecordTreatmentReplayAndConflictDoNotDuplicateEffects(t *testing.T) {
	service, repo, _ := testBatchService()
	installTreatmentFixture(repo)
	metadata := treatmentMetadata("treatment-replay-001", "corr-treatment-replay", 6)
	request := dto.TreatmentRequest{
		ReusedKg:   stringPointer("2.00"),
		RecycledKg: stringPointer("3.00"),
		DisposedKg: stringPointer("1.00"),
	}

	first, err := service.RecordTreatment(context.Background(), "batch-receipt-001", request, metadata)
	if err != nil {
		t.Fatalf("first treatment returned error: %v", err)
	}
	second, err := service.RecordTreatment(context.Background(), "batch-receipt-001", request, metadata)
	if err != nil {
		t.Fatalf("replayed treatment returned error: %v", err)
	}
	if second.Data.TreatmentID != first.Data.TreatmentID || len(repo.state.treatments) != 1 || len(repo.state.audits) != 1 || len(repo.state.outbox) != 1 {
		t.Fatalf("replay duplicated treatment effects: first=%+v second=%+v treatments=%d audits=%d outbox=%d", first.Data, second.Data, len(repo.state.treatments), len(repo.state.audits), len(repo.state.outbox))
	}

	conflicting := request
	conflicting.DisposedKg = stringPointer("2.00")
	_, err = service.RecordTreatment(context.Background(), "batch-receipt-001", conflicting, metadata)
	if !errors.Is(err, ErrBatchIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}
	if len(repo.state.treatments) != 1 || len(repo.state.audits) != 1 || len(repo.state.outbox) != 1 {
		t.Fatalf("conflict changed durable effects")
	}
}

func TestBatchServiceRecordTreatmentRollsBackWhenOutboxFails(t *testing.T) {
	service, repo, _ := testBatchService()
	installTreatmentFixture(repo)
	repo.failOutbox = true

	_, err := service.RecordTreatment(
		context.Background(),
		"batch-receipt-001",
		dto.TreatmentRequest{},
		treatmentMetadata("treatment-rollback-001", "corr-treatment-rollback", 6),
	)
	if err == nil {
		t.Fatal("expected outbox failure")
	}

	batch := repo.state.batches["batch-receipt-001"]
	if batch.Status != model.BatchStatusVerified || batch.Version != 6 ||
		len(repo.state.treatments) != 0 || len(repo.state.commands) != 0 || len(repo.state.audits) != 0 || len(repo.state.outbox) != 0 {
		t.Fatalf("outbox failure left partial treatment writes: batch=%+v treatments=%d commands=%d audits=%d outbox=%d", batch, len(repo.state.treatments), len(repo.state.commands), len(repo.state.audits), len(repo.state.outbox))
	}
}

func TestBatchServiceRecordTreatmentRejectsUnvalidatedEvidence(t *testing.T) {
	service, repo, _ := testBatchService()
	installTreatmentFixture(repo)
	repo.state.evidence["evidence-pending-001"] = &model.BatchEvidence{
		EvidenceID:       "evidence-pending-001",
		BatchID:          "batch-receipt-001",
		OrganisationID:   "facility-001",
		LifecycleStage:   model.EvidenceLifecycleTreatment,
		ValidationStatus: "PENDING",
	}

	_, err := service.RecordTreatment(
		context.Background(),
		"batch-receipt-001",
		dto.TreatmentRequest{EvidenceID: stringPointer("evidence-pending-001")},
		treatmentMetadata("treatment-evidence-001", "corr-treatment-evidence", 6),
	)
	if !errors.Is(err, ErrBatchEvidenceNotFound) {
		t.Fatalf("expected evidence validation failure, got %v", err)
	}
}

func installTreatmentFixture(repo *fakeBatchRepository) {
	batch := collectedReceiptBatch()
	batch.Status = model.BatchStatusVerified
	batch.Version = 6
	repo.state.batches[batch.ID] = batch
	repo.state.receipts[batch.ID] = &model.BatchReceipt{
		ReceiptID:       "receipt-001",
		BatchID:         batch.ID,
		FacilityOrgID:   "facility-001",
		VerifiedBy:      "recycler-user-001",
		ActualCategory:  "ICT_EQUIPMENT",
		ActualItemCount: 10,
		ActualWeightKg:  "10.50",
	}
}

func treatmentMetadata(idempotencyKey, correlationID string, version int64) BatchCommandMetadata {
	return BatchCommandMetadata{
		Actor: BatchActor{
			UserID:         "recycler-user-001",
			OrganisationID: "facility-001",
			RoleCode:       "RECYCLER",
		},
		CorrelationID:   correlationID,
		IdempotencyKey:  idempotencyKey,
		ExpectedVersion: version,
	}
}

func assertRecyclingCompletedEvent(t *testing.T, repo *fakeBatchRepository, expected map[string]any) {
	t.Helper()
	if len(repo.state.outbox) != 1 {
		t.Fatalf("expected one outbox event, got %d", len(repo.state.outbox))
	}
	event := repo.state.outbox[0]
	if event.EventType != model.RecyclingCompletedEventType || event.Topic != "ewaste.batch.events" ||
		event.AggregateVersion != 7 || event.SequenceInCommand != 1 || event.PartitionKey != "batch-receipt-001" {
		t.Fatalf("unexpected RecyclingCompleted metadata: %+v", event)
	}
	var payload map[string]any
	if err := json.Unmarshal(event.PayloadJSON, &payload); err != nil {
		t.Fatalf("decode event payload: %v", err)
	}
	for key, want := range expected {
		if got := nestedEventValue(payload, key); got != want {
			t.Errorf("event %s=%v, want %v", key, got, want)
		}
	}
}

func nestedEventValue(payload map[string]any, key string) any {
	if value, ok := payload[key]; ok {
		return value
	}
	data, _ := payload["data"].(map[string]any)
	return data[key]
}

func stringPointer(value string) *string { return new(value) }
