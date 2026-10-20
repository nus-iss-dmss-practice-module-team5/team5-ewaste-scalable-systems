package service

import (
	"testing"

	"workflow-api/internal/model"
)

func TestBatchToDTOExposesClaimEpochOnlyAfterClaim(t *testing.T) {
	batch := model.Batch{ID: "batch-1", Status: model.BatchStatusApproved, Version: 4, ClaimEpoch: 7}
	view := batchToDTO(&batch)
	if view.ClaimEpoch != "" {
		t.Fatalf("unclaimed batch exposed claim epoch %q", view.ClaimEpoch)
	}

	batch.CurrentClaimID = new("claim-1")
	view = batchToDTO(&batch)
	if view.ClaimEpoch != "7" {
		t.Fatalf("claimed batch claim epoch = %q, want 7", view.ClaimEpoch)
	}
}

func TestBatchToDTOExposesCollectorScopeID(t *testing.T) {
	scopeID := "scope-1"
	batch := model.Batch{
		ID:               "batch-1",
		Status:           model.BatchStatusApproved,
		Version:          4,
		CollectorScopeID: &scopeID,
	}

	view := batchToDTO(&batch)
	if view.CollectorScopeID != scopeID {
		t.Fatalf("collector scope id = %q, want %q", view.CollectorScopeID, scopeID)
	}
}

func TestOpportunityToDTOExposesClaimVersionAndEpoch(t *testing.T) {
	opportunity := model.WorkflowOpportunity{
		BatchID:    "batch-1",
		Status:     model.BatchStatusMatched,
		Version:    3,
		ClaimEpoch: 2,
	}

	view := opportunityToDTO(&opportunity)
	if view.Version != 3 {
		t.Fatalf("opportunity version = %d, want 3", view.Version)
	}
	if view.ClaimEpoch != "2" {
		t.Fatalf("opportunity claim epoch = %q, want 2", view.ClaimEpoch)
	}
}

func TestProcessingDetailToDTOPreservesOptionalAndDerivedFields(t *testing.T) {
	declaredCategory := "ICT_EQUIPMENT"
	declaredQuantity := 10
	declaredWeight := "12.00"
	actualCategory := "ICT_EQUIPMENT"
	actualCount := uint32(9)
	actualWeight := "11.50"
	reused := "2.00"
	recycled := "8.00"
	disposed := "1.00"
	unknown := "0.50"
	diverted := "10.00"
	quality := "PARTIAL"

	view := processingDetailToDTO(&model.ProcessingDetail{
		BatchID: "batch-1", Status: model.BatchStatusRecycled, Version: 8,
		DeclaredCategory: new(declaredCategory), DeclaredQuantity: new(declaredQuantity),
		EstimatedWeightKg: new(declaredWeight), ActualCategory: new(actualCategory),
		ActualItemCount: new(actualCount), ActualWeightKg: new(actualWeight),
		ReusedKg: new(reused), RecycledKg: new(recycled), DisposedKg: new(disposed),
		UnknownKg: new(unknown), DivertedKg: new(diverted), DataQuality: new(quality),
		EvidenceStatus: "ABSENT", AnomalyCodes: []string{"UNALLOCATED_WEIGHT"},
	})

	if view.BatchID != "batch-1" || view.Status != "RECYCLED" || view.Version != 8 {
		t.Fatalf("unexpected processing identity: %+v", view)
	}
	if view.ActualItemCount == nil || *view.ActualItemCount != 9 || view.DivertedKg == nil || *view.DivertedKg != "10.00" {
		t.Fatalf("optional or derived values were lost: %+v", view)
	}
	if len(view.AnomalyCodes) != 1 || view.AnomalyCodes[0] != "UNALLOCATED_WEIGHT" {
		t.Fatalf("anomaly codes were lost: %+v", view.AnomalyCodes)
	}
}

func TestParseProcessingStatusRejectsNonProcessingStates(t *testing.T) {
	for _, value := range []string{"DRAFT", "MATCHED", "ASSIGNED", "FAILED_COLLECTION"} {
		if _, ok := parseProcessingStatus(value); ok {
			t.Fatalf("status %q was accepted as a processing status", value)
		}
	}
	for _, value := range []string{"COLLECTED", "VERIFIED", "RECYCLED", "COMPLETED"} {
		if parsed, ok := parseProcessingStatus(value); !ok || string(parsed) != value {
			t.Fatalf("status %q was not accepted correctly: %q, %v", value, parsed, ok)
		}
	}
}

func TestAssignmentReadDistinguishesBatchAndAssignmentVersions(t *testing.T) {
	view := assignmentToDTO(&model.BatchAssignment{ID: "assignment-1", Version: 1, BatchVersion: 7})
	if view.Version != 1 || view.BatchVersion != 7 {
		t.Fatalf("versions lost: %+v", view)
	}
}
