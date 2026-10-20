package service

import (
	"testing"
	"time"
	"workflow-api/internal/matchingcontract"
	"workflow-api/internal/model"
)

// Verify the existing PR #41 writer against the exact schema consumed by Python.
func TestSubmissionProducerMatchesApprovedMatcherContract(t *testing.T) {
	submitted := time.Date(2026, 9, 16, 0, 0, 0, 123456000, time.UTC)
	batch := &model.Batch{ID: "b1000000-0000-4000-8000-000000000001", OrganizationID: "DON-001", Version: 2, ClaimEpoch: 1, SubmittedAt: &submitted, CollectionDeadline: new(submitted.Add(72 * time.Hour)), Category: new("ICT_EQUIPMENT"), Quantity: new(10), EstimatedWeightKg: new("100.00"), ConditionRating: new("REPAIRABLE"), IsDataBearing: true, Zone: new("NORTH")}
	raw, err := buildRequestSubmittedPayload("e1000000-0000-4000-8000-000000000001", "c1000000-0000-4000-8000-000000000001", batch, "producer-contract-fixture")
	if err != nil {
		t.Fatal(err)
	}
	value, err := matchingcontract.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := matchingcontract.Validate("RequestSubmitted", value); err != nil {
		t.Fatal(err)
	}
	value["data"].(map[string]any)["estimated_weight_kg"] = 100
	if err := matchingcontract.Validate("RequestSubmitted", value); err == nil {
		t.Fatal("numeric weight accepted on canonical Kafka contract")
	}
}
