package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"workflow-api/internal/dto"
	"workflow-api/internal/model"
)

func recyclerMetadata(idempotencyKey, correlationID string, version int64) BatchCommandMetadata {
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

func TestBatchServiceVerifyReceiptRollsBackAllWritesWhenOutboxFails(t *testing.T) {
	service, repo, _ := testBatchService()
	repo.state.batches["batch-receipt-001"] = collectedReceiptBatch()
	repo.failOutbox = true

	_, err := service.VerifyReceipt(
		context.Background(),
		"batch-receipt-001",
		dto.ReceiptRequest{ActualCategory: "ICT_EQUIPMENT", ActualItemCount: 10, ActualWeightKg: "10.50"},
		recyclerMetadata("receipt-command-rollback", "corr-receipt-rollback", 5),
	)
	if err == nil {
		t.Fatal("expected outbox failure")
	}

	batch := repo.state.batches["batch-receipt-001"]
	if batch.Status != model.BatchStatusCollected || batch.Version != 5 {
		t.Fatalf("transaction did not restore batch: status=%s version=%d", batch.Status, batch.Version)
	}
	if len(repo.state.receipts) != 0 || len(repo.state.commands) != 0 || len(repo.state.audits) != 0 || len(repo.state.outbox) != 0 {
		t.Fatalf("transaction left partial receipt writes: receipts=%d commands=%d audits=%d outbox=%d", len(repo.state.receipts), len(repo.state.commands), len(repo.state.audits), len(repo.state.outbox))
	}
}

func collectedReceiptBatch() *model.Batch {
	return &model.Batch{
		ID:                  "batch-receipt-001",
		OrganizationID:      "donor-001",
		Status:              model.BatchStatusCollected,
		Category:            new("ICT_EQUIPMENT"),
		Quantity:            new(12),
		EstimatedWeightKg:   new("11.00"),
		ClaimEpoch:          3,
		CurrentClaimID:      new("claim-001"),
		CurrentAssignmentID: new("assignment-001"),
		Version:             5,
	}
}

func TestBatchServiceVerifyReceiptPersistsTransitionAuditAndEvent(t *testing.T) {
	service, repo, _ := testBatchService()
	repo.state.batches["batch-receipt-001"] = collectedReceiptBatch()

	result, err := service.VerifyReceipt(
		context.Background(),
		"batch-receipt-001",
		dto.ReceiptRequest{
			ActualCategory:  "ict_equipment",
			ActualItemCount: 10,
			ActualWeightKg:  "10.50",
		},
		recyclerMetadata("receipt-command-001", "corr-receipt-001", 5),
	)
	if err != nil {
		t.Fatalf("verify receipt returned error: %v", err)
	}

	if result.Data.Status != string(model.BatchStatusVerified) || result.Data.Version != 6 {
		t.Fatalf("unexpected receipt transition: status=%s version=%d", result.Data.Status, result.Data.Version)
	}
	if result.Data.ActualCategory != "ICT_EQUIPMENT" || result.Data.ActualItemCount != 10 || result.Data.ActualWeightKg != "10.50" {
		t.Fatalf("unexpected receipt values: %+v", result.Data)
	}
	if result.EventID == "" || result.EventState != string(model.OutboxPublishStatePending) {
		t.Fatalf("expected pending ReceiptVerified outbox event, got %+v", result)
	}
	if len(repo.state.receipts) != 1 || len(repo.state.audits) != 1 || len(repo.state.outbox) != 1 {
		t.Fatalf("expected one receipt, audit, and outbox row; got receipts=%d audits=%d outbox=%d", len(repo.state.receipts), len(repo.state.audits), len(repo.state.outbox))
	}
	outbox := repo.state.outbox[0]
	if outbox.Topic != "ewaste.batch.events" || outbox.PartitionKey != "batch-receipt-001" ||
		outbox.SchemaVersion != 1 || outbox.AggregateVersion != 6 ||
		outbox.EventType != model.ReceiptVerifiedEventType {
		t.Fatalf("unexpected ReceiptVerified outbox metadata: %+v", outbox)
	}

	if repo.state.audits[0].EventType != model.BatchAuditEventReceiptVerified ||
		repo.state.audits[0].FromStatus != model.BatchStatusCollected ||
		repo.state.audits[0].ToStatus != model.BatchStatusVerified {
		t.Fatalf("unexpected audit transition: %+v", repo.state.audits[0])
	}

	var payload map[string]any
	if err := json.Unmarshal(repo.state.outbox[0].PayloadJSON, &payload); err != nil {
		t.Fatalf("outbox payload is invalid JSON: %v", err)
	}
	if payload["event_type"] != model.ReceiptVerifiedEventType ||
		payload["claim_epoch"] != "3" ||
		payload["producer"] != "go-workflow-service" ||
		payload["aggregate_type"] != "EWasteBatch" ||
		payload["aggregate_id"] != "batch-receipt-001" ||
		payload["aggregate_version"] != float64(6) {
		t.Fatalf("unexpected event envelope: %+v", payload)
	}
	data := payload["data"].(map[string]any)
	if data["batch_id"] != "batch-receipt-001" || data["actual_item_count"] != float64(10) || data["actual_weight_kg"] != "10.50" {
		t.Fatalf("unexpected event data: %+v", data)
	}
}

func TestBatchServiceVerifyReceiptReplaysAndRejectsConflictingPayload(t *testing.T) {
	service, repo, _ := testBatchService()
	repo.state.batches["batch-receipt-001"] = collectedReceiptBatch()
	metadata := recyclerMetadata("receipt-command-002", "corr-receipt-002", 5)
	request := dto.ReceiptRequest{ActualCategory: "ICT_EQUIPMENT", ActualItemCount: 10, ActualWeightKg: "10.50"}

	first, err := service.VerifyReceipt(context.Background(), "batch-receipt-001", request, metadata)
	if err != nil {
		t.Fatalf("first receipt returned error: %v", err)
	}
	second, err := service.VerifyReceipt(context.Background(), "batch-receipt-001", request, metadata)
	if err != nil {
		t.Fatalf("replayed receipt returned error: %v", err)
	}
	if second.Data.ReceiptID != first.Data.ReceiptID || len(repo.state.receipts) != 1 || len(repo.state.outbox) != 1 {
		t.Fatalf("replay created a duplicate result: first=%+v second=%+v", first.Data, second.Data)
	}

	request.ActualItemCount = 11
	_, err = service.VerifyReceipt(context.Background(), "batch-receipt-001", request, metadata)
	if !errors.Is(err, ErrBatchIdempotencyConflict) {
		t.Fatalf("expected conflicting replay error, got %v", err)
	}
}

func TestBatchServiceVerifyReceiptRejectsInvalidStateAndPayload(t *testing.T) {
	service, repo, _ := testBatchService()
	repo.state.batches["batch-receipt-001"] = collectedReceiptBatch()

	_, err := service.VerifyReceipt(
		context.Background(),
		"batch-receipt-001",
		dto.ReceiptRequest{ActualCategory: "ICT_EQUIPMENT", ActualItemCount: 10, ActualWeightKg: "10.500"},
		recyclerMetadata("receipt-command-003", "corr-receipt-003", 5),
	)
	if !errors.Is(err, ErrBatchValidation) {
		t.Fatalf("expected invalid precision error, got %v", err)
	}
	if len(repo.state.receipts) != 0 {
		t.Fatalf("invalid payload must not persist a receipt")
	}

	batch := repo.state.batches["batch-receipt-001"]
	batch.Status = model.BatchStatusVerified
	_, err = service.VerifyReceipt(
		context.Background(),
		"batch-receipt-001",
		dto.ReceiptRequest{ActualCategory: "ICT_EQUIPMENT", ActualItemCount: 10, ActualWeightKg: "10.50"},
		recyclerMetadata("receipt-command-004", "corr-receipt-004", 5),
	)
	if !errors.Is(err, ErrBatchInvalidState) {
		t.Fatalf("expected invalid state error, got %v", err)
	}
}

func TestBatchServiceVerifyReceiptRequiresRecycler(t *testing.T) {
	service, repo, _ := testBatchService()
	repo.state.batches["batch-receipt-001"] = collectedReceiptBatch()
	metadata := recyclerMetadata("receipt-command-005", "corr-receipt-005", 5)
	metadata.Actor.RoleCode = "DONOR"

	_, err := service.VerifyReceipt(
		context.Background(),
		"batch-receipt-001",
		dto.ReceiptRequest{ActualCategory: "ICT_EQUIPMENT", ActualItemCount: 10, ActualWeightKg: "10.50"},
		metadata,
	)
	if !errors.Is(err, ErrBatchForbidden) {
		t.Fatalf("expected forbidden actor error, got %v", err)
	}
	if repo.transactionN != 0 {
		t.Fatalf("forbidden actor must not start a transaction")
	}
}
